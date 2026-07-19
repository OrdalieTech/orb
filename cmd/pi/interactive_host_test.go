package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/modes"
	"github.com/OrdalieTech/pi-go/codingagent/session"
)

type recordedLifecycleEvent struct {
	kind     string
	reason   string
	hasUI    bool
	previous string
	target   string
}

type hostLifecycleRecorder struct {
	events          []recordedLifecycleEvent
	trace           []string
	discoveredTheme string
}

func (recorder *hostLifecycleRecorder) registry(cwd string) *extensions.Registry {
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<inline:host>", func(api extensions.API) error {
		api.RegisterCommand("host-cmd", extensions.Command{Description: "Host lane command"})
		api.RegisterTool(extensions.ToolDefinition{Name: "host-tool", Description: "Host lane tool"})
		api.On(extensions.EventSessionStart, func(_ context.Context, event extensions.Event, extensionContext extensions.Context) (any, error) {
			start := event.(extensions.SessionStartEvent)
			recorder.trace = append(recorder.trace, "start:"+string(start.Reason))
			previous := ""
			if start.PreviousSessionFile != nil {
				previous = *start.PreviousSessionFile
			}
			recorder.events = append(recorder.events, recordedLifecycleEvent{
				kind: "session_start", reason: string(start.Reason), hasUI: extensionContext.HasUI(), previous: previous,
			})
			return nil, nil
		})
		api.On(extensions.EventSessionShutdown, func(_ context.Context, event extensions.Event, _ extensions.Context) (any, error) {
			shutdown := event.(extensions.SessionShutdownEvent)
			recorder.trace = append(recorder.trace, "shutdown:"+string(shutdown.Reason))
			target := ""
			if shutdown.TargetSessionFile != nil {
				target = *shutdown.TargetSessionFile
			}
			recorder.events = append(recorder.events, recordedLifecycleEvent{
				kind: "session_shutdown", reason: string(shutdown.Reason), target: target,
			})
			return nil, nil
		})
		if recorder.discoveredTheme != "" {
			api.On(extensions.EventResourcesDiscover, func(_ context.Context, event extensions.Event, _ extensions.Context) (any, error) {
				discover := event.(extensions.ResourcesDiscoverEvent)
				recorder.trace = append(recorder.trace, "discover:"+string(discover.Reason))
				return extensions.ResourcesDiscoverResult{ThemePaths: []string{recorder.discoveredTheme}}, nil
			})
		}
		return nil
	}); err != nil {
		panic(err)
	}
	return registry
}

func (recorder *hostLifecycleRecorder) byKind(kind string) []recordedLifecycleEvent {
	result := make([]recordedLifecycleEvent, 0, len(recorder.events))
	for _, event := range recorder.events {
		if event.kind == kind {
			result = append(result, event)
		}
	}
	return result
}

type attachedTestUI struct{ extensions.UI }

func fauxHostModel(id string) *ai.Model {
	return &ai.Model{ID: id, Provider: "faux", API: "faux", Reasoning: true, ContextWindow: 100_000, MaxTokens: 1000}
}

func newHostAgent(messages agent.AgentMessages) *agent.Agent {
	return agent.NewAgent(agent.WithInitialState(agent.AgentState{Model: fauxHostModel("host-model"), Messages: messages}))
}

type hostFixture struct {
	root        string
	agentDir    string
	settings    *config.SettingsManager
	recorder    *hostLifecycleRecorder
	createCalls int
	failCreate  bool
	host        *interactiveSessionHost
}

func newHostFixture(t *testing.T) *hostFixture {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &hostFixture{root: root, agentDir: agentDir, settings: settings, recorder: &hostLifecycleRecorder{}}
	dependencies := cliDependencies{createRuntime: fixture.createRuntime}
	manager, err := session.Create(root, filepath.Join(root, "sessions"), session.WithSessionID("initial"))
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := fixture.createRuntime(root, CLIArgs{}, agent.AgentMessages{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := buildSessionRuntime(inputs, manager, sessionRuntimeOptions{mode: extensions.ModeTUI})
	if err != nil {
		t.Fatal(err)
	}
	fixture.host = newInteractiveSessionHost(CLIArgs{}, dependencies, runtime, inputs, agentDir, nil)
	fixture.recorder.trace = nil
	t.Cleanup(fixture.host.Dispose)
	return fixture
}

func (fixture *hostFixture) createRuntime(cwd string, args CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
	fixture.createCalls++
	fixture.recorder.trace = append(fixture.recorder.trace, "create")
	if fixture.failCreate {
		return runtimeInputs{}, errors.New("runtime creation failed")
	}
	created := newHostAgent(prior)
	if args.Model != nil {
		provider := "faux"
		if args.Provider != nil {
			provider = *args.Provider
		}
		model := *fauxHostModel(*args.Model)
		model.Provider = ai.ProviderID(provider)
		thinking := ai.ModelThinkingMedium
		if args.Thinking != nil {
			thinking = ai.ModelThinkingLevel(*args.Thinking)
		}
		created = agent.NewAgent(agent.WithInitialState(agent.AgentState{Model: &model, ThinkingLevel: thinking, Messages: prior}))
	}
	return runtimeInputs{
		Agent:           created,
		Settings:        fixture.settings,
		Extensions:      fixture.recorder.registry(cwd),
		SlashResolver:   &codingagent.SlashResolver{},
		ActiveToolNames: []string{"host-tool"},
	}, nil
}

// appendConversation persists the session file: managers flush only once an
// assistant message exists.
func appendConversation(t *testing.T, manager *session.SessionManager, userText string) string {
	t.Helper()
	entryID, err := manager.AppendMessage(map[string]any{"role": "user", "content": userText, "timestamp": 1})
	if err != nil {
		t.Fatal(err)
	}
	assistant := map[string]any{"role": "assistant", "content": []any{}, "provider": "faux", "model": "host-model", "timestamp": 2}
	if context := manager.BuildSessionContext(); context.Model != nil {
		assistant["provider"] = context.Model.Provider
		assistant["model"] = context.Model.ModelID
	}
	if _, err := manager.AppendMessage(assistant); err != nil {
		t.Fatal(err)
	}
	return entryID
}

func TestSessionRuntimeConfigPreservesRuntimeInputs(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	modelRegistry, err := config.NewModelRegistry(filepath.Join(root, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{"read"}
	apiKey := "test-key"
	streamCalled := false
	stream := func(_ context.Context, model *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		streamCalled = true
		message := &ai.AssistantMessage{
			Content:    ai.AssistantContent{&ai.TextContent{Text: "ok"}},
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			StopReason: ai.StopReasonStop,
			Timestamp:  1,
		}
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: message}, nil)
		}, nil
	}
	inputs := runtimeInputs{
		Agent:           newHostAgent(nil),
		Settings:        settings,
		StreamFn:        stream,
		ModelRegistry:   modelRegistry,
		AvailableModels: func() []ai.Model { return nil },
		GetAPIKey:       func(context.Context, ai.ProviderID) (*string, error) { return &apiKey, nil },
		GetRequestAuth: func(context.Context, ai.ProviderID) (*agent.RequestAuth, error) {
			return &agent.RequestAuth{APIKey: &apiKey}, nil
		},
		GetModelHeaders: func(context.Context, *ai.Model, *string, ai.ProviderEnv) (*map[string]string, error) { return nil, nil },
		SlashResolver:   &codingagent.SlashResolver{},
		Extensions:      extensions.NewRegistry(root),
		BaseTools:       []agent.AgentTool{},
		ActiveToolNames: []string{"read"},
		AllowedTools:    &allowed,
		ExcludedTools:   []string{"bash"},
		PromptOptions:   codingagent.SystemPromptOptions{CWD: root},
	}
	start := &extensions.SessionStartEvent{Reason: extensions.SessionStartResume}
	runtimeConfig, err := sessionRuntimeConfig(inputs, manager, sessionRuntimeOptions{
		mode: extensions.ModeTUI, sessionStart: start, deferSessionStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	samePointer := func(name string, left, right any) {
		t.Helper()
		if reflect.ValueOf(left).Pointer() != reflect.ValueOf(right).Pointer() {
			t.Fatalf("%s not preserved by session runtime config", name)
		}
	}
	samePointer("GetRequestAuth", runtimeConfig.GetRequestAuth, inputs.GetRequestAuth)
	samePointer("GetModelHeaders", runtimeConfig.GetModelHeaders, inputs.GetModelHeaders)
	samePointer("GetAPIKey", runtimeConfig.GetAPIKey, inputs.GetAPIKey)
	samePointer("AvailableModels", runtimeConfig.AvailableModels, inputs.AvailableModels)
	samePointer("StreamFn", runtimeConfig.StreamFn, inputs.StreamFn)
	if runtimeConfig.ExtensionRegistry != inputs.Extensions || runtimeConfig.SlashResolver != inputs.SlashResolver {
		t.Fatal("extension registry or slash resolver not preserved")
	}
	if runtimeConfig.AllowedToolNames != inputs.AllowedTools || !reflect.DeepEqual(runtimeConfig.ExcludedToolNames, inputs.ExcludedTools) {
		t.Fatal("tool policy not preserved")
	}
	if !reflect.DeepEqual(runtimeConfig.InitialActiveToolNames, inputs.ActiveToolNames) {
		t.Fatal("active tool names not preserved")
	}
	if runtimeConfig.SystemPromptOptions == nil || runtimeConfig.SystemPromptOptions.CWD != root {
		t.Fatal("prompt options not preserved")
	}
	if runtimeConfig.SessionStart != start || !runtimeConfig.DeferSessionStart {
		t.Fatal("deferred session start configuration not preserved")
	}
	if runtimeConfig.ExtensionMode != extensions.ModeTUI {
		t.Fatalf("extension mode = %q", runtimeConfig.ExtensionMode)
	}
	runtime, err := codingagent.NewSessionRuntime(runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	if err := runtime.Prompt(context.Background(), "exercise final runtime"); err != nil {
		t.Fatal(err)
	}
	if !streamCalled {
		t.Fatal("final session runtime replaced the settings-aware stream function")
	}
}

func TestInteractiveHostNewSessionRebindsBeforeSessionStart(t *testing.T) {
	fixture := newHostFixture(t)
	host := fixture.host
	original := host.Session()

	var rebound []*codingagent.SessionRuntime
	host.SetRebindSession(func(replacement *codingagent.SessionRuntime) error {
		fixture.recorder.trace = append(fixture.recorder.trace, "rebind")
		rebound = append(rebound, replacement)
		replacement.ExtensionRunner().SetUI(attachedTestUI{extensions.NewNoopUI()}, extensions.ModeTUI)
		return nil
	})
	invalidated := 0
	host.SetBeforeSessionInvalidate(func() {
		fixture.recorder.trace = append(fixture.recorder.trace, "invalidate")
		invalidated++
	})

	result, err := host.NewSession(context.Background(), &extensions.NewSessionOptions{ParentSession: "parent.jsonl"})
	if err != nil || result.Cancelled {
		t.Fatalf("new session = %+v, %v", result, err)
	}
	replacement := host.Session()
	if replacement == original || replacement == nil {
		t.Fatal("session was not replaced")
	}
	if len(rebound) != 1 || rebound[0] != replacement || invalidated != 1 {
		t.Fatalf("rebind calls = %d, invalidate calls = %d", len(rebound), invalidated)
	}
	wantTrace := []string{"shutdown:new", "invalidate", "create", "rebind", "start:new"}
	if !reflect.DeepEqual(fixture.recorder.trace, wantTrace) {
		t.Fatalf("replacement lifecycle = %#v, want %#v", fixture.recorder.trace, wantTrace)
	}
	header := replacement.Manager().GetHeader()
	if header == nil || header.ParentSession == nil || *header.ParentSession != "parent.jsonl" {
		t.Fatalf("replacement header = %#v", header)
	}

	starts := fixture.recorder.byKind("session_start")
	if len(starts) != 2 {
		t.Fatalf("session_start events = %#v", starts)
	}
	if starts[0].reason != "startup" || starts[1].reason != "new" {
		t.Fatalf("session_start reasons = %#v", starts)
	}
	if !starts[1].hasUI {
		t.Fatal("replacement session_start fired before the TUI attached UI")
	}
	if starts[1].previous == "" || !strings.HasSuffix(starts[1].previous, ".jsonl") {
		t.Fatalf("session_start previousSessionFile = %q", starts[1].previous)
	}
	shutdowns := fixture.recorder.byKind("session_shutdown")
	if len(shutdowns) != 1 || shutdowns[0].reason != "new" {
		t.Fatalf("session_shutdown events = %#v", shutdowns)
	}
	// Extension surface survives the replacement.
	commands := replacement.Commands()
	found := false
	for _, command := range commands {
		if command.Name == "host-cmd" {
			found = true
		}
	}
	if !found {
		t.Fatalf("replacement lost extension command: %#v", commands)
	}
	if replacement.ExtensionRunner().ToolDefinition("host-tool") == nil {
		t.Fatal("replacement lost extension tool definition")
	}
	active, err := replacement.ExtensionRunner().ActiveTools()
	if err != nil || !reflect.DeepEqual(active, []string{"host-tool"}) {
		t.Fatalf("active tools = %#v, %v", active, err)
	}
}

func TestInteractiveHostRunsAfterSessionStartHookAfterResourceDiscovery(t *testing.T) {
	fixture := newHostFixture(t)
	fixture.recorder.discoveredTheme = filepath.Join(fixture.root, "replacement-theme.json")
	fixture.host.SetRebindSession(func(*codingagent.SessionRuntime) error {
		fixture.recorder.trace = append(fixture.recorder.trace, "rebind")
		return nil
	})
	fixture.host.SetAfterSessionStart(func(replacement *codingagent.SessionRuntime) error {
		fixture.recorder.trace = append(fixture.recorder.trace, "after-start")
		resources := replacement.ExtensionResources()
		if len(resources.ThemePaths) != 1 || resources.ThemePaths[0].Path != fixture.recorder.discoveredTheme {
			t.Fatalf("post-start resources = %#v", resources.ThemePaths)
		}
		return nil
	})

	result, err := fixture.host.NewSession(context.Background(), nil)
	if err != nil || result.Cancelled {
		t.Fatalf("new session = %+v, %v", result, err)
	}
	want := []string{"shutdown:new", "create", "rebind", "start:new", "discover:startup", "after-start"}
	if !reflect.DeepEqual(fixture.recorder.trace, want) {
		t.Fatalf("replacement lifecycle = %#v, want %#v", fixture.recorder.trace, want)
	}
}

func TestInteractiveHostCreationFailureHappensAfterCurrentTeardown(t *testing.T) {
	fixture := newHostFixture(t)
	host := fixture.host
	original := host.Session()
	host.SetBeforeSessionInvalidate(func() {
		fixture.recorder.trace = append(fixture.recorder.trace, "invalidate")
	})

	fixture.failCreate = true
	_, err := host.NewSession(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "runtime creation failed") {
		t.Fatalf("expected creation failure, got %v", err)
	}
	if host.Session() != original {
		t.Fatal("creation failure changed the host's disposed runtime reference")
	}
	shutdowns := fixture.recorder.byKind("session_shutdown")
	if len(shutdowns) != 1 || shutdowns[0].reason != "new" || !strings.HasSuffix(shutdowns[0].target, ".jsonl") {
		t.Fatalf("creation failure shutdown events = %#v", shutdowns)
	}
	wantTrace := []string{"shutdown:new", "invalidate", "create"}
	if !reflect.DeepEqual(fixture.recorder.trace, wantTrace) {
		t.Fatalf("creation-failure lifecycle = %#v, want %#v", fixture.recorder.trace, wantTrace)
	}
	if starts := fixture.recorder.byKind("session_start"); len(starts) != 1 || starts[0].reason != "startup" {
		t.Fatalf("creation failure session_start events = %#v", starts)
	}
}

func TestInteractiveHostRebindFailureLeavesReplacementCurrent(t *testing.T) {
	fixture := newHostFixture(t)
	host := fixture.host
	original := host.Session()
	var replacement *codingagent.SessionRuntime
	rebindCalls := 0
	invalidated := 0
	host.SetBeforeSessionInvalidate(func() {
		fixture.recorder.trace = append(fixture.recorder.trace, "invalidate")
		invalidated++
	})
	host.SetRebindSession(func(runtime *codingagent.SessionRuntime) error {
		fixture.recorder.trace = append(fixture.recorder.trace, "rebind")
		rebindCalls++
		if runtime != original {
			replacement = runtime
			return errors.New("rebind failed")
		}
		return nil
	})

	_, err := host.NewSession(context.Background(), nil)
	if err == nil || err.Error() != "rebind failed" {
		t.Fatalf("replacement error = %v", err)
	}
	if replacement == nil || host.Session() != replacement || replacement == original {
		t.Fatal("rebind failure did not leave the replacement runtime current")
	}
	if rebindCalls != 1 || invalidated != 1 {
		t.Fatalf("rebind calls = %d, invalidate calls = %d", rebindCalls, invalidated)
	}
	shutdowns := fixture.recorder.byKind("session_shutdown")
	if len(shutdowns) != 1 || shutdowns[0].reason != "new" {
		t.Fatalf("rebind failure shutdown events = %#v", shutdowns)
	}
	wantTrace := []string{"shutdown:new", "invalidate", "create", "rebind"}
	if !reflect.DeepEqual(fixture.recorder.trace, wantTrace) {
		t.Fatalf("rebind-failure lifecycle = %#v, want %#v", fixture.recorder.trace, wantTrace)
	}
	if starts := fixture.recorder.byKind("session_start"); len(starts) != 1 || starts[0].reason != "startup" {
		t.Fatalf("rebind failure session_start events = %#v", starts)
	}
}

func TestInteractiveHostSwitchSessionRestoresModelAndRollsBackMissingCwd(t *testing.T) {
	fixture := newHostFixture(t)
	host := fixture.host

	sessionDir := filepath.Join(fixture.root, "sessions")
	target, err := session.Create(fixture.root, sessionDir, session.WithSessionID("target"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.AppendModelChange("provider-b", "model-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.AppendThinkingLevelChange("high"); err != nil {
		t.Fatal(err)
	}
	appendConversation(t, target, "hi")
	result, err := host.SwitchSession(context.Background(), target.GetSessionFile(), "", nil)
	if err != nil || result.Cancelled {
		t.Fatalf("switch = %+v, %v", result, err)
	}
	state := host.Session().State()
	if state.Model == nil || state.Model.ID != "model-b" || string(state.Model.Provider) != "provider-b" || state.ThinkingLevel != "high" {
		t.Fatalf("restored state = %#v", state)
	}
	starts := fixture.recorder.byKind("session_start")
	if starts[len(starts)-1].reason != "resume" {
		t.Fatalf("session_start reasons = %#v", starts)
	}
	shutdowns := fixture.recorder.byKind("session_shutdown")
	if shutdowns[len(shutdowns)-1].reason != "resume" || shutdowns[len(shutdowns)-1].target != target.GetSessionFile() {
		t.Fatalf("switch session_shutdown events = %#v", shutdowns)
	}

	// A session whose stored cwd no longer exists fails with upstream text and
	// keeps the current runtime; a cwd override recovers.
	missingRoot := filepath.Join(fixture.root, "gone")
	if err := os.MkdirAll(missingRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	missing, err := session.Create(missingRoot, sessionDir, session.WithSessionID("missing"))
	if err != nil {
		t.Fatal(err)
	}
	appendConversation(t, missing, "orphaned")
	missingFile := missing.GetSessionFile()
	if err := os.RemoveAll(missingRoot); err != nil {
		t.Fatal(err)
	}
	current := host.Session()
	_, err = host.SwitchSession(context.Background(), missingFile, "", nil)
	var missingErr *modes.MissingSessionCwdError
	if !errors.As(err, &missingErr) {
		t.Fatalf("expected MissingSessionCwdError, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "Stored session working directory does not exist: ") || !strings.Contains(err.Error(), "Current working directory: ") {
		t.Fatalf("error text = %q", err.Error())
	}
	if host.Session() != current {
		t.Fatal("failed switch replaced the runtime")
	}
	if result, err := host.SwitchSession(context.Background(), missingFile, fixture.root, nil); err != nil || result.Cancelled {
		t.Fatalf("switch with cwd override = %+v, %v", result, err)
	}
}

func TestInteractiveHostForkCloneAndTreeSemantics(t *testing.T) {
	fixture := newHostFixture(t)
	host := fixture.host
	manager := host.Session().Manager()
	userEntry := appendConversation(t, manager, "draft prompt")

	if _, err := host.Fork(context.Background(), "nope", nil); err == nil || err.Error() != "Invalid entry ID for forking" {
		t.Fatalf("invalid fork error = %v", err)
	}

	previousFile := manager.GetSessionFile()
	result, err := host.Fork(context.Background(), userEntry, nil)
	if err != nil || result.Cancelled || result.SelectedText != "draft prompt" {
		t.Fatalf("fork = %+v, %v", result, err)
	}
	forked := host.Session().Manager()
	if forked.GetSessionFile() == previousFile {
		t.Fatal("fork did not branch into a new session file")
	}
	for _, entry := range forked.GetBranch() {
		if entry.Type == "message" {
			t.Fatalf("fork-before branch retained message entries: %#v", forked.GetBranch())
		}
	}
	starts := fixture.recorder.byKind("session_start")
	if starts[len(starts)-1].reason != "fork" {
		t.Fatalf("session_start reasons = %#v", starts)
	}
	shutdowns := fixture.recorder.byKind("session_shutdown")
	if shutdowns[len(shutdowns)-1].reason != "fork" || shutdowns[len(shutdowns)-1].target != forked.GetSessionFile() {
		t.Fatalf("fork session_shutdown events = %#v", shutdowns)
	}

	// Clone: fork of the current leaf with position "at" keeps the branch.
	cloneSource := host.Session().Manager()
	appendConversation(t, cloneSource, "clone me")
	leaf := cloneSource.GetLeafID()
	if leaf == nil {
		t.Fatal("missing leaf id")
	}
	cloneFile := cloneSource.GetSessionFile()
	result, err = host.Fork(context.Background(), *leaf, &extensions.ForkOptions{Position: extensions.ForkAt})
	if err != nil || result.Cancelled || result.SelectedText != "" {
		t.Fatalf("clone = %+v, %v", result, err)
	}
	cloned := host.Session().Manager()
	if cloned.GetSessionFile() == cloneFile {
		t.Fatal("clone did not create a new session file")
	}
	kept := false
	for _, entry := range cloned.GetBranch() {
		if entry.Type == "message" {
			kept = true
		}
	}
	if !kept {
		t.Fatal("clone dropped the branch messages")
	}

	// Tree navigation stays runtime-owned through the merged command actions.
	runner := host.Session().ExtensionRunner()
	commandContext := runner.CreateCommandContext()
	entries := cloned.GetBranch()
	if _, err := commandContext.NavigateTree(context.Background(), entries[0].ID, nil); err != nil {
		t.Fatalf("navigate tree via command context: %v", err)
	}
}

func TestInteractiveHostImportSession(t *testing.T) {
	fixture := newHostFixture(t)
	host := fixture.host

	_, err := host.ImportSession(context.Background(), filepath.Join(fixture.root, "absent.jsonl"), "")
	var notFound *modes.SessionImportFileNotFoundError
	if !errors.As(err, &notFound) || !strings.HasPrefix(err.Error(), "File not found: ") {
		t.Fatalf("missing import error = %v", err)
	}

	external := filepath.Join(fixture.root, "external")
	source, err := session.Create(fixture.root, external, session.WithSessionID("imported"))
	if err != nil {
		t.Fatal(err)
	}
	appendConversation(t, source, "imported prompt")
	result, err := host.ImportSession(context.Background(), source.GetSessionFile(), "")
	if err != nil || result.Cancelled {
		t.Fatalf("import = %+v, %v", result, err)
	}
	imported := host.Session().Manager()
	expected := filepath.Join(fixture.root, "sessions", filepath.Base(source.GetSessionFile()))
	if imported.GetSessionFile() != expected {
		t.Fatalf("imported session file = %q, want %q", imported.GetSessionFile(), expected)
	}
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("imported copy missing: %v", err)
	}
	foundMessage := false
	for _, entry := range imported.GetEntries() {
		if entry.Type == "message" {
			foundMessage = true
		}
	}
	if !foundMessage {
		t.Fatal("imported session lost its entries")
	}
	starts := fixture.recorder.byKind("session_start")
	if starts[len(starts)-1].reason != "resume" {
		t.Fatalf("import session_start reasons = %#v", starts)
	}
	shutdowns := fixture.recorder.byKind("session_shutdown")
	if shutdowns[len(shutdowns)-1].reason != "resume" || shutdowns[len(shutdowns)-1].target != expected {
		t.Fatalf("import session_shutdown events = %#v", shutdowns)
	}
}

func TestInteractiveHostReloadRerunsCreationOnSameSession(t *testing.T) {
	fixture := newHostFixture(t)
	host := fixture.host
	before := host.Session()
	beforeFile := before.Manager().GetSessionFile()
	calls := fixture.createCalls

	if err := host.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := host.Session()
	if after == before {
		t.Fatal("reload did not rebuild the runtime")
	}
	if after.Manager().GetSessionFile() != beforeFile {
		t.Fatal("reload replaced the session file")
	}
	if fixture.createCalls != calls+1 {
		t.Fatalf("reload create calls = %d, want %d", fixture.createCalls, calls+1)
	}
	starts := fixture.recorder.byKind("session_start")
	if starts[len(starts)-1].reason != "reload" {
		t.Fatalf("session_start reasons = %#v", starts)
	}
	shutdowns := fixture.recorder.byKind("session_shutdown")
	if shutdowns[len(shutdowns)-1].reason != "reload" || shutdowns[len(shutdowns)-1].target != "" {
		t.Fatalf("reload session_shutdown events = %#v", shutdowns)
	}
}

func TestInteractiveHostTrustPersistsAndRebuilds(t *testing.T) {
	fixture := newHostFixture(t)
	host := fixture.host

	state, err := host.TrustState()
	if err != nil {
		t.Fatal(err)
	}
	if state.CWD == "" || len(state.Options) < 2 || state.SavedDecision != nil {
		t.Fatalf("trust state = %#v", state)
	}
	var updates []config.ProjectTrustUpdate
	for _, option := range state.Options {
		if option.Trusted && option.SavedPath != "" {
			updates = option.Updates
			break
		}
	}
	calls := fixture.createCalls
	if err := host.SetProjectTrust(context.Background(), updates); err != nil {
		t.Fatal(err)
	}
	decision, err := config.NewProjectTrustStore(fixture.agentDir).Get(state.CWD)
	if err != nil || decision == nil || !*decision {
		t.Fatalf("persisted trust decision = %v, %v", decision, err)
	}
	if fixture.createCalls != calls+1 {
		t.Fatal("trust change did not rebuild the runtime")
	}
	state, err = host.TrustState()
	if err != nil || state.SavedDecision == nil || !state.SavedDecision.Decision {
		t.Fatalf("trust state after save = %#v, %v", state, err)
	}
}

func TestInteractiveHostAuthOptionsAndLogout(t *testing.T) {
	fixture := newHostFixture(t)
	host := fixture.host

	storage, err := config.NewAuthStorage(filepath.Join(fixture.agentDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.host.inputs.Auth = storage
	if _, err := storage.Modify(context.Background(), "anthropic", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return aiauth.APIKeyCredential("sk-test"), nil
	}); err != nil {
		t.Fatal(err)
	}

	options, err := host.AuthOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	loginIDs := make(map[string]bool, len(options.Login))
	for _, provider := range options.Login {
		loginIDs[provider.ID] = true
	}
	for _, required := range []string{"anthropic", "openai-codex", "github-copilot", "xai"} {
		if !loginIDs[required] {
			t.Fatalf("login options missing OAuth provider %q: %#v", required, options.Login)
		}
	}
	if len(options.Logout) != 1 || options.Logout[0].ID != "anthropic" {
		t.Fatalf("logout options = %#v", options.Logout)
	}
	for _, provider := range options.Login {
		if provider.ID == "anthropic" && !provider.Configured {
			t.Fatal("anthropic should report configured auth")
		}
	}

	if err := host.Logout(context.Background(), "anthropic"); err != nil {
		t.Fatal(err)
	}
	stored, err := storage.List(context.Background())
	if err != nil || len(stored) != 0 {
		t.Fatalf("credentials after logout = %#v, %v", stored, err)
	}
	content, err := os.ReadFile(storage.Path())
	if err != nil || strings.Contains(string(content), "sk-test") {
		t.Fatalf("auth.json still holds the credential: %s, %v", content, err)
	}

	if err := host.Login(context.Background(), "groq", aiauth.AuthTypeAPIKey, fixedPromptInteraction{value: "groq-key"}); err != nil {
		t.Fatalf("api-key-only provider login = %v", err)
	}
}

func TestInteractiveHostBindsExtensionCommandActions(t *testing.T) {
	fixture := newHostFixture(t)
	host := fixture.host
	original := host.Session()

	runner := original.ExtensionRunner()
	commandContext := runner.CreateCommandContext()
	withSessionID := ""
	result, err := commandContext.NewSession(context.Background(), &extensions.NewSessionOptions{
		Setup: func(manager *session.SessionManager) error {
			_, appendErr := manager.AppendMessage(map[string]any{"role": "user", "content": "seeded", "timestamp": 1})
			return appendErr
		},
		WithSession: func(_ context.Context, replaced extensions.ReplacedSessionContext) error {
			withSessionID = replaced.SessionManager().GetSessionID()
			return nil
		},
	})
	if err != nil || result.Cancelled {
		t.Fatalf("command-context new session = %+v, %v", result, err)
	}
	replacement := host.Session()
	if replacement == original {
		t.Fatal("command context did not run the host replacement path")
	}
	messages := replacement.State().Messages
	if len(messages) != 1 {
		t.Fatalf("setup-seeded messages = %#v", messages)
	}
	if withSessionID != replacement.Manager().GetSessionID() {
		t.Fatalf("withSession context session = %q, want %q", withSessionID, replacement.Manager().GetSessionID())
	}
}

func TestRPCSessionHostReplacementKeepsExtensionsAndSurvivesFailure(t *testing.T) {
	fixture := newHostFixture(t)
	manager, err := session.InMemory(fixture.root, session.WithSessionID("rpc"))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newCLISessionRuntimeHost(context.Background(), cliSessionRuntimeHostOptions{
		BaseArgs: CLIArgs{}, Manager: manager,
		Dependencies:  cliDependencies{createRuntime: fixture.createRuntime},
		ExtensionMode: extensions.ModeRPC,
	})
	if err != nil {
		t.Fatal(err)
	}
	rpcHost, err := newRPCSessionHost(context.Background(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer rpcHost.Dispose()

	original := rpcHost.Session()
	fixture.failCreate = true
	if _, err := rpcHost.NewSession(""); err == nil {
		t.Fatal("expected replacement failure")
	}
	if rpcHost.Session() != original {
		t.Fatal("failed RPC replacement swapped the runtime")
	}
	fixture.failCreate = false

	cancelled, err := rpcHost.NewSession("")
	if err != nil || cancelled {
		t.Fatalf("rpc new session = %v, %v", cancelled, err)
	}
	replacement := rpcHost.Session()
	if replacement == original {
		t.Fatal("rpc session was not replaced")
	}
	if replacement.ExtensionRunner() == nil || replacement.ExtensionRunner().ToolDefinition("host-tool") == nil {
		t.Fatal("rpc replacement lost the extension registry")
	}
	found := false
	for _, command := range rpcSlashCommands(replacement) {
		if command.Name == "host-cmd" {
			found = true
		}
	}
	if !found {
		t.Fatal("rpc replacement lost extension commands")
	}
	shutdowns := fixture.recorder.byKind("session_shutdown")
	if len(shutdowns) == 0 || shutdowns[len(shutdowns)-1].reason != "new" {
		t.Fatalf("rpc shutdown events = %#v", shutdowns)
	}
}
