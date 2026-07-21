package modes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

// newExtensionCommandRPCRuntime builds an RPC-mode SessionRuntime whose sole
// extension registers a "/status" command that emits a UI notify. No API key or
// model is configured, so a real prompt would fail preflight; the extension
// command must still run and its notify must reach the wire.
func newExtensionCommandRPCRuntime(t *testing.T) *codingagent.SessionRuntime {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("ext-ui"))
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(root)
	if err := registry.Register("<inline:status>", func(api extensions.API) error {
		api.RegisterCommand("status", extensions.Command{
			Handler: func(_ context.Context, _ string, commandContext extensions.CommandContext) error {
				commandContext.UI().Notify("MCP status: ok", extensions.NotifyInfo)
				return nil
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(nil, agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}}))
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, ExtensionMode: extensions.ModeRPC,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

// Findings 4 + 5: an extension command dispatched over RPC must succeed on a
// keyless install (upstream dispatches extension commands before model/API-key
// validation) AND its UI notify must be emitted as an extension_ui_request
// (upstream rebindSession binds the RPC uiContext into bindExtensions). The
// pipe stays open until both lines are observed so the async prompt goroutine
// never races with EOF teardown.
func TestRPCExtensionCommandRunsWithoutKeyAndEmitsUINotify(t *testing.T) {
	runtime := newExtensionCommandRPCRuntime(t)
	input, inputWriter := io.Pipe()
	output := &notifyCloseWriter{closeAfter: `"command":"prompt","success":true`, input: inputWriter}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- RunRPCMode(context.Background(), &rpcTestHost{runtime: runtime}, RPCModeOptions{
			Stdin: input, Stdout: output, Stderr: &stderr,
		})
	}()
	if _, err := io.WriteString(inputWriter, "{\"id\":\"1\",\"type\":\"prompt\",\"message\":\"/status\"}\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case exitCode := <-done:
		if exitCode != 0 || stderr.Len() != 0 {
			t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RPC mode did not stop")
	}

	var sawNotify, sawSuccess bool
	for _, line := range bytes.Split(bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Type    string `json:"type"`
			Method  string `json:"method"`
			Message string `json:"message"`
			Command string `json:"command"`
			Success bool   `json:"success"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			t.Fatalf("bad line %s: %v", line, err)
		}
		switch probe.Type {
		case "extension_ui_request":
			if probe.Method == "notify" && probe.Message == "MCP status: ok" {
				sawNotify = true
			}
		case "response":
			if probe.Command == "prompt" {
				if !probe.Success {
					t.Fatalf("extension command blocked by preflight: %s", line)
				}
				sawSuccess = true
			}
		}
	}
	if !sawSuccess {
		t.Fatalf("no success response for /status: %s", output.String())
	}
	if !sawNotify {
		t.Fatalf("extension_ui_request notify was never emitted: %s", output.String())
	}
}

// notifyCloseWriter closes the RPC stdin once the prompt success response has
// been written, so the async command goroutine has already emitted its notify.
type notifyCloseWriter struct {
	bytes.Buffer
	closeAfter string
	input      *io.PipeWriter
	closed     bool
}

func (writer *notifyCloseWriter) Write(data []byte) (int, error) {
	count, err := writer.Buffer.Write(data)
	if err != nil || writer.closed || !bytes.Contains(writer.Bytes(), []byte(writer.closeAfter)) {
		return count, err
	}
	writer.closed = true
	_ = writer.input.Close()
	return count, err
}

// A real (non-command) prompt with no model still fails preflight and returns a
// single failure response, confirming the reorder did not drop validation.
func TestRPCRealPromptStillFailsPreflightWithoutModel(t *testing.T) {
	runtime := newExtensionCommandRPCRuntime(t)
	var stdout, stderr bytes.Buffer
	exitCode := RunRPCMode(context.Background(), &rpcTestHost{runtime: runtime}, RPCModeOptions{
		Stdin:  strings.NewReader("{\"id\":\"2\",\"type\":\"prompt\",\"message\":\"hello\"}\n"),
		Stdout: &stdout, Stderr: io.Discard,
	})
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	lines := bytes.Split(bytes.TrimSuffix(stdout.Bytes(), []byte{'\n'}), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("expected one response line, got %d: %s", len(lines), stdout.String())
	}
	var response RPCResponse
	if err := json.Unmarshal(lines[0], &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "2" || response.Command != "prompt" || response.Success || !strings.HasPrefix(response.Error, "No model selected.") {
		t.Fatalf("preflight response = %s", lines[0])
	}
}
