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
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/acp"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/chat"
	"github.com/OrdalieTech/orb/chat/telegram"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/toolenv"
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

// A team agent's tools never see its credentials, yet its shell still posts on
// Buzz the way Buzz's harness prompt says: `buzz messages send`, and the agent
// signs its own profile, owner's tag included.
func TestTeamAgentShellSeesNoSecretAndStillPostsOnBuzz(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake buzz CLI is a shell script")
	}
	root := t.TempDir()
	project, bin := filepath.Join(root, "workspace"), filepath.Join(root, "bin")
	for _, dir := range []string{project, bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(project)
	t.Setenv("HOME", root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	secrets := map[string]string{
		"OPENROUTER_API_KEY": "sk-or-secret", "TELEGRAM_BOT_TOKEN": "tg-secret",
		"BUZZ_PRIVATE_KEY": "nostr-secret", "BUZZ_AUTH_TAG": "auth-tag-secret",
	}
	for name, value := range secrets {
		t.Setenv(name, value)
	}
	t.Setenv("BUZZ_ACP_DISPLAY_NAME", "Sales")
	t.Setenv("ORB_BUZZ_ABOUT", "Answers the sales team")
	t.Setenv(toolenv.Allow, "")
	t.Setenv("ORB_BUZZ", "")
	// The shell's buzz is Orb answering as buzz; the real CLI records its run.
	shim := "#!/bin/sh\nORB_BUZZ_SHIM_HELPER=1 exec '" + os.Args[0] + "' -test.run='^TestBuzzShimHelper$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "buzz"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runs, cli := filepath.Join(root, "runs"), filepath.Join(root, "buzz-cli")
	real := "#!/bin/sh\n{ echo \"args: $*\"; echo \"input: $(cat)\"; env; } > '" + runs + "'.$$\necho '{\"ok\":true}'\n"
	if err := os.WriteFile(cli, []byte(real), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORB_BUZZ_CLI", cli)

	provider := faux.New(faux.Options{API: "faux", Provider: "faux"})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("bash", map[string]any{"command": "env"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage(faux.ToolCall("bash", map[string]any{"command": "printf 'Hello team' | buzz messages send --channel c1 --content -"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("Posted."),
	})
	agents := teamAgent(context.Background(), scriptedRuntime(provider), cliStreams{Stderr: io.Discard})
	stop, err := serveBuzzCLI(context.Background(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := &acpClient{t: t, in: stdinWriter, out: bufio.NewReader(stdoutReader), done: make(chan int, 1)}
	go func() {
		_ = acp.Serve(context.Background(), stdinReader, stdoutWriter, agents, version)
		_ = stdoutWriter.Close()
		client.done <- 0
	}()
	client.call("initialize", map[string]any{"protocolVersion": 2})
	created, _ := client.call("session/new", map[string]any{"cwd": project, "mcpServers": []any{}})
	_, notifications := client.call("session/prompt", map[string]any{
		"sessionId": created["result"].(map[string]any)["sessionId"], "prompt": []any{map[string]any{"type": "text", "text": "Say hello on Buzz."}},
	})
	client.close()

	results := updates(notifications, "tool_call_update")
	if len(results) != 2 {
		t.Fatalf("tool results = %v", results)
	}
	output := func(update map[string]any) string {
		return update["content"].([]any)[0].(map[string]any)["content"].(map[string]any)["text"].(string)
	}
	shell, sent := output(results[0]), output(results[1])
	if !strings.Contains(shell, "PATH=") || !strings.Contains(sent, `{"ok":true}`) {
		t.Fatalf("env = %q, buzz = %q", shell, sent)
	}
	// The profile is published beside the conversation, so wait for its run.
	var record string
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(record, "set-profile") && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		files, _ := filepath.Glob(runs + ".*")
		record = ""
		for _, file := range files {
			run, _ := os.ReadFile(file)
			record += string(run)
		}
	}
	for name, value := range secrets {
		if strings.Contains(shell, value) {
			t.Errorf("the shell saw %s", name)
		}
		if leaked := strings.Contains(record, value); leaked != strings.HasPrefix(name, "BUZZ_") {
			t.Errorf("the buzz CLI holds %s: %t", name, leaked)
		}
	}
	for _, run := range []string{"args: messages send --channel c1 --content -\ninput: Hello team", "args: users set-profile --name Sales --about Answers the sales team\n"} {
		if !strings.Contains(record, run) {
			t.Fatalf("the buzz CLI ran with %s", record)
		}
	}
}

func TestBuzzShimHelper(t *testing.T) {
	if os.Getenv("ORB_BUZZ_SHIM_HELPER") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	os.Exit(runBuzzShim(args, os.Stdin, os.Stdout, os.Stderr))
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
		created, _ := client.call("session/new", map[string]any{"cwd": project, "mcpServers": []any{}, "systemPrompt": "BUZZ_BASE"})
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
