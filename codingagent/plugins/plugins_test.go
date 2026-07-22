package plugins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

type widgetUI struct {
	extensions.NoopUI
	mu    sync.Mutex
	lines []string
}

type selectorUI struct {
	extensions.NoopUI
	choices []string
	index   int
}

func (ui *selectorUI) Select(_ context.Context, _ string, _ []string, _ *extensions.DialogOptions) (string, bool, error) {
	choice := ui.choices[ui.index]
	ui.index++
	return choice, true, nil
}

func TestPluginControlPersistsAndReloads(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(root)
	if err := registry.Register("<inline:plugin-control>", Control(settings)); err != nil {
		t.Fatal(err)
	}
	ui := &selectorUI{choices: []string{"[ ] tasks — " + Description("tasks"), "Done"}}
	reloads := 0
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI,
		CommandActions: &extensions.CommandActions{Reload: func(context.Context) error { reloads++; return nil }},
	})
	command := runner.Command("plugins")
	if command == nil {
		t.Fatal("/plugins missing")
	}
	if err := command.Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if !settings.GetPlugins()["tasks"] || reloads != 1 {
		t.Fatalf("tasks=%t reloads=%d", settings.GetPlugins()["tasks"], reloads)
	}
}

func TestPermissionsPolicyRules(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tests := []struct {
		name   string
		policy *Policy
		info   ToolCallInfo
		want   Action
	}{
		{
			name: "last match wins",
			policy: &Policy{Rules: []Rule{
				{Tool: "*", Action: Allow},
				{Tool: "bash", Action: Deny},
				{Tool: "bash", Command: "git status*", Action: Allow},
			}},
			info: ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "git status --short"}, CWD: root}, want: Allow,
		},
		{
			name:   "tool glob",
			policy: &Policy{Rules: []Rule{{Tool: "mcp_*", Action: Deny}}},
			info:   ToolCallInfo{Tool: "mcp_delete", Args: map[string]any{}, CWD: root}, want: Deny,
		},
		{
			name:   "command glob treats slash as command text",
			policy: &Policy{Rules: []Rule{{Tool: "bash", Command: "rm -rf *", Action: Deny}}},
			info:   ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "rm -rf /tmp/example"}, CWD: root}, want: Deny,
		},
		{
			name:   "raw path",
			policy: &Policy{Rules: []Rule{{Path: "link/*", Action: Deny}}},
			info:   ToolCallInfo{Tool: "custom", Args: map[string]any{"path": "link/file"}, CWD: root}, want: Deny,
		},
		{
			name:   "canonical path",
			policy: &Policy{Rules: []Rule{{Path: filepath.Join(realDir, "*"), Action: Deny}}},
			info:   ToolCallInfo{Tool: "custom", Args: map[string]any{"path": filepath.Join(link, "file")}, CWD: root}, want: Deny,
		},
		{
			name:   "unparseable bash is ask with restrictive rule",
			policy: &Policy{Rules: []Rule{{Tool: "bash", Command: "git push*", Action: Deny}}},
			info:   ToolCallInfo{Tool: "bash", Args: map[string]any{}, CWD: root}, want: Ask,
		},
		{
			name:   "unparseable bash is allow without restrictive rule",
			policy: &Policy{Rules: []Rule{{Tool: "bash", Action: Allow}}},
			info:   ToolCallInfo{Tool: "bash", Args: map[string]any{}, CWD: root}, want: Allow,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.policy.Evaluate(context.Background(), test.info).Action; got != test.want {
				t.Fatalf("action = %q, want %q", got, test.want)
			}
		})
	}

	called := false
	policy := &Policy{
		Authorizer: func(context.Context, ToolCallInfo) (Action, error) { called = true; return Deny, nil },
		Rules:      []Rule{{Tool: "*", Action: Allow}},
	}
	if got := policy.Evaluate(context.Background(), ToolCallInfo{Tool: "todo"}).Action; !called || got != Deny {
		t.Fatalf("authorizer called=%t action=%q", called, got)
	}
}

func TestPermissionsEnforceHidesAndBlocksStaticDeny(t *testing.T) {
	logSession := newPermissionsSession(t, faux.New(), &Policy{Rules: []Rule{{Tool: "bash", Action: Deny}}})
	if !containsName(logSession.GetActiveToolNames(), "bash") {
		t.Fatal("log mode hid bash")
	}
	conditionalSession := newPermissionsSession(t, faux.New(), &Policy{Mode: "enforce", Rules: []Rule{{Tool: "bash", Command: "rm -rf *", Action: Deny}}})
	if !containsName(conditionalSession.GetActiveToolNames(), "bash") {
		t.Fatal("command-scoped deny hid the whole tool")
	}

	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var returned string
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("bash", map[string]any{"command": "touch " + marker}, faux.ToolCallOptions{ID: "deny-1"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			returned = toolResultText(request, "bash")
			return faux.AssistantMessage("done"), nil
		}),
	})
	policy := &Policy{Mode: "enforce", Rules: []Rule{{Tool: "bash", Action: Deny}}}
	session := newPermissionsSession(t, provider, policy)
	if containsName(session.GetActiveToolNames(), "bash") {
		t.Fatal("bash remained visible after session_start")
	}
	active := append(session.GetActiveToolNames(), "bash")
	if err := session.SetActiveToolsByName(active); err != nil {
		t.Fatal(err)
	}
	if err := session.PromptSync(context.Background(), "try it"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(returned, `permissions: denied by rule 1 (tool="bash")`) {
		t.Fatalf("tool result = %q", returned)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied command changed the filesystem: %v", err)
	}
}

func TestPermissionsAskFallbackDeniesHeadless(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var returned string
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("todo", map[string]any{"items": []any{}}, faux.ToolCallOptions{ID: "ask-1"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			returned = toolResultText(request, "todo")
			return faux.AssistantMessage("done"), nil
		}),
	})
	policy := &Policy{Mode: "enforce", AskFallback: Deny, Rules: []Rule{{Tool: "todo", Action: Ask}}}
	session := newPermissionsSession(t, provider, policy, "tasks")
	if err := session.PromptSync(context.Background(), "update tasks"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(returned, "ask resolved by askFallback") {
		t.Fatalf("tool result = %q", returned)
	}
}

type approvalUI struct {
	extensions.NoopUI
	mu      sync.Mutex
	selects int
}

func (ui *approvalUI) Select(context.Context, string, []string, *extensions.DialogOptions) (string, bool, error) {
	ui.mu.Lock()
	ui.selects++
	ui.mu.Unlock()
	return "s approve for this session", true, nil
}

func (ui *approvalUI) count() int {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.selects
}

func TestPermissionsSessionApprovalAvoidsSecondPrompt(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	call := map[string]any{"items": []any{map[string]any{"text": "ship", "status": "pending"}}}
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("todo", call, faux.ToolCallOptions{ID: "ask-1"})),
		faux.AssistantMessage(faux.ToolCall("todo", call, faux.ToolCallOptions{ID: "ask-2"})),
		faux.AssistantMessage("done"),
	})
	policy := &Policy{Mode: "enforce", Rules: []Rule{{Tool: "todo", Action: Ask}}}
	session := newPermissionsSession(t, provider, policy, "tasks")
	ui := &approvalUI{}
	session.ExtensionRunner().SetUI(ui, extensions.ModeTUI)
	if err := session.PromptSync(context.Background(), "update twice"); err != nil {
		t.Fatal(err)
	}
	if got := ui.count(); got != 1 {
		t.Fatalf("permission prompts = %d, want 1", got)
	}
	logged := 0
	for _, entry := range session.Manager().GetEntries() {
		if entry.CustomType == "pigo.permissions.decision" {
			logged++
		}
	}
	if logged != 2 {
		t.Fatalf("decision log entries = %d, want 2", logged)
	}
}

func (ui *widgetUI) SetWidget(_ string, widget *extensions.Widget, _ *extensions.WidgetOptions) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.lines = nil
	if widget != nil {
		ui.lines = append([]string(nil), widget.Lines...)
	}
}

func (ui *widgetUI) snapshot() []string {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return append([]string(nil), ui.lines...)
}

func TestTasksToolReplacesTheLiveWidget(t *testing.T) {
	ui := &widgetUI{}
	tool := pluginTool(t, "tasks", "todo", Options{}, extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	result, err := tool.Execute(context.Background(), "todo-1", map[string]any{"items": []any{
		map[string]any{"text": "inspect", "status": "done"},
		map[string]any{"text": "implement", "status": "in_progress"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := ai.ContentText(result.Content)
	if text != "[x] inspect\n→ [ ] implement" {
		t.Fatalf("tool result = %q", text)
	}
	if got := strings.Join(ui.snapshot(), "\n"); got != text {
		t.Fatalf("widget = %q, want %q", got, text)
	}

	result, err = tool.Execute(context.Background(), "todo-2", map[string]any{"items": []any{
		map[string]any{"text": "ship", "status": "pending"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ai.ContentText(result.Content); got != "[ ] ship" || strings.Join(ui.snapshot(), "\n") != got {
		t.Fatalf("replacement result = %q widget = %q", got, strings.Join(ui.snapshot(), "\n"))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestWebSearchBackendsAndFetchContent(t *testing.T) {
	tests := []struct {
		name, env, endpoint, method, header, body, response, want string
	}{
		{name: "exa", env: "EXA_API_KEY", endpoint: "api.exa.ai/search", method: http.MethodPost, header: "x-api-key", body: `"query":"pigo"`, response: `{"results":[{"title":"Exa result","url":"https://exa.test","highlights":["match"]}]}`, want: "Exa result\nhttps://exa.test\nmatch"},
		{name: "brave", env: "BRAVE_API_KEY", endpoint: "api.search.brave.com/res/v1/web/search", method: http.MethodGet, header: "X-Subscription-Token", response: `{"web":{"results":[{"title":"Brave result","url":"https://brave.test","description":"match"}]}}`, want: "Brave result\nhttps://brave.test\nmatch"},
		{name: "tavily", env: "TAVILY_API_KEY", endpoint: "api.tavily.com/search", method: http.MethodPost, header: "Authorization", body: `"query":"pigo"`, response: `{"results":[{"title":"Tavily result","url":"https://tavily.test","content":"match"}]}`, want: "Tavily result\nhttps://tavily.test\nmatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
				t.Setenv(key, "")
			}
			t.Setenv(test.env, "secret")
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != test.method || !strings.Contains(request.URL.String(), test.endpoint) {
					t.Fatalf("request = %s %s", request.Method, request.URL)
				}
				if request.Header.Get(test.header) == "" {
					t.Fatalf("missing %s header", test.header)
				}
				if test.body != "" {
					body, _ := io.ReadAll(request.Body)
					if !strings.Contains(string(body), test.body) {
						t.Fatalf("body = %s", body)
					}
				}
				return response(http.StatusOK, "application/json", test.response), nil
			})}
			tool := pluginTool(t, "websearch", "web_search", Options{HTTPClient: client}, extensions.RunnerOptions{})
			result, err := tool.Execute(context.Background(), "search", map[string]any{"query": "pigo"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := ai.ContentText(result.Content); got != test.want {
				t.Fatalf("result = %q, want %q", got, test.want)
			}
		})
	}

	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(http.StatusOK, "text/html", `<html><style>no</style><body><h1>Hello &amp; hi</h1><script>no</script><p>Readable text.</p></body></html>`), nil
	})}
	fetch := pluginTool(t, "websearch", "fetch_content", Options{HTTPClient: client}, extensions.RunnerOptions{})
	result, err := fetch.Execute(context.Background(), "fetch", map[string]any{"url": "https://example.test/page"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ai.ContentText(result.Content); got != "Hello & hi Readable text." {
		t.Fatalf("content = %q", got)
	}
}

func TestWebSearchWithoutKeyReturnsActionableError(t *testing.T) {
	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", t.TempDir())
	tool := pluginTool(t, "websearch", "web_search", Options{}, extensions.RunnerOptions{})
	_, err := tool.Execute(context.Background(), "search", map[string]any{"query": "pigo"}, nil)
	if err == nil || !strings.Contains(err.Error(), "EXA_API_KEY") || !strings.Contains(err.Error(), "~/.pi/web-search.json") {
		t.Fatalf("error = %v", err)
	}
}

func TestWebSearchReadsPiWebSearchConfig(t *testing.T) {
	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".pi", "web-search.json"), []byte(`{"exaApiKey":"stored"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("x-api-key") != "stored" {
			t.Fatalf("api key = %q", request.Header.Get("x-api-key"))
		}
		return response(http.StatusOK, "application/json", `{"results":[]}`), nil
	})}
	tool := pluginTool(t, "websearch", "web_search", Options{HTTPClient: client}, extensions.RunnerOptions{})
	if _, err := tool.Execute(context.Background(), "search", map[string]any{"query": "pigo"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSubagentCompletesInProcessWithForkedContext(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var childSawParent bool
	var returned string
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"task": "answer", "agent": "scout", "context": "fork"}, faux.ToolCallOptions{ID: "sub-1"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			childSawParent = contextContains(request, "parent seed")
			return faux.AssistantMessage("child answer"), nil
		}),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			returned = toolResultText(request, "subagent")
			return faux.AssistantMessage("parent done"), nil
		}),
	})
	session := newSubagentParent(t, provider)
	if err := session.PromptSync(context.Background(), "parent seed"); err != nil {
		t.Fatal(err)
	}
	if !childSawParent || returned != "child answer" {
		t.Fatalf("childSawParent=%t tool result=%q", childSawParent, returned)
	}
}

func TestSubagentChildOptionsUseParentRegistryForDefaultStream(t *testing.T) {
	registry, err := config.NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	options, err := childOptions(registry, nil, codingagent.AgentSessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if options.ModelRegistry != registry || options.StreamFn != nil {
		t.Fatalf("model registry=%p want=%p stream set=%t", options.ModelRegistry, registry, options.StreamFn != nil)
	}
	if _, err := childOptions(nil, nil, codingagent.AgentSessionOptions{}); err == nil || !strings.Contains(err.Error(), "parent has no model registry") {
		t.Fatalf("missing registry error = %v", err)
	}
}

func TestSubagentParallelReturnsTwoChildResults(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var returned string
	childResponse := faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		return faux.AssistantMessage("child:" + lastUserText(request)), nil
	})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "parallel", "tasks": []any{
			map[string]any{"task": "alpha", "agent": "worker"},
			map[string]any{"task": "beta", "agent": "reviewer"},
		}}, faux.ToolCallOptions{ID: "sub-2"})),
		childResponse,
		childResponse,
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			returned = toolResultText(request, "subagent")
			return faux.AssistantMessage("parent done"), nil
		}),
	})
	session := newSubagentParent(t, provider)
	if err := session.PromptSync(context.Background(), "delegate"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(returned, "child:alpha") || !strings.Contains(returned, "child:beta") {
		t.Fatalf("parallel result = %q", returned)
	}
}

func TestSubagentSurfacesChildStreamError(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	providerError := "No API key for provider: anthropic"
	var returned string
	var isError bool
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"task": "inspect", "agent": "scout"}, faux.ToolCallOptions{ID: "sub-error"})),
		faux.AssistantMessage(ai.AssistantContent{}, faux.AssistantMessageOptions{StopReason: ai.StopReasonError, ErrorMessage: &providerError}),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			for index := len(request.Messages) - 1; index >= 0; index-- {
				if message, ok := request.Messages[index].(*ai.ToolResultMessage); ok && message.ToolName == "subagent" {
					returned, isError = ai.ContentText(message.Content), message.IsError
					break
				}
			}
			return faux.AssistantMessage("parent done"), nil
		}),
	})
	session := newSubagentParent(t, provider)
	if err := session.PromptSync(context.Background(), "delegate"); err != nil {
		t.Fatal(err)
	}
	if !isError || !strings.Contains(returned, "subagent: child failed: "+providerError) {
		t.Fatalf("tool error=%t result=%q", isError, returned)
	}
}

func TestSubagentInheritsPermissionsPolicy(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	childReadAbsent := false
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"task": "inspect", "agent": "scout"}, faux.ToolCallOptions{ID: "sub-policy"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			childReadAbsent = true
			if request.Tools != nil {
				for _, tool := range *request.Tools {
					if tool.Name == "read" {
						childReadAbsent = false
					}
				}
			}
			return faux.AssistantMessage("child obeyed"), nil
		}),
		faux.AssistantMessage("parent done"),
	})
	policy := &Policy{Mode: "enforce", Rules: []Rule{{Tool: "read", Action: Deny}}}
	session := newPermissionsSession(t, provider, policy, "subagents")
	if err := session.PromptSync(context.Background(), "delegate"); err != nil {
		t.Fatal(err)
	}
	if !childReadAbsent {
		t.Fatal("read was advertised to the child despite the inherited deny rule")
	}
}

func pluginTool(t *testing.T, plugin, tool string, options Options, runnerOptions extensions.RunnerOptions) agent.AgentTool {
	t.Helper()
	registry := extensions.NewRegistry(t.TempDir())
	factory := Catalog(options)[plugin]
	if factory == nil {
		t.Fatalf("plugin %q missing", plugin)
	}
	if err := registry.Register("<inline:"+plugin+">", factory); err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runnerOptions.SessionManager = manager
	runnerOptions.Actions.GetActiveTools = func() ([]string, error) { return []string{tool}, nil }
	runner := extensions.NewRunner(registry, runnerOptions)
	for _, registered := range runner.AllRegisteredTools() {
		if registered.Definition.Name == tool {
			return extensions.WrapRegisteredTool(registered, runner)
		}
	}
	t.Fatalf("tool %q missing", tool)
	return nil
}

func newPermissionsSession(t *testing.T, provider *faux.Provider, policy *Policy, enabled ...string) *codingagent.AgentSession {
	t.Helper()
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(root)
	catalog := Catalog(Options{StreamFn: provider.StreamSimple, Policy: policy})
	for _, name := range append([]string{"permissions"}, enabled...) {
		if err := registry.Register("<inline:"+name+">", catalog[name]); err != nil {
			t.Fatal(err)
		}
	}
	prompt := "permissions test"
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD: root, AgentDir: filepath.Join(root, "agent"), Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &codingagent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(result.Session.Dispose)
	return result.Session
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func response(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func newSubagentParent(t *testing.T, provider *faux.Provider) *codingagent.AgentSession {
	t.Helper()
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(root+"/agent"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(root)
	if err := registry.Register("<inline:subagents>", Catalog(Options{StreamFn: provider.StreamSimple})["subagents"]); err != nil {
		t.Fatal(err)
	}
	prompt := "parent"
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD: root, AgentDir: root + "/agent", Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &codingagent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(result.Session.Dispose)
	return result.Session
}

func contextContains(request ai.Context, needle string) bool {
	encoded, _ := json.Marshal(request.Messages)
	return strings.Contains(string(encoded), needle)
}

func lastUserText(request ai.Context) string {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if message, ok := request.Messages[index].(*ai.UserMessage); ok {
			return ai.ContentText(message.Content.Blocks)
		}
	}
	return ""
}

func toolResultText(request ai.Context, name string) string {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if message, ok := request.Messages[index].(*ai.ToolResultMessage); ok && message.ToolName == name {
			return ai.ContentText(message.Content)
		}
	}
	return ""
}
