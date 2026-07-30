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
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/codingagent"
	"github.com/OrdalieTech/orb/codingagent/config"
	"github.com/OrdalieTech/orb/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/orb/codingagent/session"
	memorysdk "github.com/OrdalieTech/orb/memory"
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

func (theme *countingTaskTheme) FG(_ string, text string) string {
	theme.calls++
	return text
}

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

func (*taskWidgetHost) Width() int  { return 80 }
func (*taskWidgetHost) Height() int { return 24 }
func (host *taskWidgetHost) Invalidate() {
	host.invalidations++
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
			name:   "canonical rule path",
			policy: &Policy{Rules: []Rule{{Path: filepath.Join(link, "*"), Action: Deny}}},
			info:   ToolCallInfo{Tool: "custom", Args: map[string]any{"path": filepath.Join(realDir, "file")}, CWD: root}, want: Deny,
		},
		{
			name:   "path rule matches a path inside a bash command",
			policy: &Policy{Rules: []Rule{{Path: "secrets.txt", Action: Deny}}},
			info:   ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "cat secrets.txt"}, CWD: root}, want: Deny,
		},
		{
			name:   "path rule ignores unrelated bash commands",
			policy: &Policy{Rules: []Rule{{Path: "secrets.txt", Action: Deny}}},
			info:   ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "ls -la"}, CWD: root}, want: Allow,
		},
		{
			name:   "path rule matches a redirect target",
			policy: &Policy{Rules: []Rule{{Path: "*.env", Action: Deny}}},
			info:   ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "echo TOKEN=1 > prod.env"}, CWD: root}, want: Deny,
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
		if entry.CustomType == "orb.permissions.decision" {
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

func (ui *widgetUI) showCount() int {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.shown
}

func (ui *widgetUI) widgetFactory() extensions.ComponentFactory {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.factory
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
	if got, want := strings.Join(ui.snapshot(), "\n"), "✓ 1/2  → implement"; got != want {
		t.Fatalf("widget = %q, want %q", got, want)
	}
	factory := ui.widgetFactory()
	if factory == nil {
		t.Fatal("TUI task widget has no click renderer")
	}
	host := &taskWidgetHost{}
	component := factory(host, nil)
	mouse, ok := component.(tui.MouseHandler)
	if !ok || !mouse.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Clicks: 1}) {
		t.Fatal("task widget did not accept a left click")
	}
	expanded := strings.Join(component.Render(80), "\n")
	if !strings.Contains(expanded, "[x] inspect") || !strings.Contains(expanded, "→ [ ] implement") || host.invalidations != 1 {
		t.Fatalf("expanded task widget = %q, invalidations = %d", expanded, host.invalidations)
	}
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
	if len(styledLines) < 3 || !strings.Contains(strings.Join(styledLines, "\n"), "\x1b[2m") || !strings.HasPrefix(styledLines[1], "    ") {
		t.Fatalf("styled expanded task widget = %#v", styledLines)
	}

	result, err = tool.Execute(context.Background(), "todo-2", map[string]any{"items": []any{
		map[string]any{"text": "ship", "status": "pending"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ai.ContentText(result.Content); got != "[ ] ship" || strings.Join(ui.snapshot(), "\n") != "✓ 0/1  ·  +1 queued" {
		t.Fatalf("replacement result = %q widget = %q", got, strings.Join(ui.snapshot(), "\n"))
	}
	details, ok := result.Details.(todoInput)
	if !ok || len(details.Items) != 1 || details.Items[0].Text != "ship" {
		t.Fatalf("result details = %#v", result.Details)
	}
}

func TestTaskWidgetCachesStableRenders(t *testing.T) {
	theme := &countingTaskTheme{}
	widget := newTaskWidget([]todoItem{{Text: "inspect", Status: "done"}, {Text: "implement", Status: "in_progress"}}, nil, theme)
	widget.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Clicks: 1})
	first := widget.Render(80)
	calls := theme.calls
	second := widget.Render(80)
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Fatalf("cached render changed: %#v != %#v", first, second)
	}
	if theme.calls != calls {
		t.Fatalf("stable render restyled tasks: calls %d -> %d", calls, theme.calls)
	}
	widget.Render(40)
	if theme.calls == calls {
		t.Fatal("width change reused a stale task render")
	}
	calls = theme.calls
	widget.Invalidate()
	widget.Render(40)
	if theme.calls == calls {
		t.Fatal("invalidation reused stale themed task lines")
	}
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
	if theme.calls == calls {
		t.Fatal("concurrent invalidation allowed stale task lines back into the cache")
	}
}

func TestTasksRebuildFromBranchDetails(t *testing.T) {
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, items := range []string{`[{"text":"first","status":"done"}]`, `[{"text":"second","status":"pending"}]`} {
		if _, err := manager.AppendMessage(&ai.ToolResultMessage{
			ToolName: "todo", Content: ai.ToolResultContent{&ai.TextContent{Text: "ok"}},
			Details: json.RawMessage(`{"items":` + items + `}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	restored := todosFromBranch(manager)
	if len(restored) != 1 || restored[0].Text != "second" || restored[0].Status != "pending" {
		t.Fatalf("restored = %#v", restored)
	}
	if items := todosFromBranch(nil); items != nil {
		t.Fatalf("nil manager returned %#v", items)
	}
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
			result, err := tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil)
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
	stubDNS(t, map[string]string{"example.test": "93.184.216.34"})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(http.StatusOK, "text/html", `<html><style>no</style><body><h1>Hello &amp; hi</h1><script>no</script><p>Readable text.</p></body></html>`), nil
	})}
	fetch := pluginTool(t, "websearch", "fetch_content", Options{HTTPClient: client}, extensions.RunnerOptions{})
	result, err := fetch.Execute(context.Background(), "fetch", map[string]any{"url": "https://example.test/page"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Block tags keep their line breaks so oversized pages stay truncatable.
	if got := ai.ContentText(result.Content); got != "Hello & hi\nReadable text." {
		t.Fatalf("content = %q", got)
	}
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
	if page.Len() <= 50<<10 {
		t.Fatalf("fixture is only %d bytes", page.Len())
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "text/html", page.String()), nil
	})}
	tool := pluginTool(t, "websearch", "fetch_content", Options{HTTPClient: client}, extensions.RunnerOptions{})
	result, err := tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://example.test/big"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := ai.ContentText(result.Content)
	if !strings.Contains(got, "Paragraph 0 carries") || len(got) < 40<<10 {
		t.Fatalf("large page returned %d bytes: %.120q", len(got), got)
	}
	if strings.Count(got, "\n") < 100 {
		t.Fatalf("page collapsed onto %d lines", strings.Count(got, "\n")+1)
	}
	if !strings.HasSuffix(got, "[output truncated]") {
		t.Fatalf("missing truncation marker: %.120q", got[max(0, len(got)-120):])
	}
	// A page with no break at all still has to yield its head, not just the marker.
	if head := truncateWeb(strings.Repeat("x", 60<<10)); len(head) < 40<<10 {
		t.Fatalf("unbreakable line truncated to %d bytes", len(head))
	}
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
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if requests != test.wantRequests {
				t.Fatalf("issued %d requests, want %d", requests, test.wantRequests)
			}
		})
	}
	if _, err := tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/ok"}, nil); err != nil {
		t.Fatalf("public destination rejected: %v", err)
	}
}

func TestFetchContentDecodesCharsetAndRejectsBinary(t *testing.T) {
	stubDNS(t, map[string]string{"public.test": "93.184.216.34"})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/binary" {
			return response(http.StatusOK, "image/png", "\x89PNG\r\n\x1a\n\xff\xfe"), nil
		}
		// 0x92 is a right single quote in windows-1252 and invalid UTF-8.
		return response(http.StatusOK, "text/html; charset=windows-1252", "<p>caf\xe9 owner\x92s</p>"), nil
	})}
	tool := pluginTool(t, "websearch", "fetch_content", Options{HTTPClient: client}, extensions.RunnerOptions{})
	result, err := tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/legacy"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ai.ContentText(result.Content); got != "café owner’s" || !utf8.ValidString(got) {
		t.Fatalf("decoded = %q", got)
	}
	if _, err := tool.Execute(context.Background(), "fetch", map[string]any{"url": "https://public.test/binary"}, nil); err == nil ||
		!strings.Contains(err.Error(), "unsupported content type") {
		t.Fatalf("binary error = %v", err)
	}
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
	if err == nil || strings.Contains(err.Error(), "sk-SECRET") {
		t.Fatalf("error leaked the provider body: %v", err)
	}
}

func TestWebSearchHonoursConfiguredProvider(t *testing.T) {
	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"provider":"brave","exaApiKey":"exa-key","braveApiKey":"brave-key"}`
	if err := os.WriteFile(filepath.Join(home, ".pi", "web-search.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if !strings.Contains(request.URL.Host, "brave") {
			t.Fatalf("provider ignored: %s", request.URL)
		}
		return response(http.StatusOK, "application/json", `{"web":{"results":[]}}`), nil
	})}
	tool := pluginTool(t, "websearch", "web_search", Options{HTTPClient: client}, extensions.RunnerOptions{})
	if _, err := tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWebSearchWithoutKeyReturnsActionableError(t *testing.T) {
	for _, key := range []string{"EXA_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", t.TempDir())
	tool := pluginTool(t, "websearch", "web_search", Options{}, extensions.RunnerOptions{})
	_, err := tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil)
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
	if _, err := tool.Execute(context.Background(), "search", map[string]any{"query": "orb"}, nil); err != nil {
		t.Fatal(err)
	}
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
	if err := session.PromptSync(context.Background(), "delegate"); err != nil {
		t.Fatal(err)
	}
	if !childReadAbsent {
		t.Fatal("read was advertised to the child despite the inherited deny rule")
	}
}

func TestSubagentClearsProgressWidgetAndFailsParallelRuns(t *testing.T) {
	ui := &widgetUI{}
	tool := pluginTool(t, "subagents", "subagent", Options{}, extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	// No parent model registry, so every child fails: the widget must still go.
	if _, err := tool.Execute(context.Background(), "sub-1", map[string]any{"mode": "single", "task": "work"}, nil); err == nil {
		t.Fatal("child without a model registry succeeded")
	}
	if ui.showCount() == 0 {
		t.Fatal("progress widget was never shown")
	}
	if lines := ui.snapshot(); len(lines) != 0 {
		t.Fatalf("progress widget left on screen: %v", lines)
	}

	_, err := tool.Execute(context.Background(), "sub-2", map[string]any{"mode": "parallel", "tasks": []any{
		map[string]any{"task": "alpha"}, map[string]any{"task": "beta"},
	}}, nil)
	if err == nil || !strings.Contains(err.Error(), "2 of 2 children failed") || !strings.Contains(err.Error(), "[2] worker") {
		t.Fatalf("parallel failure = %v", err)
	}
	if lines := ui.snapshot(); len(lines) != 0 {
		t.Fatalf("progress widget left on screen: %v", lines)
	}
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
	if _, err := tool.Execute(context.Background(), "remember-local", map[string]any{
		"target": "memory", "content": "Local profile marker.",
	}, nil); err != nil {
		t.Fatal(err)
	}
	store, err := memorysdk.NewFileStore(filepath.Join(agentDir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.Query(context.Background(), memorysdk.Filter{})
	if err != nil || len(items) != 1 || items[0].Content != "Local profile marker." {
		t.Fatalf("local items = %#v, error = %v", items, err)
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
	if err := <-gatedDone; err != nil {
		t.Fatal(err)
	}
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
		if err := <-done; err != nil {
			t.Fatal(err)
		}
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
	if err := session.PromptSync(context.Background(), "remember, recall, and forget"); err != nil {
		t.Fatal(err)
	}
	items, searched := store.snapshot()
	if len(items) != 0 || !rememberedTargeted || !searched {
		t.Fatalf("custom store items = %#v, targeted = %t, semantic searched = %t", items, rememberedTargeted, searched)
	}
	if recalled != "2026-07-23T10:00:00Z [project] The durable marker is cobalt." {
		t.Fatalf("recall result = %q", recalled)
	}
	if recalledAfterForget != "No memories found." {
		t.Fatalf("recall after forget = %q", recalledAfterForget)
	}
}

func TestMemoryRememberDoesNotDuplicateRecentExactContent(t *testing.T) {
	store := &memoryTestStore{}
	tool := memoryPluginTool(t, store, "remember")
	first, err := tool.Execute(context.Background(), "remember-first", map[string]any{
		"target": "memory", "content": " Stable project fact. ", "tags": []any{"project"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tool.Execute(context.Background(), "remember-second", map[string]any{
		"target": "memory", "content": "Stable project fact.", "tags": []any{"project", "duplicate-attempt"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := store.snapshot()
	if len(items) != 1 || !strings.HasPrefix(ai.ContentText(first.Content), "Remembered custom-1") || ai.ContentText(second.Content) != "Already remembered custom-1." {
		t.Fatalf("items = %#v, first = %q, second = %q", items, ai.ContentText(first.Content), ai.ContentText(second.Content))
	}
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
			if _, err := tool.Execute(context.Background(), "forget-test", map[string]any{"query": test.query}, nil); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("forget(%q) error = %v, want %q", test.query, err, test.want)
			}
		})
	}
	if _, err := tool.Execute(context.Background(), "forget-tabs", map[string]any{
		"target": "user", "query": "code", "tags": []any{" GO ", "go"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	items, _ := store.snapshot()
	if len(items) != 1 || items[0].ID != "spaces" {
		t.Fatalf("items after forget = %#v", items)
	}
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
		if test.name != "recall" && spec.ExecutionMode != agent.ToolExecutionSequential {
			t.Fatalf("%s execution mode = %q, want sequential", test.name, spec.ExecutionMode)
		}
		text := spec.Description + " " + string(spec.Parameters)
		for _, want := range test.want {
			if !strings.Contains(text, want) {
				t.Fatalf("%s guidance %q does not contain %q", test.name, text, want)
			}
		}
	}
}

func TestMemoryProfileMemoryCapacity(t *testing.T) {
	store := &memoryTestStore{}
	tool := memoryPluginTool(t, store, "remember")
	if _, err := tool.Execute(context.Background(), "remember-memory-full", map[string]any{
		"target": "memory", "content": strings.Repeat("m", memoryChars),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), "remember-memory-overflow", map[string]any{
		"target": "memory", "content": "x",
	}, nil); err == nil || !strings.Contains(err.Error(), "2200/2200") {
		t.Fatalf("memory overflow error = %v", err)
	}
}

func TestMemoryProfileCapacityAndReplacement(t *testing.T) {
	store := &memoryTestStore{}
	remember := memoryPluginTool(t, store, "remember")
	full := strings.Repeat("é", userMemoryChars)
	if _, err := remember.Execute(context.Background(), "remember-full", map[string]any{
		"target": "user", "content": full,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := remember.Execute(context.Background(), "remember-overflow", map[string]any{
		"target": "user", "content": "x",
	}, nil); err == nil || !strings.Contains(err.Error(), "1375/1375") || !strings.Contains(err.Error(), "replace or forget") {
		t.Fatalf("overflow error = %v", err)
	}
	replace := memoryPluginTool(t, store, "replace")
	result, err := replace.Execute(context.Background(), "replace-full", map[string]any{
		"target": "user", "old_text": strings.Repeat("é", 20),
		"content": "User prefers concise replies.", "tags": []any{"style"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := store.snapshot()
	if len(items) != 1 || items[0].Content != "User prefers concise replies." ||
		!hasMemoryTags(items[0].Tags, []string{"user", userTargetTag, "style"}) {
		t.Fatalf("items after replace = %#v", items)
	}
	if got := store.operationSnapshot(); !slices.Equal(got, []string{"append:custom-1", "append:custom-2", "delete:custom-1"}) {
		t.Fatalf("store operations = %v", got)
	}
	if !strings.HasPrefix(ai.ContentText(result.Content), "Replaced custom-1 with custom-2") {
		t.Fatalf("replace result = %q", ai.ContentText(result.Content))
	}
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
	if err := session.PromptSync(context.Background(), "first prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), memorysdk.Item{Content: "late marker", Tags: []string{memoryTargetTag}}); err != nil {
		t.Fatal(err)
	}
	if err := session.PromptSync(context.Background(), "second prompt"); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 || prompts[0] != prompts[1] {
		t.Fatalf("system prompts changed: %#v", prompts)
	}
	for _, want := range []string{
		"Persistent curated memory", "USER PROFILE [", "User prefers terse replies.",
		"MEMORY [", "Project uses Go 1.26.", "Legacy untagged facts remain memory.",
	} {
		if !strings.Contains(prompts[0], want) {
			t.Fatalf("system prompt does not contain %q:\n%s", want, prompts[0])
		}
	}
	for _, unwanted := range []string{"late marker", userTargetTag, memoryTargetTag, "2026-07-23"} {
		if strings.Contains(prompts[0], unwanted) {
			t.Fatalf("system prompt contains %q:\n%s", unwanted, prompts[0])
		}
	}
}

func memoryPluginTool(t *testing.T, store memorysdk.Store, name string) agent.AgentTool {
	t.Helper()
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<inline:memory>", MemoryWithStore(store)); err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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

func newMemoryPluginSession(
	t *testing.T,
	provider *faux.Provider,
	factory extensions.Factory,
	settings *config.SettingsManager,
) *codingagent.AgentSession {
	t.Helper()
	root := t.TempDir()
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	return newMemoryPluginSessionWithManager(t, provider, factory, settings, manager)
}

func newMemoryPluginSessionWithManager(
	t *testing.T,
	provider *faux.Provider,
	factory extensions.Factory,
	settings *config.SettingsManager,
	manager *sessionstore.SessionManager,
) *codingagent.AgentSession {
	t.Helper()
	root := manager.GetCWD()
	agentDir := filepath.Join(root, "agent")
	if settings == nil {
		var err error
		settings, err = config.NewSettingsManager(root, config.WithAgentDir(agentDir))
		if err != nil {
			t.Fatal(err)
		}
	}
	registry := extensions.NewRegistry(root)
	if err := registry.Register("<inline:memory>", factory); err != nil {
		t.Fatal(err)
	}
	prompt := "memory test"
	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
		CWD: root, AgentDir: agentDir, Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &codingagent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(result.Session.Dispose)
	return result.Session
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

func redirect(location string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusFound, Status: http.StatusText(http.StatusFound),
		Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader("")),
	}
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
	if err := registry.Register("<inline:subagents>", Catalog(Options{StreamFn: provider.StreamSimple})["subagents"]); err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI})
	definition := runner.ToolDefinition("subagent")
	if definition == nil {
		t.Fatal("subagent tool missing")
	}
	tasks := make([]any, maxParallelTasks+1)
	for index := range tasks {
		tasks[index] = map[string]any{"task": "t"}
	}
	_, err := definition.Execute(
		context.Background(), "call", map[string]any{"mode": "parallel", "tasks": tasks},
		nil, runner.CreateContext(),
	)
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("expected a width-cap refusal, got %v", err)
	}
	if provider.State().CallCount != 0 {
		t.Fatalf("an over-wide fan-out reached the provider %d times", provider.State().CallCount)
	}
}

// Command words widen the path candidate set, and rules are last-match-wins, so
// widening an allow would let it override an earlier deny.
func TestPermissionsCommandPathsDoNotWidenAllowRules(t *testing.T) {
	policy := &Policy{Mode: "enforce", Rules: []Rule{
		{Path: "secrets.txt", Action: Deny},
		{Path: "src/**", Action: Allow},
	}}
	info := ToolCallInfo{Tool: "bash", Args: map[string]any{"command": "cat src/a.go && cat secrets.txt"}}
	if decision := policy.Evaluate(context.Background(), info); decision.Action != Deny {
		t.Fatalf("action = %q, want deny: an allow widened by command words outranked the deny", decision.Action)
	}
}
