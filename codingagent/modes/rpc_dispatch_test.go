package modes

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/harness"
	"github.com/OrdalieTech/orb/codingagent"
	"github.com/OrdalieTech/orb/codingagent/config"
	"github.com/OrdalieTech/orb/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/orb/codingagent/session"
)

func newRPCDispatchRuntime(t *testing.T, sessionID string) *codingagent.SessionRuntime {
	t.Helper()
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(nil), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

// Goldens captured from upstream at the pinned commit:
// printf '<frames>' | pi --mode rpc (rpc-mode.ts:695-698,735-770 dispatches
// untyped, so non-string type members reach the Unknown command default).
func TestRPCUnknownCommandDispatchForNonStringType(t *testing.T) {
	inputs := []string{
		`{"type":5}`,
		`{"type":true}`,
		`{"type":null}`,
		`{"type":{}}`,
		`{"message":"x"}`,
		`{"type":5,"id":"x"}`,
		`{"type":[1,2]}`,
		`{"type":1e-7}`,
		`{"type":5.5,"id":7}`,
	}
	want := []string{
		`{"type":"response","command":5,"success":false,"error":"Unknown command: 5"}`,
		`{"type":"response","command":true,"success":false,"error":"Unknown command: true"}`,
		`{"type":"response","command":null,"success":false,"error":"Unknown command: null"}`,
		`{"type":"response","command":{},"success":false,"error":"Unknown command: [object Object]"}`,
		`{"type":"response","success":false,"error":"Unknown command: undefined"}`,
		`{"id":"x","type":"response","command":5,"success":false,"error":"Unknown command: 5"}`,
		`{"type":"response","command":[1,2],"success":false,"error":"Unknown command: 1,2"}`,
		`{"type":"response","command":1e-7,"success":false,"error":"Unknown command: 1e-7"}`,
		`{"id":7,"type":"response","command":5.5,"success":false,"error":"Unknown command: 5.5"}`,
	}
	runtime := newRPCDispatchRuntime(t, "unknown-type-dispatch")
	var stdout, stderr bytes.Buffer
	exitCode := RunRPCMode(context.Background(), &rpcTestHost{runtime: runtime}, RPCModeOptions{
		Stdin:  strings.NewReader(strings.Join(inputs, "\n") + "\n"),
		Stdout: &stdout, Stderr: &stderr,
	})
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("emitted %d lines, want %d:\n%s", len(got), len(want), stdout.String())
	}
	for index, line := range got {
		if line != want[index] {
			t.Errorf("frame %d:\n got %s\nwant %s", index, line, want[index])
		}
	}
}

// Upstream echoes the parsed id/type values, so JSON.stringify canonicalizes
// non-canonical input bytes: 5.0 -> 5, -0 -> 0, interior whitespace dropped,
// object key order preserved (rpc-mode.ts:695-698).
func TestRPCUnknownCommandCanonicalizesEchoedJSON(t *testing.T) {
	inputs := []string{
		`{"type":5.0}`,
		`{"type":5.50,"id":2.0}`,
		`{"type":{"b": 1, "a":[1.0, -0]}}`,
	}
	want := []string{
		`{"type":"response","command":5,"success":false,"error":"Unknown command: 5"}`,
		`{"id":2,"type":"response","command":5.5,"success":false,"error":"Unknown command: 5.5"}`,
		`{"type":"response","command":{"b":1,"a":[1,0]},"success":false,"error":"Unknown command: [object Object]"}`,
	}
	runtime := newRPCDispatchRuntime(t, "unknown-type-canonical")
	var stdout, stderr bytes.Buffer
	exitCode := RunRPCMode(context.Background(), &rpcTestHost{runtime: runtime}, RPCModeOptions{
		Stdin:  strings.NewReader(strings.Join(inputs, "\n") + "\n"),
		Stdout: &stdout, Stderr: &stderr,
	})
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("emitted %d lines, want %d:\n%s", len(got), len(want), stdout.String())
	}
	for index, line := range got {
		if line != want[index] {
			t.Errorf("frame %d:\n got %s\nwant %s", index, line, want[index])
		}
	}
}

func TestRPCExtensionShutdownHonoredAfterCommand(t *testing.T) {
	runtime := newRPCDispatchRuntime(t, "extension-shutdown")
	var stdout bytes.Buffer
	rpcContext, cancel := context.WithCancel(context.Background())
	mode := &rpcMode{
		ctx: rpcContext, cancel: cancel, host: &rpcTestHost{runtime: runtime},
		output: newSerializedOutput(&stdout),
	}
	mode.ui = newRPCExtensionUI(mode.writeObject)
	if err := mode.bindSession(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		mode.dispose()
		_ = mode.output.closeAndWait()
	}()

	// The RPC shutdownHandler only records the request (rpc-mode.ts:344-346).
	mode.requestExtensionShutdown()
	select {
	case <-rpcContext.Done():
		t.Fatal("shutdown ran before the next command settled")
	default:
	}

	var commands sync.WaitGroup
	mode.handleLine([]byte(`{"id":"s","type":"get_state"}`), &commands)
	commands.Wait()
	select {
	case <-rpcContext.Done():
	default:
		t.Fatal("shutdown request not honored after command completed")
	}
}

func TestRPCBashHonorsUserBashInterception(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(root)
	exitCode := 7
	if err := registry.Register("<inline:rpc-bash>", func(api extensions.API) error {
		api.On(extensions.EventUserBash, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return extensions.UserBashResult{Result: &extensions.BashResult{Output: "handled", ExitCode: &exitCode}}, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(nil), SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	mode := &rpcMode{ctx: context.Background()}
	response := mode.handleCommand(runtime, RPCCommand{Type: "bash", Command: "printf local"})
	if response == nil || !response.Success {
		t.Fatalf("response = %#v", response)
	}
	encoded, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"output":"handled"`) || strings.Contains(string(encoded), "local") {
		t.Fatalf("bash data = %s", encoded)
	}
	messages := runtime.State().Messages
	recorded, ok := messages[len(messages)-1].(harness.BashExecutionMessage)
	if !ok || recorded.Command != "printf local" || recorded.Output != "handled" {
		t.Fatalf("recorded bash = %#v", messages[len(messages)-1])
	}
}

// Upstream awaits checkShutdownRequested after EVERY handled command,
// including the unknown default (rpc-mode.ts:766-771), so a pending extension
// shutdown must not be deferred past an untyped frame.
func TestRPCExtensionShutdownHonoredAfterUntypedCommand(t *testing.T) {
	runtime := newRPCDispatchRuntime(t, "extension-shutdown-untyped")
	var stdout bytes.Buffer
	rpcContext, cancel := context.WithCancel(context.Background())
	mode := &rpcMode{
		ctx: rpcContext, cancel: cancel, host: &rpcTestHost{runtime: runtime},
		output: newSerializedOutput(&stdout),
	}
	mode.ui = newRPCExtensionUI(mode.writeObject)
	if err := mode.bindSession(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		mode.dispose()
		_ = mode.output.closeAndWait()
	}()

	mode.requestExtensionShutdown()
	var commands sync.WaitGroup
	mode.handleLine([]byte(`{"type":5}`), &commands)
	commands.Wait()
	select {
	case <-rpcContext.Done():
	default:
		t.Fatal("shutdown request not honored after untyped command")
	}
}

func TestRPCCheckShutdownRequestedWithoutRequestIsNoop(t *testing.T) {
	rpcContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	mode := &rpcMode{ctx: rpcContext, cancel: cancel}
	mode.checkShutdownRequested()
	select {
	case <-rpcContext.Done():
		t.Fatal("checkShutdownRequested cancelled without a request")
	default:
	}
}
