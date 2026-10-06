package main

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

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/acp"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/internal/toolenv"
	"github.com/OrdalieTech/orb/plugins/mcp"
)

// acpHost opens each ACP session the way the CLI opens one, so a client's
// sessions get the CLI's settings, plugins, skills and context files.
type acpHost struct {
	args         CLIArgs
	dependencies cliDependencies
	streams      cliStreams
}

func (host acpHost) Open(ctx context.Context, options acp.Options) (*agent.AgentSessionRuntime, func(), error) {
	args := host.args
	args.native = args.native.conversation()
	args.Session, args.SystemPrompt = nil, options.SystemPrompt
	if options.ID != "" {
		args.Session = &options.ID
	}
	if options.Append != "" {
		args.AppendSystemPrompt = append(slices.Clip(args.AppendSystemPrompt), options.Append)
	}
	args.mcpServers = nil
	for _, server := range options.MCPServers {
		// The client supplied these for this session, so their tools are
		// declared up front rather than found through tool_search.
		config := mcp.ServerConfig{Type: server.Type, Command: server.Command, Args: server.Args, URL: server.URL, Exposure: mcp.ExposureDirect}
		if server.Type == "http" || server.Type == "sse" {
			config.Type = ""
		}
		for _, variable := range server.Env {
			config.Env = setVariable(config.Env, variable)
		}
		for _, header := range server.Headers {
			config.Headers = setVariable(config.Headers, header)
		}
		if err := mcp.Validate(server.Name, &config); err != nil {
			return nil, nil, err
		}
		args.mcpServers = append(args.mcpServers, mcp.Entry{Name: server.Name, Config: config, Source: "ACP client", Scope: "session"})
	}
	cwd := options.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	manager, _, err := createCLISession(cwd, args, host.streams, nil, nil)
	if err != nil {
		args.native.release()
		return nil, nil, err
	}
	return openHeadless(ctx, args, host.dependencies, host.streams, manager)
}

// openHeadless builds a session on manager as the CLI builds one, headless;
// close disposes it and releases args' conversation claim.
func openHeadless(ctx context.Context, args CLIArgs, dependencies cliDependencies, streams cliStreams, manager *session.SessionManager) (*agent.AgentSessionRuntime, func(), error) {
	runtime, err := newCLISessionRuntimeHost(ctx, cliSessionRuntimeHostOptions{
		BaseArgs: args, Manager: manager, Dependencies: dependencies, Streams: streams, ExtensionMode: extensions.ModePrint,
	})
	if err == nil {
		if err = runtime.Session().BindExtensions(ctx); err != nil {
			runtime.Dispose(ctx)
		}
	}
	if err != nil {
		args.native.release()
		return nil, nil, err
	}
	return runtime, func() { runtime.Dispose(context.Background()); args.native.release() }, nil
}

func setVariable(values map[string]string, variable acp.Variable) map[string]string {
	if values == nil {
		values = map[string]string{}
	}
	values[variable.Name] = variable.Value
	return values
}

func runACP(ctx context.Context, args CLIArgs, dependencies cliDependencies, streams cliStreams) int {
	args.useUnknownModel = true
	if err := acp.Serve(ctx, streams.Stdin, streams.Stdout, acpHost{args: args, dependencies: dependencies, streams: streams}, version); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	return 0
}

// runBuzz serves this agent on Buzz: buzz-acp holds the agent's relay identity
// and reaches this process's sessions through an ACP socket, by the
// `orb chat connect` relay it runs as its agent. This process starts buzz-acp
// and ends with it, so a clean exit (an owner's !shutdown) stays final under
// the container's restart policy, unless ORB_ACP_SOCKET says buzz-acp runs
// outside it: then it serves that socket, open to its group, and buzz-acp can
// run as another user, out of the tools' reach.
func runBuzz(ctx context.Context, host acpHost) error {
	stopCLI, err := serveBuzzCLI(ctx, host.streams.Stderr)
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
				_ = acp.Serve(ctx, conn, conn, host, version)
			}()
		}
	}()
	if external {
		<-ctx.Done()
		return nil
	}
	self, err := os.Executable()
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
	command.Env = append(command.Env, "BUZZ_ACP_AGENT_COMMAND="+self, "BUZZ_ACP_AGENT_ARGS=chat,connect,"+socket)
	command.Stdout, command.Stderr = host.streams.Stderr, host.streams.Stderr
	// buzz-acp removes its signing keyfile on SIGTERM.
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 30 * time.Second
	return command.Run()
}

// runChatConnect relays stdio to a running `orb chat buzz` agent's ACP socket.
// It inherits buzz-acp's environment, Buzz key included, so it hides it.
func runChatConnect(socket string, in io.Reader, out io.Writer) int {
	hideProcess()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		return 1
	}
	go func() {
		_, _ = io.Copy(conn, in)
		_ = conn.(*net.UnixConn).CloseWrite()
	}()
	_, _ = io.Copy(out, conn)
	return 0
}

// teamTools are the variables a team agent's tools inherit when its operator
// sets no ORB_TOOL_ENV: none of its credentials.
const teamTools = "PATH,HOME,USER,SHELL,LANG,LC_ALL,TERM,TZ,TMPDIR"

// teamAgent prepares this process to run an agent on chat platforms: its
// tools get the teamTools environment, its own environment is hidden from them,
// and its settings.json and models.json stay the configuration of record.
func teamAgent(ctx context.Context, dependencies cliDependencies, streams cliStreams) acpHost {
	if os.Getenv(toolenv.Allow) == "" {
		_ = os.Setenv(toolenv.Allow, teamTools)
	}
	hideProcess()
	args := ParseArgs(nil)
	args.native, args.useUnknownModel = stateFromContext(ctx), true
	if args.native != nil {
		args.native.files = true
	}
	return acpHost{args: args, dependencies: dependencies, streams: streams}
}

// buzzRequest is one `buzz` command run from the agent's shell: Buzz's harness
// tells the agent to post with the buzz CLI, and the shell has no Buzz key.
type buzzRequest struct {
	Args  []string `json:"args"`
	Dir   string   `json:"dir"`
	Stdin []byte   `json:"stdin,omitempty"`
}

type buzzResult struct {
	Stdout []byte `json:"stdout"`
	Stderr []byte `json:"stderr"`
	Code   int    `json:"code"`
}

// serveBuzzCLI runs the real buzz CLI (ORB_BUZZ_CLI) for the agent's shell,
// with the Buzz credentials added to that child alone. Orb answers as `buzz`
// (runBuzzShim) and reaches it through the socket ORB_BUZZ names, which joins
// the tools' environment. It also publishes the agent's profile.
func serveBuzzCLI(ctx context.Context, log io.Writer) (func(), error) {
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
	run := func(ctx context.Context, request buzzRequest) buzzResult {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, cli, request.Args...)
		// ORB_BUZZ emptied: a CLI that is this shim again fails at once instead of looping.
		command.Dir, command.Env = request.Dir, append(append(toolenv.Environ(), credentials...), "ORB_BUZZ=")
		command.Stdin = bytes.NewReader(request.Stdin)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		result := buzzResult{}
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
	_ = os.Setenv("ORB_BUZZ", socket)
	if allowed := os.Getenv(toolenv.Allow); allowed != "" {
		_ = os.Setenv(toolenv.Allow, allowed+",ORB_BUZZ")
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				var request buzzRequest
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				_ = json.NewEncoder(conn).Encode(run(ctx, request))
			}()
		}
	}()
	go publishBuzzProfile(ctx, run, log)
	return func() { _ = listener.Close() }, nil
}

// publishBuzzProfile signs the agent's kind:0 profile with the real CLI, which
// adds BUZZ_AUTH_TAG to it: Buzz names the agent by it, and lists it in its
// agent directory only when its owner's tag is there. Each start publishes the
// same profile again (a replaceable event), merged into the one on the relay,
// so fields set elsewhere stay; it retries while the relay is unreachable.
func publishBuzzProfile(ctx context.Context, run func(context.Context, buzzRequest) buzzResult, log io.Writer) {
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
		result := run(ctx, buzzRequest{Args: args})
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

// runBuzzShim is Orb called as `buzz` from the agent's shell: it hands the
// command to the agent, which runs the real CLI with the Buzz key. Standard
// input goes along only for a "-" argument (--content -), since the bash tool
// feeds its script on the shell's stdin.
func runBuzzShim(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	socket := os.Getenv("ORB_BUZZ")
	if socket == "" {
		_, _ = fmt.Fprintln(stderr, "buzz: this shell is not inside an orb chat buzz agent")
		return 4
	}
	request := buzzRequest{Args: args}
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
	var result buzzResult
	if err := json.NewEncoder(conn).Encode(request); err != nil || json.NewDecoder(conn).Decode(&result) != nil {
		_, _ = fmt.Fprintln(stderr, "buzz: the agent did not answer")
		return 4
	}
	_, _ = stdout.Write(result.Stdout)
	_, _ = stderr.Write(result.Stderr)
	return result.Code
}
