package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OrdalieTech/orb/chat"
)

// plain is the environment the agent gets as environment; everything else,
// secrets included, reaches it as KEY=VALUE lines on ORB_SECRETS_FD, which it
// loads into an environment its tools cannot read.
var plain = regexp.MustCompile(`^(PATH|HOME|TERM|LANG|LC_[A-Z]+|TZ|ORB_[A-Z_]+|PI_[A-Z_]+)=`)

// socket is the agent's ACP socket, served for its sidecars.
const socket = "/run/orb/acp.sock"

// run sets up the agent and supervises it with its sidecars: the first
// process to exit ends the container with its status, so an owner's clean
// shutdown stays final under --restart on-failure.
func run(args []string) (int, error) {
	platforms, env, files := args, map[string]string{}, map[string][]byte{}
	if file := filepath.Join(image.home, "agent.yaml"); exists(file) {
		plan, err := load(file, image)
		if err != nil {
			return 0, err
		}
		platforms, env, files = append(plan.platforms, "--tools"), plan.env, plan.files
	}
	agent, err := lookup("agent")
	if err != nil {
		return 0, err
	}
	for path, data := range files {
		if err := writeOwned(path, data, agent); err != nil {
			return 0, err
		}
	}
	auth, err := authFile()
	if err != nil {
		return 0, err
	}
	var sidecars []chat.Platform
	for _, name := range platforms {
		if platform, ok := chat.LookupPlatform(name); ok && platform.Sidecar != nil {
			sidecars = append(sidecars, platform)
		}
	}
	agentEnv := []string{"ORB_SECRETS_FD=3", "ORB_AUTH_FD=4"}
	var secrets strings.Builder
	for _, entry := range os.Environ() {
		if plain.MatchString(entry) {
			agentEnv = append(agentEnv, entry)
		} else {
			secrets.WriteString(entry + "\n")
		}
	}
	for _, name := range sortedKeys(env) {
		secrets.WriteString(name + "=" + env[name] + "\n")
	}
	if len(sidecars) > 0 {
		agentEnv = append(agentEnv, "ORB_ACP_SOCKET="+socket)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	go func() { _, _ = writer.WriteString(secrets.String()); _ = writer.Close() }()
	orb := exec.Command("orb", append([]string{"chat"}, platforms...)...)
	orb.Env, orb.ExtraFiles = agentEnv, []*os.File{reader, auth}
	err = start(orb, agent)
	_ = reader.Close()
	_ = auth.Close()
	if err != nil {
		return 0, err
	}
	running := []*exec.Cmd{orb}
	if len(sidecars) > 0 {
		sidecar, err := lookup("sidecar")
		if err != nil {
			return 0, err
		}
		// A sidecar may start its agent at once, so the socket comes first.
		for deadline := time.Now().Add(30 * time.Second); !exists(socket) && time.Now().Before(deadline); {
			time.Sleep(100 * time.Millisecond)
		}
		for _, platform := range sidecars {
			argv, settings := platform.Sidecar([]string{"/usr/bin/nc", "-N", "-U", socket})
			command := exec.Command(argv[0], argv[1:]...)
			command.Env, command.Dir = sidecarEnv(platform, settings, env, sidecar), sidecar.HomeDir
			if err := start(command, sidecar); err != nil {
				return 0, err
			}
			running = append(running, command)
		}
	}
	return supervise(running), nil
}

// sidecarEnv is a sidecar's environment: its platform's declared variables
// from the container and the agent file, then its own settings.
func sidecarEnv(platform chat.Platform, settings, rendered map[string]string, as *user.User) []string {
	declared := func(name string) bool {
		return slices.ContainsFunc(platform.Env, func(pattern string) bool {
			prefix, wildcard := strings.CutSuffix(pattern, "*")
			return name == pattern || wildcard && strings.HasPrefix(name, prefix)
		})
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + as.HomeDir}
	for _, entry := range os.Environ() {
		if name, _, _ := strings.Cut(entry, "="); declared(name) {
			env = append(env, entry)
		}
	}
	for _, name := range sortedKeys(rendered) {
		if declared(name) {
			env = append(env, name+"="+rendered[name])
		}
	}
	for _, name := range sortedKeys(settings) {
		env = append(env, name+"="+settings[name])
	}
	return env
}

func lookup(name string) (*user.User, error) {
	found, err := user.Lookup(name)
	if err != nil {
		return nil, fmt.Errorf("the image has no %s user: %w", name, err)
	}
	return found, nil
}

func ids(as *user.User) (uint32, uint32) {
	uid, _ := strconv.ParseUint(as.Uid, 10, 32)
	gid, _ := strconv.ParseUint(as.Gid, 10, 32)
	return uint32(uid), uint32(gid)
}

// start runs command as a user, with that user's groups.
func start(command *exec.Cmd, as *user.User) error {
	uid, gid := ids(as)
	var groups []uint32
	names, _ := as.GroupIds()
	for _, name := range names {
		id, _ := strconv.ParseUint(name, 10, 32)
		groups = append(groups, uint32(id))
	}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: groups}}
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start %s: %w", command.Path, err)
	}
	return nil
}

// supervise waits for the first process to exit, stops the others and
// returns its status; SIGTERM and SIGINT go to every process.
func supervise(running []*exec.Cmd) int {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	exited := make(chan *exec.Cmd, len(running))
	for _, process := range running {
		go func() { _ = process.Wait(); exited <- process }()
	}
	stop := func(signal os.Signal) {
		for _, process := range running {
			_ = process.Process.Signal(signal)
		}
	}
	var first *exec.Cmd
	select {
	case first = <-exited:
		stop(syscall.SIGTERM)
	case received := <-signals:
		stop(received)
		first = <-exited
	}
	for range len(running) - 1 {
		<-exited
	}
	status := first.ProcessState.Sys().(syscall.WaitStatus)
	if status.Signaled() {
		return 128 + int(status.Signal())
	}
	return status.ExitStatus()
}

// authFile is the agent's OAuth logins, root's, on a descriptor the agent
// reads and rewrites while its tools cannot open it; one left in config/ is
// moved there.
func authFile() (*os.File, error) {
	dir := filepath.Join(image.home, "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "auth.json")
	if legacy := filepath.Join(image.config, "auth.json"); exists(legacy) && !exists(path) {
		if err := os.Rename(legacy, path); err != nil {
			return nil, err
		}
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	return file, errors.Join(file.Chown(0, 0), file.Chmod(0o600))
}

// writeOwned writes a rendered file as the agent's.
func writeOwned(path string, data []byte, as *user.User) error {
	uid, gid := ids(as)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.Chown(filepath.Dir(path), int(uid), int(gid)); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return os.Chown(path, int(uid), int(gid))
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
