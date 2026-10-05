package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/chat"
	"github.com/OrdalieTech/orb/chat/telegram"
	"github.com/OrdalieTech/orb/engine"
)

// acpClient drives `orb --mode acp` over its stdio, as Zed or buzz-acp does.
type acpClient struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Reader
	done   chan int
	nextID int
}

// scriptedRuntime is the real runtime (settings, plugins, the system prompt
// with a client's prompt in it) on a scripted model.
func scriptedRuntime(provider *faux.Provider) cliDependencies {
	return cliDependencies{createRuntime: func(cwd string, args CLIArgs, prior engine.AgentMessages) (runtimeInputs, error) {
		inputs, err := createRuntimeInputs(cwd, args, prior)
		if err != nil {
			return inputs, err
		}
		state := inputs.Agent.State()
		state.Model = provider.GetModel()
		inputs.Agent = engine.NewAgent(provider.StreamSimple, engine.WithInitialState(state), engine.WithConvertToLLM(agent.ConvertToLLM))
		inputs.StreamFn = provider.StreamSimple
		inputs.AvailableModels = func() []ai.Model { return []ai.Model{*provider.GetModel()} }
		inputs.GetAPIKey, inputs.GetRequestAuth, inputs.ModelRegistry = nil, nil, nil
		return inputs, nil
	}}
}

func startACP(t *testing.T, provider *faux.Provider) *acpClient {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := &acpClient{t: t, in: stdinWriter, out: bufio.NewReader(stdoutReader), done: make(chan int, 1)}
	go func() {
		client.done <- runCLIWithDependencies(context.Background(), []string{"--mode", "acp"}, cliStreams{
			Stdin: stdinReader, Stdout: stdoutWriter, Stderr: io.Discard,
		}, scriptedRuntime(provider))
		_ = stdoutWriter.Close()
	}()
	return client
}

func (client *acpClient) call(method string, params any) (map[string]any, []map[string]any) {
	client.t.Helper()
	client.nextID++
	id := client.nextID
	frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if _, err := client.in.Write(append(frame, '\n')); err != nil {
		client.t.Fatal(err)
	}
	var notifications []map[string]any
	deadline := time.AfterFunc(10*time.Second, func() { _ = client.in.CloseWithError(io.ErrUnexpectedEOF) })
	defer deadline.Stop()
	for {
		line, err := client.out.ReadBytes('\n')
		if err != nil {
			client.t.Fatalf("%s: %v", method, err)
		}
		var message map[string]any
		if err := json.Unmarshal(line, &message); err != nil {
			client.t.Fatalf("%s: frame %q: %v", method, line, err)
		}
		if message["id"] == float64(id) {
			return message, notifications
		}
		notifications = append(notifications, message)
	}
}

func (client *acpClient) close() {
	client.t.Helper()
	_ = client.in.Close()
	if code := <-client.done; code != 0 {
		client.t.Fatalf("orb --mode acp exited %d", code)
	}
}

func updates(notifications []map[string]any, kind string) []map[string]any {
	var found []map[string]any
	for _, notification := range notifications {
		params, _ := notification["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] == kind {
			found = append(found, update)
		}
	}
	return found
}

func TestACPClientRunsAndReopensAnOrbSession(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "notes.txt"), []byte("the launch is on Tuesday"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("HOME", root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))

	provider := faux.New(faux.Options{API: "faux", Provider: "faux"})
	var systemPrompt string
	provider.SetResponses([]faux.ResponseStep{
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			systemPrompt = *request.SystemPrompt
			return faux.AssistantMessage(faux.ToolCall("read", map[string]any{"path": "notes.txt"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}), nil
		}),
		faux.AssistantMessage("Tuesday."),
		faux.AssistantMessage("", faux.AssistantMessageOptions{StopReason: ai.StopReasonError, ErrorMessage: new("provider unavailable")}),
	})
	client := startACP(t, provider)

	initialized, _ := client.call("initialize", map[string]any{"protocolVersion": 2, "clientCapabilities": map[string]any{}})
	result := initialized["result"].(map[string]any)
	if result["protocolVersion"] != float64(2) || result["agentInfo"].(map[string]any)["name"] != "orb" {
		t.Fatalf("initialize = %v", initialized)
	}

	// Buzz's harness prompt replaces Orb's base prompt, as --system-prompt does.
	created, _ := client.call("session/new", map[string]any{
		"cwd": project, "mcpServers": []any{}, "systemPrompt": "BUZZ_BASE: you answer in the team channel.",
		"_meta": map[string]any{"sessionTitle": "launch planning"},
	})
	sessionID, _ := created["result"].(map[string]any)["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("session/new = %v", created)
	}

	answered, notifications := client.call("session/prompt", map[string]any{
		"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "When is the launch?"}},
	})
	if result, _ := answered["result"].(map[string]any); result["stopReason"] != "end_turn" {
		t.Fatalf("prompt = %v", answered)
	}
	if strings.Count(systemPrompt, "BUZZ_BASE") != 1 {
		t.Fatalf("system prompt = %q", systemPrompt)
	}
	calls, results := updates(notifications, "tool_call"), updates(notifications, "tool_call_update")
	if len(calls) != 1 || calls[0]["kind"] != "read" || len(results) != 1 || results[0]["status"] != "completed" ||
		!strings.Contains(results[0]["content"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"].(string), "Tuesday") {
		t.Fatalf("tool updates = %v / %v", calls, results)
	}
	if chunks := updates(notifications, "agent_message_chunk"); len(chunks) == 0 || chunks[len(chunks)-1]["content"].(map[string]any)["text"] != "Tuesday." {
		t.Fatalf("message chunks = %v", chunks)
	}
	if usage := notifications[len(notifications)-1]; usage["method"] != "_goose/unstable/session/update" {
		t.Fatalf("last notification = %v, want the turn's usage", usage)
	}

	// A failed model call fails the prompt request; the session stays usable.
	failed, _ := client.call("session/prompt", map[string]any{
		"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "And the venue?"}},
	})
	if failed["error"].(map[string]any)["message"] != "provider unavailable" {
		t.Fatalf("failed prompt = %v", failed)
	}
	client.close()

	// Another connection, as after a restart, loads the stored conversation.
	client = startACP(t, provider)
	client.call("initialize", map[string]any{"protocolVersion": 1})
	loaded, history := client.call("session/load", map[string]any{"sessionId": sessionID, "cwd": project, "mcpServers": []any{}})
	if loaded["error"] != nil {
		t.Fatalf("session/load = %v", loaded)
	}
	users, replies := updates(history, "user_message_chunk"), updates(history, "agent_message_chunk")
	if len(users) != 2 || users[0]["content"].(map[string]any)["text"] != "When is the launch?" ||
		len(replies) != 1 || replies[0]["content"].(map[string]any)["text"] != "Tuesday." {
		t.Fatalf("replayed history = %v", history)
	}
	client.close()
}

// One agent, two fronts: what a Telegram conversation asks it to remember, its
// Buzz (ACP) sessions know.
func TestTelegramAndACPConversationsShareTheAgentsMemory(t *testing.T) {
	root := t.TempDir()
	project, agentDir := filepath.Join(root, "workspace"), filepath.Join(root, "agent")
	for _, dir := range []string{project, agentDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"plugins":{"memory":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("HOME", root)
	t.Setenv(config.EnvAgentDir, agentDir)

	provider := faux.New(faux.Options{API: "faux", Provider: "faux"})
	var acpPrompt string
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("remember", map[string]any{"target": "memory", "content": "The launch is on Tuesday."}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("Noted."),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			acpPrompt = *request.SystemPrompt
			return faux.AssistantMessage("Tuesday."), nil
		}),
	})
	dependencies := scriptedRuntime(provider)

	// A Bot API with one message from a teammate, then an idle long poll.
	replies := make(chan string, 8)
	delivered := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params map[string]any
		_ = json.NewDecoder(r.Body).Decode(&params)
		result := any(true)
		switch r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] {
		case "getMe":
			result = map[string]any{"id": 42, "is_bot": true, "username": "orbbot", "first_name": "orb"}
		case "getUpdates":
			if delivered {
				<-r.Context().Done()
				return
			}
			delivered = true
			result = []any{map[string]any{"update_id": 1, "message": map[string]any{
				"message_id": 1, "date": time.Now().Unix(), "text": "Remember that the launch is on Tuesday.",
				"chat": map[string]any{"id": 7, "type": "private"}, "from": map[string]any{"id": 7, "is_bot": false, "first_name": "Ana"},
			}}}
		case "sendMessage", "editMessageText":
			replies <- params["text"].(string)
			result = map[string]any{"message_id": 2, "date": time.Now().Unix(), "chat": map[string]any{"id": 7, "type": "private"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	defer api.Close()
	adapter, err := telegram.New(telegram.Options{Token: "test", BaseURL: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	cli := ParseArgs(nil)
	cli.useUnknownModel = true
	agents := acpHost{args: cli, dependencies: dependencies, streams: cliStreams{Stderr: io.Discard}}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- runLocalChat(ctx, filepath.Join(agentDir, "chat", "telegram"), []chat.Adapter{adapter},
			[]func(context.Context, func(chat.Message) error) error{adapter.Poll}, nil,
			func(chat.Message) error { return nil }, []chat.LocalProviderOption{agentWorkspace(agents, project)}, agents.streams)
	}()
	for reply := ""; reply != "Noted."; {
		select {
		case reply = <-replies:
		case <-time.After(10 * time.Second):
			t.Fatal("no reply on Telegram")
		}
	}
	stop()
	if code := <-done; code != 0 {
		t.Fatalf("chat exited %d", code)
	}

	client := startACP(t, provider)
	client.call("initialize", map[string]any{"protocolVersion": 1})
	created, _ := client.call("session/new", map[string]any{"cwd": project, "mcpServers": []any{}})
	client.call("session/prompt", map[string]any{
		"sessionId": created["result"].(map[string]any)["sessionId"], "prompt": []any{map[string]any{"type": "text", "text": "When is the launch?"}},
	})
	client.close()
	if !strings.Contains(acpPrompt, "The launch is on Tuesday.") {
		t.Fatalf("the ACP session's prompt lacks the memory: %q", acpPrompt)
	}
}
