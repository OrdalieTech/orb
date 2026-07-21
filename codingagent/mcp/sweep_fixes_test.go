package mcp

// Regression tests for the July 2026 real-world compat sweep, cluster "mcp":
// per-entry config isolation, the "disabled" key, registry-Fresh rebind
// idempotency, dead-child tool deactivation, concurrent startup connects,
// quiet child shutdown, and the maxRetries SDK translation.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseSettingsIsolatesInvalidEntries(t *testing.T) {
	settings := decodeSettings(t, `{
		"mcpServers": {
			"bad": {"url": "https://example.test/mcp", "args": ["--oops"]},
			"memory": {"command": "mcp-server-memory"},
			" ": {"command": "unnamed"}
		}
	}`)
	servers, warnings, err := ParseSettingsWithWarnings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "memory" || servers[0].Command != "mcp-server-memory" {
		t.Fatalf("one invalid entry disabled the valid servers: %#v", servers)
	}
	want := []string{
		"mcpServers: server name must not be empty",
		"mcpServers.bad: args, env, and cwd require a stdio command",
	}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("warnings = %q, want %q", warnings, want)
	}
	legacy, err := ParseSettings(settings)
	if err != nil || len(legacy) != 1 || legacy[0].Name != "memory" {
		t.Fatalf("ParseSettings servers = %#v, error = %v", legacy, err)
	}
}

func TestParseSettingsHonorsDisabledTrue(t *testing.T) {
	servers, warnings, err := ParseSettingsWithWarnings(decodeSettings(t, `{
		"mcpServers": {
			"off": {"command": "x", "disabled": true},
			"on": {"command": "y", "disabled": false},
			"both": {"command": "z", "disabled": false, "enabled": false}
		}
	}`))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("warnings = %q, error = %v", warnings, err)
	}
	if len(servers) != 1 || servers[0].Name != "on" {
		t.Fatalf(`"disabled": true was not honored like "enabled": false: %#v`, servers)
	}
}

func TestSDKMaxRetriesTranslation(t *testing.T) {
	retries := func(value int) *int { return &value }
	tests := []struct {
		name       string
		configured *int
		want       int
	}{
		{"unset keeps the SDK default", nil, 0},
		{"explicit zero disables retries", retries(0), -1},
		{"negative one disables retries", retries(-1), -1},
		{"positive passes through", retries(3), 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sdkMaxRetries(test.configured); got != test.want {
				t.Fatalf("sdkMaxRetries(%v) = %d, want %d", test.configured, got, test.want)
			}
		})
	}
}

func TestRegistryFreshKeepsMCPToolsRegistered(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fresh", Version: "1"}, nil)
	addTextTool(server, "ping")
	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "fresh", Command: "in-memory"}})
	var connects atomic.Int64
	base := inMemoryConnector(server)
	manager.connect = func(connectCtx, lifecycleCtx context.Context, config ServerConfig, options *mcpsdk.ClientOptions, tracker progressTracker) (*mcpsdk.ClientSession, error) {
		connects.Add(1)
		return base(connectCtx, lifecycleCtx, config, options, tracker)
	}
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<mcp>", manager.Extension(), extensions.WithHidden(true)); err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)

	fresh, err := registry.Fresh(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	active := &activeTools{}
	runner := extensions.NewRunner(fresh, extensions.RunnerOptions{Actions: extensions.Actions{
		GetActiveTools: func() ([]string, error) { return active.get(), nil },
		SetActiveTools: func(names []string) error {
			active.set(names)
			return nil
		},
	}})
	tools := runner.AllRegisteredTools()
	if len(tools) != 1 || tools[0].Definition.Name != registeredToolName("fresh", "ping") {
		t.Fatalf("fresh registry lost the MCP tools: %#v", tools)
	}
	if connects.Load() != 1 {
		t.Fatalf("factory rebind reconnected the server: %d connects", connects.Load())
	}
	active.set([]string{tools[0].Definition.Name})
	result, err := extensions.WrapRegisteredTool(tools[0], runner).Execute(context.Background(), "fresh-call", map[string]any{}, nil)
	if err != nil || contentText(result.Content) != "ping" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestExecuteDeactivatesToolsAfterChildDies(t *testing.T) {
	if os.Getenv("PIGO_MCP_CRASH_HELPER") == "1" {
		return
	}
	manager := NewManager(t.TempDir(), []ServerConfig{{
		Name: "crashy", Command: os.Args[0], Args: []string{"-test.run=^TestMCPCrashStdioHelper$"}, Env: map[string]string{"PIGO_MCP_CRASH_HELPER": "1"},
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
	status := manager.Status()[0]
	if status.State != ServerError || len(status.Tools) != 0 {
		t.Fatalf("dead child left a connected-looking tool set (call error %v): %#v", err, status)
	}
	if remaining := active.get(); len(remaining) != 0 {
		t.Fatalf("active tools after the child died = %v", remaining)
	}
}

func TestMCPCrashStdioHelper(t *testing.T) {
	if os.Getenv("PIGO_MCP_CRASH_HELPER") != "1" {
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
	manager := NewManager(t.TempDir(), []ServerConfig{
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
	if os.Getenv("PIGO_MCP_STUBBORN_HELPER") == "1" {
		return
	}
	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "stubborn", Command: "unused"}})
	manager.connect = func(connectCtx, lifecycleCtx context.Context, _ ServerConfig, options *mcpsdk.ClientOptions, tracker progressTracker) (*mcpsdk.ClientSession, error) {
		command := exec.CommandContext(lifecycleCtx, os.Args[0], "-test.run=^TestMCPStubbornStdioHelper$")
		command.Env = append(os.Environ(), "PIGO_MCP_STUBBORN_HELPER=1")
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "pigo-test", Version: "0"}, options)
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
	if os.Getenv("PIGO_MCP_STUBBORN_HELPER") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stubborn-helper", Version: "1"}, nil)
	addTextTool(server, "ping")
	_ = server.Run(context.Background(), &mcpsdk.StdioTransport{})
	time.Sleep(time.Hour) // ignore stdin EOF and SIGTERM so only SIGKILL ends the child
}

func TestExtensionWarnsWhenStartupConnectFails(t *testing.T) {
	manager := NewManager(t.TempDir(), []ServerConfig{{Name: "broken", Command: "broken", TimeoutMS: 25}})
	manager.connect = func(context.Context, context.Context, ServerConfig, *mcpsdk.ClientOptions, progressTracker) (*mcpsdk.ClientSession, error) {
		return nil, errors.New("dial failed")
	}
	var output bytes.Buffer
	manager.warnOutput = &output
	_, _ = registerManager(t, manager)
	defer closeManager(t, manager)
	if got := output.String(); !strings.Contains(got, "Warning: mcp: broken: dial failed") {
		t.Fatalf("startup failure warning = %q", got)
	}
}
