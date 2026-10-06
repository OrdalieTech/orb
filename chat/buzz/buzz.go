// Package buzz runs an Orb agent on Buzz: buzz-acp holds the agent's relay
// identity and drives its sessions over ACP, and the agent's shell posts with
// the buzz CLI, which runs here with the Buzz key. It takes its configuration
// as Options; the chat/platforms catalog builds them.
package buzz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/OrdalieTech/orb/chat"
)

// Options configure Buzz for one agent.
type Options struct {
	// CLI is the real buzz CLI, which the agent's shell reaches through Shim.
	CLI string
	// Credentials are the Buzz key, owner tag and relay as KEY=VALUE, given to
	// the CLI alone.
	Credentials []string
	// Profile is published as the agent's kind:0 at start.
	Profile chat.Identity
	// Socket, when set, is the ACP socket buzz-acp, running outside this
	// process as another user, reaches the agent on. Otherwise the agent
	// starts Harness itself, with HarnessEnv (its BUZZ_* settings).
	Socket     string
	Harness    string
	HarnessEnv []string
}

// HarnessSettings are buzz-acp's settings for an Orb agent: it reaches the
// agent through relay, a command connecting its stdio to the agent's ACP
// socket, and Buzz's own memory is off, since the agent's is Orb's.
func HarnessSettings(relay []string) []string {
	return []string{"BUZZ_ACP_AGENT_COMMAND=" + relay[0], "BUZZ_ACP_AGENT_ARGS=" + strings.Join(relay[1:], ","), "BUZZ_ACP_NO_MEMORY=true"}
}

// Front serves the agent on Buzz until ctx ends: buzz-acp reaches its sessions
// through an ACP socket. Without Options.Socket the agent starts buzz-acp and
// ends with it, so a clean exit (an owner's !shutdown) stays final under the
// container's restart policy; with it, the socket is open to its group, so
// buzz-acp can run as another user, out of the tools' reach.
func Front(ctx context.Context, agent chat.Agent, options Options) error {
	stopCLI, err := serveCLI(ctx, agent, options)
	if err != nil {
		return err
	}
	defer stopCLI()
	socket, external := options.Socket, options.Socket != ""
	if !external {
		socket = filepath.Join(os.TempDir(), fmt.Sprintf("orb-acp-%d.sock", os.Getpid()))
	}
	listener, err := listen(socket)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	if external {
		if err := os.Chmod(socket, 0o660); err != nil {
			return err
		}
	}
	go accept(listener, func(conn net.Conn) { _ = agent.Serve(ctx, conn, conn) })
	if external {
		<-ctx.Done()
		return nil
	}
	relay, err := agent.Connect(socket)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, options.Harness)
	// buzz-acp gets the tools' environment and its own settings, never the
	// agent's model or chat credentials.
	command.Env = slices.Concat(agent.Environ(), options.HarnessEnv, HarnessSettings(relay))
	command.Stdout, command.Stderr = agent.Log, agent.Log
	// buzz-acp removes its signing keyfile on SIGTERM.
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 30 * time.Second
	return command.Run()
}

func listen(socket string) (net.Listener, error) {
	_ = os.Remove(socket)
	return net.Listen("unix", socket)
}

// accept hands each connection to serve, closing it after, until the
// listener closes.
func accept(listener net.Listener, serve func(net.Conn)) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = conn.Close() }()
			serve(conn)
		}()
	}
}

// request is one `buzz` command run from the agent's shell: Buzz's harness
// tells the agent to post with the buzz CLI, and the shell has no Buzz key.
type request struct {
	Args  []string `json:"args"`
	Dir   string   `json:"dir"`
	Stdin []byte   `json:"stdin,omitempty"`
}

type result struct {
	Stdout []byte `json:"stdout"`
	Stderr []byte `json:"stderr"`
	Code   int    `json:"code"`
}

// serveCLI runs the real buzz CLI for the agent's shell, with the Buzz
// credentials added to that child alone. Orb answers as `buzz` (Shim) and
// reaches it through the socket ORB_BUZZ names, which the agent exports to its
// tools. It also publishes the agent's profile.
func serveCLI(ctx context.Context, agent chat.Agent, options Options) (func(), error) {
	run := func(ctx context.Context, request request) result {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, options.CLI, request.Args...)
		// ORB_BUZZ emptied: a CLI that is this shim again fails at once instead of looping.
		command.Dir, command.Env = request.Dir, slices.Concat(agent.Environ(), options.Credentials, []string{"ORB_BUZZ="})
		command.Stdin = bytes.NewReader(request.Stdin)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		result := result{}
		if err := command.Run(); err != nil {
			result.Code = 1
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				result.Code = exit.ExitCode()
			} else {
				stderr.WriteString("buzz: " + err.Error() + "\n")
			}
		}
		result.Stdout, result.Stderr = stdout.Bytes(), stderr.Bytes()
		return result
	}
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("orb-buzz-%d.sock", os.Getpid()))
	listener, err := listen(socket)
	if err != nil {
		return nil, err
	}
	agent.Export("ORB_BUZZ", socket)
	go accept(listener, func(conn net.Conn) {
		var request request
		if json.NewDecoder(conn).Decode(&request) == nil {
			_ = json.NewEncoder(conn).Encode(run(ctx, request))
		}
	})
	go publishProfile(ctx, run, options.Profile, agent.Log)
	return func() { _ = listener.Close() }, nil
}

// publishProfile signs the agent's kind:0 profile with the real CLI, which
// adds BUZZ_AUTH_TAG to it: Buzz names the agent by it, and lists it in its
// agent directory only when its owner's tag is there. Each start publishes the
// same profile again (a replaceable event), merged into the one on the relay,
// so fields set elsewhere stay; it retries while the relay is unreachable.
func publishProfile(ctx context.Context, run func(context.Context, request) result, profile chat.Identity, log io.Writer) {
	args := []string{"users", "set-profile"}
	for _, field := range [][2]string{{"--name", profile.Name}, {"--about", profile.About}, {"--avatar", profile.Avatar}} {
		if field[1] != "" {
			args = append(args, field[0], field[1])
		}
	}
	if len(args) == 2 {
		return
	}
	for delay := 5 * time.Second; ; delay = min(2*delay, 5*time.Minute) {
		result := run(ctx, request{Args: args})
		if result.Code == 0 {
			_, _ = fmt.Fprintln(log, "buzz: published the agent's profile")
			return
		}
		_, _ = fmt.Fprintf(log, "buzz: could not publish the agent's profile, retrying in %s: %s\n", delay, bytes.TrimSpace(result.Stderr))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// Shim is Orb started as `buzz` from the agent's shell, in dir: it hands the
// command to the agent through socket (ORB_BUZZ), and the agent runs the real
// CLI with the Buzz key. Standard input goes along only for a "-" argument
// (--content -), since the bash tool feeds its script on the shell's stdin.
func Shim(socket, dir string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if socket == "" {
		_, _ = fmt.Fprintln(stderr, "buzz: this shell is not inside an orb chat buzz agent")
		return 4
	}
	request := request{Args: args, Dir: dir}
	if slices.Contains(args, "-") {
		request.Stdin, _ = io.ReadAll(io.LimitReader(stdin, 1<<20))
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "buzz: "+err.Error())
		return 4
	}
	defer func() { _ = conn.Close() }()
	var result result
	if err := json.NewEncoder(conn).Encode(request); err != nil || json.NewDecoder(conn).Decode(&result) != nil {
		_, _ = fmt.Fprintln(stderr, "buzz: the agent did not answer")
		return 4
	}
	_, _ = stdout.Write(result.Stdout)
	_, _ = stderr.Write(result.Stderr)
	return result.Code
}
