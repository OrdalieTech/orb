package rpc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/rpc"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
)

// memoryDocument is an in-process host.Document, so the session reads no
// settings, credentials or model catalogs from the host filesystem.
type memoryDocument struct {
	mu   sync.Mutex
	data []byte
}

func (document *memoryDocument) Read(context.Context) ([]byte, error) {
	document.mu.Lock()
	defer document.mu.Unlock()
	return document.data, nil
}

func (document *memoryDocument) Update(_ context.Context, update func([]byte) ([]byte, error)) error {
	document.mu.Lock()
	defer document.mu.Unlock()
	next, err := update(document.data)
	if err == nil {
		document.data = next
	}
	return err
}

const fixedNow = 1_700_000_000_000

func newPipeRuntime(t *testing.T) *agent.AgentSessionRuntime {
	t.Helper()
	const cwd, agentDir = "/orb-rpc-pipe/workspace", "/orb-rpc-pipe/agent"
	now := func() int64 { return fixedNow }
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir), config.WithGlobalDocument(&memoryDocument{}), config.WithProjectTrusted(false))
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := config.NewAuthStorageWithDocument(&memoryDocument{})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistryWithDocuments(agentDir, credentials, &memoryDocument{}, &memoryDocument{}, false)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd, sessionstore.WithSessionID("rpc-pipe"), sessionstore.WithClock(func() time.Time { return time.UnixMilli(fixedNow) }))
	if err != nil {
		t.Fatal(err)
	}
	provider := faux.New(faux.Options{API: "faux", Provider: "faux", TokenSize: faux.FixedTokenSize(4), Now: now})
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("hello over a pipe")})
	runtime, err := agent.NewAgentSessionRuntime(context.Background(), agent.AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, Model: provider.GetModel(), ThinkingLevel: ai.ModelThinkingOff,
		StreamFn: provider.StreamSimple, Clock: now, NoTools: "all",
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			key := "fixture-key"
			return &key, nil
		},
		SessionManager: manager, Settings: settings, ModelRegistry: registry, Resources: &agent.Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

// TestServeOverInMemoryPipes drives a full embedded session over io.Pipe, the
// transport every non-process host (browser worker, Worker/Celld, WASI,
// mobile) uses, and asserts the kernel frames a pi RPC client reads.
func TestServeOverInMemoryPipes(t *testing.T) {
	runtime := newPipeRuntime(t)
	host, err := rpc.NewRuntimeHost(context.Background(), runtime, true)
	if err != nil {
		t.Fatal(err)
	}
	commandReader, commandWriter := io.Pipe()
	frameReader, frameWriter := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- rpc.Serve(context.Background(), host, rpc.Options{Input: commandReader, Output: frameWriter})
		_ = frameWriter.Close()
	}()
	frames := make(chan []byte)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(frameReader)
		for scanner.Scan() {
			frames <- append([]byte(nil), scanner.Bytes()...)
		}
	}()
	next := func() []byte {
		t.Helper()
		select {
		case frame, ok := <-frames:
			if !ok {
				t.Fatal("frame stream ended early")
			}
			return frame
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a frame")
		}
		return nil
	}
	send := func(frame string) {
		t.Helper()
		if _, err := io.WriteString(commandWriter, frame+"\n"); err != nil {
			t.Fatal(err)
		}
	}

	send(`{"id":"p1","type":"prompt","message":"hi"}`)
	// Upstream answers the prompt once preflight succeeds, before the run's
	// first event.
	if frame, want := next(), `{"id":"p1","type":"response","command":"prompt","success":true}`; string(frame) != want {
		t.Fatalf("prompt response = %s, want %s", frame, want)
	}
	var types, roles []string
	var assistantEnd json.RawMessage
	for {
		frame := next()
		var header struct {
			Type                  string          `json:"type"`
			Message               json.RawMessage `json:"message"`
			AssistantMessageEvent map[string]any  `json:"assistantMessageEvent"`
		}
		if err := json.Unmarshal(frame, &header); err != nil {
			t.Fatalf("frame %s: %v", frame, err)
		}
		if len(types) == 0 || types[len(types)-1] != header.Type {
			types = append(types, header.Type)
		}
		switch header.Type {
		case "message_update":
			// RPC and JSON modes stream message_update delta-only (json-event.ts).
			if _, partial := header.AssistantMessageEvent["partial"]; partial || header.Message != nil {
				t.Fatalf("message_update is not delta-only: %s", frame)
			}
		case "message_end":
			var message struct {
				Role string `json:"role"`
			}
			if err := json.Unmarshal(header.Message, &message); err != nil {
				t.Fatal(err)
			}
			roles = append(roles, message.Role)
			assistantEnd = header.Message
		}
		if header.Type == string(agent.EventAgentSettled) {
			break
		}
	}
	wantTypes := []string{
		"agent_start", "turn_start", "message_start", "message_end", "message_start", "message_end",
		"message_start", "message_update", "message_end", "turn_end", "agent_end", string(agent.EventAgentSettled),
	}
	if !slices.Equal(types, wantTypes) {
		t.Fatalf("event types = %v, want %v", types, wantTypes)
	}
	if want := []string{"system", "user", "assistant"}; !slices.Equal(roles, want) {
		t.Fatalf("message_end roles = %v, want %v", roles, want)
	}
	var assistant struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(assistantEnd, &assistant); err != nil {
		t.Fatal(err)
	}
	if assistant.Role != "assistant" || assistant.StopReason != "stop" || len(assistant.Content) != 1 || assistant.Content[0].Text != "hello over a pipe" {
		t.Fatalf("assistant message_end = %s", assistantEnd)
	}

	// The transcript-backed system message counts (pi v0.86): system, user, assistant.
	send(`{"id":"s1","type":"get_state"}`)
	var state struct {
		ID      string `json:"id"`
		Command string `json:"command"`
		Success bool   `json:"success"`
		Data    struct {
			SessionID    string `json:"sessionId"`
			MessageCount int    `json:"messageCount"`
			IsStreaming  bool   `json:"isStreaming"`
		} `json:"data"`
	}
	if frame := next(); json.Unmarshal(frame, &state) != nil || state.ID != "s1" || state.Command != "get_state" || !state.Success ||
		state.Data.SessionID != "rpc-pipe" || state.Data.MessageCount != 3 || state.Data.IsStreaming {
		t.Fatalf("get_state response = %s", frame)
	}

	if err := commandWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("Serve exit = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop at end of input")
	}
	if frame, ok := <-frames; ok {
		t.Fatalf("unexpected frame after shutdown: %s", frame)
	}
}

// TestServeTerminateReturnsHostExitCode covers the host-driven shutdown path
// that replaces process signal handling inside the server.
func TestServeTerminateReturnsHostExitCode(t *testing.T) {
	runtime := newPipeRuntime(t)
	host, err := rpc.NewRuntimeHost(context.Background(), runtime, true)
	if err != nil {
		t.Fatal(err)
	}
	commandReader, commandWriter := io.Pipe()
	defer func() { _ = commandWriter.Close() }()
	terminate := make(chan int, 1)
	terminate <- 143
	code := rpc.Serve(context.Background(), host, rpc.Options{Input: commandReader, Output: io.Discard, Terminate: terminate})
	if code != 143 {
		t.Fatalf("Serve exit = %d, want 143", code)
	}
}
