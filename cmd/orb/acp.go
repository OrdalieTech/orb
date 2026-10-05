package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/acp"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
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
		// declared up front rather than found through tool_search. Tools named
		// _* are lifecycle hooks by Buzz's convention, never the model's.
		config := mcp.ServerConfig{
			Type: server.Type, Command: server.Command, Args: server.Args, URL: server.URL,
			Exposure: mcp.ExposureDirect, ToolExposure: mcp.ToolExposures{{Tool: "_*", Exposure: mcp.ExposureHidden}},
		}
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
// `orb chat connect` relay it runs as its agent. Its exit ends the agent, so a
// clean exit (an owner's !shutdown) stays final under the container's restart policy.
func runBuzz(ctx context.Context, host acpHost) error {
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("orb-acp-%d.sock", os.Getpid()))
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
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
	self, err := os.Executable()
	if err != nil {
		return err
	}
	harness := os.Getenv("ORB_BUZZ_ACP")
	if harness == "" {
		harness = "buzz-acp"
	}
	command := exec.CommandContext(ctx, harness)
	command.Env = append(os.Environ(), "BUZZ_ACP_AGENT_COMMAND="+self, "BUZZ_ACP_AGENT_ARGS=chat,connect,"+socket)
	command.Stdout, command.Stderr = host.streams.Stderr, host.streams.Stderr
	// buzz-acp removes its signing keyfile on SIGTERM.
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 30 * time.Second
	return command.Run()
}

// runChatConnect relays stdio to a running `orb chat buzz` agent's ACP socket.
func runChatConnect(socket string, in io.Reader, out io.Writer) int {
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
