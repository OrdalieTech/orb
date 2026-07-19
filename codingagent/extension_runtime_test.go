package codingagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

func TestSessionRuntimeWiresExtensionHooksAndLifecycle(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	var mu sync.Mutex
	var events []string
	var extensionExecuted bool
	var providerPayload any
	var providerHeaders ai.ProviderHeaders
	var providerResponse int
	if err := registry.Register("<inline:wire>", func(api extensions.API) error {
		record := func(name string) {
			mu.Lock()
			events = append(events, name)
			mu.Unlock()
		}
		for _, eventType := range []extensions.EventType{
			extensions.EventSessionStart, extensions.EventAgentStart, extensions.EventTurnStart,
			extensions.EventToolExecutionStart, extensions.EventToolExecutionEnd,
			extensions.EventTurnEnd, extensions.EventAgentEnd, extensions.EventAgentSettled,
		} {
			eventType := eventType
			api.On(eventType, func(context.Context, extensions.Event, extensions.Context) (any, error) {
				record(string(eventType))
				return nil, nil
			})
		}
		api.On(extensions.EventResourcesDiscover, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			record("resources_discover")
			return extensions.ResourcesDiscoverResult{SkillPaths: []string{"skills"}, PromptPaths: []string{"prompts"}}, nil
		})
		api.On(extensions.EventInput, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			record("input")
			event := raw.(extensions.InputEvent)
			return extensions.InputResult{Action: extensions.InputTransform, Text: event.Text + "-transformed"}, nil
		})
		api.On(extensions.EventBeforeAgentStart, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			record("before_agent_start")
			event := raw.(extensions.BeforeAgentStartEvent)
			prompt := event.SystemPrompt + "\nHOOKED"
			return extensions.BeforeAgentStartResult{
				Message:      &extensions.CustomMessage{CustomType: "wire", Content: "injected", Display: true},
				SystemPrompt: &prompt,
			}, nil
		})
		api.On(extensions.EventContext, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			record("context")
			return extensions.ContextResult{Messages: raw.(extensions.ContextEvent).Messages}, nil
		})
		api.On(extensions.EventBeforeProviderRequest, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			record("before_provider_request")
			payload := raw.(extensions.BeforeProviderRequestEvent).Payload.(map[string]any)
			payload["extension"] = true
			return extensions.ProviderRequestResult{Payload: payload, Replace: true}, nil
		})
		api.On(extensions.EventBeforeProviderHeaders, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			record("before_provider_headers")
			headers := raw.(extensions.BeforeProviderHeadersEvent).Headers
			value := "yes"
			headers["X-Extension"] = &value
			delete(headers, "X-Remove")
			return nil, nil
		})
		api.On(extensions.EventAfterProviderResponse, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			record("after_provider_response")
			providerResponse = raw.(extensions.AfterProviderResponseEvent).Status
			return nil, nil
		})
		api.On(extensions.EventToolCall, func(_ context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			record("tool_call")
			return nil, nil
		})
		api.On(extensions.EventToolResult, func(_ context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			record("tool_result")
			content := ai.ToolResultContent{&ai.TextContent{Text: "patched"}}
			details := any(map[string]any{"patched": true})
			isError := true
			return extensions.ToolResultResult{Content: &content, Details: &details, IsError: &isError}, nil
		})
		api.On(extensions.EventMessageEnd, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			message, ok := raw.(extensions.MessageEndEvent).Message.(*ai.AssistantMessage)
			if !ok || assistantText(message) != "done" {
				return nil, nil
			}
			copy := *message
			copy.Content = ai.AssistantContent{&ai.TextContent{Text: "replaced"}}
			return extensions.MessageEndResult{Message: &copy}, nil
		})
		api.RegisterTool(extensions.ToolDefinition{
			Name: "bash", Label: "extension bash", Description: "override",
			Parameters: jsonschema.Schema(`{"type":"object","properties":{}}`),
			Execute: func(context.Context, string, any, agent.AgentToolUpdateCallback, extensions.Context) (agent.AgentToolResult, error) {
				extensionExecuted = true
				return agent.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "original"}}}, nil
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("bash", map[string]any{}, faux.ToolCallOptions{ID: "call-1"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("done"),
	})
	stream := func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		payload := any(map[string]any{"original": true})
		if options.OnPayload != nil {
			var replace bool
			var err error
			payload, replace, err = options.OnPayload(ctx, payload, model)
			if err != nil {
				return nil, err
			}
			if !replace {
				t.Fatal("payload hook did not replace")
			}
		}
		providerPayload = payload
		remove := "remove"
		headers := ai.ProviderHeaders{"X-Remove": &remove}
		if options.TransformHeaders != nil {
			var err error
			headers, err = options.TransformHeaders(ctx, headers, model)
			if err != nil {
				return nil, err
			}
		}
		providerHeaders = headers
		if options.OnResponse != nil {
			if err := options.OnResponse(ctx, ai.ProviderResponse{Status: 201, Headers: map[string]string{"x": "y"}}, model); err != nil {
				return nil, err
			}
		}
		copy := *options
		copy.OnResponse = nil
		return provider.StreamSimple(ctx, model, request, &copy)
	}
	baseRan := false
	baseBash := agent.AgentToolFunc{
		AgentToolSpec: agent.AgentToolSpec{Name: "bash", Label: "bash", Description: "base", Parameters: jsonschema.Schema(`{"type":"object","properties":{}}`)},
		Run: func(context.Context, string, any, agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			baseRan = true
			return agent.AgentToolResult{}, nil
		},
	}
	promptOptions := SystemPromptOptions{CWD: cwd, SelectedTools: []string{"bash"}, ToolSnippets: map[string]string{"bash": "base"}}
	created := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{SystemPrompt: "base", Model: provider.GetModel(), Tools: []agent.AgentTool{baseBash}}),
		agent.WithStreamFn(stream), agent.WithConvertToLLM(ConvertToLLM),
	)
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
		ExtensionMode: extensions.ModePrint, BaseTools: []agent.AgentTool{baseBash},
		InitialActiveToolNames: []string{"bash"}, SystemPromptOptions: &promptOptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	listenerSawReplacement := false
	runtime.Subscribe(func(event any) {
		if ended, ok := event.(agent.MessageEndEvent); ok {
			if assistant := asAssistant(ended.Message); assistant != nil && assistantText(assistant) == "replaced" {
				listenerSawReplacement = true
			}
		}
	})
	resources := runtime.ExtensionResources()
	if len(resources.SkillPaths) != 1 || resources.SkillPaths[0].Path != "skills" || len(resources.PromptPaths) != 1 {
		t.Fatalf("resources = %#v", resources)
	}
	if err := runtime.Prompt(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if baseRan || !extensionExecuted {
		t.Fatalf("tool override: base=%t extension=%t", baseRan, extensionExecuted)
	}
	if payload, ok := providerPayload.(map[string]any); !ok || payload["extension"] != true {
		t.Fatalf("provider payload = %#v", providerPayload)
	}
	if providerHeaders["X-Extension"] == nil || *providerHeaders["X-Extension"] != "yes" || providerHeaders["X-Remove"] != nil {
		t.Fatalf("provider headers = %#v", providerHeaders)
	}
	if providerResponse != 201 {
		t.Fatalf("provider response status = %d", providerResponse)
	}
	state := runtime.State()
	if !strings.Contains(state.SystemPrompt, "HOOKED") {
		t.Fatalf("system prompt = %q", state.SystemPrompt)
	}
	var result *ai.ToolResultMessage
	var final *ai.AssistantMessage
	for _, message := range state.Messages {
		if typed, ok := message.(*ai.ToolResultMessage); ok {
			result = typed
		}
		if typed, ok := message.(*ai.AssistantMessage); ok {
			final = typed
		}
	}
	if result == nil || !result.IsError || len(result.Content) != 1 || result.Content[0].(*ai.TextContent).Text != "patched" {
		t.Fatalf("tool result = %#v", result)
	}
	var details map[string]any
	if err := json.Unmarshal(result.Details, &details); err != nil || details["patched"] != true {
		t.Fatalf("tool details = %s, %v", result.Details, err)
	}
	if final == nil || assistantText(final) != "replaced" {
		t.Fatalf("final assistant = %#v", final)
	}
	if !listenerSawReplacement {
		t.Fatal("runtime listener saw the pre-extension message")
	}
	persistedReplacement := false
	for _, raw := range manager.BuildSessionContext().Messages {
		if assistant := asAssistant(decodeSessionMessage(raw)); assistant != nil && assistantText(assistant) == "replaced" {
			persistedReplacement = true
		}
	}
	if !persistedReplacement {
		t.Fatal("session persisted the pre-extension message")
	}
	mu.Lock()
	trace := strings.Join(events, ",")
	mu.Unlock()
	for _, expected := range []string{
		"session_start,resources_discover", "input,before_agent_start", "agent_start,turn_start",
		"context,before_provider_request,before_provider_headers,after_provider_response",
		"tool_execution_start,tool_call,tool_result,tool_execution_end", "turn_end,agent_end,agent_settled",
	} {
		if !strings.Contains(trace, expected) {
			t.Fatalf("lifecycle trace %q missing %q", trace, expected)
		}
	}
}

func TestExtensionCommandInputAndNativeResourcesShareUpstreamOrder(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	extensionSkillPath := filepath.Join(cwd, "extension-skills", "ext-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(extensionSkillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionSkillPath, []byte("---\nname: ext-skill\ndescription: Extension skill.\n---\nExtension instructions."), 0o644); err != nil {
		t.Fatal(err)
	}
	extensionPromptPath := filepath.Join(cwd, "extension-prompts", "ext.md")
	if err := os.MkdirAll(filepath.Dir(extensionPromptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionPromptPath, []byte("EXT $1"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := extensions.NewRegistry(cwd)
	var extensionAPI extensions.API
	commandCalls, inputCalls := 0, 0
	extensionPath := filepath.Join(cwd, "extensions", "wire.ts")
	if err := registry.Register(extensionPath, func(api extensions.API) error {
		extensionAPI = api
		api.RegisterCommand("command", extensions.Command{Handler: func(context.Context, string, extensions.CommandContext) error {
			commandCalls++
			return nil
		}})
		api.On(extensions.EventInput, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			inputCalls++
			event := raw.(extensions.InputEvent)
			if event.Text == "/alias value" {
				return extensions.InputResult{Action: extensions.InputTransform, Text: "/ext value"}, nil
			}
			return nil, nil
		})
		api.On(extensions.EventResourcesDiscover, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return extensions.ResourcesDiscoverResult{
				SkillPaths: []string{"extension-skills"}, PromptPaths: []string{"extension-prompts"},
			}, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	baseSkill := Skill{
		Name: "base-skill", Description: "Package skill.", Content: "Package instructions.",
		FilePath: filepath.Join(cwd, "package", "base-skill", "SKILL.md"), BaseDir: filepath.Join(cwd, "package", "base-skill"),
		SourceInfo: SourceInfo{Path: "package/base-skill/SKILL.md", Source: "package", Scope: "temporary", Origin: "package", BaseDir: filepath.Join(cwd, "package")},
	}
	basePrompt := PromptTemplate{
		Name: "base", Content: "BASE $1", FilePath: filepath.Join(cwd, "package", "base.md"),
		SourceInfo: SourceInfo{Path: "package/base.md", Source: "package", Scope: "temporary", Origin: "package", BaseDir: filepath.Join(cwd, "package")},
	}
	readTool := agent.AgentToolFunc{AgentToolSpec: agent.AgentToolSpec{
		Name: "read", Description: "Read", Parameters: jsonschema.Schema(`{"type":"object","properties":{}}`),
	}, Run: func(context.Context, string, any, agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
		return agent.AgentToolResult{}, nil
	}}
	promptOptions := SystemPromptOptions{
		CWD: cwd, SelectedTools: []string{"read"}, ToolSnippets: map[string]string{"read": "Read"}, Skills: []Skill{baseSkill},
	}
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("done")})
	created := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{
			SystemPrompt: BuildSystemPrompt(promptOptions), Model: provider.GetModel(), Tools: []agent.AgentTool{readTool},
		}),
		agent.WithStreamFn(provider.StreamSimple),
	)
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
		SlashResolver: &SlashResolver{Skills: []Skill{baseSkill}, PromptTemplates: []PromptTemplate{basePrompt}},
		BaseTools:     []agent.AgentTool{readTool}, InitialActiveToolNames: []string{"read"}, SystemPromptOptions: &promptOptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	commands := runtime.Commands()
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name)
	}
	if got, want := strings.Join(names, ","), "command,base,ext,skill:base-skill,skill:ext-skill"; got != want {
		t.Fatalf("command order = %q, want %q", got, want)
	}
	apiCommands, err := extensionAPI.GetCommands()
	if err != nil || len(apiCommands) != len(commands) {
		t.Fatalf("extension getCommands = %#v, %v", apiCommands, err)
	}
	if got := runtime.slashResolver.Skills[1].SourceInfo; got.Source != "extension:wire" || got.BaseDir != filepath.Dir(extensionPath) {
		t.Fatalf("extension skill source = %#v", got)
	}
	if !strings.Contains(runtime.State().SystemPrompt, "<name>ext-skill</name>") {
		t.Fatalf("system prompt omitted extension skill: %q", runtime.State().SystemPrompt)
	}

	created.SetModel(nil)
	if err := runtime.Prompt(context.Background(), "/command now"); err != nil {
		t.Fatalf("extension command should bypass model preflight: %v", err)
	}
	if commandCalls != 1 || inputCalls != 0 {
		t.Fatalf("command/input calls = %d/%d", commandCalls, inputCalls)
	}
	created.SetModel(provider.GetModel())
	if err := runtime.Prompt(context.Background(), "/alias value"); err != nil {
		t.Fatal(err)
	}
	if inputCalls != 1 {
		t.Fatalf("input calls = %d", inputCalls)
	}
	state := runtime.State()
	if got := userMessageText(state.Messages[0]); got != "EXT value" {
		t.Fatalf("expanded user message = %q", got)
	}
}

func TestDynamicActiveToolsReportOnlyAdditions(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<inline:dynamic>", func(api extensions.API) error {
		api.RegisterTool(extensions.ToolDefinition{
			Name: "loader", Description: "loader", Parameters: jsonschema.Schema(`{"type":"object","properties":{}}`),
			Execute: func(context.Context, string, any, agent.AgentToolUpdateCallback, extensions.Context) (agent.AgentToolResult, error) {
				if err := api.SetActiveTools([]string{"loader", "late"}); err != nil {
					return agent.AgentToolResult{}, err
				}
				names := []string{"existing", "existing"}
				return agent.AgentToolResult{Content: ai.ToolResultContent{}, AddedToolNames: &names}, nil
			},
		})
		api.RegisterTool(extensions.ToolDefinition{
			Name: "late", Description: "late", Parameters: jsonschema.Schema(`{"type":"object","properties":{}}`),
			Execute: func(context.Context, string, any, agent.AgentToolUpdateCallback, extensions.Context) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{}, nil
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{SystemPrompt: "base"}))
	promptOptions := SystemPromptOptions{CWD: cwd}
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
		InitialActiveToolNames: []string{"loader"}, SystemPromptOptions: &promptOptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	if err := runtime.setActiveToolsByName([]string{"loader"}); err != nil {
		t.Fatal(err)
	}
	tool := runtime.State().Tools[0]
	result, err := tool.Execute(context.Background(), "call", map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.AddedToolNames == nil || strings.Join(*result.AddedToolNames, ",") != "existing,late" {
		t.Fatalf("added tools = %v", result.AddedToolNames)
	}
	active, _ := runtime.extensionActiveTools()
	if strings.Join(active, ",") != "loader,late" {
		t.Fatalf("active tools = %v", active)
	}
}

func TestExtensionToolOverrideReplacesBuiltInPromptMetadata(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<inline:read>", func(api extensions.API) error {
		api.RegisterTool(extensions.ToolDefinition{
			Name: "read", Description: "override", Parameters: jsonschema.Schema(`{"type":"object","properties":{}}`),
			PromptGuidelines: []string{"Use the extension reader."},
			Execute: func(context.Context, string, any, agent.AgentToolUpdateCallback, extensions.Context) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{}, nil
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base := agent.AgentToolFunc{AgentToolSpec: agent.AgentToolSpec{Name: "read", Description: "base", Parameters: jsonschema.Schema(`{}`)}}
	snippets, guidelines := BuiltInToolPromptData([]string{"read"})
	promptOptions := SystemPromptOptions{CWD: cwd, SelectedTools: []string{"read"}, ToolSnippets: snippets, PromptGuidelines: guidelines}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{SystemPrompt: BuildSystemPrompt(promptOptions), Tools: []agent.AgentTool{base}}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
		BaseTools: []agent.AgentTool{base}, InitialActiveToolNames: []string{"read"}, SystemPromptOptions: &promptOptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	prompt := runtime.State().SystemPrompt
	if strings.Contains(prompt, "Read file contents") || strings.Contains(prompt, "Use read to examine files") || !strings.Contains(prompt, "Use the extension reader.") {
		t.Fatalf("override prompt metadata = %q", prompt)
	}
}

func TestExtensionToolsApplyAllowlistAndDenylist(t *testing.T) {
	allowCustom := []string{"custom"}
	allowNone := []string{}
	for _, test := range []struct {
		name     string
		allowed  *[]string
		excluded []string
		want     string
	}{
		{name: "allow extension", allowed: &allowCustom, want: "custom"},
		{name: "disable all", allowed: &allowNone, want: ""},
		{name: "disable builtins", excluded: []string{"read"}, want: "custom"},
		{name: "deny extension", excluded: []string{"custom"}, want: "read"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			manager, settings := extensionRuntimeDependencies(t, cwd)
			registry := extensions.NewRegistry(cwd)
			if err := registry.Register("<inline:custom>", func(api extensions.API) error {
				api.RegisterTool(extensions.ToolDefinition{
					Name: "custom", Description: "custom", Parameters: jsonschema.Schema(`{}`),
					Execute: func(context.Context, string, any, agent.AgentToolUpdateCallback, extensions.Context) (agent.AgentToolResult, error) {
						return agent.AgentToolResult{}, nil
					},
				})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			base := agent.AgentToolFunc{AgentToolSpec: agent.AgentToolSpec{Name: "read", Description: "base", Parameters: jsonschema.Schema(`{}`)}}
			created := agent.NewAgent(agent.WithInitialState(agent.AgentState{SystemPrompt: "base", Tools: []agent.AgentTool{base}}))
			runtime, err := NewSessionRuntime(SessionRuntimeConfig{
				Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
				BaseTools: []agent.AgentTool{base}, InitialActiveToolNames: []string{"read"},
				AllowedToolNames: test.allowed, ExcludedToolNames: test.excluded,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Dispose()
			active, err := runtime.extensionActiveTools()
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(active, ","); got != test.want {
				t.Fatalf("active tools = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUserBashHandlerCanReplaceOperations(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	exitCode := 7
	operations := bashOperationsFunc(func(_ context.Context, command, gotCWD string, options tools.BashExecOptions) (tools.BashExecResult, error) {
		if command != "printf fixture" || gotCWD != cwd {
			t.Fatalf("command=%q cwd=%q", command, gotCWD)
		}
		options.OnData([]byte("handled"))
		return tools.BashExecResult{ExitCode: &exitCode}, nil
	})
	if err := registry.Register("<inline:bash>", func(api extensions.API) error {
		api.On(extensions.EventUserBash, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return extensions.UserBashResult{Operations: operations}, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent()
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	var chunks strings.Builder
	result, err := runtime.ExecuteUserBash(context.Background(), "printf fixture", true, func(chunk string) { chunks.WriteString(chunk) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "handled" || result.ExitCode == nil || *result.ExitCode != 7 || chunks.String() != "handled" {
		t.Fatalf("result=%#v chunks=%q", result, chunks.String())
	}
	state := runtime.State()
	message, ok := state.Messages[len(state.Messages)-1].(harness.BashExecutionMessage)
	if !ok || message.ExcludeFromContext == nil || !*message.ExcludeFromContext || message.Command != "printf fixture" {
		t.Fatalf("bash message = %#v", state.Messages[len(state.Messages)-1])
	}
}

func TestExtensionUserMessageRunsInputHookBeforeStreamingQueue(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	var observedBehavior *extensions.DeliveryMode
	var extensionAPI extensions.API
	if err := registry.Register("<inline:input>", func(api extensions.API) error {
		extensionAPI = api
		api.On(extensions.EventInput, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.InputEvent)
			if event.Source != extensions.InputExtension {
				return nil, nil
			}
			observedBehavior = event.StreamingBehavior
			if event.Text == "idle" {
				return extensions.InputResult{Action: extensions.InputHandled}, nil
			}
			return extensions.InputResult{Action: extensions.InputTransform, Text: event.Text + "-hooked"}, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{
		faux.Factory(func(ctx context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			close(started)
			select {
			case <-release:
				return faux.AssistantMessage("first"), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}),
		faux.AssistantMessage("second"),
	})
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{SystemPrompt: "base", Model: provider.GetModel()}), agent.WithStreamFn(provider.StreamSimple), agent.WithConvertToLLM(ConvertToLLM))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	idleOptions := &extensions.SendUserMessageOptions{DeliverAs: extensions.DeliverFollowUp}
	if err := extensionAPI.SendUserMessage(context.Background(), ai.NewUserText("idle"), idleOptions); err != nil {
		t.Fatal(err)
	}
	if observedBehavior != nil {
		t.Fatalf("idle input streaming behavior = %v, want nil", *observedBehavior)
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Prompt(context.Background(), "first") }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not start streaming")
	}
	if err := extensionAPI.SendUserMessage(context.Background(), ai.NewUserText("missing-mode"), nil); err == nil || err.Error() != "Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message." {
		t.Fatalf("missing streaming behavior error = %v", err)
	}
	options := &extensions.SendUserMessageOptions{DeliverAs: extensions.DeliverFollowUp}
	if err := extensionAPI.SendUserMessage(context.Background(), ai.NewUserText("queued"), options); err != nil {
		t.Fatal(err)
	}
	if observedBehavior == nil || *observedBehavior != extensions.DeliverFollowUp {
		t.Fatalf("input streaming behavior = %v", observedBehavior)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not consume queued message")
	}
	found := false
	for _, message := range runtime.State().Messages {
		if userMessageText(message) == "queued-hooked" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("transformed queued message missing from %#v", runtime.State().Messages)
	}
}

func TestNormalizeExtensionMessageFillsMissingCoreContent(t *testing.T) {
	tests := []struct {
		name    string
		message agent.AgentMessage
		valid   func(agent.AgentMessage) bool
	}{
		{
			name: "user", message: &ai.UserMessage{},
			valid: func(message agent.AgentMessage) bool {
				user := message.(*ai.UserMessage)
				return user.Content.Text == nil && user.Content.Blocks != nil
			},
		},
		{
			name: "assistant", message: &ai.AssistantMessage{},
			valid: func(message agent.AgentMessage) bool {
				return message.(*ai.AssistantMessage).Content != nil
			},
		},
		{
			name: "tool result", message: &ai.ToolResultMessage{},
			valid: func(message agent.AgentMessage) bool {
				return message.(*ai.ToolResultMessage).Content != nil
			},
		},
		{
			name: "custom", message: &harness.CustomMessage{},
			valid: func(message agent.AgentMessage) bool {
				return message.(*harness.CustomMessage).Content != nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeExtensionMessage(test.message)
			if !test.valid(normalized) {
				t.Fatalf("normalized message = %#v", normalized)
			}
		})
	}
}

func TestExtensionCustomMessageMatchesUpstreamNullPersistenceQuirk(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	var extensionAPI extensions.API
	if err := registry.Register("<inline:message>", func(api extensions.API) error {
		extensionAPI = api
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	if err := extensionAPI.SendMessage(context.Background(), extensions.CustomMessage{CustomType: "fixture"}, nil); err != nil {
		t.Fatal(err)
	}
	message := runtime.State().Messages[0].(*harness.CustomMessage)
	if message.Content == nil {
		t.Fatal("runtime custom message content was not normalized")
	}
	entries := manager.GetEntries()
	if len(entries) != 1 || string(entries[0].Content) != "null" {
		t.Fatalf("persisted custom content = %s", entries[0].Content)
	}
}

func TestExtensionCompactionHooksCanProvideSummary(t *testing.T) {
	cwd := t.TempDir()
	settingsDir := filepath.Join(cwd, config.ConfigDirName)
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(`{"compaction":{"enabled":true,"reserveTokens":50,"keepRecentTokens":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	var completed *extensions.SessionCompactEvent
	if err := registry.Register("<inline:compaction>", func(api extensions.API) error {
		api.On(extensions.EventSessionBeforeCompact, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionBeforeCompactEvent)
			if event.Reason != extensions.CompactionManual || event.WillRetry || len(event.BranchEntries) != 3 {
				t.Fatalf("before compact event = %#v", event)
			}
			return extensions.SessionBeforeCompactResult{Compaction: &harness.CompactionResult{
				Summary: "extension summary", FirstKeptEntryID: event.Preparation.FirstKeptEntryID,
				TokensBefore: event.Preparation.TokensBefore,
				Details:      harness.CompactionDetails{ReadFiles: []string{"read.go"}, ModifiedFiles: []string{}},
			}}, nil
		})
		api.On(extensions.EventSessionCompact, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionCompactEvent)
			completed = &event
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	provider := faux.New()
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Model: provider.GetModel()}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	if _, err := manager.AppendMessage(userMessage("old request")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(runtimeAssistant(provider, "old answer", 20)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(userMessage("latest request")); err != nil {
		t.Fatal(err)
	}
	runtime.syncAgentMessages()
	result, err := runtime.Compact(context.Background(), "focus")
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "extension summary" || completed == nil || !completed.FromExtension || completed.Reason != extensions.CompactionManual {
		t.Fatalf("result=%#v completed=%#v", result, completed)
	}
	entry := manager.GetEntry(completed.CompactionEntry.ID)
	if entry == nil || entry.FromHook == nil || !*entry.FromHook {
		t.Fatalf("compaction entry = %#v", entry)
	}
}

func TestExtensionTreeHooksCanProvideSummaryAndLabel(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	label := "from-extension"
	var completed *extensions.SessionTreeEvent
	if err := registry.Register("<inline:tree>", func(api extensions.API) error {
		api.On(extensions.EventSessionBeforeTree, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionBeforeTreeEvent)
			if !event.Preparation.UserWantsSummary || len(event.Preparation.EntriesToSummarize) != 3 {
				t.Fatalf("before tree event = %#v", event)
			}
			return extensions.SessionBeforeTreeResult{
				Summary: &extensions.TreeSummary{Summary: "extension branch", Details: map[string]any{"source": "hook"}},
				Label:   &label,
			}, nil
		})
		api.On(extensions.EventSessionTree, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionTreeEvent)
			completed = &event
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent()
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	first, _ := manager.AppendMessage(userMessage("first"))
	_, _ = manager.AppendMessage(&ai.AssistantMessage{Content: ai.AssistantContent{}, Timestamp: 1})
	_, _ = manager.AppendMessage(userMessage("second"))
	_, _ = manager.AppendMessage(&ai.AssistantMessage{Content: ai.AssistantContent{}, Timestamp: 2})
	runtime.syncAgentMessages()
	result, err := runtime.NavigateTree(context.Background(), first, NavigateTreeOptions{Summarize: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.SummaryEntry == nil || result.SummaryEntry.Summary != "extension branch" || result.SummaryEntry.FromHook == nil || !*result.SummaryEntry.FromHook {
		t.Fatalf("tree result = %#v", result)
	}
	if got := manager.GetLabel(result.SummaryEntry.ID); got == nil || *got != label {
		t.Fatalf("summary label = %v", got)
	}
	if completed == nil || completed.SummaryEntry == nil || completed.SummaryEntry.ID != result.SummaryEntry.ID || completed.FromExtension == nil || !*completed.FromExtension {
		t.Fatalf("completed tree event = %#v", completed)
	}
}

func TestExtensionThinkingLevelClampsAndSkipsDuplicatePersistence(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
		Model: &ai.Model{Reasoning: false}, ThinkingLevel: agent.ThinkingOff,
	}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	if err := runtime.setExtensionThinkingLevel(agent.ThinkingHigh); err != nil {
		t.Fatal(err)
	}
	if got := runtime.State().ThinkingLevel; got != agent.ThinkingOff || len(manager.GetEntries()) != 0 {
		t.Fatalf("non-reasoning state = %q, entries = %d", got, len(manager.GetEntries()))
	}
	runtime.agent.SetModel(&ai.Model{Reasoning: true})
	if err := runtime.setExtensionThinkingLevel(agent.ThinkingXHigh); err != nil {
		t.Fatal(err)
	}
	if got := runtime.State().ThinkingLevel; got != agent.ThinkingHigh || len(manager.GetEntries()) != 1 {
		t.Fatalf("clamped state = %q, entries = %d", got, len(manager.GetEntries()))
	}
	if err := runtime.setExtensionThinkingLevel(agent.ThinkingXHigh); err != nil {
		t.Fatal(err)
	}
	if len(manager.GetEntries()) != 1 {
		t.Fatalf("duplicate thinking entries = %d", len(manager.GetEntries()))
	}
}

func TestNoExtensionsLeavesRuntimeSeamUnallocated(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: agent.NewAgent(), SessionManager: manager, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	if runtime.extensionState != nil || runtime.ExtensionRunner() != nil {
		t.Fatalf("unused extension state = %#v", runtime.extensionState)
	}
}

func TestExtensionContextUsesResolvedProjectTrust(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	settings.SetProjectTrusted(false)
	registry := extensions.NewRegistry(cwd)
	trusted := true
	if err := registry.Register("<inline:trust>", func(api extensions.API) error {
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			trusted = ctx.IsProjectTrusted()
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	if trusted {
		t.Fatal("extension context reported an untrusted project as trusted")
	}
}

type bashOperationsFunc func(context.Context, string, string, tools.BashExecOptions) (tools.BashExecResult, error)

func (function bashOperationsFunc) Exec(ctx context.Context, command, cwd string, options tools.BashExecOptions) (tools.BashExecResult, error) {
	return function(ctx, command, cwd, options)
}

func extensionRuntimeDependencies(t *testing.T, cwd string) (*session.SessionManager, *config.SettingsManager) {
	t.Helper()
	manager, err := session.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	return manager, settings
}

// Port of upstream regression 2023-queued-slash-command-followup.test.ts:
// extension-origin queued slash-command follow-ups are delivered as raw user
// text to the model instead of dispatching the extension command.
func TestExtensionQueuedSlashCommandFollowUpStaysRawText(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	var api extensions.API
	var commandRuns []string
	if err := registry.Register("<inline:queued-command>", func(registered extensions.API) error {
		api = registered
		registered.RegisterCommand("testcmd", extensions.Command{
			Description: "Test command",
			Handler: func(_ context.Context, args string, _ extensions.CommandContext) error {
				commandRuns = append(commandRuns, args)
				return nil
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	waitTool := agent.AgentToolFunc{
		AgentToolSpec: agent.AgentToolSpec{
			Name: "wait", Label: "Wait", Description: "Wait for the test to release execution",
			Parameters: jsonschema.Schema(`{"type":"object","properties":{}}`),
		},
		Run: func(ctx context.Context, _ string, _ any, _ agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			select {
			case <-release:
			case <-ctx.Done():
				return agent.AgentToolResult{}, ctx.Err()
			}
			return agent.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "released"}}}, nil
		},
	}
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("wait", map[string]any{}, faux.ToolCallOptions{ID: "wait-1"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("first turn complete"),
		faux.AssistantMessage("queued follow-up handled by model"),
	})
	created := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{SystemPrompt: "test", Model: provider.GetModel(), Tools: []agent.AgentTool{waitTool}}),
		agent.WithStreamFn(provider.StreamSimple), agent.WithConvertToLLM(ConvertToLLM),
	)
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, ExtensionMode: extensions.ModePrint,
		BaseTools: []agent.AgentTool{waitTool}, InitialActiveToolNames: []string{"wait"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	toolStarted := make(chan struct{}, 1)
	runtime.Subscribe(func(event any) {
		if started, ok := event.(agent.ToolExecutionStartEvent); ok && started.ToolName == "wait" {
			select {
			case toolStarted <- struct{}{}:
			default:
			}
		}
	})
	promptDone := make(chan error, 1)
	go func() { promptDone <- runtime.Prompt(context.Background(), "start") }()
	select {
	case <-toolStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("wait tool did not start")
	}
	queued := "/testcmd queued"
	if err := api.SendUserMessage(context.Background(), ai.UserContent{Text: &queued}, &extensions.SendUserMessageOptions{DeliverAs: extensions.DeliverFollowUp}); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-promptDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not settle")
	}
	if len(commandRuns) != 0 {
		t.Fatalf("extension command dispatched for queued follow-up: %#v", commandRuns)
	}
	var userTexts, assistantTexts []string
	for _, message := range runtime.State().Messages {
		if text := userMessageText(message); text != "" {
			userTexts = append(userTexts, text)
		}
		if assistant := asAssistant(message); assistant != nil {
			assistantTexts = append(assistantTexts, assistantText(assistant))
		}
	}
	if !reflect.DeepEqual(userTexts, []string{"start", "/testcmd queued"}) {
		t.Fatalf("user texts = %#v", userTexts)
	}
	if !slices.Contains(assistantTexts, "queued follow-up handled by model") {
		t.Fatalf("assistant texts = %#v", assistantTexts)
	}
}

// Port of upstream regression 3982-message-end-cost-override.test.ts:
// extensions can replace the finalized assistant usage cost from message_end,
// and both the session state and the emitted event carry the override.
func TestExtensionMessageEndCanOverrideAssistantUsageCost(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<inline:cost-override>", func(api extensions.API) error {
		api.On(extensions.EventMessageEnd, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			message, ok := raw.(extensions.MessageEndEvent).Message.(*ai.AssistantMessage)
			if !ok {
				return nil, nil
			}
			replaced := *message
			replaced.Usage.Cost.Total = 0.123
			return extensions.MessageEndResult{Message: &replaced}, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("hello")})
	created := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{SystemPrompt: "test", Model: provider.GetModel()}),
		agent.WithStreamFn(provider.StreamSimple), agent.WithConvertToLLM(ConvertToLLM),
	)
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, ExtensionMode: extensions.ModePrint,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	var eventCost *float64
	runtime.Subscribe(func(event any) {
		if ended, ok := event.(agent.MessageEndEvent); ok {
			if assistant := asAssistant(ended.Message); assistant != nil {
				cost := assistant.Usage.Cost.Total
				eventCost = &cost
			}
		}
	})
	if err := runtime.Prompt(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	var stateAssistant *ai.AssistantMessage
	for _, message := range runtime.State().Messages {
		if assistant := asAssistant(message); assistant != nil {
			stateAssistant = assistant
		}
	}
	if stateAssistant == nil || stateAssistant.Usage.Cost.Total != 0.123 {
		t.Fatalf("session assistant cost = %#v", stateAssistant)
	}
	if eventCost == nil || *eventCost != 0.123 {
		t.Fatalf("message_end event cost = %#v", eventCost)
	}
	// The persisted entry carries the override too (message_end replacement
	// happens before persistence).
	var persistedCost float64
	persisted := false
	for _, entry := range manager.GetEntries() {
		if entry.Type != "message" {
			continue
		}
		decoded, decodeErr := ai.UnmarshalMessage(entry.Message)
		if decodeErr != nil {
			continue
		}
		if assistant, ok := decoded.(*ai.AssistantMessage); ok {
			persistedCost = assistant.Usage.Cost.Total
			persisted = true
		}
	}
	if !persisted || persistedCost != 0.123 {
		t.Fatalf("persisted assistant cost = %v (found=%t)", persistedCost, persisted)
	}
}

func TestSessionStartEventNonDefault(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	var receivedReason extensions.SessionStartReason
	if err := registry.Register("<inline:start>", func(api extensions.API) error {
		api.On(extensions.EventSessionStart, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			receivedReason = raw.(extensions.SessionStartEvent).Reason
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resumeEvent := &extensions.SessionStartEvent{Reason: extensions.SessionStartResume}
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, SessionStartEvent: resumeEvent,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	if receivedReason != extensions.SessionStartResume {
		t.Fatalf("expected SessionStartResume, got %q", receivedReason)
	}
}

func TestSessionStartEventDefaultIsStartup(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	var receivedReason extensions.SessionStartReason
	if err := registry.Register("<inline:start>", func(api extensions.API) error {
		api.On(extensions.EventSessionStart, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			receivedReason = raw.(extensions.SessionStartEvent).Reason
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	if receivedReason != extensions.SessionStartStartup {
		t.Fatalf("expected SessionStartStartup default, got %q", receivedReason)
	}
}

func TestResourcesDiscoverReasonFollowsSessionStart(t *testing.T) {
	for _, test := range []struct {
		name  string
		start extensions.SessionStartReason
		want  extensions.ResourcesDiscoverReason
	}{
		{name: "reload", start: extensions.SessionStartReload, want: extensions.ResourcesDiscoverReload},
		{name: "resume maps to startup", start: extensions.SessionStartResume, want: extensions.ResourcesDiscoverStartup},
	} {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			manager, settings := extensionRuntimeDependencies(t, cwd)
			registry := extensions.NewRegistry(cwd)
			var received extensions.ResourcesDiscoverReason
			if err := registry.Register("<inline:discover>", func(api extensions.API) error {
				api.On(extensions.EventResourcesDiscover, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
					received = raw.(extensions.ResourcesDiscoverEvent).Reason
					return nil, nil
				})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			start := &extensions.SessionStartEvent{Reason: test.start}
			runtime, err := NewSessionRuntime(SessionRuntimeConfig{
				Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
				ExtensionRegistry: registry, SessionStartEvent: start,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Dispose()
			if received != test.want {
				t.Fatalf("resources_discover reason = %q, want %q", received, test.want)
			}
		})
	}
}

func TestSessionRuntimeReloadClearsHooksRemovedByFreshRegistry(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	enabled := true
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<reload-hooks>", func(api extensions.API) error {
		if !enabled {
			return nil
		}
		api.On(extensions.EventContext, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			return extensions.ContextResult{Messages: raw.(extensions.ContextEvent).Messages}, nil
		})
		api.On(extensions.EventToolCall, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, nil
		})
		api.On(extensions.EventToolResult, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, nil
		})
		api.On(extensions.EventBeforeProviderRequest, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, nil
		})
		api.On(extensions.EventBeforeProviderHeaders, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, nil
		})
		api.On(extensions.EventAfterProviderResponse, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tool := agent.AgentToolFunc{
		AgentToolSpec: agent.AgentToolSpec{Name: "noop", Parameters: jsonschema.Schema(`{"type":"object"}`)},
		Run: func(context.Context, string, any, agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			return agent.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "done"}}}, nil
		},
	}
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("noop", map[string]any{}, faux.ToolCallOptions{ID: "call-1"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("done"),
	})
	stream := func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		copy := *options
		if copy.OnPayload != nil {
			if _, _, err := copy.OnPayload(ctx, map[string]any{}, model); err != nil {
				return nil, err
			}
		}
		if copy.TransformHeaders != nil {
			if _, err := copy.TransformHeaders(ctx, ai.ProviderHeaders{}, model); err != nil {
				return nil, err
			}
		}
		if copy.OnResponse != nil {
			if err := copy.OnResponse(ctx, ai.ProviderResponse{Status: 200}, model); err != nil {
				return nil, err
			}
		}
		copy.OnPayload = nil
		copy.TransformHeaders = nil
		copy.OnResponse = nil
		return provider.StreamSimple(ctx, model, request, &copy)
	}
	created := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{Model: provider.GetModel(), Tools: []agent.AgentTool{tool}}),
		agent.WithStreamFn(stream), agent.WithConvertToLLM(ConvertToLLM),
	)
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, StreamFn: stream,
		ExtensionRegistry: registry, BaseTools: []agent.AgentTool{tool}, InitialActiveToolNames: []string{"noop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	enabled = false
	if err := runtime.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.PromptSync(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
}

func assistantText(message *ai.AssistantMessage) string {
	if message == nil {
		return ""
	}
	var result strings.Builder
	for _, block := range message.Content {
		if text, ok := block.(*ai.TextContent); ok {
			result.WriteString(text.Text)
		}
	}
	return result.String()
}
