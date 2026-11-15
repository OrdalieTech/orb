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
)

// run sets up the agent and supervises it: its exit ends the container with
// its status, and signals go to it. Without an agent file, args are orb
// chat's, as before agent files.
func run(args []string, image layout) (int, error) {
	agent, err := lookup("agent")
	if err != nil {
		return 0, err
	}
	// The agent runs in the workspace, which a volume may lack.
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
	auth, err := authFile(image)
	if err != nil {
		return 0, err
	}
	orb := agentProcess(args, os.Environ(), rendered, image)
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
	return supervise(command), nil
}

// account is a user the image runs processes as.
type account struct {
	uid, gid uint32
	groups   []uint32
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
	return account{uid: parsed[0], gid: parsed[1], groups: parsed[2:]}, nil
}

// command runs the process as a user, with that user's groups.
func (p process) command(as account) *exec.Cmd {
	command := exec.Command(p.argv[0], p.argv[1:]...)
	command.Dir, command.Env, command.Stdout, command.Stderr = p.dir, p.env, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Credential: as.credential()}
	return command
}

// supervise waits for the agent to exit, passing it SIGTERM and SIGINT, and
// returns its status. Its exit is logged with its status or signal, so no
// death goes unexplained.
func supervise(agent *exec.Cmd) int {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	oomKills := oomKillCount()
	exited := make(chan struct{})
	go func() { _ = agent.Wait(); close(exited) }()
	for waiting := true; waiting; {
		select {
		case received := <-signals:
			_, _ = fmt.Fprintf(os.Stderr, "orb-agent: received %s, stopping the agent\n", received)
			_ = agent.Process.Signal(received)
		case <-exited:
			waiting = false
		}
	}
	status := agent.ProcessState.Sys().(syscall.WaitStatus)
	how := fmt.Sprintf("exited with status %d", status.ExitStatus())
	if status.Signaled() {
		how = fmt.Sprintf("was killed by signal %d (%s)", status.Signal(), status.Signal())
		if killed := oomKillCount() - oomKills; status.Signal() == syscall.SIGKILL && killed > 0 {
			how += fmt.Sprintf("; the kernel's OOM killer has killed %d process(es) in this container since it started", killed)
		}
	}
	_, _ = fmt.Fprintf(os.Stderr, "orb-agent: %s (pid %d) %s\n", agent.Args[0], agent.Process.Pid, how)
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
// reads and rewrites while its tools cannot open it.
func authFile(image layout) (*os.File, error) {
	dir := filepath.Join(image.home, "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "auth.json"), os.O_RDWR|os.O_CREATE, 0o600)
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
