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
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	memorysdk "github.com/OrdalieTech/pigo/memory"
)

type widgetUI struct {
	extensions.NoopUI
	mu    sync.Mutex
	lines []string
	shown int
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
	details, ok := result.Details.(todoInput)
	if !ok || len(details.Items) != 1 || details.Items[0].Text != "ship" {
		t.Fatalf("result details = %#v", result.Details)
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
	_, err := tool.Execute(context.Background(), "search", map[string]any{"query": "pigo"}, nil)
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
	if _, err := tool.Execute(context.Background(), "search", map[string]any{"query": "pigo"}, nil); err != nil {
		t.Fatal(err)
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
	mu       sync.Mutex
	items    []memorysdk.Item
	searched bool
}

// plainMemoryStore is a Store without SemanticSearcher, so recall takes the
// substring-then-word-overlap path.
type plainMemoryStore struct{ items []memorysdk.Item }

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

func TestRecallFallsBackToWordOverlap(t *testing.T) {
	store := &plainMemoryStore{items: []memorysdk.Item{
		{ID: "tabs", Content: "The user prefers tabs over spaces."},
		{ID: "deploy", Content: "Deploys run on Fridays."},
	}}
	for _, test := range []struct{ query, want string }{
		{query: "indentation preference", want: "tabs"},
		{query: "Fridays", want: "deploy"},
		{query: "prefers tabs", want: "tabs"},
	} {
		items, err := recallItems(context.Background(), store, test.query, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].ID != test.want {
			t.Fatalf("recall(%q) = %#v, want %q", test.query, items, test.want)
		}
	}
	items, err := recallItems(context.Background(), store, "kubernetes rollout", nil, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("unrelated query matched %#v (%v)", items, err)
	}
}

func (store *memoryTestStore) Append(_ context.Context, item memorysdk.Item) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	item.ID = fmt.Sprintf("custom-%d", len(store.items)+1)
	if item.Time.IsZero() {
		item.Time = time.Date(2026, 7, 23, 10, len(store.items), 0, 0, time.UTC)
	}
	store.items = append(store.items, item)
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

func TestMemoryWithStoreRememberRecallThroughRegistry(t *testing.T) {
	store := &memoryTestStore{}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var recalled string
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("remember", map[string]any{
			"content": "The durable marker is cobalt.", "tags": []any{" Project ", "PROJECT"},
		}, faux.ToolCallOptions{ID: "memory-1"})),
		faux.AssistantMessage(faux.ToolCall("recall", map[string]any{
			"query": "cobalt", "tags": []any{"project"},
		}, faux.ToolCallOptions{ID: "memory-2"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			recalled = toolResultText(request, "recall")
			return faux.AssistantMessage("done"), nil
		}),
	})
	session := newMemoryPluginSession(t, provider, MemoryWithStore(store), nil)
	if err := session.PromptSync(context.Background(), "remember and recall"); err != nil {
		t.Fatal(err)
	}
	items, searched := store.snapshot()
	if len(items) != 1 || strings.Join(items[0].Tags, ",") != "project" || !searched {
		t.Fatalf("custom store items = %#v, semantic searched = %t", items, searched)
	}
	if !strings.Contains(recalled, "The durable marker is cobalt.") {
		t.Fatalf("recall result = %q", recalled)
	}
}

func TestMemorySessionStartInjectionModes(t *testing.T) {
	for _, inject := range []string{"index", "none"} {
		t.Run(inject, func(t *testing.T) {
			store := &memoryTestStore{}
			if _, err := store.Append(context.Background(), memorysdk.Item{Content: "startup marker"}); err != nil {
				t.Fatal(err)
			}
			provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
			sawMarker := false
			provider.SetResponses([]faux.ResponseStep{
				faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
					sawMarker = contextContains(request, "startup marker")
					return faux.AssistantMessage("done"), nil
				}),
			})
			root := t.TempDir()
			settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
			if err != nil {
				t.Fatal(err)
			}
			var factory extensions.Factory
			if inject == "none" {
				settings.SetPluginSetting("memory", "inject", "none")
				factory = memoryExtension(store, provider.StreamSimple, settings, "")
			} else {
				factory = MemoryWithStore(store)
			}
			session := newMemoryPluginSession(t, provider, factory, settings)
			if err := session.PromptSync(context.Background(), "inspect context"); err != nil {
				t.Fatal(err)
			}
			indexEntries := 0
			for _, entry := range session.Manager().GetEntries() {
				if entry.CustomType == "pigo.memory.index" {
					indexEntries++
				}
			}
			want := inject == "index"
			if sawMarker != want || indexEntries != map[bool]int{false: 0, true: 1}[want] {
				t.Fatalf("saw marker = %t, index entries = %d, want injection = %t", sawMarker, indexEntries, want)
			}
		})
	}
}

func TestMemoryDistillUsesInjectedStream(t *testing.T) {
	store := &memoryTestStore{}
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	settings.SetPluginSetting("memory", "inject", "none")
	settings.SetPluginSetting("memory", "distill", true)
	settings.SetPluginSetting("memory", "distillPrompt", "CUSTOM DISTILL")
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var sawPrompt, sawTranscript bool
	provider.SetResponses([]faux.ResponseStep{
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			sawPrompt = request.SystemPrompt != nil && *request.SystemPrompt == "CUSTOM DISTILL"
			sawTranscript = contextContains(request, "persistent transcript fact")
			return faux.AssistantMessage("- distilled one\n- distilled two"), nil
		}),
	})
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(&ai.UserMessage{Content: ai.NewUserText("persistent transcript fact")}); err != nil {
		t.Fatal(err)
	}
	factory := memoryExtension(store, provider.StreamSimple, settings, "")
	session := newMemoryPluginSessionWithManager(t, provider, factory, settings, manager)
	session.Dispose()
	items, _ := store.snapshot()
	if !sawPrompt || !sawTranscript || len(items) != 2 {
		t.Fatalf("prompt = %t, transcript = %t, items = %#v", sawPrompt, sawTranscript, items)
	}
	for _, item := range items {
		if strings.Join(item.Tags, ",") != "distilled" {
			t.Fatalf("distilled tags = %v", item.Tags)
		}
	}
}

func TestMemoryIndexCapPreservesUTF8(t *testing.T) {
	index := renderMemoryIndex([]memorysdk.Item{{
		Time: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC), Content: strings.Repeat("é", 100),
	}}, 41)
	if len(index) > 41 || !utf8.ValidString(index) {
		t.Fatalf("index len = %d, valid UTF-8 = %t", len(index), utf8.ValidString(index))
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
