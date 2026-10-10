package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"slices"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/acp"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/internal/toolenv"
	"github.com/OrdalieTech/orb/platforms/native/teamenv"
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
	cwd := options.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// The session's tools run in cwd: one the agent cannot open is refused
	// here, saying why, rather than failing every tool call.
	directory, err := os.Open(cwd)
	if err != nil {
		return nil, nil, fmt.Errorf("session working directory: %w", err)
	}
	_ = directory.Close()
	// The client says where the session runs, including a stored one it loads.
	args.clientCWD = true
	// The client names its session: the CLI's own session flags do not apply.
	args.Session, args.Resume, args.Continue, args.Fork, args.SystemPrompt = nil, false, false, nil, options.SystemPrompt
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
	args.native = args.native.Conversation()
	manager, _, err := createCLISession(cwd, args, host.streams, nil)
	if err != nil {
		args.native.Release()
		return nil, nil, err
	}
	return openHeadless(ctx, args, host.dependencies, host.streams, manager)
}

// openHeadless builds a session on manager as the CLI builds one, headless;
// close disposes it and releases args' conversation claim.
func openHeadless(ctx context.Context, args CLIArgs, dependencies cliDependencies, streams cliStreams, manager *session.SessionManager) (*agent.AgentSessionRuntime, func(), error) {
	runtime, err := newCLISessionRuntimeHost(ctx, cliSessionRuntimeHostOptions{
		Args: &args, Manager: manager, Dependencies: dependencies, Stderr: streams.Stderr, ExtensionMode: extensions.ModePrint,
	})
	if err == nil {
		if err = runtime.Session().BindExtensions(ctx); err != nil {
			runtime.Dispose(ctx)
		}
	}
	if err != nil {
		args.native.Release()
		return nil, nil, err
	}
	return runtime, func() { runtime.Dispose(context.Background()); args.native.Release() }, nil
}

func setVariable(values map[string]string, variable acp.Variable) map[string]string {
	if values == nil {
		values = map[string]string{}
	}
	values[variable.Name] = variable.Value
	return values
}

// serve runs the agent's ACP server on one connection.
func (host acpHost) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	return acp.Serve(ctx, in, out, host, version)
}

func runACP(ctx context.Context, args CLIArgs, dependencies cliDependencies, streams cliStreams) int {
	args.useUnknownModel = true
	if err := (acpHost{args: args, dependencies: dependencies, streams: streams}).serve(ctx, streams.Stdin, streams.Stdout); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	return 0
}

// chatConnectCommand is the command line of `orb chat connect` to socket,
// which a chat platform's front gives an ACP client that starts its agent as
// a command.
func chatConnectCommand(socket string) ([]string, error) {
	self, err := os.Executable()
	return []string{self, "chat", "connect", socket}, err
}

// runChatConnect relays stdio to a running `orb chat` agent's ACP socket, for
// an ACP client that starts its agent as a command. It inherits that client's
// environment, which may hold the client's own keys, so it hides it.
func runChatConnect(socket string, in io.Reader, out io.Writer) int {
	teamenv.Hide()
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
// its settings.json and models.json stay the configuration of record, and the
// project it works in is never trusted: its tools write there, and a trusted
// project's code would run in this process, with what it hides.
func teamAgent(ctx context.Context, dependencies cliDependencies, streams cliStreams) acpHost {
	if os.Getenv(toolenv.Allow) == "" {
		_ = os.Setenv(toolenv.Allow, teamTools)
	}
	teamenv.Hide()
	args := ParseArgs(nil)
	args.native, args.useUnknownModel, args.ProjectTrusted = stateFromContext(ctx), true, boolPointer(false)
	if args.native != nil {
		args.native.Files = true
	}
	return acpHost{args: args, dependencies: dependencies, streams: streams}
}
