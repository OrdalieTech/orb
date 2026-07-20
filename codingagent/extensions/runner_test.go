package extensions

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

func TestRegistryResolvesExtensionPathsAgainstItsCWD(t *testing.T) {
	cwd := t.TempDir()
	registry := NewRegistry(cwd)
	if err := registry.Register(filepath.Join("extensions", "demo.go"), func(API) error { return nil }); err != nil {
		t.Fatal(err)
	}
	extension := registry.Extensions()[0]
	want := filepath.Join(cwd, "extensions", "demo.go")
	if extension.ResolvedPath != want || extension.SourceInfo.BaseDir == nil || *extension.SourceInfo.BaseDir != filepath.Dir(want) {
		t.Fatalf("extension = %#v", extension)
	}
}

func TestRegistryReportsFactoryPanic(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	err := registry.Register("panic", func(API) error {
		panic("boom")
	})
	if err == nil || err.Error() != "extensions: load panic: boom" {
		t.Fatalf("error = %v", err)
	}
	if len(registry.Extensions()) != 0 {
		t.Fatal("panicking extension was registered")
	}
}

func TestRunnerCollectsRegistrationsWithUpstreamPrecedence(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	register := func(path, description string, flagDefault bool) {
		t.Helper()
		if err := registry.Register(path, func(api API) error {
			api.RegisterTool(ToolDefinition{Name: "shared", Description: description, Parameters: jsonschema.Schema(`{"type":"object"}`)})
			api.RegisterCommand("review", Command{Description: description})
			api.RegisterFlag("shared", Flag{Type: FlagBoolean, Default: flagDefault, Description: description})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	register("first", "first", true)
	register("second", "second", false)
	runner := newRunner(t, registry, RunnerOptions{})
	tools := runner.AllRegisteredTools()
	if len(tools) != 1 || tools[0].Definition.Description != "first" {
		t.Fatalf("tools = %#v", tools)
	}
	commands := runner.RegisteredCommands()
	if len(commands) != 2 || commands[0].InvocationName != "review:1" || commands[1].InvocationName != "review:2" {
		t.Fatalf("commands = %#v", commands)
	}
	if got := runner.Flags()["shared"].Description; got != "first" {
		t.Fatalf("first flag did not win: %q", got)
	}
	if value, ok := runner.FlagValues()["shared"].(bool); !ok || !value {
		t.Fatalf("flag default = %#v", runner.FlagValues()["shared"])
	}
}

func TestRunnerFlushesQueuedProvidersAndAppliesRuntimeChanges(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	var api API
	if err := registry.Register("provider", func(value API) error {
		api = value
		value.RegisterProviderConfig("queued", ProviderConfig{Name: "Queued"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	registered := []string{}
	unregistered := []string{}
	NewRunner(registry, RunnerOptions{Actions: Actions{
		RegisterProvider: func(provider Provider) error {
			registered = append(registered, provider.ID)
			return nil
		},
		UnregisterProvider: func(name string) error {
			unregistered = append(unregistered, name)
			return nil
		},
	}})
	api.RegisterProvider(Provider{ID: "dynamic"})
	api.UnregisterProvider("dynamic")
	if !reflect.DeepEqual(registered, []string{"queued", "dynamic"}) || !reflect.DeepEqual(unregistered, []string{"dynamic"}) {
		t.Fatalf("registered = %#v, unregistered = %#v", registered, unregistered)
	}
}

func TestRunnerFlushesQueuedProviderConfigsBeforeNativeProviders(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	if err := registry.Register("providers", func(api API) error {
		api.RegisterProviderConfig("config-first", ProviderConfig{})
		api.RegisterProvider(Provider{ID: "native-second"})
		api.RegisterProviderConfig("config-third", ProviderConfig{})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	registered := []string{}
	NewRunner(registry, RunnerOptions{Actions: Actions{
		RegisterProvider: func(provider Provider) error {
			registered = append(registered, "native:"+provider.ID)
			return nil
		},
		RegisterProviderConfig: func(name string, _ ProviderConfig) error {
			registered = append(registered, "config:"+name)
			return nil
		},
	}})
	want := []string{"config:config-first", "config:config-third", "native:native-second"}
	if !reflect.DeepEqual(registered, want) {
		t.Fatalf("registered = %#v, want %#v", registered, want)
	}
}

func TestUnregisterProviderRemovesQueuedRegistrations(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	if err := registry.Register("providers", func(api API) error {
		api.RegisterProvider(Provider{ID: "removed"})
		api.RegisterProviderConfig("removed", ProviderConfig{})
		api.UnregisterProvider("removed")
		api.RegisterProvider(Provider{ID: "kept"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	registered := []string{}
	NewRunner(registry, RunnerOptions{Actions: Actions{
		RegisterProvider: func(provider Provider) error {
			registered = append(registered, provider.ID)
			return nil
		},
	}})
	if !reflect.DeepEqual(registered, []string{"kept"}) {
		t.Fatalf("registered = %#v", registered)
	}
}

func TestRunnerReportsQueuedProviderFailureDuringBind(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	if err := registry.Register("broken-provider", func(api API) error {
		api.RegisterProvider(Provider{ID: "broken"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var reported []ExtensionError
	NewRunner(registry, RunnerOptions{
		Actions: Actions{RegisterProvider: func(Provider) error { return errors.New("registration failed") }},
		ErrorHandler: func(value ExtensionError) {
			reported = append(reported, value)
		},
	})
	if len(reported) != 1 || reported[0].ExtensionPath != "broken-provider" || reported[0].Event != "register_provider" || reported[0].Error != "registration failed" {
		t.Fatalf("errors = %#v", reported)
	}
}

func TestExtensionAPIActionsRejectLoadingAndDelegateAfterBind(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	var api API
	var loadingError error
	if err := registry.Register("actions", func(value API) error {
		api = value
		loadingError = value.SendMessage(context.Background(), CustomMessage{CustomType: "early"}, nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if loadingError == nil || !strings.Contains(loadingError.Error(), "cannot be called during extension loading") {
		t.Fatalf("loading error = %v", loadingError)
	}
	var received CustomMessage
	NewRunner(registry, RunnerOptions{Actions: Actions{
		SendMessage: func(_ context.Context, message CustomMessage, _ *SendMessageOptions) error {
			received = message
			return nil
		},
	}})
	if err := api.SendMessage(context.Background(), CustomMessage{CustomType: "ready", Content: "ok"}, nil); err != nil {
		t.Fatal(err)
	}
	if received.CustomType != "ready" || received.Content != "ok" {
		t.Fatalf("message = %#v", received)
	}
}

func TestCommandContextDelegatesSessionActions(t *testing.T) {
	var calls []string
	actions := &CommandActions{
		WaitForIdle: func(context.Context) error { calls = append(calls, "idle"); return nil },
		NewSession: func(context.Context, *NewSessionOptions) (SessionReplacementResult, error) {
			calls = append(calls, "new")
			return SessionReplacementResult{Cancelled: true}, nil
		},
		Fork: func(_ context.Context, entryID string, _ *ForkOptions) (SessionReplacementResult, error) {
			calls = append(calls, "fork:"+entryID)
			return SessionReplacementResult{}, nil
		},
		NavigateTree: func(_ context.Context, targetID string, _ *NavigateTreeOptions) (SessionReplacementResult, error) {
			calls = append(calls, "tree:"+targetID)
			return SessionReplacementResult{}, nil
		},
		SwitchSession: func(_ context.Context, path string, _ *SwitchSessionOptions) (SessionReplacementResult, error) {
			calls = append(calls, "switch:"+path)
			return SessionReplacementResult{}, nil
		},
		Reload: func(context.Context) error { calls = append(calls, "reload"); return nil },
	}
	runner := newRunner(t, NewRegistry(t.TempDir()), RunnerOptions{
		CWD: "/fixture", CommandActions: actions,
		ContextActions: ContextActions{GetSystemPromptOptions: func() SystemPromptOptions {
			return SystemPromptOptions{CWD: "/prompt"}
		}},
	})
	commandContext := runner.CreateCommandContext()
	ctx := context.Background()
	if err := commandContext.WaitForIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if result, err := commandContext.NewSession(ctx, nil); err != nil || !result.Cancelled {
		t.Fatalf("new session = %#v, %v", result, err)
	}
	if _, err := commandContext.Fork(ctx, "entry", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := commandContext.NavigateTree(ctx, "target", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := commandContext.SwitchSession(ctx, "session.jsonl", nil); err != nil {
		t.Fatal(err)
	}
	if err := commandContext.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if commandContext.GetSystemPromptOptions().CWD != "/prompt" || !reflect.DeepEqual(calls, []string{"idle", "new", "fork:entry", "tree:target", "switch:session.jsonl", "reload"}) {
		t.Fatalf("options = %#v, calls = %#v", commandContext.GetSystemPromptOptions(), calls)
	}
}

func TestRunnerGenericDispatchIsOrderedAndIsolatesErrors(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	var order []string
	for _, item := range []struct {
		path string
		err  error
	}{{"first", nil}, {"broken", errors.New("boom")}, {"third", nil}} {
		item := item
		if err := registry.Register(item.path, func(api API) error {
			api.On(EventAgentStart, func(context.Context, Event, Context) (any, error) {
				order = append(order, item.path)
				return nil, item.err
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	runner := newRunner(t, registry, RunnerOptions{})
	var reported []ExtensionError
	runner.OnError(func(value ExtensionError) { reported = append(reported, value) })
	runner.Emit(context.Background(), AgentStartEvent{})
	if got := strings.Join(order, ","); got != "first,broken,third" {
		t.Fatalf("order = %q", got)
	}
	if len(reported) != 1 || reported[0].ExtensionPath != "broken" || reported[0].Event != "agent_start" || reported[0].Error != "boom" {
		t.Fatalf("errors = %#v", reported)
	}
}

func TestRunnerSessionBeforeLastResultAndCancelShortCircuit(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	var order []string
	for _, item := range []struct {
		name   string
		result *SessionBeforeSwitchResult
	}{{"one", &SessionBeforeSwitchResult{}}, {"two", &SessionBeforeSwitchResult{Cancel: true}}, {"three", nil}} {
		item := item
		if err := registry.Register(item.name, func(api API) error {
			api.On(EventSessionBeforeSwitch, func(context.Context, Event, Context) (any, error) {
				order = append(order, item.name)
				return item.result, nil
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	runner := newRunner(t, registry, RunnerOptions{})
	result := runner.Emit(context.Background(), SessionBeforeSwitchEvent{Reason: SessionSwitchNew})
	parsed, ok := result.(*SessionBeforeSwitchResult)
	if !ok || !parsed.Cancel || strings.Join(order, ",") != "one,two" {
		t.Fatalf("result = %#v, order = %#v", result, order)
	}
}

func TestRunnerContextMiddlewareUsesDeepCopy(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	if err := registry.Register("first", func(api API) error {
		api.On(EventContext, func(_ context.Context, raw Event, _ Context) (any, error) {
			event := raw.(ContextEvent)
			message := event.Messages[0].(map[string]any)
			message["nested"].(map[string]any)["value"] = "first"
			return ContextResult{Messages: append(event.Messages, map[string]any{"role": "custom", "value": "added"})}, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("second", func(api API) error {
		api.On(EventContext, func(_ context.Context, raw Event, _ Context) (any, error) {
			event := raw.(ContextEvent)
			event.Messages[0].(map[string]any)["seen"] = len(event.Messages)
			return ContextResult(event), nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	original := agent.AgentMessages{map[string]any{"role": "custom", "nested": map[string]any{"value": "original"}}}
	result := newRunner(t, registry, RunnerOptions{}).EmitContext(context.Background(), original)
	if original[0].(map[string]any)["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("context handler mutated caller messages")
	}
	first := result[0].(map[string]any)
	if first["seen"] != 2 || first["nested"].(map[string]any)["value"] != "first" || len(result) != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunnerToolResultChainsPartialPatchesAndErrors(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	content := ai.ToolResultContent{&ai.TextContent{Text: "first"}}
	details := any(map[string]any{"source": "first"})
	firstUsage := &ai.Usage{Input: 2, TotalTokens: 2, Cost: ai.Cost{Total: 0.2}}
	finalUsage := &ai.Usage{Input: 3, TotalTokens: 3, Cost: ai.Cost{Total: 0.3}}
	isError := true
	for _, factory := range []Factory{
		func(api API) error {
			api.On(EventToolResult, func(context.Context, Event, Context) (any, error) {
				return ToolResultResult{Content: &content, Details: &details, Usage: firstUsage}, nil
			})
			return nil
		},
		func(api API) error {
			api.On(EventToolResult, func(context.Context, Event, Context) (any, error) {
				return nil, errors.New("ignored")
			})
			return nil
		},
		func(api API) error {
			api.On(EventToolResult, func(_ context.Context, raw Event, _ Context) (any, error) {
				event := raw.(ToolResultEvent)
				if event.Content[0].(*ai.TextContent).Text != "first" || event.Details.(map[string]any)["source"] != "first" || event.Usage == nil || event.Usage.TotalTokens != 2 {
					return nil, errors.New("middleware did not receive prior patch")
				}
				return ToolResultResult{IsError: &isError, Usage: finalUsage}, nil
			})
			return nil
		},
	} {
		if err := registry.Register("extension", factory); err != nil {
			t.Fatal(err)
		}
	}
	runner := newRunner(t, registry, RunnerOptions{})
	var reported []ExtensionError
	runner.OnError(func(value ExtensionError) { reported = append(reported, value) })
	result := runner.EmitToolResult(context.Background(), ToolResultEvent{
		ToolName: "tool", ToolCallID: "call", Input: map[string]any{},
		Content: ai.ToolResultContent{&ai.TextContent{Text: "base"}}, Details: map[string]any{"base": true},
	})
	if result == nil || result.Content == nil || (*result.Content)[0].(*ai.TextContent).Text != "first" || result.IsError == nil || !*result.IsError || result.Usage == nil || result.Usage.TotalTokens != 3 {
		t.Fatalf("result = %#v", result)
	}
	if len(reported) != 1 || reported[0].Error != "ignored" {
		t.Fatalf("errors = %#v", reported)
	}
}

func TestRunnerToolCallMutatesInOrderBlocksAndFailsSafe(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	var reached bool
	if err := registry.Register("mutate", func(api API) error {
		api.On(EventToolCall, func(_ context.Context, raw Event, _ Context) (any, error) {
			raw.(ToolCallEvent).Input["command"] = "prefixed"
			return ToolCallResult{}, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("block", func(api API) error {
		api.On(EventToolCall, func(_ context.Context, raw Event, _ Context) (any, error) {
			if raw.(ToolCallEvent).Input["command"] != "prefixed" {
				return nil, errors.New("mutation not visible")
			}
			return ToolCallResult{Block: true, Reason: "denied"}, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("unreached", func(api API) error {
		api.On(EventToolCall, func(context.Context, Event, Context) (any, error) {
			reached = true
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, registry, RunnerOptions{})
	input := map[string]any{"command": "base"}
	result := runner.EmitToolCall(context.Background(), ToolCallEvent{ToolName: "bash", Input: input})
	if result == nil || !result.Block || result.Reason != "denied" || reached || input["command"] != "prefixed" {
		t.Fatalf("result = %#v, reached = %v, input = %#v", result, reached, input)
	}

	failing := NewRegistry(t.TempDir())
	if err := failing.Register("broken", func(api API) error {
		api.On(EventToolCall, func(context.Context, Event, Context) (any, error) {
			return nil, errors.New("handler boom")
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	failingRunner := newRunner(t, failing, RunnerOptions{})
	var reported []ExtensionError
	failingRunner.OnError(func(value ExtensionError) { reported = append(reported, value) })
	failSafe := failingRunner.EmitToolCall(context.Background(), ToolCallEvent{})
	if failSafe == nil || !failSafe.Block || failSafe.Reason != "handler boom" || len(reported) != 1 {
		t.Fatalf("fail-safe = %#v, errors = %#v", failSafe, reported)
	}
}

func TestRunnerBeforeAgentStartChainsPromptAndMessages(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	for _, suffix := range []string{"first", "second"} {
		suffix := suffix
		if err := registry.Register(suffix, func(api API) error {
			api.On(EventBeforeAgentStart, func(_ context.Context, raw Event, extensionContext Context) (any, error) {
				event := raw.(BeforeAgentStartEvent)
				if event.SystemPrompt != extensionContext.GetSystemPrompt() {
					return nil, errors.New("context prompt was stale")
				}
				next := event.SystemPrompt + "\n" + suffix
				return BeforeAgentStartResult{
					Message: &CustomMessage{CustomType: suffix, Content: suffix}, SystemPrompt: &next,
				}, nil
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	result := newRunner(t, registry, RunnerOptions{}).EmitBeforeAgentStart(
		context.Background(), "hello", nil, "base", SystemPromptOptions{CWD: "/fixture"},
	)
	if result == nil || result.SystemPrompt == nil || *result.SystemPrompt != "base\nfirst\nsecond" || len(result.Messages) != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunnerMessageEndKeepsRoleAndChains(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	for _, role := range []string{"assistant", "user", "assistant"} {
		role := role
		if err := registry.Register(role, func(api API) error {
			api.On(EventMessageEnd, func(context.Context, Event, Context) (any, error) {
				return MessageEndResult{Message: map[string]any{"role": role, "source": role}}, nil
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	runner := newRunner(t, registry, RunnerOptions{})
	var reported []ExtensionError
	runner.OnError(func(value ExtensionError) { reported = append(reported, value) })
	result := runner.EmitMessageEnd(context.Background(), MessageEndEvent{Message: map[string]any{"role": "assistant"}})
	if result.(map[string]any)["source"] != "assistant" || len(reported) != 1 {
		t.Fatalf("result = %#v, errors = %#v", result, reported)
	}
}

func TestRunnerProviderInputResourcesAndTrustChains(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	for _, name := range []string{"first", "second"} {
		name := name
		if err := registry.Register(name, func(api API) error {
			api.On(EventBeforeProviderRequest, func(_ context.Context, raw Event, _ Context) (any, error) {
				payload := raw.(BeforeProviderRequestEvent).Payload.(map[string]any)
				copy := make(map[string]any, len(payload)+1)
				for key, value := range payload {
					copy[key] = value
				}
				copy[name] = true
				return copy, nil
			})
			api.On(EventBeforeProviderHeaders, func(_ context.Context, raw Event, _ Context) (any, error) {
				value := name
				raw.(BeforeProviderHeadersEvent).Headers["X-"+name] = &value
				return nil, nil
			})
			api.On(EventInput, func(_ context.Context, raw Event, _ Context) (any, error) {
				event := raw.(InputEvent)
				return InputResult{Action: InputTransform, Text: event.Text + "[" + name + "]"}, nil
			})
			api.On(EventResourcesDiscover, func(context.Context, Event, Context) (any, error) {
				return ResourcesDiscoverResult{SkillPaths: []string{"/" + name}}, nil
			})
			api.On(EventProjectTrust, func(context.Context, Event, Context) (any, error) {
				if name == "first" {
					return ProjectTrustResult{Trusted: ProjectTrustUndecided}, nil
				}
				return ProjectTrustResult{Trusted: ProjectTrustNo, Remember: true}, nil
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	runner := newRunner(t, registry, RunnerOptions{})
	payload := runner.EmitBeforeProviderRequest(context.Background(), map[string]any{"base": true}).(map[string]any)
	if !payload["first"].(bool) || !payload["second"].(bool) {
		t.Fatalf("payload = %#v", payload)
	}
	headers := runner.EmitBeforeProviderHeaders(context.Background(), ai.ProviderHeaders{})
	if headers["X-first"] == nil || headers["X-second"] == nil {
		t.Fatalf("headers = %#v", headers)
	}
	input := runner.EmitInput(context.Background(), "x", nil, InputRPC, nil)
	if input.Action != InputTransform || input.Text != "x[first][second]" {
		t.Fatalf("input = %#v", input)
	}
	resources := runner.EmitResourcesDiscover(context.Background(), "/fixture", ResourcesDiscoverStartup)
	if len(resources.SkillPaths) != 2 || resources.SkillPaths[0].ExtensionPath != "first" {
		t.Fatalf("resources = %#v", resources)
	}
	decision, trustErrors := runner.EmitProjectTrust(context.Background(), ProjectTrustEvent{CWD: "/fixture"}, nil)
	if decision == nil || decision.Trusted != ProjectTrustNo || !decision.Remember || len(trustErrors) != 0 {
		t.Fatalf("trust = %#v, errors = %#v", decision, trustErrors)
	}
}

func TestRunnerInputReturnsContinueWhenTransformKeepsOriginalValues(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	if err := registry.Register("same", func(api API) error {
		api.On(EventInput, func(_ context.Context, raw Event, _ Context) (any, error) {
			event := raw.(InputEvent)
			return InputResult{Action: InputTransform, Text: event.Text, Images: event.Images}, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	image := &ai.ImageContent{Data: "fixture", MimeType: "image/png"}
	result := newRunner(t, registry, RunnerOptions{}).EmitInput(
		context.Background(), "same", []*ai.ImageContent{image}, InputRPC, nil,
	)
	if result.Action != InputContinue {
		t.Fatalf("input result = %#v", result)
	}
}

func TestRunnerHeadlessUIAndStaleContext(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	runner := newRunner(t, registry, RunnerOptions{Mode: ModeJSON, UI: fakeUI{}})
	ctx := runner.CreateContext()
	if ctx.HasUI() || ctx.Mode() != ModeJSON {
		t.Fatalf("headless context hasUI=%v mode=%q", ctx.HasUI(), ctx.Mode())
	}
	if choice, selected, err := ctx.UI().Select(context.Background(), "title", []string{"yes"}, nil); err != nil || selected || choice != "" {
		t.Fatalf("no-op select = %q %v %v", choice, selected, err)
	}
	runner.Invalidate("stale")
	defer func() {
		if recovered := recover(); recovered != "stale" {
			t.Fatalf("stale panic = %#v", recovered)
		}
	}()
	_ = ctx.CWD()
}

func TestRunnerRPCModeUsesProvidedDialogBridge(t *testing.T) {
	runner := newRunner(t, NewRegistry(t.TempDir()), RunnerOptions{Mode: ModeRPC, UI: fakeUI{}})
	ctx := runner.CreateContext()
	choice, selected, err := ctx.UI().Select(context.Background(), "title", []string{"yes"}, nil)
	if err != nil || !ctx.HasUI() || ctx.Mode() != ModeRPC || !selected || choice != "yes" {
		t.Fatalf("rpc UI choice=%q selected=%v hasUI=%v mode=%q error=%v", choice, selected, ctx.HasUI(), ctx.Mode(), err)
	}
}

func TestEventBusOrderedIsolationAndUnsubscribe(t *testing.T) {
	bus := NewEventBus()
	var values []int
	bus.On("channel", func(context.Context, any) error { values = append(values, 1); return errors.New("boom") })
	unsubscribe := bus.On("channel", func(context.Context, any) error { values = append(values, 2); return nil })
	errorsSeen := bus.Emit(context.Background(), "channel", nil)
	if !reflect.DeepEqual(values, []int{1, 2}) || len(errorsSeen) != 1 {
		t.Fatalf("values = %#v, errors = %#v", values, errorsSeen)
	}
	unsubscribe()
	values = nil
	bus.Emit(context.Background(), "channel", nil)
	if !reflect.DeepEqual(values, []int{1}) {
		t.Fatalf("values after unsubscribe = %#v", values)
	}
}

func TestExecCapturesExitAndTimeout(t *testing.T) {
	result, err := Exec(context.Background(), "sh", []string{"-c", "printf out; printf err >&2; exit 7"}, nil)
	if err != nil || result.Stdout != "out" || result.Stderr != "err" || result.Code != 7 || result.Killed {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	result, err = Exec(context.Background(), "sh", []string{"-c", "sleep 2"}, &ExecOptions{Timeout: 20})
	if err != nil || !result.Killed || result.Code != 0 {
		t.Fatalf("timeout = %#v, error = %v", result, err)
	}
	result, err = Exec(context.Background(), "pi-go-command-that-does-not-exist", nil, nil)
	if err != nil || result.Code != 1 || result.Killed {
		t.Fatalf("spawn failure = %#v, error = %v", result, err)
	}
}

func TestWrappedToolRecordsPurelyAdditiveActivations(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	active := []string{"loader"}
	var activeMu sync.Mutex
	if err := registry.Register("tools", func(api API) error {
		api.RegisterTool(ToolDefinition{
			Name: "loader", Parameters: jsonschema.Schema(`{"type":"object"}`),
			Execute: func(context.Context, string, any, agent.AgentToolUpdateCallback, Context) (agent.AgentToolResult, error) {
				activeMu.Lock()
				active = []string{"loader", "loaded"}
				activeMu.Unlock()
				return agent.AgentToolResult{Content: ai.ToolResultContent{}}, nil
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	actions := Actions{GetActiveTools: func() ([]string, error) {
		activeMu.Lock()
		defer activeMu.Unlock()
		return append([]string(nil), active...), nil
	}}
	runner := newRunner(t, registry, RunnerOptions{Actions: actions})
	wrapped := WrapRegisteredTool(runner.AllRegisteredTools()[0], runner)
	result, err := wrapped.Execute(context.Background(), "call", map[string]any{}, nil)
	if err != nil || result.AddedToolNames == nil || !reflect.DeepEqual(*result.AddedToolNames, []string{"loaded"}) {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestWrappedToolPreservesReportedNamesWithoutDynamicActivation(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	if err := registry.Register("tool", func(api API) error {
		api.RegisterTool(ToolDefinition{
			Name: "tool", Parameters: jsonschema.Schema(`{"type":"object"}`),
			Execute: func(context.Context, string, any, agent.AgentToolUpdateCallback, Context) (agent.AgentToolResult, error) {
				reported := []string{"reported", "reported"}
				return agent.AgentToolResult{AddedToolNames: &reported}, nil
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, registry, RunnerOptions{Actions: Actions{
		GetActiveTools: func() ([]string, error) { return []string{"tool"}, nil },
	}})
	result, err := WrapRegisteredTool(runner.AllRegisteredTools()[0], runner).Execute(
		context.Background(), "call", map[string]any{}, nil,
	)
	if err != nil || result.AddedToolNames == nil || !reflect.DeepEqual(*result.AddedToolNames, []string{"reported", "reported"}) {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func newRunner(t *testing.T, registry *Registry, options RunnerOptions) *Runner {
	t.Helper()
	manager, err := session.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	options.SessionManager = manager
	return NewRunner(registry, options)
}

type fakeUI struct{ NoopUI }

func (fakeUI) Select(context.Context, string, []string, *DialogOptions) (string, bool, error) {
	return "yes", true, nil
}
