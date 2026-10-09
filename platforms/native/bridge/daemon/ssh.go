package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/bridge/protocol"
	nativebridge "github.com/OrdalieTech/orb/platforms/native/bridge"
	"github.com/OrdalieTech/orb/platforms/native/selfupdate"
)

// OpenSSH is an explicit native host integration, like the system clipboard;
// SSH keys and host verification stay with the user's existing SSH configuration.
func sshArgs(target, command string) ([]string, error) {
	if target == "" || len(target) > 255 || strings.HasPrefix(target, "-") || strings.Trim(target, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-@[]:") != "" {
		return nil, fmt.Errorf("enter an SSH alias or user@host, without command-line options")
	}
	return []string{"-T", "-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-oConnectTimeout=10", "-oClearAllForwardings=yes", "-oForwardAgent=no", "--", target, command}, nil
}

func SSHCommand(target, remoteOrb, profile string, args ...string) ([]string, error) {
	if !nativebridge.ValidName(profile) || remoteOrb == "" || strings.ContainsAny(remoteOrb, "\r\n\x00") {
		return nil, fmt.Errorf("invalid remote Orb path or profile")
	}
	command := []string{remoteOrb, "bridge", "--profile", profile}
	command = append(command, args...)
	for i, arg := range command {
		command[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	return sshArgs(target, "exec "+strings.Join(command, " "))
}

func RunSSH(ctx context.Context, target, remoteOrb, profile string, args ...string) ([]byte, error) {
	arguments, err := SSHCommand(target, remoteOrb, profile, args...)
	if err != nil {
		return nil, err
	}
	return runSSH(ctx, arguments, nil)
}

func runSSH(ctx context.Context, arguments []string, input io.Reader) ([]byte, error) {
	wait := 40 * time.Second
	if input != nil {
		wait = selfupdate.DownloadWait
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	command := exec.CommandContext(ctx, "ssh", arguments...)
	command.WaitDelay = time.Second
	command.Stdin = input
	command.Stderr = io.Discard
	pipe, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = command.Start(); err != nil {
		return nil, fmt.Errorf("SSH unavailable: %w", err)
	}
	output, readErr := io.ReadAll(io.LimitReader(pipe, protocol.MaxFrame+1))
	if len(output) > protocol.MaxFrame {
		cancel()
		_ = command.Wait()
		return nil, fmt.Errorf("SSH response exceeded the Bridge limit")
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("SSH setup interrupted.\nCheck the connection and try again")
	}
	if readErr != nil || waitErr != nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			switch exit.ExitCode() {
			case 255:
				return nil, fmt.Errorf("SSH login failed.\nCheck your SSH keys and saved host key")
			case 42:
				return nil, fmt.Errorf("this release does not support Bridge pairing.\nInstall a Bridge-enabled build on the server")
			case 43:
				return nil, fmt.Errorf("uploaded Orb failed its checksum.\nReconnect to retry the installation")
			}
		}
		return nil, fmt.Errorf("server setup failed.\nCheck its install directory and Bridge service")
	}
	return output, nil
}

func EnsureSSH(ctx context.Context, target, remoteOrb string, updater selfupdate.Updater) (string, error) {
	if remoteOrb != "orb" {
		help, err := RunSSH(ctx, target, remoteOrb, "personal", "--help")
		if err != nil {
			return "", err
		}
		if !strings.Contains(string(help), "orb bridge trust") {
			return "", fmt.Errorf("the selected Orb does not support Bridge pairing.\nUpdate that executable or use automatic setup")
		}
		return remoteOrb, nil
	}
	args, err := sshArgs(target, `for orb_path in "$(command -v orb || true)" "${ORB_INSTALL_DIR:-$HOME/.local/bin}/orb"; do
  if [ -x "$orb_path" ] && ORB_OFFLINE=1 "$orb_path" bridge --help 2>/dev/null | grep -q 'orb bridge trust'; then
    printf 'ready\n%s\n' "$orb_path"; exit 0
  fi
done
printf 'install\n'; uname -sm`)
	if err != nil {
		return "", err
	}
	raw, err := runSSH(ctx, args, nil)
	if err != nil {
		return "", err
	}
	state, value, ok := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	if !ok {
		return "", fmt.Errorf("could not detect Orb on the server.\nCheck its SSH shell configuration")
	}
	if state == "ready" {
		return value, nil
	}
	platform := strings.Fields(value)
	if state != "install" || len(platform) != 2 {
		return "", fmt.Errorf("could not detect the server platform")
	}
	goos := strings.ToLower(platform[0])
	goarch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform[1]]
	tag, err := updater.Latest(ctx)
	if err != nil {
		return "", err
	}
	payload, err := updater.Download(ctx, tag, goos, goarch)
	if err != nil {
		return "", fmt.Errorf("could not download Orb for the server.\n%w", err)
	}
	return installSSH(ctx, target, payload)
}

func installSSH(ctx context.Context, target string, payload []byte) (string, error) {
	args, err := sshArgs(target, fmt.Sprintf("expected=%x\n", sha256.Sum256(payload))+`set -eu
umask 077
dir="${ORB_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dir"
stage=$(mktemp "$dir/.orb-install.XXXXXXXX")
trap 'rm -f "$stage"' EXIT
cat > "$stage"
if command -v sha256sum >/dev/null 2>&1; then digest=$(sha256sum "$stage"); else digest=$(shasum -a 256 "$stage"); fi
[ "${digest%% *}" = "$expected" ] || exit 43
chmod 755 "$stage"
ORB_OFFLINE=1 "$stage" bridge --help 2>/dev/null | grep -q 'orb bridge trust' || exit 42
mv -f "$stage" "$dir/orb"
printf '%s\n' "$dir/orb"`)
	if err != nil {
		return "", err
	}
	raw, err := runSSH(ctx, args, bytes.NewReader(payload))
	return strings.TrimSpace(string(raw)), err
}
