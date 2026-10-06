package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/platforms/native"
	nativebridge "github.com/OrdalieTech/orb/platforms/native/bridge"
)

// A server's Bridge should outlive the SSH session that paired it and the machine's reboots:
// `orb bridge service install` runs it as a systemd user service. Elsewhere Orb starts the
// Bridge whenever it needs it, so this is Linux only.

func UnitName(profile string) string {
	if profile == "personal" {
		return "orb-bridge.service"
	}
	return "orb-bridge-" + profile + ".service"
}

// unit restarts a crashed Bridge but not a deliberately stopped one (`orb bridge stop`
// exits cleanly). It carries PATH so the Orbs it starts for peers find the owner's tools.
func unit(exe, profile string, env map[string]string) string {
	lines := []string{"[Unit]", "Description=Orb Bridge (" + profile + ")", "Wants=network-online.target", "After=network-online.target", "",
		"[Service]", fmt.Sprintf("ExecStart=%s bridge run --profile %s", systemdQuote(exe, true), systemdQuote(profile, true)), "Restart=on-failure", "RestartSec=2"}
	for _, k := range []string{"PATH", "ORB_BRIDGE_HOME", "ORB_STATE_HOME"} {
		if v := env[k]; v != "" {
			lines = append(lines, "Environment="+systemdQuote(k+"="+v, false))
		}
	}
	return strings.Join(append(lines, "", "[Install]", "WantedBy=default.target", ""), "\n")
}

// systemdQuote quotes a value for a unit file: systemd reads % as a specifier everywhere, and $
// as a variable in commands, so both are doubled to stay literal.
func systemdQuote(s string, command bool) string {
	s = strings.ReplaceAll(strconv.Quote(s), "%", "%%")
	if command {
		s = strings.ReplaceAll(s, "$", "$$")
	}
	return s
}

func unitPath(profile string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "systemd", "user", UnitName(profile)), nil
}

// Installed reports whether this profile's Bridge already runs as a user service.
func Installed(profile string) bool {
	path, err := unitPath(profile)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func Systemctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// Install writes and starts the unit. The returned note says what is left for the
// owner, if anything (lingering, which may need sudo).
func Install(ctx context.Context, state *native.State, profile string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", errors.New("the Bridge service is for Linux servers; elsewhere Orb starts Bridge when it needs it")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", errors.New("systemd is not available here; run orb bridge start after each reboot")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	path, err := unitPath(profile)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	env := map[string]string{}
	for _, k := range []string{"PATH", "ORB_BRIDGE_HOME", "ORB_STATE_HOME"} {
		env[k] = os.Getenv(k)
	}
	if err = os.WriteFile(path, []byte(unit(exe, profile, env)), 0o644); err != nil {
		return "", err
	}
	// A Bridge started by hand holds the socket; it hands over to the service.
	if c, err := Admin(ctx, state, profile); err == nil {
		if c.Call(ctx, "stop", struct{}{}, nil) == nil {
			_ = WaitStopped(ctx, c)
		}
		_ = c.Close()
	}
	// The service is an explicit start: a stop marker from before no longer applies.
	if dir, err := nativebridge.Dir(profile); err == nil {
		_ = state.Write(ctx, filepath.Join(dir, "stopped"), nil)
	}
	if err = Systemctl(ctx, "daemon-reload"); err == nil {
		err = Systemctl(ctx, "enable", "--now", UnitName(profile))
	}
	if err != nil {
		return "", err
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if c, err := Admin(ctx, state, profile); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			return "", errors.New("the service did not start; see: journalctl --user -u " + UnitName(profile))
		}
	}
	// Without lingering, systemd stops user services when the owner's last session ends.
	u, err := user.Current()
	if err != nil {
		return "", nil
	}
	if out, _ := exec.CommandContext(ctx, "loginctl", "show-user", u.Username, "-p", "Linger", "--value").Output(); strings.TrimSpace(string(out)) == "yes" {
		return "", nil
	}
	if exec.CommandContext(ctx, "loginctl", "enable-linger", u.Username).Run() == nil {
		return "", nil
	}
	return "To keep it running while you are logged out, run: sudo loginctl enable-linger " + u.Username, nil
}

func Remove(ctx context.Context, profile string) error {
	path, err := unitPath(profile)
	if err != nil {
		return err
	}
	if _, err = os.Stat(path); err != nil {
		return errors.New("no Bridge service is installed")
	}
	_ = Systemctl(ctx, "disable", "--now", UnitName(profile))
	if err = os.Remove(path); err != nil {
		return err
	}
	return Systemctl(ctx, "daemon-reload")
}
