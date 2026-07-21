package jsbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/codingagent/tools"
	"github.com/grafana/sobek"
)

func TestAllEventHooksAndMiddlewareShapes(t *testing.T) {
	eventTypes := []extensions.EventType{
		extensions.EventProjectTrust,
		extensions.EventResourcesDiscover,
		extensions.EventSessionStart,
		extensions.EventSessionInfoChanged,
		extensions.EventSessionBeforeSwitch,
		extensions.EventSessionBeforeFork,
		extensions.EventSessionBeforeCompact,
		extensions.EventSessionCompact,
		extensions.EventSessionShutdown,
		extensions.EventSessionBeforeTree,
		extensions.EventSessionTree,
		extensions.EventContext,
		extensions.EventBeforeProviderRequest,
		extensions.EventBeforeProviderHeaders,
		extensions.EventAfterProviderResponse,
		extensions.EventBeforeAgentStart,
		extensions.EventAgentStart,
		extensions.EventAgentEnd,
		extensions.EventAgentSettled,
		extensions.EventTurnStart,
		extensions.EventTurnEnd,
		extensions.EventMessageStart,
		extensions.EventMessageUpdate,
		extensions.EventMessageEnd,
		extensions.EventToolExecutionStart,
		extensions.EventToolExecutionUpdate,
		extensions.EventToolExecutionEnd,
		extensions.EventModelSelect,
		extensions.EventThinkingLevelSelect,
		extensions.EventToolCall,
		extensions.EventToolResult,
		extensions.EventUserBash,
		extensions.EventInput,
	}
	encodedEvents, err := json.Marshal(eventTypes)
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	source := fmt.Sprintf(`
const eventTypes = %s;
export default function (pi) {
  for (const eventType of eventTypes) {
    pi.on(eventType, async (event) => {
      await Promise.resolve();
      if (event.type !== eventType) throw new Error("wrong event type: " + event.type);
    });
  }
  pi.on("tool_call", async (event, ctx) => {
    if (event.toolCallId !== "call-1" || event.toolName !== "echo") throw new Error("tool_call shape");
    if (ctx.cwd !== %q || ctx.mode !== "print" || ctx.hasUI) throw new Error("ctx shape");
    event.input.steps = ["first"];
    await Promise.resolve();
    return { block: false };
  });
  pi.on("tool_call", (event) => {
    if (event.input.steps.join(",") !== "first") throw new Error("tool_call mutation did not chain");
    event.input.steps.push("second");
  });
  pi.on("before_provider_headers", (event) => {
    event.headers.Trace = "one";
    event.headers.Remove = null;
  });
  pi.on("before_provider_headers", async (event) => {
    await Promise.resolve();
    event.headers.Trace += "-two";
  });
  pi.on("tool_result", async (event) => ({
	...(event.details === undefined ? {} : (() => { throw new Error("undefined tool details became null"); })()),
	...(event.usage?.totalTokens === 1 ? {} : (() => { throw new Error("tool usage missing"); })()),
    content: [{ type: "text", text: event.content[0].text + "-patched" }],
    details: { from: event.toolCallId },
    isError: true,
	usage: { input: 2, output: 3, cacheRead: 4, cacheWrite: 5, totalTokens: 14, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0.14 } },
  }));
  pi.on("before_provider_request", (event) => ({ ...event.payload, chained: true }));
  pi.on("before_agent_start", (event, ctx) => ({ systemPrompt: ctx.getSystemPrompt() + ":one" }));
  pi.on("before_agent_start", async (event, ctx) => {
    if (event.systemPrompt !== "base:one" || ctx.getSystemPrompt() !== "base:one") throw new Error("prompt middleware");
    return { systemPrompt: event.systemPrompt + ":two" };
  });
  pi.on("session_before_tree", (event) => {
		const descriptor = Object.getOwnPropertyDescriptor(event, "preparation");
		if (typeof descriptor?.get !== "function" || Object.isFrozen(event.preparation)) throw new Error("tree preparation was not lazy/mutable");
		event.preparation.observed = true;
		if (!event.preparation.observed) throw new Error("tree preparation mutation failed");
    if (event.preparation.targetId !== "target" || !event.signal || event.signal.aborted) throw new Error("tree shape");
    return { label: "tree-label", replaceInstructions: true };
  });
	pi.on("context", (event) => {
		const descriptor = Object.getOwnPropertyDescriptor(event, "messages");
		if (typeof descriptor?.get !== "function" || Object.isFrozen(event.messages)) throw new Error("messages were not lazy/mutable");
		event.messages.push({ role: "future", content: { mutable: true } });
	});
	pi.on("context", (event) => {
		if (event.messages.length !== 2 || event.messages[1].role !== "future") throw new Error("context mutation did not chain");
		return {};
	});
	pi.on("model_select", (event) => {
		const descriptor = Object.getOwnPropertyDescriptor(event, "model");
		if (typeof descriptor?.get !== "function" || Object.isFrozen(event.model) || event.model.id !== "model-1") throw new Error("model was not lazy/mutable");
		event.model.observed = true;
	});
  pi.on("input", (event) => ({ action: "transform", text: event.text + "!", images: event.images }));
	pi.on("user_bash", (event) => ({
		operations: { exec: async (command, cwd, options) => {
			if (command !== "remote" || cwd !== %q || !options.signal) throw new Error("bash operation shape");
			options.onData("remote-output");
			return { exitCode: 3 };
		}},
		result: { output: "handled", exitCode: 0, cancelled: false, truncated: false },
	}));
}
`, string(encodedEvents), project, project)
	var eventErrors []extensions.ExtensionError
	runner := loadBridgeRunner(t, project, []bridgeSource{{"events.ts", source}}, extensions.RunnerOptions{
		CWD: project,
		ErrorHandler: func(value extensions.ExtensionError) {
			eventErrors = append(eventErrors, value)
		},
	})
	for _, eventType := range eventTypes {
		if !runner.HasHandlers(eventType) {
			t.Fatalf("event %q was not registered", eventType)
		}
	}

	input := map[string]any{"value": "x"}
	if result := runner.EmitToolCall(context.Background(), extensions.ToolCallEvent{ToolCallID: "call-1", ToolName: "echo", Input: input}); result == nil || result.Block {
		t.Fatalf("tool_call result = %#v", result)
	}
	if got, want := input["steps"], []any{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool_call mutation = %#v, want %#v; errors = %#v", got, want, eventErrors)
	}

	one, remove := "original", "remove"
	headers := runner.EmitBeforeProviderHeaders(context.Background(), ai.ProviderHeaders{"Original": &one, "Remove": &remove})
	if headers["Trace"] == nil || *headers["Trace"] != "one-two" || headers["Remove"] != nil {
		t.Fatalf("headers = %#v", headers)
	}
	patched := runner.EmitToolResult(context.Background(), extensions.ToolResultEvent{
		ToolCallID: "call-1", ToolName: "echo", Content: ai.ToolResultContent{&ai.TextContent{Text: "result"}}, Usage: &ai.Usage{Input: 1, TotalTokens: 1},
	})
	if patched == nil || patched.Content == nil || (*patched.Content)[0].(*ai.TextContent).Text != "result-patched" || patched.IsError == nil || !*patched.IsError || patched.Usage == nil || patched.Usage.TotalTokens != 14 || patched.Usage.Cost.Total != 0.14 {
		t.Fatalf("tool result patch = %#v", patched)
	}
	if got := runner.EmitBeforeProviderRequest(context.Background(), map[string]any{"base": true}); !reflect.DeepEqual(got, map[string]any{"base": true, "chained": true}) {
		t.Fatalf("provider request = %#v", got)
	}
	prompt := runner.EmitBeforeAgentStart(context.Background(), "prompt", nil, "base", extensions.SystemPromptOptions{CWD: project})
	if prompt == nil || prompt.SystemPrompt == nil || *prompt.SystemPrompt != "base:one:two" {
		t.Fatalf("prompt result = %#v", prompt)
	}
	tree := runner.Emit(context.Background(), extensions.SessionBeforeTreeEvent{
		Preparation: extensions.TreePreparation{TargetID: "target"}, Signal: context.Background(),
	})
	treeResult, ok := tree.(extensions.SessionBeforeTreeResult)
	if !ok || treeResult.Label == nil || *treeResult.Label != "tree-label" || treeResult.ReplaceInstructions == nil || !*treeResult.ReplaceInstructions {
		t.Fatalf("tree result = %#v", tree)
	}
	messages := agent.AgentMessages{&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1}}
	if got := runner.EmitContext(context.Background(), messages); len(got) != 2 {
		t.Fatalf("context messages = %#v", got)
	}
	selectedModel := &ai.Model{ID: "model-1", Provider: "provider-1"}
	runner.Emit(context.Background(), extensions.ModelSelectEvent{Model: selectedModel, Source: extensions.ModelSelectSet})
	if len(eventErrors) != 0 {
		t.Fatalf("event errors = %#v", eventErrors)
	}
	inputResult := runner.EmitInput(context.Background(), "hello", nil, extensions.InputInteractive, nil)
	if inputResult.Action != extensions.InputTransform || inputResult.Text != "hello!" {
		t.Fatalf("input result = %#v", inputResult)
	}
	bashResult := runner.EmitUserBash(context.Background(), extensions.UserBashEvent{Command: "remote", CWD: project})
	if bashResult == nil || bashResult.Result == nil || bashResult.Result.Output != "handled" || bashResult.Operations == nil {
		t.Fatalf("user_bash result = %#v", bashResult)
	}
	var bashOutput string
	execution, err := bashResult.Operations.Exec(context.Background(), "remote", project, tools.BashExecOptions{
		OnData: func(data []byte) { bashOutput += string(data) },
	})
	if err != nil || execution.ExitCode == nil || *execution.ExitCode != 3 || bashOutput != "remote-output" {
		t.Fatalf("bash operation = %#v, output = %q, err = %v", execution, bashOutput, err)
	}
}

func TestToolBindingTypeBoxMetadataPrepareUpdatesAndErrors(t *testing.T) {
	project := t.TempDir()
	source := `
import { Type } from "typebox";
export default function (pi) {
	  pi.registerTool({
    name: "bound",
    label: "Bound tool",
    description: "Exercises the bridge",
    promptSnippet: "Use bound carefully",
    promptGuidelines: ["First", "Second"],
    renderShell: "self",
    executionMode: "parallel",
    parameters: Type.Object({
      message: Type.String({ description: "Message" }),
      count: Type.Optional(Type.Number({ minimum: 1 })),
    }),
    prepareArguments(args) {
      return { message: args.message ?? args.legacy, count: args.count };
    },
    async execute(toolCallId, params, signal, onUpdate, ctx) {
      if (params.message === "throw") throw new Error("tool exploded");
      if (!signal || signal.aborted || ctx.getSystemPrompt() !== "system") throw new Error("tool context");
      onUpdate?.({ content: [{ type: "text", text: "update:" + params.message }], details: { phase: 1 } });
      await Promise.resolve();
      return {
        content: [{ type: "text", text: toolCallId + ":" + params.message }],
        details: { cwd: ctx.cwd, count: params.count },
        terminate: true,
        addedToolNames: ["dynamic-one", "dynamic-two"],
      };
	    },
	  });
	  pi.registerTool({
	    name: "async-prepare",
	    label: "Async prepare",
	    description: "Rejects a Promise from the synchronous preparation hook",
	    parameters: Type.Object({ message: Type.String() }),
	    async prepareArguments(args) { return args; },
	    async execute() { return { content: [] }; },
	  });
	}
`
	runner := loadBridgeRunner(t, project, []bridgeSource{{"tool.ts", source}}, extensions.RunnerOptions{
		CWD: project,
		ContextActions: extensions.ContextActions{
			GetSignal:       func() context.Context { return context.Background() },
			GetSystemPrompt: func() string { return "system" },
		},
	})
	tool := runner.ToolDefinition("bound")
	if tool == nil {
		t.Fatal("bound tool was not registered")
	}
	if tool.Label != "Bound tool" || tool.PromptSnippet != "Use bound carefully" || !reflect.DeepEqual(tool.PromptGuidelines, []string{"First", "Second"}) || tool.RenderShell != extensions.RenderShellSelf || tool.ExecutionMode != agent.ToolExecutionParallel {
		t.Fatalf("tool metadata = %#v", tool)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if got := schema["required"]; !reflect.DeepEqual(got, []any{"message"}) {
		t.Fatalf("required = %#v", got)
	}
	properties := schema["properties"].(map[string]any)
	if properties["count"].(map[string]any)["minimum"] != float64(1) {
		t.Fatalf("schema = %#v", schema)
	}
	prepared, err := tool.PrepareArguments(map[string]any{"legacy": "old", "count": 2})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared, map[string]any{"message": "old", "count": int64(2)}) && !reflect.DeepEqual(prepared, map[string]any{"message": "old", "count": float64(2)}) {
		t.Fatalf("prepared = %#v", prepared)
	}
	var updates []agent.AgentToolResult
	result, err := tool.Execute(context.Background(), "call-7", prepared, func(update agent.AgentToolResult) {
		updates = append(updates, update)
	}, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].Content[0].(*ai.TextContent).Text != "update:old" {
		t.Fatalf("updates = %#v", updates)
	}
	if result.Content[0].(*ai.TextContent).Text != "call-7:old" || result.Terminate == nil || !*result.Terminate || result.AddedToolNames == nil || !reflect.DeepEqual(*result.AddedToolNames, []string{"dynamic-one", "dynamic-two"}) {
		t.Fatalf("result = %#v", result)
	}
	_, err = tool.Execute(context.Background(), "call-8", map[string]any{"message": "throw"}, nil, runner.CreateContext())
	if err == nil || !strings.Contains(err.Error(), "tool exploded") {
		t.Fatalf("throw error = %v", err)
	}
	_, err = runner.ToolDefinition("async-prepare").PrepareArguments(map[string]any{"message": "hello"})
	if err == nil || !strings.Contains(err.Error(), "must return synchronously") {
		t.Fatalf("async prepare error = %v", err)
	}
}

func TestTypeBoxCompileValueAndStringEnumAcrossVMs(t *testing.T) {
	project := t.TempDir()
	sources := make([]bridgeSource, 0, 2)
	for _, name := range []string{"first", "second"} {
		sources = append(sources, bridgeSource{name + ".ts", fmt.Sprintf(`
import { Type } from "typebox";
import { Compile } from "typebox/compile";
import { Check, Value } from "typebox/value";
import { StringEnum } from "@earendil-works/pi-ai";
export default function (pi) {
	const kind = StringEnum(["one", "two"], { description: "Kind", default: "one", ignored: "no" });
	const empty = StringEnum([""], { description: "", default: "", ignored: "no" });
	const schema = Type.Object({ kind, count: Type.Number() });
	const validator = Compile(schema);
	if (!validator.Check({ kind: "one", count: 1 }) || validator.Check({ kind: "bad", count: 1 })) throw new Error("Compile.Check failed");
	if (!Value.Check(schema, { kind: "two", count: 2 }) || !Check(schema, { kind: "one", count: 3 })) throw new Error("typebox/value Check failed");
	if ("ignored" in kind || "ignored" in empty || "description" in empty || "default" in empty) throw new Error("StringEnum options differ from upstream");
	pi.registerTool({ name: %q, label: %q, description: "TypeBox", parameters: schema, async execute() { return { content: [] }; } });
}
`, name, name)})
	}
	runner := loadBridgeRunner(t, project, sources, extensions.RunnerOptions{CWD: project})
	for _, name := range []string{"first", "second"} {
		tool := runner.ToolDefinition(name)
		if tool == nil {
			t.Fatalf("%s tool was not registered", name)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		kind := schema["properties"].(map[string]any)["kind"].(map[string]any)
		if kind["description"] != "Kind" || kind["default"] != "one" {
			t.Fatalf("%s StringEnum schema = %#v", name, kind)
		}
		if _, exists := kind["ignored"]; exists {
			t.Fatalf("%s StringEnum copied unrelated options: %#v", name, kind)
		}
	}
}

func TestProviderRequestUndefinedAndNullChaining(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
	pi.on("before_provider_request", (event) => {
		if (event.payload.stage !== "original") throw new Error("first payload");
		return undefined;
	});
	pi.on("before_provider_request", (event) => {
		if (event.payload.stage !== "original") throw new Error("undefined changed payload");
		return null;
	});
	pi.on("before_provider_request", (event) => {
		if (event.payload !== null) throw new Error("null replacement was lost");
		return undefined;
	});
}
`
	var eventErrors []extensions.ExtensionError
	runner := loadBridgeRunner(t, project, []bridgeSource{{"provider-request.ts", source}}, extensions.RunnerOptions{
		CWD: project,
		ErrorHandler: func(value extensions.ExtensionError) {
			eventErrors = append(eventErrors, value)
		},
	})
	if got := runner.EmitBeforeProviderRequest(context.Background(), map[string]any{"stage": "original"}); got != nil {
		t.Fatalf("provider payload = %#v, want explicit nil replacement", got)
	}
	if len(eventErrors) != 0 {
		t.Fatalf("provider request errors = %#v", eventErrors)
	}
}

func TestMessageEndAcceptsFutureMessageShape(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
	pi.on("message_end", (event) => ({ message: { ...event.message, futureField: { nested: [1, 2, 3] } } }));
}
`
	runner := loadBridgeRunner(t, project, []bridgeSource{{"message-end.ts", source}}, extensions.RunnerOptions{CWD: project})
	original := map[string]any{"role": "futureRole", "content": map[string]any{"text": "hello"}}
	replaced := runner.EmitMessageEnd(context.Background(), extensions.MessageEndEvent{Message: original})
	message, ok := replaced.(map[string]any)
	if !ok || message["role"] != "futureRole" {
		t.Fatalf("replacement = %#v", replaced)
	}
	if _, exists := original["futureField"]; exists {
		t.Fatalf("replacement mutated original message: %#v", original)
	}
	if !reflect.DeepEqual(message["futureField"], map[string]any{"nested": []any{float64(1), float64(2), float64(3)}}) {
		t.Fatalf("future message field = %#v", message["futureField"])
	}
}

func TestCommandActionsContextsSignalAndRegistrations(t *testing.T) {
	t.Setenv("BRIDGE_NATIVE_KEY", "native-secret")
	project := t.TempDir()
	manager, err := session.Create(project, filepath.Join(project, "sessions"), session.WithSessionID("bridge-session"))
	if err != nil {
		t.Fatal(err)
	}
	model := ai.Model{ID: "model-1", Name: "Model One", API: ai.APIOpenAIResponses, Provider: "provider-1", BaseURL: "https://example.invalid", ContextWindow: 1000, MaxTokens: 100}
	registry := &bridgeModelRegistry{models: []ai.Model{model}, key: "secret", headers: map[string]string{"X-Test": "yes"}, errorText: "registry warning"}
	signalContext, cancelSignal := context.WithCancel(context.Background())
	t.Cleanup(cancelSignal)

	type actionRecord struct {
		name string
		data any
	}
	var mu sync.Mutex
	var records []actionRecord
	record := func(name string, data any) {
		mu.Lock()
		records = append(records, actionRecord{name: name, data: data})
		mu.Unlock()
	}
	ready := make(chan struct{}, 1)
	aborted := make(chan struct{}, 1)
	execReady := make(chan struct{}, 1)
	execKilled := make(chan bool, 1)
	compactComplete := make(chan string, 1)
	compactContext := make(chan bool, 1)
	directContext := make(chan bool, 1)
	type callbackContextKey struct{}
	callbackMarker := callbackContextKey{}
	capturedProviders := make(map[string]extensions.Provider)
	var unregistered string
	var currentRunner *extensions.Runner
	actions := extensions.Actions{
		SendMessage: func(_ context.Context, message extensions.CustomMessage, _ *extensions.SendMessageOptions) error {
			record("sendMessage", message)
			return nil
		},
		SendUserMessage: func(_ context.Context, content ai.UserContent, _ *extensions.SendUserMessageOptions) error {
			record("sendUserMessage", content)
			return nil
		},
		AppendEntry: func(ctx context.Context, customType string, data any) error {
			record(customType, data)
			switch customType {
			case "signal-ready":
				select {
				case ready <- struct{}{}:
				default:
				}
			case "signal-aborted":
				select {
				case aborted <- struct{}{}:
				default:
				}
			case "exec-ready":
				select {
				case execReady <- struct{}{}:
				default:
				}
			case "exec-result":
				if object, ok := data.(map[string]any); ok {
					execKilled <- object["killed"] == true
				}
			case "compact-complete":
				if object, ok := data.(map[string]any); ok {
					compactComplete <- fmt.Sprint(object["summary"])
				}
				compactContext <- ctx.Value(callbackMarker) == "surface"
			case "direct-context":
				directContext <- ctx.Value(callbackMarker) == "surface"
			}
			return nil
		},
		SetSessionName: func(_ context.Context, name string) error { record("setSessionName", name); return nil },
		GetSessionName: func(context.Context) (*string, error) { value := "named"; return &value, nil },
		SetLabel: func(_ context.Context, id string, label *string) error {
			record("setLabel", []any{id, label})
			return nil
		},
		GetActiveTools: func() ([]string, error) { return []string{"read"}, nil },
		GetAllTools: func() ([]extensions.ToolInfo, error) {
			return []extensions.ToolInfo{{Name: "read", Description: "Read"}}, nil
		},
		SetActiveTools: func(names []string) error { record("setActiveTools", names); return nil },
		GetCommands: func() ([]extensions.SlashCommandInfo, error) {
			return []extensions.SlashCommandInfo{{Name: "help", Source: extensions.SlashCommandExtension}}, nil
		},
		SetModel: func(_ context.Context, selected *ai.Model) (bool, error) {
			record("setModel", selected)
			return true, nil
		},
		GetThinkingLevel: func() (agent.ThinkingLevel, error) { return agent.ThinkingLow, nil },
		SetThinkingLevel: func(level agent.ThinkingLevel) error { record("setThinking", level); return nil },
		RegisterProvider: func(provider extensions.Provider) error {
			capturedProviders[provider.ID] = provider
			return registry.RegisterProvider(provider)
		},
		RegisterProviderConfig: func(id string, config extensions.ProviderConfig) error {
			capturedProviders[id] = extensions.Provider{ID: id, Name: config.Name, BaseURL: config.BaseURL, Config: config}
			return registry.RegisterProviderConfig(id, config)
		},
		UnregisterProvider: func(name string) error {
			unregistered = name
			return registry.UnregisterProvider(name)
		},
	}
	contextActions := extensions.ContextActions{
		GetModel:           func() *ai.Model { copy := model; return &copy },
		IsIdle:             func() bool { return false },
		IsProjectTrusted:   func() bool { return true },
		GetSignal:          func() context.Context { return signalContext },
		HasPendingMessages: func() bool { return true },
		GetContextUsage: func() *extensions.ContextUsage {
			tokens, percent := int64(250), 25.0
			return &extensions.ContextUsage{Tokens: &tokens, ContextWindow: 1000, Percent: &percent}
		},
		GetSystemPrompt: func() string { return "effective prompt" },
		Abort:           func() { record("abort", true) },
		Shutdown:        func() { record("shutdown", true) },
		Compact: func(options *extensions.CompactOptions) {
			record("compact", options.CustomInstructions)
			if options.OnComplete != nil {
				options.OnComplete(harness.CompactionResult{Summary: "compacted"})
			}
		},
		GetSystemPromptOptions: func() extensions.SystemPromptOptions {
			return extensions.SystemPromptOptions{CWD: project, SelectedTools: []string{"read"}}
		},
	}
	commandActions := extensions.CommandActions{
		WaitForIdle: func(context.Context) error { return nil },
		NewSession: func(ctx context.Context, options *extensions.NewSessionOptions) (extensions.SessionReplacementResult, error) {
			if options == nil || options.ParentSession != "parent.jsonl" {
				return extensions.SessionReplacementResult{}, errors.New("newSession options")
			}
			if err := options.Setup(manager); err != nil {
				return extensions.SessionReplacementResult{}, err
			}
			if options.WithSession != nil {
				replaced := bridgeReplacedContext{CommandContext: currentRunner.CreateCommandContext(), sendMessage: actions.SendMessage, sendUserMessage: actions.SendUserMessage}
				if err := options.WithSession(ctx, replaced); err != nil {
					return extensions.SessionReplacementResult{}, err
				}
			}
			return extensions.SessionReplacementResult{Cancelled: false}, nil
		},
		Fork: func(_ context.Context, entryID string, options *extensions.ForkOptions) (extensions.SessionReplacementResult, error) {
			record("fork", []any{entryID, options.Position})
			return extensions.SessionReplacementResult{}, nil
		},
		NavigateTree: func(_ context.Context, targetID string, options *extensions.NavigateTreeOptions) (extensions.SessionReplacementResult, error) {
			record("navigate", []any{targetID, options.Summarize, options.Label})
			return extensions.SessionReplacementResult{}, nil
		},
		SwitchSession: func(_ context.Context, path string, _ *extensions.SwitchSessionOptions) (extensions.SessionReplacementResult, error) {
			record("switch", path)
			return extensions.SessionReplacementResult{}, nil
		},
		Reload: func(context.Context) error { record("reload", true); return nil },
	}
	source := `
export default function (pi) {
	const modelConfig = { id: "legacy-model", name: "Legacy Model", api: "openai-responses", baseUrl: "https://provider.invalid", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 1000.5, maxTokens: 100.25 };
	let streamClosed = false;
	const stream = async function* (_model, _context, options) {
		try {
			if (options !== undefined) throw new Error("absent stream options became null");
			yield { type: "text_delta", contentIndex: 0, delta: "streamed", partial: { role: "assistant", content: [], api: "openai-responses", provider: "legacy-provider", model: "legacy-model", usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } }, stopReason: "stop", timestamp: 1 } };
		} finally { streamClosed = true; }
	};
  pi.registerFlag("bridge-flag", { description: "Bridge flag", type: "string", default: "default-value" });
  pi.registerShortcut("ctrl+x", { description: "Bridge shortcut", handler: async (ctx) => {
    await Promise.resolve();
    pi.appendEntry("shortcut", { cwd: ctx.cwd });
  }});
  pi.registerMessageRenderer("bridge-message", () => "deferred");
  pi.registerEntryRenderer("bridge-entry", () => "deferred");
  pi.registerProvider("legacy-provider", {
    name: "Legacy Provider",
    baseUrl: "https://provider.invalid",
    apiKey: "$BRIDGE_KEY",
    api: "openai-responses",
    authHeader: true,
    headers: { "X-Provider": "yes" },
		models: [modelConfig],
		refreshModels: async (ctx) => {
			const deleted = await ctx.store.delete();
			if (deleted !== undefined) throw new Error("store.delete did not resolve undefined");
			const stored = await ctx.store.read();
			if (stored !== undefined) throw new Error("empty store read did not resolve undefined");
			const written = await ctx.store.write({ models: [], checkedAt: 7 });
			if (written !== undefined) throw new Error("store.write did not resolve undefined");
			return [modelConfig];
		},
		oauth: {
			name: "Bridge OAuth",
			login: async (callbacks) => {
				callbacks.onAuth({ url: "https://auth.invalid", instructions: "Open it" });
				callbacks.onDeviceCode({ userCode: "CODE", verificationUri: "https://verify.invalid", intervalSeconds: 1, expiresInSeconds: 60 });
				callbacks.onProgress("working");
				const prompted = await callbacks.onPrompt({ message: "Token", placeholder: "value", allowEmpty: false });
				const manual = await callbacks.onManualCodeInput();
				const selected = await callbacks.onSelect({ message: "Account", options: [{ id: "one", label: "One" }] });
				return { refresh: manual, access: prompted + ":" + selected, expires: 9, tenant: "acme" };
			},
			refreshToken: async (credentials) => ({ ...credentials, access: credentials.access + ":refreshed" }),
			getApiKey: (credentials) => credentials.access,
			modifyModels: (models, credentials) => models.map((model) => ({ ...model, name: model.name + ":" + credentials.access })),
		},
		streamSimple: stream,
  });
	pi.registerProvider({
		id: "native-provider",
		name: "Native Provider",
		baseUrl: "https://native.invalid",
		headers: { "X-Native": "yes" },
		auth: { apiKey: { name: "Native key", resolve: async ({ ctx }) => ({ auth: { apiKey: await ctx.env("BRIDGE_NATIVE_KEY") }, source: "test" }) } },
		getModels: () => [{ ...modelConfig, id: "native-model", name: "Native Model", provider: "native-provider" }],
		filterModels: (models, credential) => {
			if (credential?.key !== "native-filter-key") throw new Error("native filter credential missing");
			return models.slice(0, 1);
		},
		stream,
		streamSimple: stream,
	});
  pi.registerCommand("surface", {
    description: "Surface command",
    getArgumentCompletions: async (prefix) => {
      await Promise.resolve();
      return [{ value: prefix + "-value", label: "Completion", description: "Async" }];
    },
    handler: async (args, ctx) => {
		if (ctx.sessionManager !== ctx.sessionManager || ctx.modelRegistry !== ctx.modelRegistry || ctx.signal !== ctx.signal) throw new Error("context wrapper identity changed");
      const execResult = await pi.exec("/bin/pwd", [], { cwd: ctx.cwd, timeout: 5000, signal: ctx.signal });
		const refreshed = await ctx.modelRegistry.refresh();
		if (refreshed !== undefined) throw new Error("modelRegistry.refresh did not resolve undefined");
      const auth = await ctx.modelRegistry.getApiKeyAndHeaders(ctx.model);
      const providerKey = await ctx.modelRegistry.getApiKeyForProvider(ctx.model.provider);
      const providerAuth = await ctx.modelRegistry.getProviderAuth(ctx.model.provider);
		const registeredProviderIds = ctx.modelRegistry.getRegisteredProviderIds();
		if (!registeredProviderIds.includes("legacy-provider") || !registeredProviderIds.includes("native-provider")) throw new Error("registered provider ids missing");
		const modelSet = await pi.setModel(ctx.model);
		const waited = await ctx.waitForIdle();
		if (waited !== undefined) throw new Error("waitForIdle did not resolve undefined");
      const created = await ctx.newSession({
        parentSession: "parent.jsonl",
        setup: async (sessionManager) => { sessionManager.appendCustomEntry("setup-entry", { ok: true }); },
        withSession: async (next) => {
          await next.sendMessage({ customType: "replacement", content: { ok: true }, display: false });
          await next.sendUserMessage("replacement user");
        },
      });
      const forked = await ctx.fork("entry-1", { position: "before" });
      const navigated = await ctx.navigateTree("entry-2", { summarize: true, label: "branch" });
      const switched = await ctx.switchSession("other.jsonl");
		const reloaded = await ctx.reload();
		if (reloaded !== undefined) throw new Error("reload did not resolve undefined");
      pi.sendMessage({ customType: "surface-message", content: { args }, display: true }, { triggerTurn: true, deliverAs: "followUp" });
      pi.sendUserMessage([{ type: "text", text: "surface user" }], { deliverAs: "steer" });
      pi.setSessionName("new-name");
      pi.setLabel("entry-1", "label-1");
      pi.setActiveTools(["read", "bound"]);
      pi.setThinkingLevel("high");
			ctx.compact({ customInstructions: "compact this", onComplete: async (result) => {
				await Promise.resolve();
				pi.appendEntry("compact-complete", { summary: result.summary });
			}});
			ctx.abort();
			ctx.shutdown();
		pi.appendEntry("direct-context", {});
      pi.appendEntry("surface-result", {
        cwd: ctx.cwd,
        mode: ctx.mode,
        hasUI: ctx.hasUI,
        isIdle: ctx.isIdle(),
        trusted: ctx.isProjectTrusted(),
        pending: ctx.hasPendingMessages(),
        prompt: ctx.getSystemPrompt(),
        promptCwd: ctx.getSystemPromptOptions().cwd,
        sessionId: ctx.sessionManager.getSessionId(),
        leafId: ctx.sessionManager.getLeafId(),
        modelId: ctx.modelRegistry.find("provider-1", "model-1").id,
        allCount: ctx.modelRegistry.getAll().length,
        availableCount: ctx.modelRegistry.getAvailable().length,
        configured: ctx.modelRegistry.hasConfiguredAuth(ctx.model),
		providerStatus: ctx.modelRegistry.getProviderAuthStatus("provider-1").configured,
		registeredStatus: ctx.modelRegistry.getProviderAuthStatus("legacy-provider").configured,
		registryError: ctx.modelRegistry.getError(),
		providerIds: registeredProviderIds,
		providerConfigUrl: ctx.modelRegistry.getRegisteredProviderConfig("legacy-provider").baseUrl,
		nativeProviderId: ctx.modelRegistry.getRegisteredNativeProvider("native-provider").id,
		providerViewId: ctx.modelRegistry.getProvider("legacy-provider").id,
		providerDisplayName: ctx.modelRegistry.getProviderDisplayName("legacy-provider"),
		providerOAuth: ctx.modelRegistry.isUsingOAuth({ provider: "legacy-provider" }),
		unknownOAuth: ctx.modelRegistry.isUsingOAuth(ctx.model),
		unknownProvider: ctx.modelRegistry.getProvider("unknown"),
        authOk: auth.ok,
		facadeHasNullHeader: Object.prototype.hasOwnProperty.call(auth.headers ?? {}, "X-Null"),
        providerKey,
        providerAuthKey: providerAuth.auth.apiKey,
		providerAuthHeader: providerAuth.auth.headers["X-Test"],
		providerAuthNullHeader: providerAuth.auth.headers["X-Null"] === null,
        modelSet,
        created: created.cancelled,
        forked: forked.cancelled,
        navigated: navigated.cancelled,
        switched: switched.cancelled,
        execCwd: execResult.stdout.trim(),
        flag: pi.getFlag("bridge-flag"),
        sessionName: pi.getSessionName(),
        active: pi.getActiveTools(),
        tools: pi.getAllTools().map((tool) => tool.name),
        commands: pi.getCommands().map((command) => command.name),
        thinking: pi.getThinkingLevel(),
        usage: ctx.getContextUsage(),
      });
		if (ctx.modelRegistry.getProvider("legacy-provider").getModels()[0].id !== "legacy-model") throw new Error("provider lookup lost registered models");
		const configView = ctx.modelRegistry.getRegisteredProviderConfig("legacy-provider");
		if (typeof configView.refreshModels !== "function" || typeof configView.oauth?.login !== "function" || typeof configView.streamSimple !== "function") throw new Error("registered config view lost callbacks");
      pi.unregisterProvider("legacy-provider");
		if (ctx.modelRegistry.getRegisteredProviderConfig("legacy-provider") !== undefined || ctx.modelRegistry.getRegisteredProviderIds().includes("legacy-provider")) throw new Error("unregister did not update registry view");
    },
  });
	pi.registerCommand("stream-closed", { handler: async () => {
		await Promise.resolve();
		pi.appendEntry("stream-closed", { value: streamClosed });
	}});
  pi.registerCommand("signal", { handler: async (_args, ctx) => {
    await new Promise((resolve) => {
			const signal = ctx.signal;
			if (signal !== ctx.signal) throw new Error("AbortSignal identity changed");
			let onabort = false;
			signal.onabort = () => { onabort = true; };
			signal.addEventListener("abort", (event) => {
				if (!onabort || event.target !== signal || event.currentTarget !== signal) throw new Error("AbortSignal event shape");
				pi.appendEntry("signal-aborted", { aborted: signal.aborted, reason: String(signal.reason) });
        resolve();
      });
      pi.appendEntry("signal-ready", {});
    });
  }});
	pi.registerCommand("exec-cancel", { handler: async (_args, ctx) => {
		pi.appendEntry("exec-ready", {});
		const result = await pi.exec("/bin/sh", ["-c", "while :; do :; done"], { signal: ctx.signal });
		pi.appendEntry("exec-result", result);
	}});
	pi.registerCommand("exec-timeout", { handler: async () => {
		const result = await pi.exec("/bin/sh", ["-c", "while :; do :; done"], { timeout: 10 });
		pi.appendEntry("exec-result", result);
	}});
	pi.registerCommand("registry-refresh-error", { handler: async (_args, ctx) => {
		try {
			await ctx.modelRegistry.refresh();
			throw new Error("refresh unexpectedly succeeded");
		} catch (error) {
			pi.appendEntry("registry-refresh-error", { message: String(error) });
		}
	}});
}
`
	currentRunner = loadBridgeRunner(t, project, []bridgeSource{{"surface.ts", source}}, extensions.RunnerOptions{
		CWD: project, SessionManager: manager, ModelRegistry: registry, Actions: actions,
		ContextActions: contextActions, CommandActions: &commandActions,
	})
	legacyProvider := capturedProviders["legacy-provider"]
	if legacyProvider.ID != "legacy-provider" || legacyProvider.Config.BaseURL != "https://provider.invalid" || legacyProvider.Config.AuthHeader == nil || !*legacyProvider.Config.AuthHeader {
		t.Fatalf("legacy provider = %#v", legacyProvider)
	}
	if len(legacyProvider.Config.Models) != 1 || legacyProvider.Config.Models[0].ContextWindow != 1000.5 || legacyProvider.Config.Models[0].MaxTokens != 100.25 {
		t.Fatalf("legacy provider numeric model fields = %#v", legacyProvider.Config.Models)
	}
	nativeProvider := capturedProviders["native-provider"]
	if nativeProvider.ID != "native-provider" || nativeProvider.BaseURL != "https://native.invalid" || nativeProvider.GetModels == nil || nativeProvider.FilterModels == nil || nativeProvider.Stream == nil || nativeProvider.StreamSimple == nil {
		t.Fatalf("native provider = %#v", nativeProvider)
	}
	exerciseProviderCallbacks(t, legacyProvider, nativeProvider, model)
	if err := currentRunner.Command("stream-closed").Handler(context.Background(), "", currentRunner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if currentRunner.MessageRenderer("bridge-message") == nil || currentRunner.EntryRenderer("bridge-entry") == nil {
		t.Fatal("deferred renderers were not recorded")
	}
	flag := currentRunner.Flags()["bridge-flag"]
	if flag.Type != extensions.FlagString || flag.Default != "default-value" || currentRunner.FlagValues()["bridge-flag"] != "default-value" {
		t.Fatalf("flag = %#v, values = %#v", flag, currentRunner.FlagValues())
	}
	shortcut := currentRunner.Shortcuts(nil)["ctrl+x"]
	if shortcut.Handler == nil {
		t.Fatal("shortcut was not registered")
	}
	if err := shortcut.Handler(context.Background(), currentRunner.CreateContext()); err != nil {
		t.Fatal(err)
	}
	command := currentRunner.Command("surface")
	if command == nil {
		t.Fatal("surface command was not registered")
	}
	completions, err := command.GetArgumentCompletions(context.Background(), "pre")
	if err != nil || len(completions) != 1 || completions[0].Value != "pre-value" {
		t.Fatalf("completions = %#v, err = %v", completions, err)
	}
	surfaceContext := context.WithValue(context.Background(), callbackMarker, "surface")
	if err := command.Handler(surfaceContext, "arguments", currentRunner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if unregistered != "legacy-provider" {
		t.Fatalf("unregistered = %q", unregistered)
	}
	if manager.GetEntries() == nil || manager.GetEntries()[len(manager.GetEntries())-1].Type != "custom" {
		t.Fatalf("setup did not append a custom entry: %#v", manager.GetEntries())
	}
	mu.Lock()
	var surface map[string]any
	streamClosed := false
	for _, item := range records {
		if item.name == "surface-result" {
			surface, _ = item.data.(map[string]any)
		}
		if item.name == "stream-closed" {
			value, _ := item.data.(map[string]any)
			streamClosed, _ = value["value"].(bool)
		}
	}
	mu.Unlock()
	if surface == nil || surface["execCwd"] != project || surface["flag"] != "default-value" || surface["authOk"] != true || surface["facadeHasNullHeader"] != false || surface["providerKey"] != "secret" || surface["providerAuthKey"] != "secret" || surface["providerAuthHeader"] != "yes" || surface["providerAuthNullHeader"] != true || surface["sessionName"] != "named" || surface["providerStatus"] != true || surface["registeredStatus"] != false || surface["registryError"] != "registry warning" || surface["providerConfigUrl"] != "https://provider.invalid" || surface["nativeProviderId"] != "native-provider" || surface["providerViewId"] != "legacy-provider" || surface["providerDisplayName"] != "Legacy Provider" || surface["providerOAuth"] != true {
		t.Fatalf("surface result = %#v", surface)
	}
	if !streamClosed {
		t.Fatal("async iterator return() did not close the provider generator")
	}
	if surface["unknownProvider"] != nil || surface["unknownOAuth"] != false {
		t.Fatalf("unknown provider data = provider %#v, oauth %#v", surface["unknownProvider"], surface["unknownOAuth"])
	}
	if registry.reloads != 1 {
		t.Fatalf("model registry reloads = %d", registry.reloads)
	}
	registry.reloadErr = errors.New("registry reload failed")
	if err := currentRunner.Command("registry-refresh-error").Handler(context.Background(), "", currentRunner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	var refreshError string
	for _, item := range records {
		if item.name == "registry-refresh-error" {
			if object, ok := item.data.(map[string]any); ok {
				refreshError = fmt.Sprint(object["message"])
			}
		}
	}
	mu.Unlock()
	if !strings.Contains(refreshError, "registry reload failed") || registry.reloads != 2 {
		t.Fatalf("model registry refresh error = %q, reloads = %d", refreshError, registry.reloads)
	}
	select {
	case active := <-directContext:
		if !active {
			t.Fatal("command callback did not install its active context")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("command context was not observed")
	}
	select {
	case summary := <-compactComplete:
		if summary != "compacted" {
			t.Fatalf("compact summary = %q", summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("compact callback did not run")
	}
	select {
	case active := <-compactContext:
		if !active {
			t.Fatal("compact callback did not inherit its active context")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("compact callback context was not observed")
	}

	signalCommand := currentRunner.Command("signal")
	done := make(chan error, 1)
	go func() { done <- signalCommand.Handler(context.Background(), "", currentRunner.CreateCommandContext()) }()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("signal command did not become ready")
	}
	cancelSignal()
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("AbortSignal listener did not run")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("signal command did not settle")
	}

	execSignal, cancelExec := context.WithCancel(context.Background())
	signalContext = execSignal
	done = make(chan error, 1)
	go func() {
		done <- currentRunner.Command("exec-cancel").Handler(context.Background(), "", currentRunner.CreateCommandContext())
	}()
	select {
	case <-execReady:
	case <-time.After(2 * time.Second):
		t.Fatal("exec-cancel did not start")
	}
	cancelExec()
	select {
	case killed := <-execKilled:
		if !killed {
			t.Fatal("signal-cancelled exec was not marked killed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("signal-cancelled exec did not settle")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	signalContext = context.Background()
	if err := currentRunner.Command("exec-timeout").Handler(context.Background(), "", currentRunner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	select {
	case killed := <-execKilled:
		if !killed {
			t.Fatal("timed out exec was not marked killed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out exec did not settle")
	}
}

func TestCrossVMEventBusEmitAndUnsubscribe(t *testing.T) {
	project := t.TempDir()
	received := make(chan map[string]any, 2)
	activeContext := make(chan bool, 2)
	type eventContextKey struct{}
	contextMarker := eventContextKey{}
	actions := extensions.Actions{AppendEntry: func(ctx context.Context, customType string, data any) error {
		if customType == "bus-received" {
			received <- data.(map[string]any)
			activeContext <- ctx.Value(contextMarker) == "event"
		}
		return nil
	}}
	listener := `
export default function (pi) {
  const unsubscribe = pi.events.on("bridge:event", async (data) => {
    await Promise.resolve();
    pi.appendEntry("bus-received", data);
  });
  pi.registerCommand("unsubscribe", { handler: async () => unsubscribe() });
}
`
	sender := `
export default function (pi) {
  pi.registerCommand("emit-event", { handler: async (args) => {
    pi.events.emit("bridge:event", { value: args });
  }});
}
`
	runner := loadBridgeRunner(t, project, []bridgeSource{{"listener.ts", listener}, {"sender.ts", sender}}, extensions.RunnerOptions{CWD: project, Actions: actions})
	emitContext := context.WithValue(context.Background(), contextMarker, "event")
	if err := runner.Command("emit-event").Handler(emitContext, "one", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-received:
		if value["value"] != "one" {
			t.Fatalf("event data = %#v", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cross-VM event was not delivered")
	}
	select {
	case active := <-activeContext:
		if !active {
			t.Fatal("event-bus callback did not inherit the emitter context")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event-bus callback context was not observed")
	}
	if err := runner.Command("unsubscribe").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if err := runner.Command("emit-event").Handler(context.Background(), "two", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-received:
		t.Fatalf("event delivered after unsubscribe: %#v", value)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCrossVMEventBusHandlerStartsBeforeEmitReturns(t *testing.T) {
	project := t.TempDir()
	listenerPath := filepath.Join(project, "listener.ts")
	senderPath := filepath.Join(project, "sender.ts")
	mustWrite(t, listenerPath, `
export default function (pi) {
  pi.events.on("sync-prefix", async () => {
    pi.appendEntry("listener-start", {});
    await Promise.resolve();
  });
}
`)
	mustWrite(t, senderPath, `
export default function (pi) {
  pi.registerCommand("sync-prefix", { handler: async () => {
    pi.events.emit("sync-prefix", {});
    pi.appendEntry("sender-after", {});
  }});
}
`)
	loader := NewLoader(DiscoveryOptions{
		CWD: project, AgentDir: filepath.Join(project, "agent"), ExplicitPaths: []string{listenerPath, senderPath}, ProjectTrusted: true,
	})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 || len(loader.vms) != 2 {
		t.Fatalf("load result = %#v, VMs = %d", loaded.Errors, len(loader.vms))
	}
	var mu sync.Mutex
	var order []string
	runner := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{CWD: project, Actions: extensions.Actions{
		AppendEntry: func(_ context.Context, customType string, _ any) error {
			mu.Lock()
			order = append(order, customType)
			mu.Unlock()
			return nil
		},
	}})
	blocked := make(chan struct{})
	release := make(chan struct{})
	if !loader.vms[0].postWithContext(context.Background(), func(*sobek.Runtime) error {
		close(blocked)
		<-release
		return nil
	}) {
		t.Fatal("listener VM closed before test")
	}
	<-blocked
	done := make(chan error, 1)
	go func() {
		done <- runner.Command("sync-prefix").Handler(context.Background(), "", runner.CreateCommandContext())
	}()
	completedBeforeListenerCouldRun := false
	select {
	case <-done:
		completedBeforeListenerCouldRun = true
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if !completedBeforeListenerCouldRun {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cross-VM synchronous prefix deadlocked")
		}
	}
	if completedBeforeListenerCouldRun {
		t.Fatal("cross-VM emit returned before the listener started")
	}
	mu.Lock()
	defer mu.Unlock()
	listenerIndex := slices.Index(order, "listener-start")
	senderIndex := slices.Index(order, "sender-after")
	if listenerIndex < 0 || senderIndex < 0 || listenerIndex > senderIndex {
		t.Fatalf("cross-VM event order = %#v", order)
	}
}

func TestSameVMEventBusHandlerStartsSynchronously(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  const phases = [];
  pi.events.on("same", async () => {
    phases.push("started");
    await Promise.resolve();
    phases.push("settled");
  });
  pi.registerCommand("same-vm", { handler: async () => {
    pi.events.emit("same", {});
    if (phases[0] !== "started") throw new Error("same-VM handler did not start synchronously");
    await Promise.resolve();
    pi.appendEntry("same-vm", { phases });
  }});
}
`
	var phases []string
	actions := extensions.Actions{AppendEntry: func(_ context.Context, customType string, data any) error {
		if customType == "same-vm" {
			encoded, _ := json.Marshal(data)
			var value struct {
				Phases []string `json:"phases"`
			}
			if err := json.Unmarshal(encoded, &value); err != nil {
				return err
			}
			phases = value.Phases
		}
		return nil
	}}
	runner := loadBridgeRunner(t, project, []bridgeSource{{"same-vm.ts", source}}, extensions.RunnerOptions{CWD: project, Actions: actions})
	if err := runner.Command("same-vm").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if len(phases) == 0 || phases[0] != "started" {
		t.Fatalf("same-VM phases = %#v", phases)
	}
}

func TestCompactCallbackSurvivesCancelledDispatchContext(t *testing.T) {
	// Compaction resolves long after the dispatching event's context is
	// cancelled; upstream still invokes onComplete/onError, so the bridge
	// falls back to the VM lifetime context (WP sweep: ctx.compact()
	// callbacks were silently dropped in every mode).
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("compact-later", { handler: async (_args, ctx) => {
    ctx.compact({ onComplete: () => pi.appendEntry("late-callback", {}) });
  }});
}
`
	var complete func(harness.CompactionResult)
	called := make(chan struct{}, 1)
	actions := extensions.Actions{AppendEntry: func(context.Context, string, any) error {
		called <- struct{}{}
		return nil
	}}
	contextActions := extensions.ContextActions{Compact: func(options *extensions.CompactOptions) { complete = options.OnComplete }}
	runner := loadBridgeRunner(t, project, []bridgeSource{{"cancelled-callback.ts", source}}, extensions.RunnerOptions{CWD: project, Actions: actions, ContextActions: contextActions})
	ctx, cancel := context.WithCancel(context.Background())
	if err := runner.Command("compact-later").Handler(ctx, "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	cancel()
	complete(harness.CompactionResult{Summary: "late"})
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("compact onComplete was dropped after the dispatch context was cancelled")
	}
}

func TestWave1UpstreamExamplesLoadAndExercise(t *testing.T) {
	t.Run("hello", func(t *testing.T) {
		project := t.TempDir()
		runner := loadBridgeRunner(t, project, []bridgeSource{{"hello.ts", fixtureSource(t, "hello.ts")}}, extensions.RunnerOptions{CWD: project})
		tool := runner.ToolDefinition("hello")
		result, err := tool.Execute(context.Background(), "hello-1", map[string]any{"name": "Pi"}, nil, runner.CreateContext())
		if err != nil || result.Content[0].(*ai.TextContent).Text != "Hello, Pi!" || result.Details.(map[string]any)["greeted"] != "Pi" {
			t.Fatalf("hello result = %#v, err = %v", result, err)
		}
	})

	t.Run("pirate", func(t *testing.T) {
		project := t.TempDir()
		runner := loadBridgeRunner(t, project, []bridgeSource{{"pirate.ts", fixtureSource(t, "pirate.ts")}}, extensions.RunnerOptions{CWD: project})
		if result := runner.EmitBeforeAgentStart(context.Background(), "prompt", nil, "base prompt", extensions.SystemPromptOptions{CWD: project}); result != nil {
			t.Fatalf("disabled pirate result = %#v", result)
		}
		if err := runner.Command("pirate").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
			t.Fatal(err)
		}
		result := runner.EmitBeforeAgentStart(context.Background(), "prompt", nil, "base prompt", extensions.SystemPromptOptions{CWD: project})
		if result == nil || result.SystemPrompt == nil || !strings.Contains(*result.SystemPrompt, "PIRATE MODE") {
			t.Fatalf("enabled pirate result = %#v", result)
		}
	})

	t.Run("summarize", func(t *testing.T) {
		project := t.TempDir()
		manager, err := session.Create(project, filepath.Join(project, "sessions"), session.WithSessionID("summarize-session"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.AppendMessage(&ai.UserMessage{Content: ai.NewUserText("Summarize this non-empty conversation"), Timestamp: 1}); err != nil {
			t.Fatal(err)
		}
		registry := &bridgeModelRegistry{key: "summary-key"}
		var requestBody string
		previousTransport := http.DefaultTransport
		http.DefaultTransport = bridgeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				return nil, readErr
			}
			requestBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(strings.Join([]string{
					`data: {"type":"response.created","response":{"id":"resp_summary"}}`,
					``,
					`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_summary","role":"assistant","content":[],"status":"in_progress"}}`,
					``,
					`data: {"type":"response.output_text.delta","output_index":0,"delta":"Summary complete"}`,
					``,
					`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_summary","role":"assistant","content":[{"type":"output_text","text":"Summary complete","annotations":[]}],"status":"completed","phase":"final_answer"}}`,
					``,
					`data: {"type":"response.completed","response":{"id":"resp_summary","status":"completed","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`,
					``, `data: [DONE]`, ``,
				}, "\n"))),
				Request: request,
			}, nil
		})
		t.Cleanup(func() { http.DefaultTransport = previousTransport })
		runner := loadBridgeRunner(t, project, []bridgeSource{{"summarize.ts", fixtureSource(t, "summarize.ts")}}, extensions.RunnerOptions{CWD: project, SessionManager: manager, ModelRegistry: registry})
		if err := runner.Command("summarize").Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
			t.Fatal(err)
		}
		if registry.resolveCalls == 0 {
			t.Fatal("summarize did not reach model auth resolution")
		}
		if !strings.Contains(requestBody, "Summarize this non-empty conversation") || !strings.Contains(requestBody, "Summarize this conversation so I can resume it later") {
			t.Fatalf("summarize completion request = %s", requestBody)
		}
	})

	t.Run("todo", func(t *testing.T) {
		project := t.TempDir()
		runner := loadBridgeRunner(t, project, []bridgeSource{{"todo.ts", fixtureSource(t, "todo.ts")}}, extensions.RunnerOptions{CWD: project})
		tool := runner.ToolDefinition("todo")
		if tool == nil {
			t.Fatal("todo tool was not registered")
		}
		added, err := tool.Execute(context.Background(), "todo-1", map[string]any{"action": "add", "text": "ship WP-520"}, nil, runner.CreateContext())
		if err != nil || added.Content[0].(*ai.TextContent).Text != "Added todo #1: ship WP-520" {
			t.Fatalf("todo add = %#v, err = %v", added, err)
		}
		listed, err := tool.Execute(context.Background(), "todo-2", map[string]any{"action": "list"}, nil, runner.CreateContext())
		if err != nil || !strings.Contains(listed.Content[0].(*ai.TextContent).Text, "ship WP-520") {
			t.Fatalf("todo list = %#v, err = %v", listed, err)
		}
		toggled, err := tool.Execute(context.Background(), "todo-3", map[string]any{"action": "toggle", "id": 1}, nil, runner.CreateContext())
		if err != nil || toggled.Content[0].(*ai.TextContent).Text != "Todo #1 completed" {
			t.Fatalf("todo toggle = %#v, err = %v", toggled, err)
		}
		cleared, err := tool.Execute(context.Background(), "todo-4", map[string]any{"action": "clear"}, nil, runner.CreateContext())
		if err != nil || cleared.Content[0].(*ai.TextContent).Text != "Cleared 1 todos" {
			t.Fatalf("todo clear = %#v, err = %v", cleared, err)
		}
	})

	t.Run("structured-output", func(t *testing.T) {
		project := t.TempDir()
		runner := loadBridgeRunner(t, project, []bridgeSource{{"structured-output.ts", fixtureSource(t, "structured-output.ts")}}, extensions.RunnerOptions{CWD: project})
		tool := runner.ToolDefinition("structured_output")
		result, err := tool.Execute(context.Background(), "structured-1", map[string]any{
			"headline": "Done", "summary": "Implemented", "actionItems": []any{"Review"},
		}, nil, runner.CreateContext())
		details := result.Details.(map[string]any)
		if err != nil || result.Terminate == nil || !*result.Terminate || result.Content[0].(*ai.TextContent).Text != "Saved structured output: Done" || details["headline"] != "Done" {
			t.Fatalf("structured result = %#v, err = %v", result, err)
		}
	})

	t.Run("dynamic-tools", func(t *testing.T) {
		project := t.TempDir()
		runner := loadBridgeRunner(t, project, []bridgeSource{{"dynamic-tools.ts", fixtureSource(t, "dynamic-tools.ts")}}, extensions.RunnerOptions{CWD: project})
		runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
		if runner.ToolDefinition("echo_session") == nil {
			t.Fatal("session tool was not registered")
		}
		if tool := runner.ToolDefinition("echo_session"); tool.PromptSnippet == "" || len(tool.PromptGuidelines) != 1 {
			t.Fatalf("dynamic tool metadata = %#v", tool)
		}
		if err := runner.Command("add-echo-tool").Handler(context.Background(), "echo_review", runner.CreateCommandContext()); err != nil {
			t.Fatal(err)
		}
		tool := runner.ToolDefinition("echo_review")
		result, err := tool.Execute(context.Background(), "echo-1", map[string]any{"message": "ready"}, nil, runner.CreateContext())
		if err != nil || result.Content[0].(*ai.TextContent).Text != "[echo_review] ready" {
			t.Fatalf("dynamic result = %#v, err = %v", result, err)
		}
	})

	t.Run("commands", func(t *testing.T) {
		project := t.TempDir()
		actions := extensions.Actions{GetCommands: func() ([]extensions.SlashCommandInfo, error) {
			return []extensions.SlashCommandInfo{{
				Name: "commands", Description: "List commands", Source: extensions.SlashCommandExtension,
				SourceInfo: extensions.SourceInfo{Path: "commands.ts", Source: "local"},
			}}, nil
		}}
		runner := loadBridgeRunner(t, project, []bridgeSource{{"commands.ts", fixtureSource(t, "commands.ts")}}, extensions.RunnerOptions{CWD: project, Actions: actions})
		command := runner.Command("commands")
		items, err := command.GetArgumentCompletions(context.Background(), "ex")
		if err != nil || len(items) != 1 || items[0].Value != "extension" {
			t.Fatalf("command completions = %#v, err = %v", items, err)
		}
		if err := command.Handler(context.Background(), "skill", runner.CreateCommandContext()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("send-user-message", func(t *testing.T) {
		project := t.TempDir()
		var sent ai.UserContent
		actions := extensions.Actions{SendUserMessage: func(_ context.Context, content ai.UserContent, _ *extensions.SendUserMessageOptions) error {
			sent = content
			return nil
		}}
		runner := loadBridgeRunner(t, project, []bridgeSource{{"send-user-message.ts", fixtureSource(t, "send-user-message.ts")}}, extensions.RunnerOptions{CWD: project, Actions: actions})
		if err := runner.Command("ask").Handler(context.Background(), "What next?", runner.CreateCommandContext()); err != nil {
			t.Fatal(err)
		}
		if sent.Text == nil || *sent.Text != "What next?" {
			t.Fatalf("sent user content = %#v", sent)
		}
	})

	t.Run("event-bus", func(t *testing.T) {
		project := t.TempDir()
		runner := loadBridgeRunner(t, project, []bridgeSource{{"event-bus.ts", fixtureSource(t, "event-bus.ts")}}, extensions.RunnerOptions{CWD: project})
		runner.Emit(context.Background(), extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
		if err := runner.Command("emit").Handler(context.Background(), "from test", runner.CreateCommandContext()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestToolOverrideRegistrationUsesLastDefinition(t *testing.T) {
	project := t.TempDir()
	source := `
import { Type } from "typebox";
export default function (pi) {
	pi.registerTool({ name: "read", label: "First", description: "first", parameters: Type.Object({}), async execute() { return { content: [{ type: "text", text: "first" }] }; } });
	pi.registerTool({ name: "read", label: "Override", description: "override", parameters: Type.Object({}), async execute() { return { content: [{ type: "text", text: "override" }] }; } });
}
`
	runner := loadBridgeRunner(t, project, []bridgeSource{{"override.ts", source}}, extensions.RunnerOptions{CWD: project})
	tool := runner.ToolDefinition("read")
	result, err := tool.Execute(context.Background(), "read-1", map[string]any{}, nil, runner.CreateContext())
	if err != nil || tool.Label != "Override" || result.Content[0].(*ai.TextContent).Text != "override" {
		t.Fatalf("override tool = %#v, result = %#v, err = %v", tool, result, err)
	}
}

func TestJSNativeProviderUsesSessionRuntimeRegistryAuthAndStream(t *testing.T) {
	project := t.TempDir()
	agentDir := filepath.Join(project, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `
export default function (pi) {
  const provider = (baseUrl, text) => {
    let refreshed = false;
    const refreshNetworks = [];
    return ({
    id: "js-native",
    name: "JS Native",
    auth: { apiKey: { name: "JS key", resolve: async () => ({ auth: { apiKey: "js-key", headers: { "X-JS-Auth": "yes", Authorization: null } }, source: "js" }) } },
    getModels: () => [{
      id: "js-model", name: refreshed ? "JS Model Refreshed" : "JS Model", api: "openai-responses", provider: "js-native",
      baseUrl, reasoning: false, input: ["text"],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 1000, maxTokens: 100
    }],
    refreshModels: async (ctx) => {
      await Promise.resolve();
      if (ctx.credential?.key !== "js-key") throw new Error("refresh credential/context missing");
      refreshNetworks.push(ctx.allowNetwork);
      refreshed = true;
    },
    stream: () => { throw new Error("stream should not be selected"); },
    streamSimple: async function* (_model, _context, options) {
      if (options.apiKey !== "js-key") throw new Error("registry auth did not reach streamSimple");
      if (options.headers["X-JS-Auth"] !== "yes" || options.headers.Authorization !== null) throw new Error("nullable registry headers did not reach streamSimple");
      const message = {
        role: "assistant", content: [{ type: "text", text }],
        api: "openai-responses", provider: "js-native", model: "js-model",
        usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
        stopReason: "stop", timestamp: 7
      };
      yield { type: "done", reason: "stop", message };
    }
  }); };
  let activeProvider = provider("https://unused.invalid", "from-js-provider");
  pi.registerProvider(activeProvider);
  pi.registerCommand("inspect-provider", {
    handler: async (_args, ctx) => {
      if (!ctx.modelRegistry.getAvailable().some((entry) => entry.provider === "js-native")) throw new Error("provider unavailable");
      if (!ctx.modelRegistry.getProviderAuthStatus("js-native").configured) throw new Error("provider auth status missing");
      const nativeView = ctx.modelRegistry.getRegisteredNativeProvider("js-native");
      if (nativeView !== activeProvider) throw new Error("native provider view lost registration identity");
      if (nativeView.getModels()[0].id !== "js-model" || typeof nativeView.auth.apiKey.resolve !== "function" || typeof nativeView.refreshModels !== "function" || typeof nativeView.stream !== "function" || typeof nativeView.streamSimple !== "function") throw new Error("native provider view missing callbacks");
      const auth = await ctx.modelRegistry.getApiKeyAndHeaders(ctx.model);
      if (!auth.ok || auth.apiKey !== "js-key") throw new Error("native request auth missing");
      if (await ctx.modelRegistry.refresh() !== undefined) throw new Error("refresh did not resolve undefined");
      if (refreshNetworks[refreshNetworks.length - 1] !== true) throw new Error("explicit refresh did not allow network access");
      if (ctx.modelRegistry.find("js-native", "js-model").name !== "JS Model Refreshed") throw new Error("native refreshModels was not published");
    }
  });
  pi.registerCommand("replace-native", {
    handler: async () => {
      activeProvider = provider("https://replacement.invalid", "from-replaced-provider");
      pi.registerProvider(activeProvider);
    }
  });
}
`
	path := filepath.Join(project, "provider.ts")
	mustWrite(t, path, source)
	loader := NewLoader(DiscoveryOptions{CWD: project, AgentDir: agentDir, ExplicitPaths: []string{path}, ProjectTrusted: true})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	models, err := config.NewModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	var registrationErrors []extensions.ExtensionError
	loaded.Registry.BindModelRegistry(models, func(value extensions.ExtensionError) { registrationErrors = append(registrationErrors, value) })
	if len(registrationErrors) != 0 {
		t.Fatalf("registration errors = %#v", registrationErrors)
	}
	model, ok := models.Find("js-native", "js-model")
	if !ok || !models.HasConfiguredAuth("js-native", nil) {
		t.Fatalf("registered model/auth missing: model=%#v ok=%v", model, ok)
	}
	settings, err := config.NewSettingsManager(project, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(project)
	if err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
		Model: &model, SystemPrompt: "test", Messages: agent.AgentMessages{}, Tools: []agent.AgentTool{},
	}))
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: loaded.Registry, ModelRegistry: models,
		GetRequestAuth: func(ctx context.Context, provider ai.ProviderID) (*agent.RequestAuth, error) {
			resolved, resolveErr := models.ResolveProviderAuth(ctx, string(provider), nil)
			if resolveErr != nil || resolved == nil {
				return nil, resolveErr
			}
			return &agent.RequestAuth{APIKey: resolved.Auth.APIKey, Headers: resolved.Auth.Headers, BaseURL: resolved.Auth.BaseURL, Env: ai.ProviderEnv(resolved.Env)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	if err := runtime.Prompt(context.Background(), "use the JS provider"); err != nil {
		t.Fatal(err)
	}
	messages := runtime.State().Messages
	assistant, ok := messages[len(messages)-1].(*ai.AssistantMessage)
	text := ""
	if ok && len(assistant.Content) == 1 {
		if content, textOK := assistant.Content[0].(*ai.TextContent); textOK {
			text = content.Text
		}
	}
	if text != "from-js-provider" {
		errorMessage := ""
		if assistant != nil && assistant.ErrorMessage != nil {
			errorMessage = *assistant.ErrorMessage
		}
		t.Fatalf("runtime assistant = %#v content=%#v text=%q error=%q", assistant, assistant.Content, text, errorMessage)
	}
	if err := runtime.Prompt(context.Background(), "/inspect-provider"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Prompt(context.Background(), "/replace-native"); err != nil {
		t.Fatal(err)
	}
	if current := runtime.State().Model; current == nil || current.BaseURL != "https://replacement.invalid" {
		t.Fatalf("command-time provider did not refresh active model: %#v", current)
	}
	if err := runtime.Prompt(context.Background(), "use the replacement"); err != nil {
		t.Fatal(err)
	}
	messages = runtime.State().Messages
	assistant, ok = messages[len(messages)-1].(*ai.AssistantMessage)
	if !ok || len(assistant.Content) != 1 || assistant.Content[0].(*ai.TextContent).Text != "from-replaced-provider" {
		t.Fatalf("replacement provider stream result = %#v", assistant)
	}
}

func TestModelRegistryRefreshAllowsProviderReplacementFromRefreshCallback(t *testing.T) {
	project := t.TempDir()
	agentDir := filepath.Join(project, "agent")
	path := filepath.Join(project, "refresh-replacement.ts")
	mustWrite(t, path, `
export default function (pi) {
  const stream = async function* () {};
  const model = (name) => ({
    id: "refresh-model", name, api: "openai-responses", provider: "refresh-mutator",
    baseUrl: "https://refresh.invalid", reasoning: false, input: ["text"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 1000, maxTokens: 100
  });
  const replacement = {
    id: "refresh-mutator", name: "Replacement",
    auth: { apiKey: { name: "Key", resolve: async () => ({ auth: { apiKey: "key" } }) } },
    getModels: () => [model("Replacement Model")], stream, streamSimple: stream,
  };
  pi.registerProvider({
    id: "refresh-mutator", name: "Original",
    auth: { apiKey: { name: "Key", resolve: async () => ({ auth: { apiKey: "key" } }) } },
    getModels: () => [model("Original Model")],
    refreshModels: async (ctx) => {
      await Promise.resolve();
      if (ctx.allowNetwork) pi.registerProvider(replacement);
    },
    stream, streamSimple: stream,
  });
  pi.registerCommand("refresh-replacement", {
    handler: async (_args, ctx) => {
      await ctx.modelRegistry.refresh();
    },
  });
}
`)
	loader := NewLoader(DiscoveryOptions{CWD: project, AgentDir: agentDir, ExplicitPaths: []string{path}, ProjectTrusted: true})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	registry, err := config.NewModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	var registrationErrors []extensions.ExtensionError
	loaded.Registry.BindModelRegistry(registry, func(value extensions.ExtensionError) { registrationErrors = append(registrationErrors, value) })
	if len(registrationErrors) != 0 {
		t.Fatalf("registration errors = %#v", registrationErrors)
	}
	runner := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{
		CWD: project, ModelRegistry: registry,
		Actions: extensions.Actions{
			RegisterProvider: registry.RegisterProvider, RegisterProviderConfig: registry.RegisterProviderConfig, UnregisterProvider: registry.UnregisterProvider,
		},
	})
	command := runner.Command("refresh-replacement")
	if command == nil {
		t.Fatal("refresh command was not registered")
	}
	if err := command.Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	provider, ok := registry.RegisteredNativeProvider("refresh-mutator")
	model, modelOK := registry.Find("refresh-mutator", "refresh-model")
	if !ok || provider.Name != "Replacement" || !modelOK || model.Name != "Replacement Model" {
		t.Fatalf("replacement provider/model = %#v, %#v (ok=%v, modelOK=%v)", provider, model, ok, modelOK)
	}
}

func TestJSNativeProviderRejectsAsyncGetModelsWithoutMutatingRegistry(t *testing.T) {
	project := t.TempDir()
	agentDir := filepath.Join(project, "agent")
	path := filepath.Join(project, "async-models.ts")
	mustWrite(t, path, `
export default function (pi) {
  const stream = async function* () {};
  pi.registerProvider({
    id: "async-models", name: "Async Models",
    auth: { apiKey: { name: "Key", resolve: async () => ({ auth: { apiKey: "key" } }) } },
    getModels: async () => [], stream, streamSimple: stream,
  });
}
`)
	loader := NewLoader(DiscoveryOptions{CWD: project, AgentDir: agentDir, ExplicitPaths: []string{path}, ProjectTrusted: true})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	registry, err := config.NewModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	var registrationErrors []extensions.ExtensionError
	loaded.Registry.BindModelRegistry(registry, func(value extensions.ExtensionError) { registrationErrors = append(registrationErrors, value) })
	if len(registrationErrors) != 1 || !strings.Contains(registrationErrors[0].Error, "getModels must return synchronously") {
		t.Fatalf("registration errors = %#v", registrationErrors)
	}
	if _, ok := registry.RegisteredNativeProvider("async-models"); ok {
		t.Fatal("failed native registration mutated the shared registry")
	}
}

func exerciseProviderCallbacks(t *testing.T, legacy, native extensions.Provider, model ai.Model) {
	t.Helper()
	store := &bridgeProviderStore{entry: &extensions.ProviderModelsStoreEntry{Models: []ai.Model{model}}}
	models, err := legacy.Config.RefreshModels(extensions.RefreshModelsContext{
		Credential: aiauth.APIKeyCredential("credential"), Store: store, AllowNetwork: true, Force: true, Signal: context.Background(),
	})
	if err != nil || len(models) != 1 || models[0].ID != "legacy-model" {
		t.Fatalf("refresh models = %#v, err = %v", models, err)
	}
	store.mu.Lock()
	written := store.written
	store.mu.Unlock()
	if written == nil || written.CheckedAt == nil || *written.CheckedAt != 7 {
		t.Fatalf("provider store write = %#v", written)
	}
	if legacy.Config.OAuth == nil {
		t.Fatal("OAuth provider was not decoded")
	}
	var authInfo extensions.OAuthAuthInfo
	var deviceInfo extensions.OAuthDeviceCodeInfo
	var progress string
	credentials, err := legacy.Config.OAuth.Login(context.Background(), extensions.OAuthLoginCallbacks{
		Signal:       context.Background(),
		OnAuth:       func(value extensions.OAuthAuthInfo) { authInfo = value },
		OnDeviceCode: func(value extensions.OAuthDeviceCodeInfo) { deviceInfo = value },
		OnPrompt: func(prompt extensions.OAuthPrompt) (string, error) {
			if prompt.Message != "Token" {
				return "", fmt.Errorf("prompt = %#v", prompt)
			}
			return "prompted", nil
		},
		OnProgress:        func(value string) { progress = value },
		OnManualCodeInput: func() (string, error) { return "manual", nil },
		OnSelect: func(prompt extensions.OAuthSelectPrompt) (*string, error) {
			if len(prompt.Options) != 1 || prompt.Options[0].ID != "one" {
				return nil, fmt.Errorf("select = %#v", prompt)
			}
			value := "one"
			return &value, nil
		},
	})
	if err != nil || credentials.Access != "prompted:one" || credentials.Refresh != "manual" || credentials.Extra["tenant"] != "acme" || authInfo.URL != "https://auth.invalid" || deviceInfo.UserCode != "CODE" || progress != "working" {
		t.Fatalf("OAuth login = %#v, auth = %#v, device = %#v, progress = %q, err = %v", credentials, authInfo, deviceInfo, progress, err)
	}
	nullableCredentials, err := legacy.Config.OAuth.Login(context.Background(), extensions.OAuthLoginCallbacks{
		Signal:            context.Background(),
		OnPrompt:          func(extensions.OAuthPrompt) (string, error) { return "", nil },
		OnManualCodeInput: func() (string, error) { return "", nil },
		OnSelect:          func(extensions.OAuthSelectPrompt) (*string, error) { return nil, nil },
	})
	if err != nil || nullableCredentials.Access != ":undefined" {
		t.Fatalf("nullable OAuth selection = %#v, err = %v", nullableCredentials, err)
	}
	refreshed, err := legacy.Config.OAuth.RefreshToken(context.Background(), credentials)
	apiKey, keyErr := legacy.Config.OAuth.GetAPIKey(refreshed)
	if err != nil || keyErr != nil || refreshed.Access != "prompted:one:refreshed" || refreshed.Extra["tenant"] != "acme" || apiKey != refreshed.Access {
		t.Fatalf("OAuth refresh = %#v, err = %v", refreshed, err)
	}
	modified, err := legacy.Config.OAuth.ModifyModels([]ai.Model{model}, credentials)
	if err != nil || len(modified) != 1 || modified[0].Name != "Model One:prompted:one" {
		t.Fatalf("OAuth models = %#v", modified)
	}
	assertProviderStream(t, legacy.Config.Stream, model)

	nativeModels, err := native.GetModels()
	if err != nil || len(nativeModels) != 1 || nativeModels[0].ID != "native-model" {
		t.Fatalf("native models = %#v, err = %v", nativeModels, err)
	}
	if filtered, filterErr := native.FilterModels(append(nativeModels, model), aiauth.APIKeyCredential("native-filter-key")); filterErr != nil || len(filtered) != 1 || filtered[0].ID != "native-model" {
		t.Fatalf("native filtered models = %#v", filtered)
	}
	resolved, err := native.Auth.APIKey.Resolve(context.Background(), aiauth.EnvironmentContext{}, nil)
	if err != nil || resolved == nil || resolved.Auth.APIKey == nil || *resolved.Auth.APIKey != "native-secret" {
		t.Fatalf("native auth = %#v, err = %v", resolved, err)
	}
	assertProviderStream(t, native.Stream, model)
}

func assertProviderStream(t *testing.T, stream agent.StreamFn, model ai.Model) {
	t.Helper()
	events, err := stream(context.Background(), &model, ai.Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var received ai.AssistantMessageEvent
	var streamError error
	events(func(event ai.AssistantMessageEvent, err error) bool {
		received, streamError = event, err
		return false
	})
	if streamError != nil {
		t.Fatal(streamError)
	}
	delta, ok := received.(ai.TextDeltaEvent)
	if !ok || delta.Delta != "streamed" {
		t.Fatalf("stream event = %#v", received)
	}
}

type bridgeSource struct {
	name   string
	source string
}

func loadBridgeRunner(t *testing.T, cwd string, sources []bridgeSource, options extensions.RunnerOptions) *extensions.Runner {
	t.Helper()
	paths := make([]string, 0, len(sources))
	for _, source := range sources {
		path := filepath.Join(cwd, source.name)
		mustWrite(t, path, source.source)
		paths = append(paths, path)
	}
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: paths, ProjectTrusted: true})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	options.CWD = cwd
	return extensions.NewRunner(loaded.Registry, options)
}

type bridgeModelRegistry struct {
	models       []ai.Model
	key          string
	headers      map[string]string
	errorText    string
	reloadErr    error
	reloads      int
	resolveCalls int
	configs      map[string]extensions.ProviderConfig
	native       map[string]extensions.Provider
	providerIDs  []string
}

type bridgeRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip bridgeRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func (registry *bridgeModelRegistry) Reload() error {
	registry.reloads++
	return registry.reloadErr
}

func (registry *bridgeModelRegistry) Error() string { return registry.errorText }

func (registry bridgeModelRegistry) Models() []ai.Model {
	return append([]ai.Model(nil), registry.models...)
}
func (registry bridgeModelRegistry) Find(provider, id string) (ai.Model, bool) {
	for _, model := range registry.models {
		if string(model.Provider) == provider && model.ID == id {
			return model, true
		}
	}
	return ai.Model{}, false
}
func (registry bridgeModelRegistry) HasConfiguredAuth(provider string, _ map[string]string) bool {
	return provider == "provider-1" && registry.key != ""
}
func (registry bridgeModelRegistry) GetProviderAuthStatus(provider string, env map[string]string) extensions.AuthStatus {
	return extensions.AuthStatus{Configured: registry.HasConfiguredAuth(provider, env), Source: "environment", Label: "fixture"}
}
func (registry bridgeModelRegistry) IsUsingOAuth(provider string) bool {
	return provider == "legacy-provider"
}
func (registry bridgeModelRegistry) Available(map[string]string) []ai.Model { return registry.Models() }
func (registry bridgeModelRegistry) AvailableWithError(map[string]string) ([]ai.Model, error) {
	return registry.Models(), nil
}
func (registry *bridgeModelRegistry) ResolveAPIKey(context.Context, string, map[string]string) (*string, error) {
	registry.resolveCalls++
	if registry.key == "" {
		return nil, nil
	}
	value := registry.key
	return &value, nil
}
func (registry bridgeModelRegistry) ResolveModelHeaders(context.Context, ai.Model, map[string]string, ...*string) (*map[string]string, error) {
	value := make(map[string]string, len(registry.headers))
	for name, header := range registry.headers {
		value[name] = header
	}
	return &value, nil
}
func (registry *bridgeModelRegistry) ResolveProviderAuth(context.Context, string, map[string]string) (*aiauth.AuthResult, error) {
	registry.resolveCalls++
	if registry.key == "" {
		return nil, nil
	}
	key := registry.key
	header := "yes"
	return &aiauth.AuthResult{Auth: aiauth.ModelAuth{APIKey: &key, Headers: map[string]*string{"X-Test": &header, "X-Null": nil}}}, nil
}
func (registry bridgeModelRegistry) StreamSimple(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
	return nil, errors.New("unused")
}
func (registry *bridgeModelRegistry) Provider(id string) (extensions.Provider, bool) {
	if provider, ok := registry.native[id]; ok {
		return provider, true
	}
	config, ok := registry.configs[id]
	if ok {
		models := make([]ai.Model, 0)
		for _, model := range registry.models {
			if string(model.Provider) == id {
				models = append(models, model)
			}
		}
		for _, model := range config.Models {
			models = append(models, ai.Model{ID: model.ID, Name: model.Name, Provider: ai.ProviderID(id), API: model.API, BaseURL: model.BaseURL})
		}
		return extensions.Provider{
			ID: id, Name: config.Name, BaseURL: config.BaseURL, Config: config,
			GetModels: func() ([]ai.Model, error) { return append([]ai.Model(nil), models...), nil },
		}, true
	}
	for _, model := range registry.models {
		if string(model.Provider) == id {
			return extensions.Provider{ID: id}, true
		}
	}
	return extensions.Provider{}, false
}
func (registry *bridgeModelRegistry) ProviderDisplayName(id string) string {
	provider, ok := registry.Provider(id)
	if ok && provider.Name != "" {
		return provider.Name
	}
	return id
}
func (registry *bridgeModelRegistry) ProviderAuth(id string) aiauth.ProviderAuth {
	provider, _ := registry.Provider(id)
	return provider.Auth
}
func (registry *bridgeModelRegistry) RegisteredProviderConfig(id string) (extensions.ProviderConfig, bool) {
	config, ok := registry.configs[id]
	return config, ok
}
func (registry *bridgeModelRegistry) RegisteredNativeProvider(id string) (extensions.Provider, bool) {
	provider, ok := registry.native[id]
	return provider, ok
}
func (registry *bridgeModelRegistry) RegisteredProviderIDs() []string {
	result := make([]string, 0, len(registry.providerIDs))
	for _, id := range registry.providerIDs {
		if _, config := registry.configs[id]; config {
			result = append(result, id)
			continue
		}
		if _, native := registry.native[id]; native {
			result = append(result, id)
		}
	}
	return result
}
func (registry *bridgeModelRegistry) rememberProvider(id string) {
	if !slices.Contains(registry.providerIDs, id) {
		registry.providerIDs = append(registry.providerIDs, id)
	}
}
func (registry *bridgeModelRegistry) RegisterProvider(provider extensions.Provider) error {
	if registry.native == nil {
		registry.native = make(map[string]extensions.Provider)
	}
	registry.native[provider.ID] = provider
	delete(registry.configs, provider.ID)
	registry.rememberProvider(provider.ID)
	return nil
}
func (registry *bridgeModelRegistry) RegisterProviderConfig(id string, config extensions.ProviderConfig) error {
	if registry.configs == nil {
		registry.configs = make(map[string]extensions.ProviderConfig)
	}
	registry.configs[id] = config
	delete(registry.native, id)
	registry.rememberProvider(id)
	return nil
}
func (registry *bridgeModelRegistry) UnregisterProvider(id string) error {
	delete(registry.configs, id)
	delete(registry.native, id)
	return nil
}

type bridgeReplacedContext struct {
	extensions.CommandContext
	sendMessage     func(context.Context, extensions.CustomMessage, *extensions.SendMessageOptions) error
	sendUserMessage func(context.Context, ai.UserContent, *extensions.SendUserMessageOptions) error
}

type bridgeProviderStore struct {
	mu      sync.Mutex
	entry   *extensions.ProviderModelsStoreEntry
	written *extensions.ProviderModelsStoreEntry
}

func (store *bridgeProviderStore) Read(context.Context) (*extensions.ProviderModelsStoreEntry, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.entry, nil
}

func (store *bridgeProviderStore) Write(_ context.Context, entry extensions.ProviderModelsStoreEntry) error {
	store.mu.Lock()
	store.written = &entry
	store.entry = &entry
	store.mu.Unlock()
	return nil
}

func (store *bridgeProviderStore) Delete(context.Context) error {
	store.mu.Lock()
	store.entry = nil
	store.mu.Unlock()
	return nil
}

func (contextValue bridgeReplacedContext) SendMessage(ctx context.Context, message extensions.CustomMessage, options *extensions.SendMessageOptions) error {
	return contextValue.sendMessage(ctx, message, options)
}

func (contextValue bridgeReplacedContext) SendUserMessage(ctx context.Context, content ai.UserContent, options *extensions.SendUserMessageOptions) error {
	return contextValue.sendUserMessage(ctx, content, options)
}
