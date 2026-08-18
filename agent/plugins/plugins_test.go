package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/memory"
	"github.com/OrdalieTech/orb/sandbox"
	"github.com/OrdalieTech/orb/tui"
)

type widgetUI struct {
	extensions.NoopUI
	mu      sync.Mutex
	lines   []string
	factory extensions.ComponentFactory
	shown   int
}

type taskWidgetHost struct{ invalidations int }
type dimTaskTheme struct{}
type countingTaskTheme struct{ calls int }
type blockingTaskTheme struct {
	calls   int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (theme *countingTaskTheme) FG(_ string, text string) string { theme.calls++; return text }

func (theme *blockingTaskTheme) FG(_ string, text string) string {
	theme.calls++
	theme.once.Do(func() {
		close(theme.started)
		<-theme.release
	})
	return text
}

func (dimTaskTheme) FG(color, text string) string {
	if color == "dim" {
		return "\x1b[2m" + text + "\x1b[22m"
	}
	return text
}

func (*taskWidgetHost) Width() int       { return 80 }
func (*taskWidgetHost) Height() int      { return 24 }
func (host *taskWidgetHost) Invalidate() { host.invalidations++ }

type selectorUI struct {
	extensions.NoopUI
	choices []string
	index   int
	keys    []string // raw key events fed to the /plugins window component
}

func (ui *selectorUI) Select(_ context.Context, _ string, _ []string, _ *extensions.DialogOptions) (string, bool, error) {
	choice := ui.choices[ui.index]
	ui.index++
	return choice, true, nil
}

type noopHost struct{}

func (noopHost) Width() int  { return 100 }
func (noopHost) Height() int { return 40 }
func (noopHost) Invalidate() {}

// Custom runs the window factory headlessly and replays the scripted keys.
func (ui *selectorUI) Custom(_ context.Context, factory extensions.CustomFactory, _ *extensions.CustomOptions) (any, bool, error) {
	var result any
	component, err := factory(noopHost{}, extensions.NewNoopUI().Theme(), nil, func(value any) { result = value })
	if err != nil {
		return nil, false, err
	}
	handler, ok := component.(interface{ HandleInput(tui.KeyEvent) })
	if !ok {
		return nil, false, fmt.Errorf("window component does not handle input")
	}
	if len(component.Render(80)) == 0 {
		return nil, false, fmt.Errorf("window component renders nothing")
	}
	for _, key := range ui.keys {
		handler.HandleInput(tui.KeyEvent{Raw: key})
	}
	return result, true, nil
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func mustOK(err error) {
	if err != nil {
		panic(err)
	}
}

func require(t *testing.T, condition bool, format string, args ...any) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

func requireError(t *testing.T, err error, want string) {
	t.Helper()
	require(t, err != nil && strings.Contains(err.Error(), want), "error = %v, want %q", err, want)
}

func TestPluginControlPersistsAndReloads(t *testing.T) {
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	registry := extensions.NewRegistry(root)
	mustOK(registry.Register("<inline:plugin-control>", Control("", "", settings)))
	ui := &selectorUI{keys: []string{" ", "\x1b"}} // toggle the first row (tasks), close
	reloads := 0
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{
		UI: ui, Mode: extensions.ModeTUI,
		CommandActions: &extensions.CommandActions{Reload: func(context.Context) error { reloads++; return nil }},
	})
	command := runner.Command("plugins")
	require(t, command != nil, "/plugins missing")
	mustOK(command.Handler(context.Background(), "", runner.CreateCommandContext()))
	require(t, settings.GetPlugins()["tasks"] && reloads == 1, "tasks=%t reloads=%d", settings.GetPlugins()["tasks"], reloads)
}

func TestPermissionsPresetsAndSandboxMode(t *testing.T) {
	checkPolicy := func(settings map[string]any, mode string, sandboxMode sandbox.Mode) {
		policy := must(policyFromSettings(settings))
		require(t, policy.Mode == mode && policy.Sandbox == sandboxMode, "policy = %#v", policy)
	}
	checkPolicy(map[string]any{"preset": "workspace-write"}, "enforce", sandbox.ModeWorkspaceWrite)
	checkPolicy(map[string]any{"preset": "danger-full-access"}, "log", sandbox.ModeDangerFullAccess)
	checkPolicy(map[string]any{"preset": "workspace-write", "mode": "log", "sandbox": "read-only"}, "log", sandbox.ModeReadOnly)
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	checkMode := func(want sandbox.Mode) {
		got, err := SandboxMode(settings)
		require(t, err == nil && got == want, "sandbox mode = %q, %v", got, err)
	}
	checkMode(sandbox.ModeDangerFullAccess)
	settings.SetPluginSetting("permissions", "preset", "workspace-write")
	checkMode(sandbox.ModeWorkspaceWrite)
	settings.SetPluginSetting("permissions", "sandbox", "read-only")
	checkMode(sandbox.ModeReadOnly)
	for _, invalid := range []map[string]any{{"preset": true}, {"preset": "unknown"}, {"sandbox": nil}, {"sandbox": true}, {"sandbox": "unknown"}, {"mode": ""}, {"mode": "audit"}, {"askFallback": "ask"}, {"rules": nil}, {"rules": []Rule{{Action: "audit"}}}, {"rules": []Rule{{Tool: "[", Action: Deny}}}, {"rules": []Rule{{Command: "[", Action: Deny}}}, {"rules": []Rule{{Path: "[", Action: Deny}}}, {"rules": []any{map[string]any{"tool": nil, "action": "allow"}}}, {"rules": []any{map[string]any{"command": nil, "action": "allow"}}}, {"rules": []any{map[string]any{"path": nil, "action": "allow"}}}, {"rules": []any{map[string]any{"commnad": "git *", "action": "allow"}}}, {"mdoe": "enforce"}} {
		_, err := policyFromSettings(invalid)
		require(t, err != nil, "settings %#v accepted", invalid)
	}
	for _, invalid := range [][4]any{{"sandbox", "unknown", "read-only", "permissions.sandbox"}, {"mode", "", "log", "permissions.mode"}, {"mode", "audit", "log", "permissions.mode"}, {"askFallback", "ask", "allow", "permissions.askFallback"}, {"rules", []Rule{{Action: "audit"}}, []Rule{}, "permissions.rules[0].action"}, {"rules", []Rule{{Tool: "[", Action: Deny}}, []Rule{}, "permissions.rules[0]"}, {"rules", []Rule{{Command: "[", Action: Deny}}, []Rule{}, "permissions.rules[0]"}, {"rules", []Rule{{Path: "[", Action: Deny}}, []Rule{}, "permissions.rules[0]"}, {"rules", []any{map[string]any{"tool": nil, "action": "allow"}}, []Rule{}, "rules[0].tool"}, {"rules", []any{map[string]any{"command": nil, "action": "allow"}}, []Rule{}, "rules[0].command"}, {"rules", []any{map[string]any{"path": nil, "action": "allow"}}, []Rule{}, "rules[0].path"}, {"rules", []any{map[string]any{"commnad": "git *", "action": "allow"}}, []Rule{}, "rules[0].commnad"}} {
		key := invalid[0].(string)
		settings.SetPluginSetting("permissions", key, invalid[1])
		registry := extensions.NewRegistry(root)
		mustOK(registry.Register("<inline:permissions>", Catalog(Options{Settings: settings})["permissions"]))
		blocked := extensions.NewRunner(registry, extensions.RunnerOptions{}).EmitToolCall(context.Background(), extensions.ToolCallEvent{ToolName: "read"})
		require(t, blocked != nil && blocked.Block && strings.Contains(blocked.Reason, invalid[3].(string)), "invalid SDK policy for %s = %#v", key, blocked)
		settings.SetPluginSetting("permissions", key, invalid[2])
	}
	settings.SetPluginEnabled("permissions", false)
	for _, settings := range []*config.SettingsManager{settings, nil} {
		got, err := SandboxMode(settings)
		require(t, err == nil && got == sandbox.ModeDangerFullAccess, "disabled sandbox mode = %q, %v", got, err)
	}
}

func TestPermissionsPolicyRules(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	mustOK(os.Mkdir(realDir, 0o755))
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
		{"last match wins", &Policy{Rules: []Rule{{Tool: "*", Action: Allow}, {Tool: "bash", Action: Deny}, {Tool: "bash", Command: "git status*", Action: Allow}}}, ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "git status --short"}, CWD: root}, Allow},
		{"tool glob", &Policy{Rules: []Rule{{Tool: "mcp_*", Action: Deny}}}, ToolCallInfo{Tool: "mcp_delete", Args: map[string]any{}, CWD: root}, Deny},
		{"command glob treats slash as command text", &Policy{Rules: []Rule{{Tool: "bash", Command: "rm -rf *", Action: Deny}}}, ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "rm -rf /tmp/example"}, CWD: root}, Deny},
		{"raw path", &Policy{Rules: []Rule{{Path: "link/*", Action: Deny}}}, ToolCallInfo{Tool: "custom", Args: map[string]any{"path": "link/file"}, CWD: root}, Deny},
		{"canonical path", &Policy{Rules: []Rule{{Path: filepath.Join(realDir, "*"), Action: Deny}}}, ToolCallInfo{Tool: "custom", Args: map[string]any{"path": filepath.Join(link, "file")}, CWD: root}, Deny},
		{"canonical rule path", &Policy{Rules: []Rule{{Path: filepath.Join(link, "*"), Action: Deny}}}, ToolCallInfo{Tool: "custom", Args: map[string]any{"path": filepath.Join(realDir, "file")}, CWD: root}, Deny},
		{"path rule matches a path inside a bash command", &Policy{Rules: []Rule{{Path: "secrets.txt", Action: Deny}}}, ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "cat secrets.txt"}, CWD: root}, Deny},
		{"path rule ignores unrelated bash commands", &Policy{Rules: []Rule{{Path: "secrets.txt", Action: Deny}}}, ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "ls -la"}, CWD: root}, Allow},
		{"path rule matches a redirect target", &Policy{Rules: []Rule{{Path: "*.env", Action: Deny}}}, ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "echo TOKEN=1 > prod.env"}, CWD: root}, Deny},
		{"unparseable bash is ask with restrictive rule", &Policy{Rules: []Rule{{Tool: "bash", Command: "git push*", Action: Deny}}}, ToolCallInfo{Tool: "bash", Args: map[string]any{}, CWD: root}, Ask},
		{"unparseable bash is allow without restrictive rule", &Policy{Rules: []Rule{{Tool: "bash", Action: Allow}}}, ToolCallInfo{Tool: "bash", Args: map[string]any{}, CWD: root}, Allow},
		{"authorizer deny is final", &Policy{Authorizer: func(context.Context, ToolCallInfo) (Action, error) { return Deny, nil }, Guards: []func(context.Context, ToolCallInfo) string{func(context.Context, ToolCallInfo) string { panic("guard ran") }}, Rules: []Rule{{Tool: "*", Action: Allow}}}, ToolCallInfo{Tool: "todo"}, Deny},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.policy.Evaluate(context.Background(), test.info).Action
			require(t, got == test.want, "action = %q, want %q", got, test.want)
		})
	}
	order := []string{}
	guard := func(label, reason string) func(context.Context, ToolCallInfo) string {
		return func(context.Context, ToolCallInfo) string { order = append(order, label); return reason }
	}
	policy := &Policy{
		Authorizer: func(context.Context, ToolCallInfo) (Action, error) {
			order = append(order, "authorizer")
			return Allow, nil
		},
		Guards: []func(context.Context, ToolCallInfo) string{guard("guard 1", ""), guard("guard 2", "extension denied"), guard("guard 3", "")},
		Rules:  []Rule{{Tool: "*", Action: Allow}},
	}
	decision := policy.Evaluate(context.Background(), ToolCallInfo{Tool: "todo"})
	require(t, decision.Action == Deny && decision.Resolution == "extension denied" && strings.Join(order, ",") == "authorizer,guard 1,guard 2", "guard decision = %#v, order = %v", decision, order)
	policy.Guards, policy.Rules = nil, []Rule{{Tool: "*", Action: Deny}}
	got := policy.Evaluate(context.Background(), ToolCallInfo{Tool: "todo"}).Action
	require(t, got == Allow, "authorizer allow was not final: %q", got)
}

func TestPermissionsEnforceHidesAndBlocksStaticDeny(t *testing.T) {
	logSession := newPermissionsSession(t, faux.New(), &Policy{Rules: []Rule{{Tool: "bash", Action: Deny}}})
	require(t, containsName(logSession.GetActiveToolNames(), "bash"), "log mode hid bash")
	conditionalSession := newPermissionsSession(t, faux.New(), &Policy{Mode: "enforce", Rules: []Rule{{Tool: "bash", Command: "rm -rf *", Action: Deny}}})
	require(t, containsName(conditionalSession.GetActiveToolNames(), "bash"), "command-scoped deny hid the whole tool")

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
	require(t, !containsName(session.GetActiveToolNames(), "bash"), "bash remained visible after session_start")
	active := append(session.GetActiveToolNames(), "bash")
	mustOK(session.SetActiveToolsByName(active))
	mustOK(session.PromptSync(context.Background(), "try it"))
	require(t, strings.Contains(returned, `permissions: denied by rule 1 (tool="bash")`), "tool result = %q", returned)
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
	mustOK(session.PromptSync(context.Background(), "update tasks"))
	require(t, strings.Contains(returned, "ask resolved by askFallback"), "tool result = %q", returned)
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

func (ui *approvalUI) count() int { ui.mu.Lock(); defer ui.mu.Unlock(); return ui.selects }

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
	mustOK(session.PromptSync(context.Background(), "update twice"))
	if got := ui.count(); got != 1 {
		t.Fatalf("permission prompts = %d, want 1", got)
	}
	logged := 0
	for _, entry := range session.Manager().GetEntries() {
		if entry.CustomType == "orb.permissions.decision" {
			logged++
		}
	}
	require(t, logged == 2, "decision log entries = %d, want 2", logged)
}

func (ui *widgetUI) SetWidget(_ string, widget *extensions.Widget, _ *extensions.WidgetOptions) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.lines, ui.factory = nil, nil
	if widget != nil {
		ui.lines = append([]string(nil), widget.Lines...)
		ui.factory = widget.Factory
		ui.shown++
	}
}

func (ui *widgetUI) snapshot() []string {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return append([]string(nil), ui.lines...)
}

func (ui *widgetUI) showCount() int { ui.mu.Lock(); defer ui.mu.Unlock(); return ui.shown }

func (ui *widgetUI) widgetFactory() extensions.ComponentFactory {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.factory
}

func TestTasksToolReplacesTheLiveWidget(t *testing.T) {
	ui := &widgetUI{}
	tool := pluginTool(t, "tasks", "todo", Options{}, extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	result := must(tool.Execute(context.Background(), "todo-1", map[string]any{"items": []any{
		map[string]any{"text": "inspect", "status": "done"},
		map[string]any{"text": "implement", "status": "in_progress"},
	}}, nil))
	text := ai.ContentText(result.Content)
	require(t, text == "[x] inspect\n→ [ ] implement", "tool result = %q", text)
	if got, want := strings.Join(ui.snapshot(), "\n"), "✓ 1/2  → implement"; got != want {
		t.Fatalf("widget = %q, want %q", got, want)
	}
	factory := ui.widgetFactory()
	require(t, factory != nil, "TUI task widget has no click renderer")
	host := &taskWidgetHost{}
	component := factory(host, nil)
	mouse, ok := component.(tui.MouseHandler)
	require(t, ok && mouse.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Clicks: 1}), "task widget did not accept a left click")
	expanded := strings.Join(component.Render(80), "\n")
	require(t, strings.Contains(expanded, "[x] inspect") && strings.Contains(expanded, "→ [ ] implement") && host.invalidations == 1, "expanded task widget = %q, invalidations = %d", expanded, host.invalidations)
	for width := 1; width <= 4; width++ {
		for _, line := range component.Render(width) {
			if got := tui.VisibleWidth(line); got > width {
				t.Fatalf("task widget width %d rendered %d cells: %q", width, got, line)
			}
		}
	}
	styled := newTaskWidget([]todoItem{{Text: "inspect", Status: "done"}, {Text: "implement", Status: "in_progress"}}, host, dimTaskTheme{})
	styled.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Clicks: 1})
	styledLines := styled.Render(80)
	require(t, len(styledLines) >= 3 && strings.Contains(strings.Join(styledLines, "\n"), "\x1b[2m") && strings.HasPrefix(styledLines[1], "    "), "styled expanded task widget = %#v", styledLines)

	result = must(tool.Execute(context.Background(), "todo-2", map[string]any{"items": []any{
		map[string]any{"text": "ship", "status": "pending"},
	}}, nil))
	got := ai.ContentText(result.Content)
	require(t, got == "[ ] ship" && strings.Join(ui.snapshot(), "\n") == "✓ 0/1  ·  +1 queued", "replacement result = %q widget = %q", got, strings.Join(ui.snapshot(), "\n"))
	details, ok := result.Details.(todoInput)
	require(t, ok && len(details.Items) == 1 && details.Items[0].Text == "ship", "result details = %#v", result.Details)
}

func TestTaskWidgetCachesStableRenders(t *testing.T) {
	theme := &countingTaskTheme{}
	widget := newTaskWidget([]todoItem{{Text: "inspect", Status: "done"}, {Text: "implement", Status: "in_progress"}}, nil, theme)
	widget.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Clicks: 1})
	first := widget.Render(80)
	calls := theme.calls
	second := widget.Render(80)
	require(t, strings.Join(first, "\n") == strings.Join(second, "\n"), "cached render changed: %#v != %#v", first, second)
	require(t, theme.calls == calls, "stable render restyled tasks: calls %d -> %d", calls, theme.calls)
	widget.Render(40)
	require(t, theme.calls != calls, "width change reused a stale task render")
	calls = theme.calls
	widget.Invalidate()
	widget.Render(40)
	require(t, theme.calls != calls, "invalidation reused stale themed task lines")
}

func TestTaskWidgetInvalidationWinsConcurrentRender(t *testing.T) {
	theme := &blockingTaskTheme{started: make(chan struct{}), release: make(chan struct{})}
	widget := newTaskWidget([]todoItem{{Text: "inspect", Status: "done"}}, nil, theme)
	widget.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Clicks: 1})
	done := make(chan struct{})
	go func() {
		widget.Render(80)
		close(done)
	}()
	<-theme.started
	widget.Invalidate()
	close(theme.release)
	<-done
	calls := theme.calls
	widget.Render(80)
	require(t, theme.calls != calls, "concurrent invalidation allowed stale task lines back into the cache")
}

func TestTasksRebuildFromBranchDetails(t *testing.T) {
	manager := must(sessionstore.InMemory(t.TempDir()))
	for _, items := range []string{`[{"text":"first","status":"done"}]`, `[{"text":"second","status":"pending"}]`} {
		_ = must(manager.AppendMessage(&ai.ToolResultMessage{
			ToolName: "todo", Content: ai.ToolResultContent{&ai.TextContent{Text: "ok"}},
			Details: json.RawMessage(`{"items":` + items + `}`),
		}))
	}
	restored := todosFromBranch(manager)
	require(t, len(restored) == 1 && restored[0].Text == "second" && restored[0].Status == "pending", "restored = %#v", restored)
	require(t, todosFromBranch(nil) == nil, "nil manager returned %#v", todosFromBranch(nil))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestWebSearchBackendsAndFetchContent(t *testing.T) {
	tests := []struct {
		name, env, endpoint, method, header, body, response, want string
	}{
		{name: "exa", env: "EXA_API_KEY", endpoint: "api.exa.ai/search", method: http.MethodPost, header: "x-api-key", body: `"query":"orb"`, response: `{"results":[{"title":"Exa result","url":"https://exa.test","highlights":["match"]}]}`, want: "Exa result\nhttps://exa.test\nmatch"},
		{name: "brave", env: "BRAVE_API_KEY", endpoint: "api.search.brave.com/res/v1/web/search", method: http.MethodGet, header: "X-Subscription-Token", response: `{"web":{"results":[{"title":"Brave result","url":"https://brave.test","description":"match"}]}}`, want: "Brave result\nhttps://brave.test\nmatch"},
		{name: "tavily", env: "TAVILY_API_KEY", endpoint: "api.tavily.com/search", method: http.MethodPost, header: "Authorization", body: `"query":"orb"`, response: `{"results":[{"title":"Tavily result","url":"https://tavily.test","content":"match"}]}`, want: "Tavily result\nhttps://tavily.test\nmatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
				t.Setenv(key, "")
			}
			t.Setenv(test.env, "secret")
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				require(t, request.Method == test.method && strings.Contains(request.URL.String(), test.endpoint), "request = %s %s", request.Method, request.URL)
				require(t, request.Header.Get(test.header) != "", "missing %s header", test.header)
				if test.body != "" {
					body, _ := io.ReadAll(request.Body)
					require(t, strings.Contains(string(body), test.body), "body = %s", body)
				}
				return response(http.StatusOK, "application/json", test.response), nil
			})}
			tool := pluginTool(t, "websearch", "web_search", Options{HTTPClient: client}, extensions.RunnerOptions{})
			result := must(tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil))
			require(t, ai.ContentText(result.Content) == test.want, "result = %q, want %q", ai.ContentText(result.Content), test.want)
		})
	}

	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	stubDNS(t, map[string]string{"example.test": "93.184.216.34"})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(http.StatusOK, "text/html", `<html><style>no</style><body><h1>Hello &amp; hi</h1><script>no</script><p>Readable text.</p></body></html>`), nil
	})}
	fetch := pluginTool(t, "websearch", "fetch_content", Options{HTTPClient: client}, extensions.RunnerOptions{})
	result := must(fetch.Execute(context.Background(), "fetch", map[string]any{"url": "https://example.test/page"}, nil))
	// Block tags keep their line breaks so oversized pages stay truncatable.
	require(t, ai.ContentText(result.Content) == "Hello & hi\nReadable text.", "content = %q", ai.ContentText(result.Content))
}

// stubDNS pins hostname resolution so the SSRF guard is exercised without
// depending on the network. Unlisted hosts fail to resolve, as they would live.
func stubDNS(t *testing.T, addresses map[string]string) {
	t.Helper()
	original := lookupIP
	lookupIP = func(_ context.Context, host string) ([]net.IPAddr, error) {
		address, ok := addresses[host]
		if !ok {
			return nil, fmt.Errorf("no such host")
		}
		return []net.IPAddr{{IP: net.ParseIP(address)}}, nil
	}
	t.Cleanup(func() { lookupIP = original })
}

func TestFetchContentKeepsLargePagesReadable(t *testing.T) {
	stubDNS(t, map[string]string{"example.test": "93.184.216.34"})
	var page strings.Builder
	page.WriteString("<html><body>")
	for index := range 2000 {
		fmt.Fprintf(&page, "<p>Paragraph %d carries enough prose to push this page past the fifty kilobyte cap.</p>", index)
	}
	page.WriteString("</body></html>")
	require(t, page.Len() > 50<<10, "fixture is only %d bytes", page.Len())
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "text/html", page.String()), nil
	})}
	tool := pluginTool(t, "websearch", "fetch_content", Options{HTTPClient: client}, extensions.RunnerOptions{})
	result := must(tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://example.test/big"}, nil))
	got := ai.ContentText(result.Content)
	require(t, strings.Contains(got, "Paragraph 0 carries") && len(got) >= 40<<10, "large page returned %d bytes: %.120q", len(got), got)
	require(t, strings.Count(got, "\n") >= 100, "page collapsed onto %d lines", strings.Count(got, "\n")+1)
	require(t, strings.HasSuffix(got, "[output truncated]"), "missing truncation marker: %.120q", got[max(0, len(got)-120):])
	// A page with no break at all still has to yield its head, not just the marker.
	head := truncateWeb(strings.Repeat("x", 60<<10))
	require(t, len(head) >= 40<<10, "unbreakable line truncated to %d bytes", len(head))
}

func TestFetchContentRejectsNonPublicDestinations(t *testing.T) {
	stubDNS(t, map[string]string{
		"public.test":   "93.184.216.34",
		"internal.test": "10.0.0.5",
	})
	var requests int
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		switch request.URL.Path {
		case "/to-loopback":
			return redirect("http://127.0.0.1/admin"), nil
		case "/loop":
			return redirect("https://public.test/loop"), nil
		}
		return response(http.StatusOK, "text/plain", "reached "+request.URL.String()), nil
	})}
	tool := pluginTool(t, "websearch", "fetch_content", Options{HTTPClient: client}, extensions.RunnerOptions{})
	for _, test := range []struct {
		name, url, want string
		wantRequests    int
	}{
		{name: "loopback literal", url: "http://127.0.0.1/", want: "blocked non-public address"},
		{name: "metadata service", url: "http://169.254.169.254/latest/meta-data/", want: "blocked non-public address"},
		{name: "rfc1918 literal", url: "http://192.168.1.1/", want: "blocked non-public address"},
		{name: "ipv6 loopback", url: "http://[::1]/", want: "blocked non-public address"},
		{name: "localhost name", url: "http://localhost:8080/", want: "blocked internal hostname"},
		{name: "private via dns", url: "http://internal.test/", want: "blocked non-public address"},
		{name: "non-http scheme", url: "file:///etc/passwd", want: "must use http or https"},
		{name: "unresolvable host", url: "http://nowhere.test/", want: "resolve nowhere.test"},
		{name: "redirect to loopback", url: "https://public.test/to-loopback", want: "blocked non-public address", wantRequests: 1},
		{name: "redirect loop", url: "https://public.test/loop", want: "too many redirects", wantRequests: webMaxRedirects + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests = 0
			_, err := tool.Execute(context.Background(), "fetch", map[string]any{"url": test.url}, nil)
			require(t, err != nil && strings.Contains(err.Error(), test.want), "error = %v, want %q", err, test.want)
			require(t, requests == test.wantRequests, "issued %d requests, want %d", requests, test.wantRequests)
		})
	}
	_ = must(tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/ok"}, nil))
}

func TestFetchContentDecodesCharsetAndRejectsBinary(t *testing.T) {
	stubDNS(t, map[string]string{"public.test": "93.184.216.34"})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/binary" {
			return response(http.StatusOK, "image/png", "\x89PNG\r\n\x1a\n\xff\xfe"), nil
		}
		if request.URL.Path == "/sjis" {
			return response(http.StatusOK, "text/html; charset=shift_jis", "<p>hello \x82\xb1\x82\xf1\x82\xc9\x82\xbf\x82\xcd</p>"), nil
		}
		// 0x92 is a right single quote in windows-1252 and invalid UTF-8.
		return response(http.StatusOK, "text/html; charset=windows-1252", "<p>caf\xe9 owner\x92s</p>"), nil
	})}
	tool := pluginTool(t, "websearch", "fetch_content", Options{HTTPClient: client}, extensions.RunnerOptions{})
	result := must(tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/legacy"}, nil))
	got := ai.ContentText(result.Content)
	require(t, got == "café owner’s" && utf8.ValidString(got), "decoded = %q", got)
	result = must(tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/sjis"}, nil))
	got = ai.ContentText(result.Content)
	require(t, got == "hello こんにちは" && utf8.ValidString(got), "decoded = %q", got)
	_, err := tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/binary"}, nil)
	require(t, err != nil && strings.Contains(err.Error(), "unsupported content type"), "binary error = %v", err)
}

func TestWebSearchDropsProviderErrorBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{"BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("EXA_API_KEY", "sk-SECRET")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusUnauthorized, "application/json", `{"error":"invalid key sk-SECRET"}`), nil
	})}
	tool := pluginTool(t, "websearch", "web_search", Options{HTTPClient: client}, extensions.RunnerOptions{})
	_, err := tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil)
	require(t, err != nil && !strings.Contains(err.Error(), "sk-SECRET"), "error leaked the provider body: %v", err)
}

func TestWebSearchHonoursConfiguredProvider(t *testing.T) {
	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustOK(os.MkdirAll(filepath.Join(home, ".pi"), 0o755))
	config := `{"provider":"brave","exaApiKey":"exa-key","braveApiKey":"brave-key"}`
	mustOK(os.WriteFile(filepath.Join(home, ".pi", "web-search.json"), []byte(config), 0o600))
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require(t, strings.Contains(request.URL.Host, "brave"), "provider ignored: %s", request.URL)
		return response(http.StatusOK, "application/json", `{"web":{"results":[]}}`), nil
	})}
	tool := pluginTool(t, "websearch", "web_search", Options{HTTPClient: client}, extensions.RunnerOptions{})
	_ = must(tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil))
}

func TestWebSearchWithoutKeyReturnsActionableError(t *testing.T) {
	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", t.TempDir())
	tool := pluginTool(t, "websearch", "web_search", Options{}, extensions.RunnerOptions{})
	_, err := tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil)
	require(t, err != nil && strings.Contains(err.Error(), "EXA_API_KEY") && strings.Contains(err.Error(), "~/.pi/web-search.json"), "error = %v", err)
}

func TestWebSearchReadsPiWebSearchConfig(t *testing.T) {
	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustOK(os.MkdirAll(filepath.Join(home, ".pi"), 0o755))
	mustOK(os.WriteFile(filepath.Join(home, ".pi", "web-search.json"), []byte(`{"exaApiKey":"stored"}`), 0o600))
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require(t, request.Header.Get("x-api-key") == "stored", "api key = %q", request.Header.Get("x-api-key"))
		return response(http.StatusOK, "application/json", `{"results":[]}`), nil
	})}
	tool := pluginTool(t, "websearch", "web_search", Options{HTTPClient: client}, extensions.RunnerOptions{})
	_ = must(tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil))
}

func TestSubagentCompletesInProcessWithForkedContext(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var childSawParent bool
	var returned string
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "single", "task": "answer", "agent": "scout", "context": "fork"}, faux.ToolCallOptions{ID: "sub-1"})),
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
	mustOK(session.PromptSync(context.Background(), "parent seed"))
	require(t, childSawParent && returned == "child answer", "childSawParent=%t tool result=%q", childSawParent, returned)
}

func TestSubagentChildOptionsUseParentRegistryForDefaultStream(t *testing.T) {
	registry := must(config.NewModelRegistry(t.TempDir()))
	options := must(childOptions(registry, nil, agent.AgentSessionOptions{}))
	require(t, options.ModelRegistry == registry && options.StreamFn == nil, "model registry=%p want=%p stream set=%t", options.ModelRegistry, registry, options.StreamFn != nil)
	_, err := childOptions(nil, nil, agent.AgentSessionOptions{})
	requireError(t, err, "parent has no model registry")
}

func TestSubagentExternalCLIConfigSchemaAndExecution(t *testing.T) {
	tool := externalSubagentTool(t, map[string]any{"zeta": "/bin/cat", "alpha": "/bin/cat"})
	wantEnum := `"enum":["scout","worker","reviewer","alpha","zeta"]`
	schema := string(tool.Spec().Parameters)
	require(t, strings.Count(schema, wantEnum) == 2, "external agent enum = %s", schema)
	result := must(tool.Execute(t.Context(), "call", map[string]any{"mode": "single", "agent": "alpha", "task": "task over stdin"}, nil))
	require(t, ai.ContentText(result.Content) == "task over stdin", "external result = %q", ai.ContentText(result.Content))

	for _, invalid := range []struct {
		name, key, want string
		value           any
	}{{"nullable enabled", "enabled", "subagents.enabled must be true or false", nil}, {"nullable external", "external", "subagents.external must map names to commands", nil}, {"built-in collision", "external", `cannot replace built-in agent "worker"`, map[string]any{"worker": "/bin/cat"}}, {"non-string command", "external", "invalid subagents settings", map[string]any{"bad": true}}, {"control in name", "external", "names must be non-empty without whitespace or control characters", map[string]any{"bad\nname": "/bin/cat"}}, {"empty command", "external", "subagents.external.bad command must not be empty", map[string]any{"bad": " "}}, {"unknown key", "externl", `unknown field "externl"`, map[string]any{"bad": "/bin/cat"}}} {
		t.Run(invalid.name, func(t *testing.T) {
			root := t.TempDir()
			settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
			settings.SetPluginSetting("subagents", invalid.key, invalid.value)
			err := extensions.NewRegistry(root).Register("<inline:subagents>", Catalog(Options{Settings: settings})["subagents"])
			require(t, err != nil && strings.Contains(err.Error(), invalid.want), "setting %s=%#v error = %v, want %q", invalid.key, invalid.value, err, invalid.want)
		})
	}
}

func TestSubagentExternalCLIStopsDescendantsAndBoundsOutput(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "cancel-marker")
	successMarker := filepath.Join(root, "success-marker")
	t.Setenv("ORB_SUBAGENT_MARKER", marker)
	t.Setenv("ORB_SUBAGENT_SUCCESS_MARKER", successMarker)
	tool := externalSubagentTool(t, map[string]any{"cancel": `(sleep 1; touch "$ORB_SUBAGENT_MARKER") & sleep 5`})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := tool.Execute(ctx, "cancel", map[string]any{"mode": "single", "agent": "cancel", "task": "stop"}, nil)
	requireError(t, err, "deadline exceeded")
	tool = externalSubagentTool(t, map[string]any{"success": `(sleep 1; touch "$ORB_SUBAGENT_SUCCESS_MARKER") & exit 0`})
	_, err = tool.Execute(t.Context(), "success", map[string]any{"mode": "single", "agent": "success", "task": "stop"}, nil)
	require(t, err == nil, "successful background command = %v", err)
	time.Sleep(1200 * time.Millisecond)
	for _, path := range []string{marker, successMarker} {
		_, err := os.Stat(path)
		require(t, os.IsNotExist(err), "external descendant created %s: %v", path, err)
	}

	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			redirect := ""
			if stream == "stderr" {
				redirect = " >&2"
			}
			tool := externalSubagentTool(t, map[string]any{"noisy": `while :; do printf 0123456789abcdef0123456789abcdef` + redirect + `; done`})
			_, err := tool.Execute(t.Context(), "noisy", map[string]any{"mode": "single", "agent": "noisy", "task": "overflow"}, nil)
			requireError(t, err, stream+" exceeded the 1048576-byte limit")
		})
	}
}

func externalSubagentTool(t *testing.T, commands map[string]any) engine.AgentTool {
	t.Helper()
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	settings.SetPluginSetting("subagents", "external", commands)
	return pluginTool(t, "subagents", "subagent", Options{Settings: settings}, extensions.RunnerOptions{})
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
	mustOK(session.PromptSync(context.Background(), "delegate"))
	require(t, strings.Contains(returned, "child:alpha") && strings.Contains(returned, "child:beta"), "parallel result = %q", returned)
}

func TestSubagentSurfacesChildStreamError(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	providerError := "No API key for provider: anthropic"
	var returned string
	var isError bool
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "single", "task": "inspect", "agent": "scout"}, faux.ToolCallOptions{ID: "sub-error"})),
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
	mustOK(session.PromptSync(context.Background(), "delegate"))
	require(t, isError && strings.Contains(returned, "subagent: child failed: "+providerError), "tool error=%t result=%q", isError, returned)
}

func TestSubagentInheritsPermissionsPolicy(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	childReadAbsent := false
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "single", "task": "inspect", "agent": "scout"}, faux.ToolCallOptions{ID: "sub-policy"})),
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
	mustOK(session.PromptSync(context.Background(), "delegate"))
	require(t, childReadAbsent, "read was advertised to the child despite the inherited deny rule")
}

func TestSubagentClearsProgressWidgetAndFailsParallelRuns(t *testing.T) {
	ui := &widgetUI{}
	tool := pluginTool(t, "subagents", "subagent", Options{}, extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	// No parent model registry, so every child fails: the widget must still go.
	_, err := tool.Execute(context.Background(), "sub-1", map[string]any{"mode": "single", "task": "work"}, nil)
	require(t, err != nil, "child without a model registry succeeded")
	require(t, ui.showCount() > 0, "progress widget was never shown")
	require(t, len(ui.snapshot()) == 0, "progress widget left on screen: %v", ui.snapshot())

	_, err = tool.Execute(context.Background(), "sub-2", map[string]any{"mode": "parallel", "tasks": []any{
		map[string]any{"task": "alpha"}, map[string]any{"task": "beta"},
	}}, nil)
	require(t, err != nil && strings.Contains(err.Error(), "2 of 2 children failed") && strings.Contains(err.Error(), "[2] worker"), "parallel failure = %v", err)
	require(t, len(ui.snapshot()) == 0, "progress widget left on screen: %v", ui.snapshot())
}

type memoryTestStore struct {
	mu         sync.Mutex
	items      []memorysdk.Item
	operations []string
	nextID     int
	searched   bool
}

// plainMemoryStore is a Store without SemanticSearcher, so recall takes the
// substring-then-word-overlap path.
type plainMemoryStore struct{ items []memorysdk.Item }

// gatedMemoryStore signals each Query entry and waits for release, so a test
// can pin one plugin instance inside its store mid-operation.
type gatedMemoryStore struct {
	plainMemoryStore
	entered chan struct{}
	release chan struct{}
}

func newGatedMemoryStore() *gatedMemoryStore {
	return &gatedMemoryStore{entered: make(chan struct{}, 2), release: make(chan struct{}, 2)}
}

func (store *gatedMemoryStore) Query(ctx context.Context, filter memorysdk.Filter) ([]memorysdk.Item, error) {
	store.entered <- struct{}{}
	<-store.release
	return store.plainMemoryStore.Query(ctx, filter)
}

func (store *plainMemoryStore) Append(_ context.Context, item memorysdk.Item) (string, error) {
	store.items = append(store.items, item)
	return item.ID, nil
}

func (store *plainMemoryStore) Get(context.Context, string) (memorysdk.Item, error) {
	return memorysdk.Item{}, os.ErrNotExist
}

func (store *plainMemoryStore) Delete(context.Context, string) error { return nil }

func (store *plainMemoryStore) Query(_ context.Context, filter memorysdk.Filter) ([]memorysdk.Item, error) {
	return filterMemoryTestItems(store.items, filter.Contains, filter.Tags, filter.Limit), nil
}

func (store *memoryTestStore) Append(_ context.Context, item memorysdk.Item) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.nextID < len(store.items) {
		store.nextID = len(store.items)
	}
	store.nextID++
	item.ID = fmt.Sprintf("custom-%d", store.nextID)
	if item.Time.IsZero() {
		item.Time = time.Date(2026, 7, 23, 10, store.nextID-1, 0, 0, time.UTC)
	}
	store.items = append(store.items, item)
	store.operations = append(store.operations, "append:"+item.ID)
	return item.ID, nil
}

func (store *memoryTestStore) Get(_ context.Context, id string) (memorysdk.Item, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, item := range store.items {
		if item.ID == id {
			return item, nil
		}
	}
	return memorysdk.Item{}, os.ErrNotExist
}

func (store *memoryTestStore) Query(_ context.Context, filter memorysdk.Filter) ([]memorysdk.Item, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return filterMemoryTestItems(store.items, filter.Contains, filter.Tags, filter.Limit), nil
}

func (store *memoryTestStore) Delete(_ context.Context, id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.operations = append(store.operations, "delete:"+id)
	for index, item := range store.items {
		if item.ID == id {
			store.items = append(store.items[:index], store.items[index+1:]...)
			break
		}
	}
	return nil
}

func (store *memoryTestStore) Search(_ context.Context, query string, limit int) ([]memorysdk.Scored, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.searched = true
	items := filterMemoryTestItems(store.items, query, nil, limit)
	result := make([]memorysdk.Scored, len(items))
	for index := range items {
		result[index] = memorysdk.Scored{Item: items[index], Score: 1}
	}
	return result, nil
}

func (store *memoryTestStore) snapshot() ([]memorysdk.Item, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]memorysdk.Item(nil), store.items...), store.searched
}

func (store *memoryTestStore) operationSnapshot() []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]string(nil), store.operations...)
}

func filterMemoryTestItems(items []memorysdk.Item, contains string, tags []string, limit int) []memorysdk.Item {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var result []memorysdk.Item
	for index := len(items) - 1; index >= 0 && len(result) < limit; index-- {
		if contains != "" && !strings.Contains(strings.ToLower(items[index].Content), strings.ToLower(contains)) ||
			!hasMemoryTags(items[index].Tags, tags) {
			continue
		}
		result = append(result, items[index])
	}
	return result
}

const (
	userMemoryChars = 1375
	memoryChars     = 2200
	userTargetTag   = "orb:memory:user"
	memoryTargetTag = "orb:memory:memory"
)

func hasMemoryTags(itemTags, required []string) bool {
	for _, tag := range required {
		if !slices.Contains(itemTags, tag) {
			return false
		}
	}
	return true
}

func TestMemoryCatalogUsesAgentDirStore(t *testing.T) {
	agentDir := t.TempDir()
	tool := pluginTool(t, "memory", "remember", Options{AgentDir: agentDir}, extensions.RunnerOptions{})
	_ = must(tool.Execute(context.Background(), "remember-local", map[string]any{
		"target": "memory", "content": "Local profile marker.",
	}, nil))
	store := must(memorysdk.NewFileStore(filepath.Join(agentDir, "memory")))
	items := must(store.Query(context.Background(), memorysdk.Filter{}))
	if len(items) != 1 || items[0].Content != "Local profile marker." {
		t.Fatalf("local items = %#v", items)
	}
}

func TestMemoryWithStoreRejectsNil(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<inline:memory>", MemoryWithStore(nil)); err == nil || !strings.Contains(err.Error(), "store is required") {
		t.Fatalf("MemoryWithStore(nil) error = %v", err)
	}
}

func recallOnInstance(t *testing.T, store memorysdk.Store, done chan<- error) {
	t.Helper()
	tool := memoryPluginTool(t, store, "recall")
	go func() {
		_, err := tool.Execute(context.Background(), "recall", map[string]any{}, nil)
		done <- err
	}()
}

func TestMemoryInstancesDoNotShareStoreLock(t *testing.T) {
	gated := newGatedMemoryStore()
	gatedDone := make(chan error, 1)
	recallOnInstance(t, gated, gatedDone)
	<-gated.entered

	otherDone := make(chan error, 1)
	recallOnInstance(t, &plainMemoryStore{}, otherDone)
	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recall on a separate plugin instance blocked behind another instance's store query")
	}

	gated.release <- struct{}{}
	mustOK(<-gatedDone)
}

func TestMemorySameInstanceSerializesStoreOperations(t *testing.T) {
	gated := newGatedMemoryStore()
	done := make(chan error, 2)
	tool := memoryPluginTool(t, gated, "recall")
	go func() {
		_, err := tool.Execute(context.Background(), "recall-1", map[string]any{}, nil)
		done <- err
	}()
	<-gated.entered // first operation is inside the store, holding the instance lock
	go func() {
		_, err := tool.Execute(context.Background(), "recall-2", map[string]any{}, nil)
		done <- err
	}()

	select {
	case <-gated.entered:
		t.Fatal("two operations on one plugin instance entered the store concurrently")
	case <-time.After(100 * time.Millisecond):
	}

	gated.release <- struct{}{} // first operation leaves the store and unlocks
	<-gated.entered             // only now the second operation enters
	gated.release <- struct{}{}
	for range 2 {
		mustOK(<-done)
	}
}

func TestMemoryWithStoreRememberRecallForgetThroughRegistry(t *testing.T) {
	store := &memoryTestStore{}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var recalled, recalledAfterForget string
	var rememberedTargeted bool
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("remember", map[string]any{
			"target": "memory", "content": "The durable marker is cobalt.", "tags": []any{" Project ", "PROJECT"},
		}, faux.ToolCallOptions{ID: "memory-1"})),
		faux.AssistantMessage(faux.ToolCall("recall", map[string]any{
			"query": "cobalt", "tags": []any{"project"},
		}, faux.ToolCallOptions{ID: "memory-2"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			recalled = toolResultText(request, "recall")
			items, _ := store.snapshot()
			if len(items) == 1 {
				rememberedTargeted = hasMemoryTags(items[0].Tags, []string{"project", memoryTargetTag})
			}
			return faux.AssistantMessage(faux.ToolCall("forget", map[string]any{
				"target": "memory", "query": "cobalt", "tags": []any{"project"},
			}, faux.ToolCallOptions{ID: "memory-3"})), nil
		}),
		faux.AssistantMessage(faux.ToolCall("recall", map[string]any{
			"query": "cobalt", "tags": []any{"project"},
		}, faux.ToolCallOptions{ID: "memory-4"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			recalledAfterForget = toolResultText(request, "recall")
			return faux.AssistantMessage("done"), nil
		}),
	})
	session := newMemoryPluginSession(t, provider, MemoryWithStore(store), nil)
	mustOK(session.PromptSync(context.Background(), "remember, recall, and forget"))
	items, searched := store.snapshot()
	require(t, len(items) == 0 && rememberedTargeted && searched, "custom store items = %#v, targeted = %t, semantic searched = %t", items, rememberedTargeted, searched)
	require(t, recalled == "2026-07-23T10:00:00Z [project] The durable marker is cobalt.", "recall result = %q", recalled)
	require(t, recalledAfterForget == "No memories found.", "recall after forget = %q", recalledAfterForget)
}

func TestMemoryRememberDoesNotDuplicateRecentExactContent(t *testing.T) {
	store := &memoryTestStore{}
	tool := memoryPluginTool(t, store, "remember")
	first := must(tool.Execute(context.Background(), "remember-first", map[string]any{
		"target": "memory", "content": " Stable project fact. ", "tags": []any{"project"},
	}, nil))
	second := must(tool.Execute(context.Background(), "remember-second", map[string]any{
		"target": "memory", "content": "Stable project fact.", "tags": []any{"project", "duplicate-attempt"},
	}, nil))
	items, _ := store.snapshot()
	require(t, len(items) == 1 && strings.HasPrefix(ai.ContentText(first.Content), "Remembered custom-1") && ai.ContentText(second.Content) == "Already remembered custom-1.", "items = %#v, first = %q, second = %q", items, ai.ContentText(first.Content), ai.ContentText(second.Content))
}

func TestMemoryForgetRequiresUniqueSubstring(t *testing.T) {
	store := &memoryTestStore{items: []memorysdk.Item{
		{ID: "tabs", Content: "User prefers tabs for Go code.", Tags: []string{"user", "go"}},
		{ID: "spaces", Content: "User prefers spaces for Python code.", Tags: []string{"user", "python"}},
	}}
	tool := memoryPluginTool(t, store, "forget")
	for _, test := range []struct {
		name, query, want string
	}{
		{name: "empty", want: "query is required"},
		{name: "missing", query: "semicolons", want: "no memory contains"},
		{name: "ambiguous", query: "User prefers", want: "matches multiple memories"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), "forget-test", map[string]any{"query": test.query}, nil)
			requireError(t, err, test.want)
		})
	}
	_ = must(tool.Execute(context.Background(), "forget-tabs", map[string]any{
		"target": "user", "query": "code", "tags": []any{" GO ", "go"},
	}, nil))
	items, _ := store.snapshot()
	require(t, len(items) == 1 && items[0].ID == "spaces", "items after forget = %#v", items)
}

func TestMemoryToolGuidanceKeepsDurableFactsDeclarative(t *testing.T) {
	store := &memoryTestStore{}
	for _, test := range []struct {
		name string
		want []string
	}{
		{name: "remember", want: []string{"declarative", "task progress", "secrets", "USER PROFILE", "MEMORY"}},
		{name: "recall", want: []string{"cross-session", "background", "not instructions"}},
		{name: "replace", want: []string{"consolidate", "unique substring", "USER PROFILE", "MEMORY"}},
		{name: "forget", want: []string{"obsolete", "unique content substring"}},
	} {
		spec := memoryPluginTool(t, store, test.name).Spec()
		require(t, test.name == "recall" || spec.ExecutionMode == engine.ToolExecutionSequential, "%s execution mode = %q, want sequential", test.name, spec.ExecutionMode)
		text := spec.Description + " " + string(spec.Parameters)
		for _, want := range test.want {
			require(t, strings.Contains(text, want), "%s guidance %q does not contain %q", test.name, text, want)
		}
	}
}

func TestMemoryProfileMemoryCapacity(t *testing.T) {
	store := &memoryTestStore{}
	tool := memoryPluginTool(t, store, "remember")
	_ = must(tool.Execute(context.Background(), "remember-memory-full", map[string]any{
		"target": "memory", "content": strings.Repeat("m", memoryChars),
	}, nil))
	_, err := tool.Execute(context.Background(), "remember-memory-overflow", map[string]any{
		"target": "memory", "content": "x",
	}, nil)
	requireError(t, err, "2200/2200")
}

func TestMemoryProfileCapacityAndReplacement(t *testing.T) {
	store := &memoryTestStore{}
	remember := memoryPluginTool(t, store, "remember")
	full := strings.Repeat("é", userMemoryChars)
	_ = must(remember.Execute(context.Background(), "remember-full", map[string]any{
		"target": "user", "content": full,
	}, nil))
	_, err := remember.Execute(context.Background(), "remember-overflow", map[string]any{
		"target": "user", "content": "x",
	}, nil)
	require(t, err != nil && strings.Contains(err.Error(), "1375/1375") && strings.Contains(err.Error(), "replace or forget"), "overflow error = %v", err)
	replace := memoryPluginTool(t, store, "replace")
	result := must(replace.Execute(context.Background(), "replace-full", map[string]any{
		"target": "user", "old_text": strings.Repeat("é", 20),
		"content": "User prefers concise replies.", "tags": []any{"style"},
	}, nil))
	items, _ := store.snapshot()
	require(t, len(items) == 1 && items[0].Content == "User prefers concise replies." && hasMemoryTags(items[0].Tags, []string{"user", userTargetTag, "style"}), "items after replace = %#v", items)
	require(t, slices.Equal(store.operationSnapshot(), []string{"append:custom-1", "append:custom-2", "delete:custom-1"}), "store operations = %v", store.operationSnapshot())
	require(t, strings.HasPrefix(ai.ContentText(result.Content), "Replaced custom-1 with custom-2"), "replace result = %q", ai.ContentText(result.Content))
}

func TestMemoryProfileIsFrozenInSystemPrompt(t *testing.T) {
	store := &memoryTestStore{items: []memorysdk.Item{
		{ID: "user", Time: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC), Content: "User prefers terse replies.", Tags: []string{"user", "style", userTargetTag}},
		{ID: "project", Time: time.Date(2026, 7, 23, 10, 1, 0, 0, time.UTC), Content: "Project uses Go 1.26.", Tags: []string{"project", memoryTargetTag}},
		{ID: "legacy", Time: time.Date(2026, 7, 23, 10, 2, 0, 0, time.UTC), Content: "Legacy untagged facts remain memory.", Tags: []string{"legacy"}},
	}}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var prompts []string
	provider.SetResponses([]faux.ResponseStep{
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			prompts = append(prompts, *request.SystemPrompt)
			return faux.AssistantMessage("first"), nil
		}),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			prompts = append(prompts, *request.SystemPrompt)
			return faux.AssistantMessage("second"), nil
		}),
	})
	session := newMemoryPluginSession(t, provider, MemoryWithStore(store), nil)
	mustOK(session.PromptSync(context.Background(), "first prompt"))
	_ = must(store.Append(context.Background(), memorysdk.Item{Content: "late marker", Tags: []string{memoryTargetTag}}))
	mustOK(session.PromptSync(context.Background(), "second prompt"))
	require(t, len(prompts) == 2 && prompts[0] == prompts[1], "system prompts changed: %#v", prompts)
	for _, want := range []string{
		"Persistent curated memory", "USER PROFILE [", "User prefers terse replies.",
		"MEMORY [", "Project uses Go 1.26.", "Legacy untagged facts remain memory.",
	} {
		require(t, strings.Contains(prompts[0], want), "system prompt does not contain %q:\n%s", want, prompts[0])
	}
	for _, unwanted := range []string{"late marker", userTargetTag, memoryTargetTag, "2026-07-23"} {
		require(t, !strings.Contains(prompts[0], unwanted), "system prompt contains %q:\n%s", unwanted, prompts[0])
	}
}

func memoryPluginTool(t *testing.T, store memorysdk.Store, name string) engine.AgentTool {
	t.Helper()
	registry := extensions.NewRegistry(t.TempDir())
	mustOK(registry.Register("<inline:memory>", MemoryWithStore(store)))
	manager := must(sessionstore.InMemory(t.TempDir()))
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{
		SessionManager: manager,
		Actions: extensions.Actions{
			GetActiveTools: func() ([]string, error) { return []string{name}, nil },
		},
	})
	for _, registered := range runner.AllRegisteredTools() {
		if registered.Definition.Name == name {
			return extensions.WrapRegisteredTool(registered, runner)
		}
	}
	t.Fatalf("memory tool %q missing", name)
	return nil
}

func pluginTool(t *testing.T, plugin, tool string, options Options, runnerOptions extensions.RunnerOptions) engine.AgentTool {
	t.Helper()
	registry := extensions.NewRegistry(t.TempDir())
	factory := Catalog(options)[plugin]
	if factory == nil {
		t.Fatalf("plugin %q missing", plugin)
	}
	mustOK(registry.Register("<inline:"+plugin+">", factory))
	manager := must(sessionstore.InMemory(t.TempDir()))
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

func newMemoryPluginSession(
	t *testing.T,
	provider *faux.Provider,
	factory extensions.Factory,
	settings *config.SettingsManager,
) *agent.AgentSession {
	t.Helper()
	root := t.TempDir()
	manager := must(sessionstore.InMemory(root))
	return newMemoryPluginSessionWithManager(t, provider, factory, settings, manager)
}

func newMemoryPluginSessionWithManager(
	t *testing.T,
	provider *faux.Provider,
	factory extensions.Factory,
	settings *config.SettingsManager,
	manager *sessionstore.SessionManager,
) *agent.AgentSession {
	t.Helper()
	root := manager.GetCWD()
	agentDir := filepath.Join(root, "agent")
	if settings == nil {
		settings = must(config.NewSettingsManager(root, config.WithAgentDir(agentDir)))
	}
	registry := extensions.NewRegistry(root)
	mustOK(registry.Register("<inline:memory>", factory))
	prompt := "memory test"
	result := must(agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: root, AgentDir: agentDir, Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &agent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	}))
	t.Cleanup(result.Session.Dispose)
	return result.Session
}

func newPermissionsSession(t *testing.T, provider *faux.Provider, policy *Policy, enabled ...string) *agent.AgentSession {
	t.Helper()
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	manager := must(sessionstore.InMemory(root))
	registry := extensions.NewRegistry(root)
	catalog := Catalog(Options{StreamFn: provider.StreamSimple, Policy: policy})
	for _, name := range append([]string{"permissions"}, enabled...) {
		mustOK(registry.Register("<inline:"+name+">", catalog[name]))
	}
	prompt := "permissions test"
	result := must(agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: root, AgentDir: filepath.Join(root, "agent"), Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &agent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	}))
	t.Cleanup(result.Session.Dispose)
	return result.Session
}

func containsName(names []string, want string) bool { return slices.Contains(names, want) }

func response(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func redirect(location string) *http.Response {
	return &http.Response{StatusCode: http.StatusFound, Status: http.StatusText(http.StatusFound), Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader(""))}
}

func newSubagentParent(t *testing.T, provider *faux.Provider) *agent.AgentSession {
	t.Helper()
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(root+"/agent")))
	manager := must(sessionstore.InMemory(root))
	registry := extensions.NewRegistry(root)
	mustOK(registry.Register("<inline:subagents>", Catalog(Options{StreamFn: provider.StreamSimple})["subagents"]))
	prompt := "parent"
	result := must(agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: root, AgentDir: root + "/agent", Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &agent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	}))
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

// Dispose while a fan-out is in flight used to panic the host: children read the
// parent extensions.Context from their own goroutines, and every accessor panics
// once the session is torn down.
func TestSubagentSurvivesDisposeMidFanOut(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(10)})
	parent := newSubagentParent(t, provider)
	started := make(chan struct{}, 8)
	steps := []faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "parallel", "tasks": []any{
			map[string]any{"task": "a"}, map[string]any{"task": "b"},
			map[string]any{"task": "c"}, map[string]any{"task": "d"},
			map[string]any{"task": "e"}, map[string]any{"task": "f"},
		}})),
	}
	for index := 0; index < 6; index++ {
		steps = append(steps, faux.Factory(func(ctx context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}))
	}
	steps = append(steps, faux.AssistantMessage("parent done"))
	provider.SetResponses(steps)

	go func() { _ = parent.PromptSync(context.Background(), "fan out") }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("no child started")
	}
	time.Sleep(200 * time.Millisecond)
	parent.Dispose()
	// Late children keep touching the stale context after Dispose returns.
	time.Sleep(500 * time.Millisecond)
}

// tasks is model-controlled, and every entry costs a goroutine, a temp dir, a
// session, and a provider call, so the width is capped in Execute and not only
// by the schema a host may or may not enforce.
func TestSubagentParallelWidthIsCapped(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(10)})
	root := t.TempDir()
	registry := extensions.NewRegistry(root)
	mustOK(registry.Register("<inline:subagents>", Catalog(Options{StreamFn: provider.StreamSimple})["subagents"]))
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI})
	definition := runner.ToolDefinition("subagent")
	require(t, definition != nil, "subagent tool missing")
	tasks := make([]any, maxParallelTasks+1)
	for index := range tasks {
		tasks[index] = map[string]any{"task": "t"}
	}
	_, err := definition.Execute(
		context.Background(), "call", map[string]any{"mode": "parallel", "tasks": tasks},
		nil, runner.CreateContext(),
	)
	requireError(t, err, "at most")
	require(t, provider.State().CallCount == 0, "an over-wide fan-out reached the provider %d times", provider.State().CallCount)
}

// Command words widen the path candidate set, and rules are last-match-wins, so
// widening an allow would let it override an earlier deny.
func TestPermissionsCommandPathsDoNotWidenAllowRules(t *testing.T) {
	policy := &Policy{Mode: "enforce", Rules: []Rule{
		{Path: "secrets.txt", Action: Deny},
		{Path: "src/**", Action: Allow},
	}}
	info := ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "cat src/a.go && cat secrets.txt"}}
	decision := policy.Evaluate(context.Background(), info)
	require(t, decision.Action == Deny, "action = %q, want deny: an allow widened by command words outranked the deny", decision.Action)
}

func TestSubagentExternalObjectFormTogglesWithoutLosingCommands(t *testing.T) {
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	settings.SetPluginSetting("subagents", "external", map[string]any{
		"claude": map[string]any{"command": "/bin/cat", "enabled": false},
		"codex":  "/bin/cat",
	})
	entries, err := externalSubagentEntries(settings)
	require(t, err == nil && len(entries) == 2, "entries = %#v, %v", entries, err)
	require(t, !entries["claude"].Enabled && entries["claude"].Command == "/bin/cat", "claude = %#v", entries["claude"])
	require(t, entries["codex"].Enabled, "codex = %#v", entries["codex"])
	enabled, err := externalSubagents(settings)
	require(t, err == nil && len(enabled) == 1 && enabled["codex"] == "/bin/cat", "enabled = %#v, %v", enabled, err)
	settings.SetPluginSetting("subagents", "external", map[string]any{
		"bad": map[string]any{"command": "x", "typo": true},
	})
	_, err = externalSubagentEntries(settings)
	require(t, err != nil && strings.Contains(err.Error(), "must be a command or {command, enabled}"), "error = %v", err)
}

func TestToggleExternalCLIPreservesCommandsAndAddsKnownCLIs(t *testing.T) {
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	settings.SetPluginSetting("subagents", "external", map[string]any{"claude": "/bin/cat"})
	mustOK(toggleExternalCLI(settings, "claude"))
	entries := must(externalSubagentEntries(settings))
	require(t, !entries["claude"].Enabled && entries["claude"].Command == "/bin/cat", "after off: %#v", entries)
	mustOK(toggleExternalCLI(settings, "claude"))
	raw := settingsObjectValue(settings.GetPluginSettings("subagents")["external"])
	require(t, raw["claude"] == "/bin/cat", "re-enabled entry should collapse to the string form: %#v", raw)
	mustOK(toggleExternalCLI(settings, "codex"))
	entries = must(externalSubagentEntries(settings))
	require(t, entries["codex"].Enabled && strings.HasPrefix(entries["codex"].Command, "codex exec"), "detected add: %#v", entries)
	require(t, toggleExternalCLI(settings, "nope") != nil, "unknown CLI must be rejected")
	require(t, len(settings.DrainErrors()) == 0, "settings errors")
}
