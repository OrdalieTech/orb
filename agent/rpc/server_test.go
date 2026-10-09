package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
)

func TestRPCExtensionUIDialogRoundTrip(t *testing.T) {
	requests := make(chan ExtensionUIRequest, 1)
	ui := newExtensionUI(func(value any) error {
		request, ok := value.(ExtensionUIRequest)
		if !ok {
			t.Fatalf("UI output = %T", value)
		}
		requests <- request
		return nil
	})
	defer ui.close()

	result := make(chan *string, 1)
	timeout := int64(250)
	go func() {
		value, err := ui.Select(context.Background(), "Pick", []string{"a", "b"}, &timeout)
		if err != nil {
			t.Errorf("select: %v", err)
		}
		result <- value
	}()
	request := <-requests
	encoded, err := ai.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Type    string   `json:"type"`
		ID      string   `json:"id"`
		Method  string   `json:"method"`
		Title   string   `json:"title"`
		Options []string `json:"options"`
		Timeout int64    `json:"timeout"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Type != "extension_ui_request" || wire.ID == "" || wire.Method != "select" || wire.Title != "Pick" || wire.Timeout != 250 || len(wire.Options) != 2 {
		t.Fatalf("select request = %s", encoded)
	}
	selected := "b"
	ui.HandleResponse(ExtensionUIResponse{Type: "extension_ui_response", ID: wire.ID, Value: &selected})
	if value := <-result; value == nil || *value != selected {
		t.Fatalf("select result = %#v", value)
	}
}

func TestRPCExtensionUIAlreadyCancelledDoesNotEmit(t *testing.T) {
	requests := make(chan any, 1)
	ui := newExtensionUI(func(value any) error {
		requests <- value
		return nil
	})
	defer ui.close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if value, err := ui.Select(ctx, "Pick", []string{"a"}, nil); err != nil || value != nil {
		t.Fatalf("select = (%#v, %v)", value, err)
	}
	select {
	case request := <-requests:
		t.Fatalf("cancelled dialog emitted %#v", request)
	default:
	}
}

// A client that sends its next message the moment it hears something gets it run: a follow-up
// right after the first prompt's response is queued, and a plain prompt sent on agent_end waits
// for the ending run instead of being refused as "already processing".
func TestRPCImmediateNextPromptRuns(t *testing.T) {
	for name, next := range map[string]immediateWriter{
		"follow-up after the response": {after: `"id":"p1","type":"response","command":"prompt"`, send: `{"id":"p2","type":"prompt","message":"second","streamingBehavior":"followUp"}`},
		"prompt on agent_end":          {after: `"type":"agent_end"`, send: `{"id":"p2","type":"prompt","message":"second"}`},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
			if err != nil {
				t.Fatal(err)
			}
			manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("prompt-follow-up"))
			if err != nil {
				t.Fatal(err)
			}
			provider := faux.New(faux.Options{API: "faux", Provider: "faux", TokenSize: faux.FixedTokenSize(4)})
			// The first reply waits until a listener is in place that keeps the run returning a while
			// after agent_end, so the next line arrives in that window.
			gate := make(chan struct{})
			provider.SetResponses([]faux.ResponseStep{
				faux.Factory(func(context.Context, ai.Context, *ai.StreamOptions, faux.State, *ai.Model) (*ai.AssistantMessage, error) {
					<-gate
					return faux.AssistantMessage("first"), nil
				}),
				faux.AssistantMessage("second"),
			})
			model := provider.GetModel()
			created := engine.NewAgent(
				provider.StreamSimple, engine.WithInitialState(engine.AgentState{Model: model, Messages: engine.AgentMessages{}, Tools: []engine.AgentTool{}}),
				engine.WithConvertToLLM(agent.ConvertToLLM),
			)
			runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
				Agent: created, SessionManager: manager, Settings: settings,
				GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
					key := "fixture-key"
					return &key, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			input, inputWriter := io.Pipe()
			output := &next
			output.input = inputWriter
			var stderr bytes.Buffer
			done := make(chan int, 1)
			go func() {
				done <- Serve(context.Background(), &rpcTestHost{runtime: runtime}, Options{
					Input: input, Output: output, Diagnostics: &stderr,
				})
			}()
			if _, err := io.WriteString(inputWriter, `{"id":"p1","type":"prompt","message":"first"}`+"\n"); err != nil {
				t.Fatal(err)
			}
			for provider.State().CallCount == 0 {
				time.Sleep(time.Millisecond)
			}
			defer runtime.Subscribe(func(event any) {
				if _, ok := event.(agent.SessionAgentEndEvent); ok {
					time.Sleep(100 * time.Millisecond)
				}
			})()
			close(gate)
			deadline := time.Now().Add(10 * time.Second)
			for provider.State().CallCount != 2 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if calls := provider.State().CallCount; calls != 2 {
				t.Fatalf("provider calls = %d, want 2 turns\n%s", calls, output.Bytes())
			}
			if err := runtime.WaitForIdle(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := inputWriter.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case exitCode := <-done:
				if exitCode != 0 || stderr.Len() != 0 {
					t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("RPC mode did not stop")
			}
			for _, id := range []string{"p1", "p2"} {
				if !bytes.Contains(output.Bytes(), []byte(`"id":"`+id+`","type":"response","command":"prompt","success":true`)) {
					t.Fatalf("missing successful %s response in %s", id, output.Bytes())
				}
			}
		})
	}
}

type rpcTestHost struct{ runtime *agent.SessionRuntime }

func (host *rpcTestHost) Session() *agent.SessionRuntime     { return host.runtime }
func (*rpcTestHost) NewSession(string) (bool, error)         { return true, nil }
func (*rpcTestHost) SwitchSession(string) (bool, error)      { return true, nil }
func (*rpcTestHost) Fork(string, bool) (string, bool, error) { return "", true, nil }
func (host *rpcTestHost) Dispose()                           { host.runtime.Dispose() }

func TestServeTerminateInterruptsBlockedOutput(t *testing.T) {
	for _, exitCode := range []int{143, 129} {
		for _, eof := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/eof=%t", exitCode, eof), func(t *testing.T) {
				root := t.TempDir()
				settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
				if err != nil {
					t.Fatal(err)
				}
				manager, err := sessionstore.InMemory(root)
				if err != nil {
					t.Fatal(err)
				}
				runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
					Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings,
				})
				if err != nil {
					t.Fatal(err)
				}
				synctest.Test(t, func(t *testing.T) {
					input, inputWriter := io.Pipe()
					defer func() { _ = inputWriter.Close() }()
					writer := &blockedFrameWriter{started: make(chan struct{}), release: make(chan struct{})}
					defer close(writer.release)
					terminate := make(chan int, 1)
					done := make(chan int, 1)
					go func() {
						done <- Serve(context.Background(), &rpcTestHost{runtime: runtime}, Options{
							Input: input, Output: writer, Terminate: terminate,
						})
					}()
					if _, err := io.WriteString(inputWriter, "{\"type\":\"get_state\"}\n"); err != nil {
						t.Fatal(err)
					}
					<-writer.started
					if eof {
						if err := inputWriter.Close(); err != nil {
							t.Fatal(err)
						}
					}
					synctest.Wait()
					terminate <- exitCode
					select {
					case code := <-done:
						if code != exitCode {
							t.Fatalf("exit=%d want=%d", code, exitCode)
						}
					case <-time.After(time.Second):
						t.Fatal("termination waited for blocked RPC output")
					}
				})
			})
		}
	}
}

func TestFrameWriterStopsAfterWriterFailure(t *testing.T) {
	writer := &failRPCWriter{}
	output := NewFrameWriter(writer)
	output.WriteFrame([]byte(`{"first":true}`))
	output.WriteFrame([]byte(`{"second":true}`))
	err := output.Close()
	if err == nil || writer.calls != 1 {
		t.Fatalf("error = %v, writes = %d", err, writer.calls)
	}
}

func TestRPCEOFAbortsRunningCommandBeforeWaiting(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("eof"))
	if err != nil {
		t.Fatal(err)
	}
	created := engine.NewAgent(nil, engine.WithInitialState(engine.AgentState{Messages: engine.AgentMessages{}}))
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Serve(context.Background(), &rpcTestHost{runtime: runtime}, Options{
			Input:  strings.NewReader("{\"id\":\"b\",\"type\":\"bash\",\"command\":\"sleep 30\"}\n"),
			Output: &stdout, Diagnostics: &stderr,
		})
	}()
	select {
	case exitCode := <-done:
		if exitCode != 0 || stderr.Len() != 0 {
			t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC EOF waited for the uncancelled bash command")
	}
}

type failRPCWriter struct{ calls int }

func (writer *failRPCWriter) Write([]byte) (int, error) {
	writer.calls++
	return 0, io.ErrClosedPipe
}

// immediateWriter sends a line to the server's input the moment it writes one containing after.
type immediateWriter struct {
	bytes.Buffer
	after, send string
	input       *io.PipeWriter
	injected    bool
}

func (writer *immediateWriter) Write(data []byte) (int, error) {
	count, err := writer.Buffer.Write(data)
	if err != nil || writer.injected || !bytes.Contains(data, []byte(writer.after)) {
		return count, err
	}
	writer.injected = true
	if _, writeErr := io.WriteString(writer.input, writer.send+"\n"); writeErr != nil {
		return count, writeErr
	}
	// Keep this write active long enough for the input loop to dispatch the line while the
	// run that wrote it is still returning.
	time.Sleep(20 * time.Millisecond)
	return count, nil
}
