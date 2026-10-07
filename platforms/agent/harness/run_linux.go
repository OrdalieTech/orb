package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OrdalieTech/orb/chat/platforms"
)

// run sets up the agent and supervises it with its sidecars: the first
// process to exit ends the container with its status, so an owner's clean
// shutdown stays final under --restart on-failure. Without an agent file,
// args are orb chat's, as before agent files.
func run(args []string, image layout) (int, error) {
	agent, err := lookup("agent")
	if err != nil {
		return 0, err
	}
	// Every process runs in the workspace, which a volume may lack.
	if err := os.MkdirAll(image.workspace, 0o755); err != nil {
		return 0, err
	}
	if err := os.Chown(image.workspace, int(agent.uid), int(agent.gid)); err != nil {
		return 0, err
	}
	rendered := map[string]string{}
	if file := filepath.Join(image.home, "agent.yaml"); exists(file) {
		plan, err := load(file, image)
		if err != nil {
			return 0, err
		}
		for path, data := range plan.files {
			if err := render(path, data, agent); err != nil {
				return 0, err
			}
		}
		args, rendered = append(plan.platforms, "--tools"), plan.env
	}
	var sidecars []platforms.Platform
	for _, name := range args {
		if platform, ok := platforms.Lookup(name); ok && platform.Sidecar != nil {
			sidecars = append(sidecars, platform)
		}
	}
	auth, err := authFile(image)
	if err != nil {
		return 0, err
	}
	// A socket left by a process that died is not one the agent listens on:
	// sidecars wait for the agent's own, and a restart is not blocked.
	if err := os.Remove(image.socket); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	orb := agentProcess(args, os.Environ(), rendered, image, len(sidecars) > 0)
	reader, writer, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	go func() { _, _ = writer.WriteString(orb.secrets); _ = writer.Close() }()
	command := orb.command(agent)
	command.ExtraFiles = []*os.File{reader, auth}
	err = command.Start()
	_ = reader.Close()
	_ = auth.Close()
	if err != nil {
		return 0, err
	}
	running := []*exec.Cmd{command}
	if len(sidecars) > 0 {
		sidecar, err := lookup("sidecar")
		if err != nil {
			return 0, err
		}
		// A sidecar may start its agent at once, so the socket comes first.
		for deadline := time.Now().Add(30 * time.Second); !exists(image.socket) && time.Now().Before(deadline); {
			time.Sleep(100 * time.Millisecond)
		}
		for _, platform := range sidecars {
			command := sidecarProcess(platform, os.Environ(), rendered, image, sidecar.home).command(sidecar)
			if err := command.Start(); err != nil {
				return 0, err
			}
			running = append(running, command)
		}
	}
	return supervise(running), nil
}

// account is a user the image runs processes as.
type account struct {
	uid, gid uint32
	groups   []uint32
	home     string
}

func (a account) credential() *syscall.Credential {
	return &syscall.Credential{Uid: a.uid, Gid: a.gid, Groups: a.groups}
}

// lookup returns the image's user name; an id it cannot read stops the
// container rather than running anything as root.
func lookup(name string) (account, error) {
	found, err := user.Lookup(name)
	if err != nil {
		return account{}, fmt.Errorf("the image has no %s user: %w", name, err)
	}
	groups, err := found.GroupIds()
	if err != nil {
		return account{}, fmt.Errorf("user %s: %w", name, err)
	}
	ids := append([]string{found.Uid, found.Gid}, groups...)
	parsed := make([]uint32, len(ids))
	for i, id := range ids {
		value, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return account{}, fmt.Errorf("user %s: id %q: %w", name, id, err)
		}
		parsed[i] = uint32(value)
	}
	return account{uid: parsed[0], gid: parsed[1], groups: parsed[2:], home: found.HomeDir}, nil
}

// command runs the process as a user, with that user's groups.
func (p process) command(as account) *exec.Cmd {
	command := exec.Command(p.argv[0], p.argv[1:]...)
	command.Dir, command.Env, command.Stdout, command.Stderr = p.dir, p.env, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Credential: as.credential()}
	return command
}

// supervise waits for the first process to exit, stops the others and
// returns its status; SIGTERM and SIGINT go to every process. Every exit is
// logged with its status or signal, so no death goes unexplained.
func supervise(running []*exec.Cmd) int {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	oomKills := oomKillCount()
	exited := make(chan *exec.Cmd, len(running))
	for _, process := range running {
		go func() { _ = process.Wait(); exited <- process }()
	}
	stop := func(signal os.Signal) {
		for _, process := range running {
			_ = process.Process.Signal(signal)
		}
	}
	report := func(process *exec.Cmd) {
		status := process.ProcessState.Sys().(syscall.WaitStatus)
		how := fmt.Sprintf("exited with status %d", status.ExitStatus())
		if status.Signaled() {
			how = fmt.Sprintf("was killed by signal %d (%s)", status.Signal(), status.Signal())
			if killed := oomKillCount() - oomKills; status.Signal() == syscall.SIGKILL && killed > 0 {
				how += fmt.Sprintf("; the kernel's OOM killer has killed %d process(es) in this container since it started", killed)
			}
		}
		_, _ = fmt.Fprintf(os.Stderr, "orb-agent: %s (pid %d) %s\n", process.Args[0], process.Process.Pid, how)
	}
	var first *exec.Cmd
	select {
	case first = <-exited:
		report(first)
		_, _ = fmt.Fprintln(os.Stderr, "orb-agent: stopping the container")
		stop(syscall.SIGTERM)
	case received := <-signals:
		_, _ = fmt.Fprintf(os.Stderr, "orb-agent: received %s, stopping the container\n", received)
		stop(received)
		first = <-exited
		report(first)
	}
	for range len(running) - 1 {
		report(<-exited)
	}
	status := first.ProcessState.Sys().(syscall.WaitStatus)
	if status.Signaled() {
		return 128 + int(status.Signal())
	}
	return status.ExitStatus()
}

// oomKillCount is how many processes the kernel's OOM killer has killed in
// the container (its cgroup v2 memory.events), or 0 when it does not tell.
func oomKillCount() int {
	data, _ := os.ReadFile("/sys/fs/cgroup/memory.events")
	_, rest, _ := strings.Cut(string(data), "oom_kill ")
	count, _ := strconv.Atoi(strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0]))
	return count
}

// authFile is the agent's OAuth logins, root's, on a descriptor the agent
// reads and rewrites while its tools cannot open it; one left in config/ is
// moved there.
func authFile(image layout) (*os.File, error) {
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

// render writes a file the agent file owns, as the agent's, or removes it
// when the file no longer asks for it.
func render(path string, data []byte, as account) error {
	if data == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return errors.Join(os.Chown(dir, int(as.uid), int(as.gid)), os.Chown(path, int(as.uid), int(as.gid)))
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
