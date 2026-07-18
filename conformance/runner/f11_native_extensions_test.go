package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

func TestF11NativeExtensionDispatchMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "F11-native")
	if manifest.Family != "F11-native" || manifest.Generator != "conformance/extract/f11-extension-runner.ts" {
		t.Fatalf("unexpected F11-native manifest: %+v", manifest)
	}

	var fixture map[string]json.RawMessage
	runner.LoadJSON(t, "F11-native", "cases.json", &fixture)
	actual := runF11NativeCases(t)
	if len(actual) != len(fixture) {
		t.Fatalf("F11-native cases = %d, want %d", len(actual), len(fixture))
	}
	for name, expected := range fixture {
		name, expected := name, expected
		t.Run(name, func(t *testing.T) {
			value, ok := actual[name]
			if !ok {
				t.Fatalf("Go runner did not produce fixture case %q", name)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshal Go result: %v", err)
			}
			want, err := runner.CanonicalJSON(expected)
			if err != nil {
				t.Fatalf("canonicalize fixture: %v", err)
			}
			got, err := runner.CanonicalJSON(encoded)
			if err != nil {
				t.Fatalf("canonicalize Go result: %v", err)
			}
			if diff := runner.ByteDiff(want, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func runF11NativeCases(t *testing.T) map[string]any {
	t.Helper()
	ctx := context.Background()

	orderedRegistry := extensions.NewRegistry("/fixture")
	orderedCalls := []string{}
	for _, item := range []struct {
		path string
		err  error
	}{{"first", nil}, {"broken", errors.New("boom")}, {"third", nil}} {
		item := item
		registerF11(t, orderedRegistry, item.path, func(api extensions.API) {
			api.On(extensions.EventAgentStart, func(context.Context, extensions.Event, extensions.Context) (any, error) {
				orderedCalls = append(orderedCalls, item.path)
				return nil, item.err
			})
		})
	}
	orderedRunner := extensions.NewRunner(orderedRegistry, extensions.RunnerOptions{CWD: "/fixture"})
	orderedErrors := []map[string]any{}
	orderedRunner.OnError(func(value extensions.ExtensionError) {
		orderedErrors = append(orderedErrors, map[string]any{
			"extensionPath": value.ExtensionPath,
			"event":         value.Event,
			"error":         value.Error,
		})
	})
	orderedRunner.Emit(ctx, extensions.AgentStartEvent{})

	contextRegistry := extensions.NewRegistry("/fixture")
	registerF11(t, contextRegistry, "first", func(api extensions.API) {
		api.On(extensions.EventContext, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.ContextEvent)
			event.Messages[0].(map[string]any)["nested"].(map[string]any)["value"] = "first"
			return extensions.ContextResult{Messages: append(event.Messages, map[string]any{"role": "custom", "added": true})}, nil
		})
	})
	registerF11(t, contextRegistry, "second", func(api extensions.API) {
		api.On(extensions.EventContext, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.ContextEvent)
			event.Messages[0].(map[string]any)["seen"] = len(event.Messages)
			return extensions.ContextResult(event), nil
		})
	})
	originalContext := agent.AgentMessages{map[string]any{"role": "custom", "nested": map[string]any{"value": "original"}}}
	contextResult := extensions.NewRunner(contextRegistry, extensions.RunnerOptions{CWD: "/fixture"}).EmitContext(ctx, originalContext)

	toolResultRegistry := extensions.NewRegistry("/fixture")
	registerF11(t, toolResultRegistry, "first", func(api extensions.API) {
		api.On(extensions.EventToolResult, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			content := ai.ToolResultContent{&ai.TextContent{Text: "first"}}
			details := any(map[string]any{"source": "first"})
			return extensions.ToolResultResult{Content: &content, Details: &details}, nil
		})
	})
	registerF11(t, toolResultRegistry, "broken", func(api extensions.API) {
		api.On(extensions.EventToolResult, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, errors.New("ignored")
		})
	})
	registerF11(t, toolResultRegistry, "third", func(api extensions.API) {
		api.On(extensions.EventToolResult, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.ToolResultEvent)
			content := append(event.Content, &ai.TextContent{Text: event.Details.(map[string]any)["source"].(string)})
			isError := true
			return extensions.ToolResultResult{Content: &content, IsError: &isError}, nil
		})
	})
	toolResultRunner := extensions.NewRunner(toolResultRegistry, extensions.RunnerOptions{CWD: "/fixture"})
	toolResultErrors := []string{}
	toolResultRunner.OnError(func(value extensions.ExtensionError) { toolResultErrors = append(toolResultErrors, value.Error) })
	toolResult := toolResultRunner.EmitToolResult(ctx, extensions.ToolResultEvent{
		ToolName: "fixture", ToolCallID: "call-1", Input: map[string]any{},
		Content: ai.ToolResultContent{&ai.TextContent{Text: "base"}}, Details: map[string]any{"initial": true},
	})

	toolCallRegistry := extensions.NewRegistry("/fixture")
	toolCallOrder := []string{}
	registerF11(t, toolCallRegistry, "first", func(api extensions.API) {
		api.On(extensions.EventToolCall, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			toolCallOrder = append(toolCallOrder, "first")
			raw.(extensions.ToolCallEvent).Input["command"] = "prefixed"
			return nil, nil
		})
	})
	registerF11(t, toolCallRegistry, "second", func(api extensions.API) {
		api.On(extensions.EventToolCall, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			toolCallOrder = append(toolCallOrder, "second:"+raw.(extensions.ToolCallEvent).Input["command"].(string))
			return extensions.ToolCallResult{Block: true, Reason: "denied"}, nil
		})
	})
	registerF11(t, toolCallRegistry, "third", func(api extensions.API) {
		api.On(extensions.EventToolCall, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			toolCallOrder = append(toolCallOrder, "third")
			return nil, nil
		})
	})
	toolInput := map[string]any{"command": "base"}
	toolCall := extensions.NewRunner(toolCallRegistry, extensions.RunnerOptions{CWD: "/fixture"}).EmitToolCall(ctx, extensions.ToolCallEvent{
		ToolName: "bash", ToolCallID: "call-2", Input: toolInput,
	})
	failingToolCallRegistry := extensions.NewRegistry("/fixture")
	registerF11(t, failingToolCallRegistry, "broken", func(api extensions.API) {
		api.On(extensions.EventToolCall, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, errors.New("tool-call-boom")
		})
	})
	toolCallFailure := extensions.NewRunner(failingToolCallRegistry, extensions.RunnerOptions{CWD: "/fixture"}).EmitToolCall(ctx, extensions.ToolCallEvent{
		ToolName: "bash", ToolCallID: "call-3", Input: map[string]any{},
	})

	beforeAgentRegistry := extensions.NewRegistry("/fixture")
	for _, suffix := range []string{"first", "second"} {
		suffix := suffix
		registerF11(t, beforeAgentRegistry, suffix, func(api extensions.API) {
			api.On(extensions.EventBeforeAgentStart, func(_ context.Context, raw extensions.Event, extensionContext extensions.Context) (any, error) {
				event := raw.(extensions.BeforeAgentStartEvent)
				next := event.SystemPrompt + "\n" + suffix
				return extensions.BeforeAgentStartResult{
					Message:      &extensions.CustomMessage{CustomType: suffix, Content: extensionContext.GetSystemPrompt(), Display: true},
					SystemPrompt: &next,
				}, nil
			})
		})
	}
	beforeAgent := extensions.NewRunner(beforeAgentRegistry, extensions.RunnerOptions{CWD: "/fixture"}).EmitBeforeAgentStart(
		ctx, "hello", nil, "base", extensions.SystemPromptOptions{CWD: "/fixture"},
	)

	inputRegistry := extensions.NewRegistry("/fixture")
	for _, suffix := range []string{"first", "second"} {
		suffix := suffix
		registerF11(t, inputRegistry, suffix, func(api extensions.API) {
			api.On(extensions.EventInput, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
				return extensions.InputResult{Action: extensions.InputTransform, Text: raw.(extensions.InputEvent).Text + "[" + suffix + "]"}, nil
			})
		})
	}
	streamingBehavior := extensions.DeliverSteer
	input := extensions.NewRunner(inputRegistry, extensions.RunnerOptions{CWD: "/fixture"}).EmitInput(
		ctx, "x", nil, extensions.InputRPC, &streamingBehavior,
	)

	providerRegistry := extensions.NewRegistry("/fixture")
	registerF11(t, providerRegistry, "first", func(api extensions.API) {
		api.On(extensions.EventBeforeProviderRequest, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			payload := cloneF11Map(raw.(extensions.BeforeProviderRequestEvent).Payload.(map[string]any))
			payload["first"] = true
			return payload, nil
		})
		api.On(extensions.EventBeforeProviderHeaders, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			value := "one"
			raw.(extensions.BeforeProviderHeadersEvent).Headers["X-First"] = &value
			return nil, nil
		})
	})
	registerF11(t, providerRegistry, "broken", func(api extensions.API) {
		api.On(extensions.EventBeforeProviderRequest, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, errors.New("payload ignored")
		})
		api.On(extensions.EventBeforeProviderHeaders, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, errors.New("headers ignored")
		})
	})
	registerF11(t, providerRegistry, "second", func(api extensions.API) {
		api.On(extensions.EventBeforeProviderRequest, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			payload := cloneF11Map(raw.(extensions.BeforeProviderRequestEvent).Payload.(map[string]any))
			payload["second"] = true
			return payload, nil
		})
		api.On(extensions.EventBeforeProviderHeaders, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			value := "two"
			headers := raw.(extensions.BeforeProviderHeadersEvent).Headers
			headers["Remove"] = nil
			headers["X-Second"] = &value
			return nil, nil
		})
	})
	providerRunner := extensions.NewRunner(providerRegistry, extensions.RunnerOptions{CWD: "/fixture"})
	providerErrors := []string{}
	providerRunner.OnError(func(value extensions.ExtensionError) {
		providerErrors = append(providerErrors, value.Event+":"+value.Error)
	})
	payload := providerRunner.EmitBeforeProviderRequest(ctx, map[string]any{"base": true})
	existing, remove := "yes", "old"
	headers := providerRunner.EmitBeforeProviderHeaders(ctx, ai.ProviderHeaders{"Existing": &existing, "Remove": &remove})

	resourcesRegistry := extensions.NewRegistry("/fixture")
	registerF11(t, resourcesRegistry, "first", func(api extensions.API) {
		api.On(extensions.EventResourcesDiscover, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return extensions.ResourcesDiscoverResult{SkillPaths: []string{"/skill-a"}, PromptPaths: []string{"/prompt-a"}}, nil
		})
	})
	registerF11(t, resourcesRegistry, "second", func(api extensions.API) {
		api.On(extensions.EventResourcesDiscover, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return extensions.ResourcesDiscoverResult{SkillPaths: []string{"/skill-b"}, ThemePaths: []string{"/theme-b"}}, nil
		})
	})
	resources := extensions.NewRunner(resourcesRegistry, extensions.RunnerOptions{CWD: "/fixture"}).EmitResourcesDiscover(
		ctx, "/fixture", extensions.ResourcesDiscoverStartup,
	)

	trustRegistry := extensions.NewRegistry("/fixture")
	for _, result := range []extensions.ProjectTrustResult{
		{Trusted: extensions.ProjectTrustUndecided, Remember: true},
		{Trusted: extensions.ProjectTrustNo, Remember: true},
		{Trusted: extensions.ProjectTrustYes},
	} {
		result := result
		registerF11(t, trustRegistry, string(result.Trusted), func(api extensions.API) {
			api.On(extensions.EventProjectTrust, func(context.Context, extensions.Event, extensions.Context) (any, error) {
				return result, nil
			})
		})
	}
	trust, trustErrors := extensions.NewRunner(trustRegistry, extensions.RunnerOptions{CWD: "/fixture"}).EmitProjectTrust(
		ctx, extensions.ProjectTrustEvent{CWD: "/fixture"}, nil,
	)

	sessionRegistry := extensions.NewRegistry("/fixture")
	sessionOrder := []string{}
	for _, item := range []struct {
		name   string
		result any
	}{{"first", extensions.SessionBeforeSwitchResult{}}, {"second", extensions.SessionBeforeSwitchResult{Cancel: true}}, {"third", nil}} {
		item := item
		registerF11(t, sessionRegistry, item.name, func(api extensions.API) {
			api.On(extensions.EventSessionBeforeSwitch, func(context.Context, extensions.Event, extensions.Context) (any, error) {
				sessionOrder = append(sessionOrder, item.name)
				return item.result, nil
			})
		})
	}
	sessionBefore := extensions.NewRunner(sessionRegistry, extensions.RunnerOptions{CWD: "/fixture"}).Emit(
		ctx, extensions.SessionBeforeSwitchEvent{Reason: extensions.SessionSwitchNew},
	).(extensions.SessionBeforeSwitchResult)

	return map[string]any{
		"orderedErrorIsolation": map[string]any{"calls": orderedCalls, "errors": orderedErrors},
		"contextMiddleware":     map[string]any{"original": originalContext, "result": contextResult},
		"toolResultMiddleware": map[string]any{
			"result": map[string]any{"content": *toolResult.Content, "details": *toolResult.Details, "isError": *toolResult.IsError},
			"errors": toolResultErrors,
		},
		"toolCall":        map[string]any{"input": toolInput, "order": toolCallOrder, "result": toolCall},
		"toolCallFailure": toolCallFailure,
		"beforeAgentStart": map[string]any{
			"messages": beforeAgent.Messages, "systemPrompt": *beforeAgent.SystemPrompt,
		},
		"input": input,
		"providerHooks": map[string]any{
			"payload": payload, "headers": headers, "errors": providerErrors,
		},
		"resources": resources,
		"projectTrust": map[string]any{
			"result": trust, "errors": f11Errors(trustErrors),
		},
		"sessionBefore": map[string]any{
			"order": sessionOrder, "result": map[string]any{"cancel": sessionBefore.Cancel},
		},
	}
}

func registerF11(t *testing.T, registry *extensions.Registry, path string, setup func(extensions.API)) {
	t.Helper()
	if err := registry.Register(path, func(api extensions.API) error {
		setup(api)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func cloneF11Map(value map[string]any) map[string]any {
	clone := make(map[string]any, len(value)+1)
	for key, item := range value {
		clone[key] = item
	}
	return clone
}

func f11Errors(values []extensions.ExtensionError) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		result = append(result, map[string]any{
			"extensionPath": value.ExtensionPath,
			"event":         value.Event,
			"error":         value.Error,
		})
	}
	return result
}
