package mcp

// Regression tests for the July 2026 real-world compat sweep, cluster "mcp":
// dead children, concurrent startup connects and quiet child shutdown.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExecuteMarksServerFailedAfterChildDies(t *testing.T) {
	if os.Getenv("ORB_MCP_CRASH_HELPER") == "1" {
		return
	}
	manager := NewManager(t.TempDir(), []testServer{{
		Name: "crashy", Command: os.Args[0], Args: []string{"-test.run=^TestMCPCrashStdioHelper$"}, Env: map[string]string{"ORB_MCP_CRASH_HELPER": "1"},
	}})
	runner, active := registerManager(t, manager)
	defer closeManager(t, manager)
	tools := runner.AllRegisteredTools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, status = %#v", len(tools), manager.Status())
	}
	active.set([]string{tools[0].Definition.Name})
	_, err := extensions.WrapRegisteredTool(tools[0], runner).Execute(context.Background(), "crash-call", map[string]any{}, nil)
	if err == nil {
		t.Fatal("crash call succeeded")
	}
	// The next call reconnects.
	if status := manager.Status()[0]; status.State != ServerFailed {
		t.Fatalf("dead child left a connected-looking server (call error %v): %#v", err, status)
	}
}

func TestMCPCrashStdioHelper(t *testing.T) {
	if os.Getenv("ORB_MCP_CRASH_HELPER") != "1" {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "crash-helper", Version: "1"}, nil)
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: "crash"}, func(context.Context, *mcpsdk.CallToolRequest, map[string]any) (*mcpsdk.CallToolResult, any, error) {
		os.Exit(1)
		return nil, nil, nil
	})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestStartConnectsServersConcurrently(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "parallel", Version: "1"}, nil)
	addTextTool(server, "ping")
	manager := NewManager(t.TempDir(), []testServer{
		{Name: "one", Command: "in-memory"},
		{Name: "two", Command: "in-memory"},
	})
	base := inMemoryConnector(server)
	release := make(chan struct{})
	var arrivals atomic.Int64
	manager.connect = func(connectCtx, lifecycleCtx context.Context, config ServerConfig, options *mcpsdk.ClientOptions, tracker progressTracker) (*mcpsdk.ClientSession, error) {
		if arrivals.Add(1) == 2 {
			close(release)
		}
		select {
		case <-release:
		case <-time.After(2 * time.Second):
			return nil, errors.New("startup connects are serialized")
		}
		return base(connectCtx, lifecycleCtx, config, options, tracker)
	}
	runner, _ := registerManager(t, manager)
	defer closeManager(t, manager)
	for _, status := range manager.Status() {
		if status.State != ServerConnected {
			t.Fatalf("server %s = %#v", status.Name, status)
		}
	}
	if tools := runner.AllRegisteredTools(); len(tools) != 2 {
		t.Fatalf("tools = %d", len(tools))
	}
}

func TestCloseIgnoresStdioChildExitStatus(t *testing.T) {
	if os.Getenv("ORB_MCP_STUBBORN_HELPER") == "1" {
		return
	}
	manager := NewManager(t.TempDir(), []testServer{{Name: "stubborn", Command: "unused"}})
	manager.connect = func(connectCtx, lifecycleCtx context.Context, _ ServerConfig, options *mcpsdk.ClientOptions, tracker progressTracker) (*mcpsdk.ClientSession, error) {
		command := exec.CommandContext(lifecycleCtx, os.Args[0], "-test.run=^TestMCPStubbornStdioHelper$")
		command.Env = append(os.Environ(), "ORB_MCP_STUBBORN_HELPER=1")
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "orb-test", Version: "0"}, options)
		transport := &mcpsdk.CommandTransport{Command: command, TerminateDuration: 50 * time.Millisecond}
		return client.Connect(connectCtx, tracker.wrapTransport(transport), nil)
	}
	runner, _ := registerManager(t, manager)
	if len(runner.AllRegisteredTools()) != 1 {
		t.Fatalf("status = %#v", manager.Status())
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("shutdown of a stdio child surfaced its exit status: %v", err)
	}
}

func TestMCPStubbornStdioHelper(t *testing.T) {
	if os.Getenv("ORB_MCP_STUBBORN_HELPER") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stubborn-helper", Version: "1"}, nil)
	addTextTool(server, "ping")
	_ = server.Run(context.Background(), &mcpsdk.StdioTransport{})
	time.Sleep(time.Hour) // ignore stdin EOF and SIGTERM so only SIGKILL ends the child
}

func TestStartupConnectFailureIsRecorded(t *testing.T) {
	manager := NewManager(t.TempDir(), []testServer{{Name: "broken", Command: "broken", Timeout: 0.025}})
	manager.connect = func(context.Context, context.Context, ServerConfig, *mcpsdk.ClientOptions, progressTracker) (*mcpsdk.ClientSession, error) {
		return nil, errors.New("dial failed")
	}
	_, _ = registerManager(t, manager)
	defer closeManager(t, manager)
	if status := manager.Status()[0]; status.State != ServerFailed || status.Error != "dial failed" {
		t.Fatalf("status = %#v", status)
	}
}
