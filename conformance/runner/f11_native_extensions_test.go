package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/conformance/runner"
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

	genericVoidAndPanic := runF11GenericVoidAndPanic(t)
	nilHandler := runF11NilHandler(t)
	inputIdentity := runF11InputIdentity(t)
	projectTrustBoundary := runF11ProjectTrustBoundary(t)
	projectTrustStartupOrder := runF11ProjectTrustStartupOrder(t)
	providerRegistration := runF11ProviderRegistration(t)
	registrationConflicts := runF11RegistrationConflicts(t)
	runnerLifecycle := runF11RunnerLifecycle(t)

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
		"genericVoidAndPanic":      genericVoidAndPanic,
		"nilHandler":               nilHandler,
		"inputIdentity":            inputIdentity,
		"projectTrustBoundary":     projectTrustBoundary,
		"projectTrustStartupOrder": projectTrustStartupOrder,
		"providerRegistration":     providerRegistration,
		"registrationConflicts":    registrationConflicts,
		"runnerLifecycle":          runnerLifecycle,
	}
}

func runF11GenericVoidAndPanic(t *testing.T) map[string]any {
	t.Helper()
	calls := []string{}
	registry := extensions.NewRegistry("/fixture")
	registerF11(t, registry, "returns-value", func(api extensions.API) {
		api.On(extensions.EventAgentStart, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			calls = append(calls, "returns-value")
			return map[string]any{"ignored": true}, nil
		})
	})
	registerF11(t, registry, "panic-origin", func(api extensions.API) {
		api.On(extensions.EventAgentStart, f11PanicOriginMarker(&calls))
	})
	registerF11(t, registry, "after-panic", func(api extensions.API) {
		api.On(extensions.EventAgentStart, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			calls = append(calls, "after-panic")
			return nil, nil
		})
	})
	value := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: "/fixture"})
	errorsSeen := []map[string]any{}
	value.OnError(func(extensionError extensions.ExtensionError) {
		errorsSeen = append(errorsSeen, map[string]any{
			"extensionPath":       extensionError.ExtensionPath,
			"event":               extensionError.Event,
			"error":               extensionError.Error,
			"hasStack":            extensionError.Stack != "",
			"stackIncludesOrigin": strings.Contains(extensionError.Stack, "f11PanicOriginMarker"),
		})
	})
	result := value.Emit(context.Background(), extensions.AgentStartEvent{})
	return map[string]any{"calls": calls, "result": result, "errors": errorsSeen}
}

func f11PanicOriginMarker(calls *[]string) extensions.Handler {
	return func(context.Context, extensions.Event, extensions.Context) (any, error) {
		*calls = append(*calls, "panic")
		panic("panic-origin")
	}
}

func runF11NilHandler(t *testing.T) map[string]any {
	t.Helper()
	calls := []string{}
	registry := extensions.NewRegistry("/fixture")
	registerF11(t, registry, "nil-handler", func(api extensions.API) {
		api.On(extensions.EventAgentStart, nil)
		api.On(extensions.EventAgentStart, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			calls = append(calls, "after-nil")
			return nil, nil
		})
	})
	value := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: "/fixture"})
	errorsSeen := []map[string]any{}
	value.OnError(func(extensionError extensions.ExtensionError) {
		errorsSeen = append(errorsSeen, map[string]any{
			"extensionPath": extensionError.ExtensionPath,
			"event":         extensionError.Event,
			"reported":      true,
		})
	})
	value.Emit(context.Background(), extensions.AgentStartEvent{})
	return map[string]any{"calls": calls, "errors": errorsSeen}
}

func runF11InputIdentity(t *testing.T) map[string]any {
	t.Helper()
	images := []*ai.ImageContent{{Data: "original", MimeType: "image/png"}}
	copiedRegistry := extensions.NewRegistry("/fixture")
	registerF11(t, copiedRegistry, "copy-images", func(api extensions.API) {
		api.On(extensions.EventInput, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.InputEvent)
			copied := append([]*ai.ImageContent(nil), event.Images...)
			return extensions.InputResult{Action: extensions.InputTransform, Text: event.Text, Images: copied}, nil
		})
	})
	copied := extensions.NewRunner(copiedRegistry, extensions.RunnerOptions{CWD: "/fixture"}).EmitInput(
		context.Background(), "same", images, extensions.InputRPC, nil,
	)
	retainedRegistry := extensions.NewRegistry("/fixture")
	registerF11(t, retainedRegistry, "retain-images", func(api extensions.API) {
		api.On(extensions.EventInput, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.InputEvent)
			return extensions.InputResult{Action: extensions.InputTransform, Text: event.Text, Images: event.Images}, nil
		})
	})
	retained := extensions.NewRunner(retainedRegistry, extensions.RunnerOptions{CWD: "/fixture"}).EmitInput(
		context.Background(), "same", images, extensions.InputRPC, nil,
	)
	return map[string]any{"copied": copied, "retained": retained}
}

func runF11ProjectTrustBoundary(t *testing.T) map[string]any {
	t.Helper()
	order := []string{}
	var observed map[string]any
	observe := func(extensionContext extensions.Context) {
		if observed != nil {
			return
		}
		observed = map[string]any{
			"cwd":                extensionContext.CWD(),
			"mode":               extensionContext.Mode(),
			"hasUI":              extensionContext.HasUI(),
			"hasGetSystemPrompt": f11CallSucceeded(func() { _ = extensionContext.GetSystemPrompt() }),
			"hasFullUI": f11CallSucceeded(func() {
				extensionContext.UI().SetStatus("fixture", nil)
			}),
		}
	}
	registry := extensions.NewRegistry("/fixture")
	registerF11(t, registry, "trust-broken", func(api extensions.API) {
		api.On(extensions.EventProjectTrust, func(_ context.Context, _ extensions.Event, extensionContext extensions.Context) (any, error) {
			observe(extensionContext)
			order = append(order, "broken")
			return nil, errors.New("trust-boom")
		})
	})
	registerF11(t, registry, "trust-undecided", func(api extensions.API) {
		api.On(extensions.EventProjectTrust, func(_ context.Context, _ extensions.Event, extensionContext extensions.Context) (any, error) {
			observe(extensionContext)
			order = append(order, "undecided")
			return extensions.ProjectTrustResult{Trusted: extensions.ProjectTrustUndecided, Remember: true}, nil
		})
	})
	registerF11(t, registry, "trust-decided", func(api extensions.API) {
		api.On(extensions.EventProjectTrust, func(_ context.Context, _ extensions.Event, extensionContext extensions.Context) (any, error) {
			observe(extensionContext)
			order = append(order, "decided")
			return extensions.ProjectTrustResult{Trusted: extensions.ProjectTrustNo, Remember: true}, nil
		})
	})
	registerF11(t, registry, "trust-after", func(api extensions.API) {
		api.On(extensions.EventProjectTrust, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			order = append(order, "after")
			return extensions.ProjectTrustResult{Trusted: extensions.ProjectTrustYes}, nil
		})
	})
	value := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: "/fixture", Mode: extensions.ModePrint})
	result, trustErrors := value.EmitProjectTrust(
		context.Background(), extensions.ProjectTrustEvent{CWD: "/fixture"}, nil,
	)
	return map[string]any{
		"order": order, "context": observed, "result": result, "errors": f11Errors(trustErrors),
	}
}

func f11CallSucceeded(call func()) (succeeded bool) {
	succeeded = true
	defer func() {
		if recover() != nil {
			succeeded = false
		}
	}()
	call()
	return succeeded
}

func runF11ProjectTrustStartupOrder(t *testing.T) []string {
	t.Helper()
	order := []string{}
	registry := extensions.NewRegistry("/fixture")
	registerF11(t, registry, "trust-startup", func(api extensions.API) {
		api.RegisterProviderConfig("queued-before-trust", extensions.ProviderConfig{BaseURL: "https://queued.test"})
		api.On(extensions.EventProjectTrust, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			order = append(order, "project_trust")
			return extensions.ProjectTrustResult{Trusted: extensions.ProjectTrustYes}, nil
		})
	})
	value := extensions.NewRunner(registry, extensions.RunnerOptions{
		CWD: "/fixture",
		Actions: extensions.Actions{RegisterProviderConfig: func(string, extensions.ProviderConfig) error {
			order = append(order, "register_provider")
			return nil
		}},
	})
	value.EmitProjectTrust(context.Background(), extensions.ProjectTrustEvent{CWD: "/fixture"}, nil)
	return order
}

type f11ProviderModelRegistry struct {
	extensions.ModelRegistry
	registrations *[]map[string]any
}

func (registry *f11ProviderModelRegistry) RegisterProviderConfig(name string, config extensions.ProviderConfig) error {
	if name == "broken-provider" {
		return errors.New("bad registration")
	}
	var oauthName any
	if config.OAuth != nil {
		oauthName = config.OAuth.Name
	}
	*registry.registrations = append(*registry.registrations, map[string]any{
		"kind": "config", "id": name, "name": config.Name, "oauthName": oauthName,
	})
	return nil
}

func (registry *f11ProviderModelRegistry) RegisterProvider(provider extensions.Provider) error {
	var apiKeyName any
	if provider.Auth.APIKey != nil {
		apiKeyName = provider.Auth.APIKey.Name()
	}
	*registry.registrations = append(*registry.registrations, map[string]any{
		"kind": "native", "id": provider.ID, "name": provider.Name,
		"apiKeyName": apiKeyName, "hasOAuth": provider.Auth.OAuth != nil,
	})
	return nil
}

func (registry *f11ProviderModelRegistry) UnregisterProvider(string) error { return nil }

func runF11ProviderRegistration(t *testing.T) map[string]any {
	t.Helper()
	registry := extensions.NewRegistry("/fixture")
	registerF11(t, registry, "config-extension", func(api extensions.API) {
		api.RegisterProviderConfig("config-first", extensions.ProviderConfig{
			Name: "Config First", BaseURL: "https://config.test", OAuth: &extensions.OAuthProvider{Name: "Fixture OAuth"},
		})
	})
	registerF11(t, registry, "native-extension", func(api extensions.API) {
		api.RegisterProvider(extensions.Provider{
			ID: "native-provider", Name: "Native Provider", BaseURL: "https://native.test",
			Auth: aiauth.ProviderAuth{APIKey: aiauth.EnvAPIKeyAuth{DisplayName: "Native API key"}},
		})
	})
	registerF11(t, registry, "broken-extension", func(api extensions.API) {
		api.RegisterProviderConfig("broken-provider", extensions.ProviderConfig{Name: "Broken"})
	})
	registerF11(t, registry, "repeat-one", func(api extensions.API) {
		api.RegisterProviderConfig("repeated-provider", extensions.ProviderConfig{Name: "Repeated One"})
	})
	var postBind extensions.API
	registerF11(t, registry, "repeat-two", func(api extensions.API) {
		postBind = api
		api.RegisterProviderConfig("repeated-provider", extensions.ProviderConfig{Name: "Repeated Two"})
	})
	registrations := []map[string]any{}
	providerErrors := []map[string]any{}
	extensions.NewRunner(registry, extensions.RunnerOptions{
		CWD:           "/fixture",
		ModelRegistry: &f11ProviderModelRegistry{registrations: &registrations},
		ErrorHandler: func(value extensions.ExtensionError) {
			providerErrors = append(providerErrors, map[string]any{
				"extensionPath": value.ExtensionPath, "event": value.Event, "error": value.Error,
			})
		},
	})
	postBindError := f11PanicValue(func() {
		postBind.RegisterProviderConfig("post-bind", extensions.ProviderConfig{Name: "Post Bind"})
	})
	return map[string]any{
		"registrations": registrations, "errors": providerErrors, "postBindError": postBindError,
	}
}

func runF11RegistrationConflicts(t *testing.T) map[string]any {
	t.Helper()
	registry := extensions.NewRegistry("/fixture")
	registerF11(t, registry, "registration-first", func(api extensions.API) {
		api.RegisterTool(extensions.ToolDefinition{Name: "shared", Description: "first-initial"})
		api.RegisterTool(extensions.ToolDefinition{Name: "shared", Description: "first-final"})
		api.RegisterCommand("duplicate", extensions.Command{Description: "first-initial"})
		api.RegisterCommand("duplicate", extensions.Command{Description: "first-final"})
		api.RegisterFlag("shared", extensions.Flag{Type: extensions.FlagBoolean, Default: true, Description: "first-initial"})
		api.RegisterFlag("shared", extensions.Flag{Type: extensions.FlagBoolean, Default: false, Description: "first-final"})
	})
	registerF11(t, registry, "registration-second", func(api extensions.API) {
		api.RegisterTool(extensions.ToolDefinition{Name: "shared", Description: "second"})
		api.RegisterCommand("duplicate", extensions.Command{Description: "second"})
		api.RegisterFlag("shared", extensions.Flag{Type: extensions.FlagBoolean, Default: false, Description: "second"})
	})
	value := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: "/fixture"})
	toolDescriptions := []string{}
	for _, tool := range value.AllRegisteredTools() {
		toolDescriptions = append(toolDescriptions, tool.Definition.Description)
	}
	commands := []map[string]any{}
	for _, command := range value.RegisteredCommands() {
		commands = append(commands, map[string]any{
			"name": command.Name, "invocationName": command.InvocationName, "description": command.Description,
		})
	}
	flag := value.Flags()["shared"]
	flagValue := value.FlagValues()["shared"]
	return map[string]any{
		"toolDescriptions": toolDescriptions,
		"commands":         commands,
		"flag": map[string]any{
			"description": flag.Description, "value": flagValue,
		},
	}
}

func runF11RunnerLifecycle(t *testing.T) map[string]any {
	t.Helper()
	records := []map[string]any{}
	errorsSeen := []string{}
	var captured extensions.Context
	registry := extensions.NewRegistry("/fixture")
	registerF11(t, registry, "shutdown-broken", func(api extensions.API) {
		api.On(extensions.EventSessionShutdown, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, errors.New("shutdown-boom")
		})
	})
	registerF11(t, registry, "shutdown-observer", func(api extensions.API) {
		api.On(extensions.EventSessionShutdown, func(_ context.Context, raw extensions.Event, extensionContext extensions.Context) (any, error) {
			captured = extensionContext
			event := raw.(extensions.SessionShutdownEvent)
			var target any
			if event.TargetSessionFile != nil {
				target = *event.TargetSessionFile
			}
			records = append(records, map[string]any{
				"type": event.Type(), "reason": event.Reason, "targetSessionFile": target,
			})
			return map[string]any{"ignored": true}, nil
		})
	})
	value := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: "/fixture"})
	value.OnError(func(extensionError extensions.ExtensionError) {
		errorsSeen = append(errorsSeen, extensionError.Error)
	})
	target := "/fixture/next.jsonl"
	emitted := extensions.EmitSessionShutdown(context.Background(), value, extensions.SessionShutdownEvent{
		Reason: extensions.SessionShutdownResume, TargetSessionFile: &target,
	})
	beforeInvalidation := captured.CWD()
	value.Invalidate("stale")
	staleError := f11PanicValue(func() { _ = captured.CWD() })
	missing := extensions.EmitSessionShutdown(
		context.Background(),
		extensions.NewRunner(extensions.NewRegistry("/fixture"), extensions.RunnerOptions{CWD: "/fixture"}),
		extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit},
	)
	return map[string]any{
		"emitted": emitted, "missing": missing, "records": records, "errors": errorsSeen,
		"beforeInvalidation": beforeInvalidation, "staleError": staleError,
	}
}

func f11PanicValue(call func()) (recovered any) {
	defer func() {
		value := recover()
		switch typed := value.(type) {
		case nil:
			recovered = nil
		case error:
			recovered = typed.Error()
		case string:
			recovered = typed
		default:
			recovered = "panic"
		}
	}()
	call()
	return nil
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
