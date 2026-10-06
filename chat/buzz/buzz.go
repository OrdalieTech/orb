// Package buzz runs an Orb agent on Buzz (`orb chat buzz`): buzz-acp holds the
// agent's relay identity and drives its sessions over ACP, and the agent's
// shell posts with the buzz CLI, which runs here with the Buzz key. Linking it
// in is the binary's one line of Buzz.
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
	"github.com/OrdalieTech/orb/internal/multicall"
	"github.com/OrdalieTech/orb/internal/toolenv"
)

func init() {
	chat.Register("buzz", chat.Platform{
		Env:   []string{"BUZZ_PRIVATE_KEY", "BUZZ_RELAY_URL", "BUZZ_AUTH_TAG", "BUZZ_ACP_*"},
		About: "starts buzz-acp (or ORB_BUZZ_ACP), which holds the agent's Buzz identity and reaches it over ACP",
		Front: front,
	})
	multicall.Register("buzz", shim)
}

// front serves this agent on Buzz: buzz-acp reaches this process's sessions
// through an ACP socket, by the relay command agent.Connect names, which it
// runs as its agent.
// This process starts buzz-acp and ends with it, so a clean exit (an owner's
// !shutdown) stays final under the container's restart policy, unless
// ORB_ACP_SOCKET says buzz-acp runs outside it: then it serves that socket,
// open to its group, and buzz-acp can run as another user, out of the tools'
// reach.
func front(ctx context.Context, agent chat.Agent) error {
	stopCLI, err := serveCLI(ctx, agent.Log)
	if err != nil {
		return err
	}
	defer stopCLI()
	socket, external := os.LookupEnv("ORB_ACP_SOCKET")
	if !external {
		socket = filepath.Join(os.TempDir(), fmt.Sprintf("orb-acp-%d.sock", os.Getpid()))
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	if external {
		if err := os.Chmod(socket, 0o660); err != nil {
			return err
		}
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = agent.Serve(ctx, conn, conn)
			}()
		}
	}()
	if external {
		<-ctx.Done()
		return nil
	}
	relay, err := agent.Connect(socket)
	if err != nil {
		return err
	}
	harness := os.Getenv("ORB_BUZZ_ACP")
	if harness == "" {
		harness = "buzz-acp"
	}
	command := exec.CommandContext(ctx, harness)
	// buzz-acp gets the tools' environment and its own BUZZ_* settings, never
	// the agent's model or chat credentials.
	command.Env = toolenv.Environ()
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "BUZZ_") || strings.HasPrefix(entry, "RUST_LOG=") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "BUZZ_ACP_AGENT_COMMAND="+relay[0], "BUZZ_ACP_AGENT_ARGS="+strings.Join(relay[1:], ","))
	command.Stdout, command.Stderr = agent.Log, agent.Log
	// buzz-acp removes its signing keyfile on SIGTERM.
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 30 * time.Second
	return command.Run()
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

// serveCLI runs the real buzz CLI (ORB_BUZZ_CLI) for the agent's shell, with
// the Buzz credentials added to that child alone. Orb answers as `buzz`
// (shim) and reaches it through the socket ORB_BUZZ names, which it exports
// to the tools. It also publishes the agent's profile.
func serveCLI(ctx context.Context, log io.Writer) (func(), error) {
	cli := os.Getenv("ORB_BUZZ_CLI")
	if cli == "" {
		cli = "buzz"
	}
	var credentials []string
	for _, name := range []string{"BUZZ_PRIVATE_KEY", "BUZZ_AUTH_TAG", "BUZZ_RELAY_URL"} {
		if value, ok := os.LookupEnv(name); ok {
			credentials = append(credentials, name+"="+value)
		}
	}
	run := func(ctx context.Context, request request) result {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, cli, request.Args...)
		// ORB_BUZZ emptied: a CLI that is this shim again fails at once instead of looping.
		command.Dir, command.Env = request.Dir, append(append(toolenv.Environ(), credentials...), "ORB_BUZZ=")
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
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	toolenv.Export("ORB_BUZZ", socket)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				var request request
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				_ = json.NewEncoder(conn).Encode(run(ctx, request))
			}()
		}
	}()
	go publishProfile(ctx, run, log)
	return func() { _ = listener.Close() }, nil
}

// publishProfile signs the agent's kind:0 profile with the real CLI, which
// adds BUZZ_AUTH_TAG to it: Buzz names the agent by it, and lists it in its
// agent directory only when its owner's tag is there. Each start publishes the
// same profile again (a replaceable event), merged into the one on the relay,
// so fields set elsewhere stay; it retries while the relay is unreachable.
func publishProfile(ctx context.Context, run func(context.Context, request) result, log io.Writer) {
	args := []string{"users", "set-profile"}
	for _, field := range [][2]string{{"--name", "BUZZ_ACP_DISPLAY_NAME"}, {"--about", "ORB_BUZZ_ABOUT"}, {"--avatar", "ORB_BUZZ_AVATAR"}} {
		if value := os.Getenv(field[1]); value != "" {
			args = append(args, field[0], value)
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

// shim is Orb started as `buzz` from the agent's shell: it hands the command
// to the agent, which runs the real CLI with the Buzz key. Standard input goes
// along only for a "-" argument (--content -), since the bash tool feeds its
// script on the shell's stdin.
func shim(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	socket := os.Getenv("ORB_BUZZ")
	if socket == "" {
		_, _ = fmt.Fprintln(stderr, "buzz: this shell is not inside an orb chat buzz agent")
		return 4
	}
	request := request{Args: args}
	request.Dir, _ = os.Getwd()
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
