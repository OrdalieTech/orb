package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	"github.com/OrdalieTech/orb/internal/toolenv"
)

// acpClient drives `orb --mode acp` over its stdio, as an editor or a chat platform's ACP client does.
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
	return startACPIn(context.Background(), t, provider)
}

// startACPIn runs `orb --mode acp` with flags in ctx, which may carry the
// native store.
func startACPIn(ctx context.Context, t *testing.T, provider *faux.Provider, flags ...string) *acpClient {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := &acpClient{t: t, in: stdinWriter, out: bufio.NewReader(stdoutReader), done: make(chan int, 1)}
	go func() {
		client.done <- runCLIWithDependencies(ctx, append([]string{"--mode", "acp"}, flags...), cliStreams{
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

	// A client's harness prompt replaces Orb's base prompt, as --system-prompt does.
	created, _ := client.call("session/new", map[string]any{
		"cwd": project, "mcpServers": []any{}, "systemPrompt": "CLIENT_BASE: you answer in the team channel.",
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
	if strings.Count(systemPrompt, "CLIENT_BASE") != 1 {
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
// ACP sessions know.
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

// A team agent's tools never see its credentials.
func TestTeamAgentShellSeesNoSecret(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shell lists its environment with env")
	}
	root := t.TempDir()
	project := filepath.Join(root, "workspace")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("HOME", root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	secrets := map[string]string{"OPENROUTER_API_KEY": "sk-or-secret", "TELEGRAM_BOT_TOKEN": "tg-secret", "PLATFORM_PRIVATE_KEY": "platform-secret"}
	for name, value := range secrets {
		t.Setenv(name, value)
	}
	t.Setenv(toolenv.Allow, "")
	provider := faux.New(faux.Options{API: "faux", Provider: "faux"})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("bash", map[string]any{"command": "env"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("Done."),
	})
	agents := teamAgent(context.Background(), scriptedRuntime(provider), cliStreams{Stderr: io.Discard})
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := &acpClient{t: t, in: stdinWriter, out: bufio.NewReader(stdoutReader), done: make(chan int, 1)}
	go func() {
		_ = agents.serve(context.Background(), stdinReader, stdoutWriter)
		_ = stdoutWriter.Close()
		client.done <- 0
	}()
	client.call("initialize", map[string]any{"protocolVersion": 2})
	created, _ := client.call("session/new", map[string]any{"cwd": project, "mcpServers": []any{}})
	_, notifications := client.call("session/prompt", map[string]any{
		"sessionId": created["result"].(map[string]any)["sessionId"], "prompt": []any{map[string]any{"type": "text", "text": "Show your environment."}},
	})
	client.close()

	results := updates(notifications, "tool_call_update")
	if len(results) != 1 {
		t.Fatalf("tool results = %v", results)
	}
	shell := results[0]["content"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"].(string)
	if !strings.Contains(shell, "PATH=") {
		t.Fatalf("env = %q", shell)
	}
	for name, value := range secrets {
		if strings.Contains(shell, value) {
			t.Errorf("the shell saw %s", name)
		}
	}
}

// One agent, two live conversations: what one saves, the other knows from
// its next turn, without its prompt changing (so a provider's cache holds) and
// without telling the saver what it already did.
func TestLiveConversationsShareMemoryAsItChanges(t *testing.T) {
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

	type seen struct{ prompt, context string }
	var requests []seen
	record := func(reply ai.AssistantContentBlock, stop ai.StopReason) faux.ResponseStep {
		return faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			context, _ := json.Marshal(request.Messages)
			requests = append(requests, seen{*request.SystemPrompt, string(context)})
			return faux.AssistantMessage(reply, faux.AssistantMessageOptions{StopReason: stop}), nil
		})
	}
	provider := faux.New(faux.Options{API: "faux", Provider: "faux"})
	provider.SetResponses([]faux.ResponseStep{
		record(faux.Text("Hello."), ai.StopReasonStop),
		record(faux.ToolCall("remember", map[string]any{"target": "memory", "content": "The code of the day is tournesol."}), ai.StopReasonToolUse),
		record(faux.Text("Saved."), ai.StopReasonStop),
		record(faux.Text("Tournesol."), ai.StopReasonStop),
		record(faux.Text("Still tournesol."), ai.StopReasonStop),
	})
	client := startACP(t, provider)
	client.call("initialize", map[string]any{"protocolVersion": 2})
	open := func() string {
		created, _ := client.call("session/new", map[string]any{"cwd": project, "mcpServers": []any{}, "systemPrompt": "CLIENT_BASE"})
		return created["result"].(map[string]any)["sessionId"].(string)
	}
	ask := func(session, text string) {
		answer, _ := client.call("session/prompt", map[string]any{"sessionId": session, "prompt": []any{map[string]any{"type": "text", "text": text}}})
		if answer["error"] != nil {
			t.Fatalf("prompt %q: %v", text, answer["error"])
		}
	}
	channel, thread := open(), open()
	ask(channel, "Hello")
	ask(thread, "Remember the code of the day: tournesol.")
	ask(channel, "What is the code of the day?")
	ask(thread, "And now?")
	client.close()

	if len(requests) != 5 {
		t.Fatalf("model calls = %d", len(requests))
	}
	before, after, saver := requests[0], requests[3], requests[4]
	if after.prompt != before.prompt {
		t.Fatalf("the channel's prompt changed:\n%s\n---\n%s", before.prompt, after.prompt)
	}
	if !strings.Contains(after.context, "MEMORY: The code of the day is tournesol.") {
		t.Fatalf("the channel did not learn the saved memory: %s", after.context)
	}
	if strings.Contains(saver.context, "Persistent memory changed") {
		t.Fatalf("the saver was told its own change: %s", saver.context)
	}
}

// A session's tools run where its client says, whatever the session's
// history: a working directory the agent cannot enter is refused when the
// session opens, not discovered by every tool, and loading a stored session
// from another directory moves it there.
func TestACPSessionsWorkWhereTheirClientSays(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions that bind its user")
	}
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "session files", true: "native store"}[native], func(t *testing.T) {
			sessionsWorkWhereTheirClientSays(t, native)
		})
	}
}

func sessionsWorkWhereTheirClientSays(t *testing.T, native bool) {
	root := t.TempDir()
	first, moved, locked := filepath.Join(root, "first"), filepath.Join(root, "moved"), filepath.Join(root, "locked")
	for _, dir := range []string{first, moved, locked} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(locked, 0o755) }()
	t.Chdir(root)
	t.Setenv("HOME", root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	ctx := context.Background()
	if native {
		state, err := openNativeState(ctx, filepath.Join(root, "agent"), true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = state.Close() }()
		ctx = context.WithValue(ctx, nativeStateKey{}, state)
	}
	provider := faux.New(faux.Options{API: "faux", Provider: "faux"})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage("Hello."),
		faux.AssistantMessage(faux.ToolCall("bash", map[string]any{"command": "pwd"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("Done."),
	})

	client := startACPIn(ctx, t, provider)
	client.call("initialize", map[string]any{"protocolVersion": 2})
	if refused, _ := client.call("session/new", map[string]any{"cwd": locked, "mcpServers": []any{}}); refused["error"] == nil ||
		!strings.Contains(fmt.Sprint(refused["error"]), locked) {
		t.Fatalf("session/new in a directory the agent cannot enter = %v, want an error naming it", refused)
	}
	created, _ := client.call("session/new", map[string]any{"cwd": first, "mcpServers": []any{}})
	id := created["result"].(map[string]any)["sessionId"]
	client.call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "Hello."}}})
	client.close()

	client = startACPIn(ctx, t, provider)
	client.call("initialize", map[string]any{"protocolVersion": 2})
	if loaded, _ := client.call("session/load", map[string]any{"sessionId": id, "cwd": moved, "mcpServers": []any{}}); loaded["error"] != nil {
		t.Fatalf("session/load = %v", loaded)
	}
	_, notifications := client.call("session/prompt", map[string]any{"sessionId": id, "prompt": []any{map[string]any{"type": "text", "text": "Where are you?"}}})
	client.close()
	results := updates(notifications, "tool_call_update")
	if len(results) != 1 {
		t.Fatalf("tool results = %v", results)
	}
	if output := results[0]["content"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"].(string); !strings.Contains(output, moved) {
		t.Fatalf("the loaded session's bash ran in %q, want %s", output, moved)
	}
}

// An ACP client names its sessions, so the CLI's session flags do not reach
// them: `orb --mode acp --resume` used to crash on the first session/new.
func TestACPIgnoresTheCLIsSessionFlags(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("HOME", root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	client := startACPIn(context.Background(), t, faux.New(faux.Options{API: "faux", Provider: "faux"}), "--resume")
	client.call("initialize", map[string]any{"protocolVersion": 2})
	if created, _ := client.call("session/new", map[string]any{"cwd": root, "mcpServers": []any{}}); created["result"] == nil {
		t.Fatalf("session/new = %v", created)
	}
	client.close()
}
