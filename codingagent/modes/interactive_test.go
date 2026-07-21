package modes

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"

	theme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
)

func initTestTheme(t *testing.T) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	darkJSON := filepath.Join(filepath.Dir(file), "theme", "dark.json")
	data, err := os.ReadFile(darkJSON)
	if err != nil {
		t.Fatal("reading dark.json:", err)
	}
	parsed, err := theme.Parse("dark", data, theme.TrueColor)
	if err != nil {
		t.Fatal("parsing dark theme:", err)
	}
	theme.SetCurrent(parsed)
	t.Cleanup(func() { theme.SetCurrent(nil) })
}

func TestParseSlashCommand(t *testing.T) {
	tests := []struct {
		input string
		name  string
		args  string
	}{
		{"/quit", "quit", ""},
		{"/model claude-3", "model", "claude-3"},
		{"/compact   custom instructions  ", "compact", "custom instructions"},
		{"/name", "name", ""},
	}
	for _, tt := range tests {
		name, args := parseSlashCommand(tt.input)
		if name != tt.name || args != tt.args {
			t.Errorf("parseSlashCommand(%q) = (%q, %q), want (%q, %q)", tt.input, name, args, tt.name, tt.args)
		}
	}
}

func TestInteractiveModeInstallsExactResourceLoaderThemeObject(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	builtin, err := os.ReadFile(filepath.Join(filepath.Dir(file), "theme", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	themePath := filepath.Join(cwd, "extension-theme.json")
	if err := os.WriteFile(themePath, []byte(strings.Replace(string(builtin), `"name": "dark"`, `"name": "extension-theme"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings, NoThemes: true,
		AdditionalThemePaths: []string{themePath}, NoContextFiles: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	loaded := loader.GetThemes().Themes
	if len(loaded) != 1 {
		t.Fatalf("loaded themes = %#v", loaded)
	}
	settings.SetTheme("extension-theme")
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	sessionRuntime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings, ResourceLoader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessionRuntime.Dispose)
	mode := &InteractiveMode{session: sessionRuntime, ui: tui.NewTUI(newFakeTerminal(80, 24)), cwd: cwd}
	if err := mode.initializeTheme(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { theme.SetCurrent(nil) })
	registered, found := mode.themeRegistry.Get("extension-theme")
	if !found || registered != loaded[0] || mode.themeController.Current() != loaded[0] || theme.Current() != loaded[0] {
		t.Fatalf("resource theme identity: found=%t registered=%p loaded=%p controller=%p current=%p",
			found, registered, loaded[0], mode.themeController.Current(), theme.Current())
	}
}

func TestInteractiveModeResourceThemeRefreshReplacesStaleThemesAndAppliesSettings(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	builtin, err := os.ReadFile(filepath.Join(filepath.Dir(file), "theme", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	parse := func(name string) *theme.Theme {
		t.Helper()
		parsed, parseErr := theme.Parse(name, []byte(strings.Replace(string(builtin), `"name": "dark"`, `"name": "`+name+`"`, 1)), theme.TrueColor)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return parsed
	}
	themeA, themeB := parse("theme-a"), parse("theme-b")
	loaded := []*theme.Theme{themeA}
	loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings, NoThemes: true, NoContextFiles: true,
		ThemesOverride: func(codingagent.ResourceThemesResult) codingagent.ResourceThemesResult {
			return codingagent.ResourceThemesResult{Themes: append([]*theme.Theme(nil), loaded...)}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	settings.SetTheme("theme-b")
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	sessionRuntime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings, ResourceLoader: loader,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessionRuntime.Dispose)
	mode := &InteractiveMode{session: sessionRuntime, ui: tui.NewTUI(newFakeTerminal(80, 24)), cwd: cwd}
	if err := mode.initializeTheme(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { theme.SetCurrent(nil) })

	loaded = []*theme.Theme{themeB}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := mode.extendExtensionThemes(); err != nil {
		t.Fatal(err)
	}
	if _, found := mode.themeRegistry.Get("theme-a"); found {
		t.Error("theme-a remained registered after the loader replaced it")
	}
	registered, found := mode.themeRegistry.Get("theme-b")
	if !found || registered != themeB || mode.themeController.Current() != themeB || theme.Current() != themeB {
		t.Fatalf("refreshed theme identity: found=%t registered=%p loaded=%p controller=%p current=%p",
			found, registered, themeB, mode.themeController.Current(), theme.Current())
	}
}

func TestInteractiveModeRebindPropagatesInvalidResourceThemeName(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	newRuntime := func(loader codingagent.ResourceLoader) *codingagent.SessionRuntime {
		t.Helper()
		manager, managerErr := sessionstore.InMemory(cwd)
		if managerErr != nil {
			t.Fatal(managerErr)
		}
		created, runtimeErr := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
			Agent: agent.NewAgent(), SessionManager: manager, Settings: settings, ResourceLoader: loader,
		})
		if runtimeErr != nil {
			t.Fatal(runtimeErr)
		}
		t.Cleanup(created.Dispose)
		return created
	}
	initial := newRuntime(nil)
	mode := &InteractiveMode{
		session: initial, ui: tui.NewTUI(newFakeTerminal(80, 24)), cwd: cwd,
		keybindings: NewAppKeybindings(nil), inputCh: make(chan inputEntry, 1),
		toolComponents: make(map[string]*ToolExecutionComponent), footerStatuses: make(map[string]string),
	}
	if err := mode.init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { theme.SetCurrent(nil) })

	loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings, NoThemes: true, NoContextFiles: true,
		ThemesOverride: func(codingagent.ResourceThemesResult) codingagent.ResourceThemesResult {
			return codingagent.ResourceThemesResult{Themes: []*theme.Theme{{Name: "bad/name"}}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := mode.rebindHostSession(newRuntime(loader)); err == nil || !strings.Contains(err.Error(), "invalid theme name") {
		t.Fatalf("invalid loader theme rebind error = %v", err)
	}
}

func TestAuthProviderLabelDescribesConfiguredTypeAndSource(t *testing.T) {
	tests := []struct {
		name   string
		option InteractiveAuthProvider
		want   string
	}{
		{
			name: "different auth type",
			option: InteractiveAuthProvider{Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth,
				Status: &InteractiveAuthStatus{Type: aiauth.AuthTypeAPIKey, Source: "stored credential"}},
			want: "Anthropic • API key configured",
		},
		{
			name: "environment source",
			option: InteractiveAuthProvider{Name: "Groq", AuthType: aiauth.AuthTypeAPIKey,
				Status: &InteractiveAuthStatus{Type: aiauth.AuthTypeAPIKey, Source: "GROQ_API_KEY"}},
			want: "Groq ✓ env: GROQ_API_KEY",
		},
		{
			name:   "unconfigured",
			option: InteractiveAuthProvider{Name: "Google", AuthType: aiauth.AuthTypeAPIKey},
			want:   "Google • unconfigured",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := authProviderLabel(test.option, true); got != test.want {
				t.Fatalf("authProviderLabel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAuthMethodLabelUsesProviderOAuthLabel(t *testing.T) {
	option := InteractiveAuthProvider{
		Name: "xAI", AuthType: aiauth.AuthTypeOAuth,
		LoginLabel: "Sign in with SuperGrok or X Premium",
	}
	if got := authMethodLabel(option); got != option.LoginLabel {
		t.Fatalf("authMethodLabel() = %q, want %q", got, option.LoginLabel)
	}
	option.LoginLabel = ""
	if got := authMethodLabel(option); got != "Sign in with an account" {
		t.Fatalf("default OAuth label = %q", got)
	}
	option.AuthType = aiauth.AuthTypeAPIKey
	if got := authMethodLabel(option); got != "Sign in with an API key" {
		t.Fatalf("API-key label = %q", got)
	}
}

func TestDuplicateLoginProviderNamesRemainDistinct(t *testing.T) {
	options := []InteractiveAuthProvider{
		{ID: "first", Name: "Shared Provider", AuthType: aiauth.AuthTypeAPIKey},
		{ID: "second", Name: "Shared Provider", AuthType: aiauth.AuthTypeAPIKey},
	}
	matched := matchingAuthProviders(options, "shared provider")
	if len(matched) != 2 {
		t.Fatalf("matched providers = %#v", matched)
	}
	if allAuthOptionsForSameProvider(matched) {
		t.Fatal("duplicate display name was routed to auth-method selection")
	}
	items := authProviderSelectItems(matched, true)
	if len(items) != 2 || items[0].Value == items[1].Value {
		t.Fatalf("provider selector identities = %#v", items)
	}
	if items[0].Label != items[1].Label {
		t.Fatalf("duplicate upstream labels changed: %#v", items)
	}
}

func TestFormatMissingSessionCwdPrompt(t *testing.T) {
	err := &MissingSessionCwdError{SessionCWD: "/gone", FallbackCWD: "/current"}
	want := "cwd from session file does not exist\n/gone\n\ncontinue in current cwd\n/current"
	if got := formatMissingSessionCwdPrompt(err); got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestTreeEntryVisibleUsesConfiguredFilter(t *testing.T) {
	label := "keep"
	user := &sessionstore.SessionTreeNode{Entry: sessionstore.SessionEntry{Type: "message", Message: json.RawMessage(`{"role":"user","content":"hello"}`)}}
	tool := &sessionstore.SessionTreeNode{Entry: sessionstore.SessionEntry{Type: "message", Message: json.RawMessage(`{"role":"toolResult","content":[]}`)}}
	bookkeeping := &sessionstore.SessionTreeNode{Entry: sessionstore.SessionEntry{Type: "model_change"}, Label: &label}
	if !treeEntryVisible(user, false, "user-only") || treeEntryVisible(tool, false, "user-only") {
		t.Fatal("user-only filter did not isolate user messages")
	}
	if treeEntryVisible(tool, false, "no-tools") || treeEntryVisible(bookkeeping, false, "default") {
		t.Fatal("default/no-tools filter retained hidden entries")
	}
	if !treeEntryVisible(bookkeeping, false, "labeled-only") || !treeEntryVisible(bookkeeping, false, "all") {
		t.Fatal("labeled/all filter omitted requested entries")
	}
}

func TestUserMessageText(t *testing.T) {
	text := "hello"
	msg := &ai.UserMessage{Content: ai.NewUserText(text)}
	if got := userMessageText(msg); got != text {
		t.Errorf("userMessageText() = %q, want %q", got, text)
	}

	blocks := ai.NewUserContent(&ai.TextContent{Text: "a"}, &ai.TextContent{Text: "b"})
	msg2 := ai.UserMessage{Content: blocks}
	if got := userMessageText(msg2); got != "a\nb" {
		t.Errorf("userMessageText(blocks) = %q, want %q", got, "a\nb")
	}

	if got := userMessageText("not a message"); got != "" {
		t.Errorf("userMessageText(other) = %q, want empty", got)
	}
}

func TestAsAssistantMessage(t *testing.T) {
	msg := &ai.AssistantMessage{Model: "test"}
	if got := asAssistantMessage(msg); got != msg {
		t.Error("asAssistantMessage(*) should return same pointer")
	}

	val := ai.AssistantMessage{Model: "test2"}
	if got := asAssistantMessage(val); got == nil || got.Model != "test2" {
		t.Error("asAssistantMessage(val) should return pointer to copy")
	}

	if got := asAssistantMessage("other"); got != nil {
		t.Error("asAssistantMessage(other) should return nil")
	}
}

func TestUserMessageComponentRender(t *testing.T) {
	initTestTheme(t)
	comp := NewUserMessageComponent("hello", theme.MarkdownTheme(), 0)
	lines := comp.Render(40)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render")
	}
	found := false
	for _, line := range lines {
		if strings.Contains(line, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'hello' in render output")
	}
}

func TestAssistantMessageComponentRender(t *testing.T) {
	initTestTheme(t)
	msg := &ai.AssistantMessage{
		Content: ai.AssistantContent{&ai.TextContent{Text: "response"}},
	}
	comp := NewAssistantMessageComponent(msg, false, theme.MarkdownTheme(), "", 0)
	lines := comp.Render(40)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "response") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'response' in render output")
	}
}

func TestAssistantMessageComponentError(t *testing.T) {
	initTestTheme(t)
	errMsg := "something failed"
	msg := &ai.AssistantMessage{
		StopReason:   ai.StopReasonError,
		ErrorMessage: &errMsg,
	}
	comp := NewAssistantMessageComponent(msg, false, theme.MarkdownTheme(), "", 0)
	lines := comp.Render(60)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "something failed") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected error message in render output")
	}
}

func TestToolExecutionComponentLifecycle(t *testing.T) {
	initTestTheme(t)
	fake := &fakeRenderRequester{}
	comp := NewToolExecutionComponent("read", "call-1", map[string]any{"path": "/tmp"}, false, nil, fake, "/")
	lines := comp.Render(60)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render")
	}

	comp.MarkExecutionStarted()
	comp.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: "file content"}}, false, nil, false)
	lines = comp.Render(60)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "file content") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'file content' in render output after result")
	}
}

func TestDynamicBorderRender(t *testing.T) {
	initTestTheme(t)
	border := NewDynamicBorder()
	lines := border.Render(10)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "─") {
		t.Error("expected border character")
	}
	if want := theme.FG("border", strings.Repeat("─", 10)); lines[0] != want {
		t.Fatalf("default border = %q, want upstream border color %q", lines[0], want)
	}
}

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		count    int64
		expected string
	}{
		{500, "500"},
		{1500, "1.5k"},
		{1_500_000, "1.5M"},
	}
	for _, tt := range tests {
		if got := formatTokens(tt.count); got != tt.expected {
			t.Errorf("formatTokens(%d) = %q, want %q", tt.count, got, tt.expected)
		}
	}
}

func TestIdleStatusRender(t *testing.T) {
	idle := IdleStatus{}
	lines := idle.Render(20)
	if len(lines) != 2 {
		t.Errorf("IdleStatus should render 2 lines, got %d", len(lines))
	}
}

func TestStatusIndicatorCreation(t *testing.T) {
	fake := &fakeRenderRequester{}
	si := NewWorkingStatusIndicator(fake, "Testing...")
	if si.Kind != StatusWorking {
		t.Errorf("expected StatusWorking, got %s", si.Kind)
	}
	si.Dispose()

	si2 := NewRetryStatusIndicator(fake, 1, 3, 5000)
	if si2.Kind != StatusRetry {
		t.Errorf("expected StatusRetry, got %s", si2.Kind)
	}
	si2.Dispose()

	si3 := NewCompactionStatusIndicator(fake, "manual")
	if si3.Kind != StatusCompaction {
		t.Errorf("expected StatusCompaction, got %s", si3.Kind)
	}
	si3.Dispose()
}

func TestCompactionSummaryMessageRender(t *testing.T) {
	initTestTheme(t)
	comp := NewCompactionSummaryMessage("Summary text", 50000, theme.MarkdownTheme())
	lines := comp.Render(60)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render")
	}
}

func TestBranchSummaryMessageRender(t *testing.T) {
	initTestTheme(t)
	comp := NewBranchSummaryMessage("Branch summary", theme.MarkdownTheme())
	lines := comp.Render(60)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render")
	}
}

func TestSkillInvocationMessageRender(t *testing.T) {
	initTestTheme(t)
	comp := NewSkillInvocationMessage("test-skill", "Skill content", theme.MarkdownTheme())
	lines := comp.Render(60)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render")
	}
	comp.SetExpanded(true)
	expanded := comp.Render(60)
	if len(expanded) == 0 {
		t.Fatal("expected non-empty expanded render")
	}
}

func TestSkillInvocationInvalidateRebuildsTheme(t *testing.T) {
	theme.SetCurrent(nil)
	comp := NewSkillInvocationMessage("test-skill", "Skill content", theme.MarkdownTheme())
	initTestTheme(t)
	comp.Invalidate()
	if rendered := strings.Join(comp.Render(60), "\n"); !strings.Contains(rendered, theme.FG("customMessageLabel", theme.Bold("[skill]")+" ")) {
		t.Fatalf("invalidated render did not adopt current theme: %q", rendered)
	}
}

func TestRestoredSkillInvocationRendersSeparateUserMessage(t *testing.T) {
	for _, test := range []struct {
		name, suffix string
		children     int
	}{
		{name: "skill only", children: 1},
		{name: "with user message", suffix: "\n\n  inspect this  ", children: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := `<skill name="audit" location="/tmp/audit/SKILL.md">
References are relative to /tmp/audit.

Read the logs.
</skill>` + test.suffix
			mode := newPendingToolMode(t, []any{&ai.UserMessage{Content: ai.NewUserText(text)}})
			mode.renderInitialMessages()

			children := mode.chat.Children()
			if len(children) != test.children {
				t.Fatalf("children = %d, want %d (%T)", len(children), test.children, children[0])
			}
			skill, ok := children[0].(*SkillInvocationMessageComponent)
			if !ok || len(mode.expandables) != 1 || mode.expandables[0] != skill {
				t.Fatalf("skill child/expandables = %T/%#v", children[0], mode.expandables)
			}
			if test.suffix != "" {
				if _, ok := children[1].(*tui.Spacer); !ok {
					t.Fatalf("middle child = %T, want spacer", children[1])
				}
				if _, ok := children[2].(*UserMessageComponent); !ok {
					t.Fatalf("last child = %T, want user message", children[2])
				}
			}
			rendered := strings.Join(normalizeWP450Lines(mode.chat.Render(120)), "\n")
			if strings.Contains(rendered, "<skill") || strings.Contains(rendered, "Read the logs.") {
				t.Fatalf("collapsed render exposed raw skill: %q", rendered)
			}
			if !strings.Contains(rendered, "audit") || test.suffix != "" && !strings.Contains(rendered, "inspect this") {
				t.Fatalf("collapsed render = %q", rendered)
			}
			skill.SetExpanded(true)
			if expanded := strings.Join(normalizeWP450Lines(mode.chat.Render(120)), "\n"); !strings.Contains(expanded, "Read the logs.") {
				t.Fatalf("expanded render = %q", expanded)
			}
		})
	}
}

func TestAppKeybindings(t *testing.T) {
	kb := NewAppKeybindings(nil)
	if kb == nil {
		t.Fatal("expected non-nil keybindings")
	}
	keys := kb.Keys("app.interrupt")
	if len(keys) == 0 {
		t.Error("expected keys for app.interrupt")
	}
	if keys[0] != "escape" {
		t.Errorf("expected 'escape' for app.interrupt, got %q", keys[0])
	}
}

func TestCustomEditorInterceptor(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	ui := tui.NewTUI(terminal)
	kb := NewAppKeybindings(nil)
	tui.SetKeybindings(kb)
	editor := NewCustomEditor(ui, tui.EditorTheme{}, kb)

	handled := false
	editor.OnAction("app.clear", func() { handled = true })

	editor.HandleInput(tui.KeyEvent{Raw: "\x03"})
	if !handled {
		t.Error("expected app.clear handler to be called on Ctrl+C")
	}
}

func TestKeyText(t *testing.T) {
	kb := NewAppKeybindings(nil)
	tui.SetKeybindings(kb)
	text := KeyText("app.interrupt")
	if text == "" || text == "app.interrupt" {
		t.Error("expected resolved key text for app.interrupt")
	}
}

func TestThemePackageLevelAccessors(t *testing.T) {
	if text := theme.FG("dim", "test"); text != "test" {
		t.Errorf("FG with nil theme should return text as-is, got %q", text)
	}
	if text := theme.BG("selectedBg", "test"); text != "test" {
		t.Errorf("BG with nil theme should return text as-is, got %q", text)
	}

	initTestTheme(t)

	styled := theme.FG("dim", "test")
	if styled == "test" {
		t.Error("FG with active theme should apply styling")
	}
	if !strings.Contains(styled, "test") {
		t.Error("styled text should contain original")
	}
}

func TestHandleSlashCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		expected bool
	}{
		{"quit", "", true},
		{"compact", "", true},
		{"copy", "", true},
		{"name", "test", true},
		{"hotkeys", "", true},
		{"settings", "", true},
		{"model", "", true},
		{"export", "", true},
		{"session", "", true},
		{"changelog", "", true},
		{"login", "", true},
		{"logout", "", true},
		{"resume", "", true},
		{"reload", "", true},
		{"new", "", true},
		{"unknown", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			terminal := newFakeTerminal(80, 24)
			ui := tui.NewTUI(terminal)
			kb := NewAppKeybindings(nil)
			tui.SetKeybindings(kb)

			mode := &InteractiveMode{
				ui:             ui,
				chat:           &tui.Container{},
				keybindings:    kb,
				editor:         NewCustomEditor(ui, tui.EditorTheme{}, kb),
				toolComponents: make(map[string]*ToolExecutionComponent),
				footerStatuses: make(map[string]string),
				cwd:            "/tmp",
			}
			mode.interactiveUI = NewInteractiveUI(mode)
			mode.widgetAbove = &tui.Container{}
			mode.widgetBelow = &tui.Container{}

			// Skip commands that require a non-nil session
			needsSession := map[string]bool{
				"quit": true, "compact": true, "copy": true, "model": true,
				"export": true, "session": true, "fork": true, "name": true,
				"settings": true, "share": true, "tree": true,
			}
			if needsSession[tt.name] {
				return
			}

			got := mode.handleSlashCommand(tt.name, tt.args)
			if got != tt.expected {
				t.Errorf("handleSlashCommand(%q, %q) = %v, want %v", tt.name, tt.args, got, tt.expected)
			}
		})
	}
}

func TestBashExecutionComponentLifecycle(t *testing.T) {
	initTestTheme(t)
	fake := &fakeRenderRequester{}
	comp := NewBashExecutionComponent("echo hello", fake, false)
	lines := comp.Render(60)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render")
	}

	comp.AppendOutput("hello\n")
	lines = comp.Render(60)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'hello' in output")
	}

	exitCode := 0
	comp.SetComplete(&exitCode, false)
	lines = comp.Render(60)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render after complete")
	}
}

func TestBashExecutionComponentExcludeContext(t *testing.T) {
	initTestTheme(t)
	fake := &fakeRenderRequester{}
	comp := NewBashExecutionComponent("ls", fake, true)
	lines := comp.Render(60)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "!!") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected '!!' prefix for exclude-from-context commands")
	}
}

func TestFooterComponentRender(t *testing.T) {
	initTestTheme(t)
	session := &fakeFooterSession{}
	provider := &fakeFooterDataProvider{branch: "main"}
	footer := NewFooterComponent(session, provider)
	lines := footer.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected non-empty footer render")
	}
	found := false
	for _, line := range lines {
		if strings.Contains(line, "main") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected git branch in footer")
	}
}

func TestGitBranchReportsDetachedHead(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v: %s", err, output)
		}
	}
	git("init", "--initial-branch=trunk")
	git("commit", "--allow-empty", "-m", "one")

	mode := &InteractiveMode{cwd: dir}
	if branch := mode.GitBranch(); branch != "trunk" {
		t.Fatalf("GitBranch on branch = %q, want %q", branch, "trunk")
	}
	// Upstream footer-data-provider resolves the branch from nested
	// directories of a regular repo too.
	nested := filepath.Join(dir, "src", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if branch := (&InteractiveMode{cwd: nested}).GitBranch(); branch != "trunk" {
		t.Fatalf("GitBranch from nested dir = %q, want %q", branch, "trunk")
	}
	git("checkout", "--detach")
	// Upstream footer-data-provider labels detached HEAD "detached", never "HEAD".
	if branch := mode.GitBranch(); branch != "detached" {
		t.Fatalf("GitBranch detached = %q, want %q", branch, "detached")
	}
	outside := &InteractiveMode{cwd: t.TempDir()}
	if branch := outside.GitBranch(); branch != "" {
		t.Fatalf("GitBranch outside repo = %q, want empty", branch)
	}
}

// Ports the reftable intents of upstream footer-data-provider.test.ts: in a
// reftable repository .git/HEAD holds the "refs/heads/.invalid" sentinel and
// only git itself can resolve the branch. pigo always delegates to git, so
// the branch and the detached state must come back correct regardless.
func TestGitBranchReftableRepo(t *testing.T) {
	dir := t.TempDir()
	git := func(fatal bool, args ...string) bool {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if output, err := cmd.CombinedOutput(); err != nil {
			if fatal {
				t.Fatalf("git %v: %v: %s", args, err, output)
			}
			return false
		}
		return true
	}
	if !git(false, "init", "--ref-format=reftable", "--initial-branch=main") {
		t.Skip("git without reftable support (needs git >= 2.45)")
	}
	git(true, "commit", "--allow-empty", "-m", "one")
	head, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(head), ".invalid") {
		t.Fatalf(".git/HEAD = %q, want the reftable .invalid sentinel", head)
	}
	mode := &InteractiveMode{cwd: dir}
	if branch := mode.GitBranch(); branch != "main" {
		t.Fatalf("GitBranch reftable = %q, want %q", branch, "main")
	}
	git(true, "checkout", "--detach")
	if branch := mode.GitBranch(); branch != "detached" {
		t.Fatalf("GitBranch reftable detached = %q, want %q", branch, "detached")
	}
}

func TestFooterComponentStatuses(t *testing.T) {
	initTestTheme(t)
	session := &fakeFooterSession{}
	provider := &fakeFooterDataProvider{
		branch:   "dev",
		statuses: map[string]string{"ext": "active"},
	}
	footer := NewFooterComponent(session, provider)
	lines := footer.Render(80)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "active") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected extension status in footer")
	}
}

func TestInteractiveUISetStatus(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	ui := tui.NewTUI(terminal)
	kb := NewAppKeybindings(nil)
	tui.SetKeybindings(kb)

	mode := &InteractiveMode{
		ui:             ui,
		chat:           &tui.Container{},
		keybindings:    kb,
		editor:         NewCustomEditor(ui, tui.EditorTheme{}, kb),
		toolComponents: make(map[string]*ToolExecutionComponent),
		footerStatuses: make(map[string]string),
	}
	iui := NewInteractiveUI(mode)

	text := "active"
	iui.SetStatus("test", &text)
	if mode.footerStatuses["test"] != "active" {
		t.Errorf("expected footer status 'active', got %q", mode.footerStatuses["test"])
	}

	iui.SetStatus("test", nil)
	if _, exists := mode.footerStatuses["test"]; exists {
		t.Error("expected footer status to be removed")
	}
}

func TestInteractiveUISetTitle(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	ui := tui.NewTUI(terminal)
	kb := NewAppKeybindings(nil)
	tui.SetKeybindings(kb)

	mode := &InteractiveMode{
		ui:             ui,
		chat:           &tui.Container{},
		keybindings:    kb,
		editor:         NewCustomEditor(ui, tui.EditorTheme{}, kb),
		toolComponents: make(map[string]*ToolExecutionComponent),
		footerStatuses: make(map[string]string),
	}
	iui := NewInteractiveUI(mode)

	// Should not panic
	iui.SetTitle("Test Title")
}

func TestInteractiveUIWidgets(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	ui := tui.NewTUI(terminal)
	kb := NewAppKeybindings(nil)
	tui.SetKeybindings(kb)

	mode := &InteractiveMode{
		ui:             ui,
		chat:           &tui.Container{},
		keybindings:    kb,
		editor:         NewCustomEditor(ui, tui.EditorTheme{}, kb),
		toolComponents: make(map[string]*ToolExecutionComponent),
		footerStatuses: make(map[string]string),
		widgetAbove:    &tui.Container{},
		widgetBelow:    &tui.Container{},
	}
	iui := NewInteractiveUI(mode)

	widget := &extensions.Widget{Lines: []string{"status line"}}
	iui.SetWidget("test", widget, nil)

	if _, exists := iui.widgets["test"]; !exists {
		t.Error("expected widget to be registered")
	}

	iui.SetWidget("test", nil, nil)
	if _, exists := iui.widgets["test"]; exists {
		t.Error("expected widget to be removed")
	}
}

func TestSelectListTheme(t *testing.T) {
	initTestTheme(t)
	slTheme := selectListTheme()
	if slTheme.SelectedPrefix == nil {
		t.Error("expected non-nil SelectedPrefix")
	}
	styled := slTheme.SelectedPrefix("test")
	if styled == "" {
		t.Error("expected non-empty styled text")
	}
}

func TestNewStyledText(t *testing.T) {
	initTestTheme(t)
	comp := newStyledText("dim", "test message")
	lines := comp.Render(40)
	if len(lines) != 1 {
		t.Errorf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "test message") {
		t.Error("expected text content in render")
	}
}

type fakeRenderRequester struct{}

func (f *fakeRenderRequester) RequestRender() {}

type fakeFooterSession struct{}

func (f *fakeFooterSession) State() agent.AgentState {
	return agent.AgentState{}
}

type fakeFooterDataProvider struct {
	branch   string
	statuses map[string]string
}

func (f *fakeFooterDataProvider) GitBranch() string { return f.branch }
func (f *fakeFooterDataProvider) Statuses() map[string]string {
	if f.statuses == nil {
		return map[string]string{}
	}
	return f.statuses
}

type fakeTerminalImpl struct {
	columns int
	rows    int
}

func newFakeTerminal(columns, rows int) *fakeTerminalImpl {
	return &fakeTerminalImpl{columns: columns, rows: rows}
}

func (f *fakeTerminalImpl) Start(func(string), func()) error { return nil }
func (f *fakeTerminalImpl) Stop() error                      { return nil }
func (f *fakeTerminalImpl) DrainInput(_, _ time.Duration)    {}
func (f *fakeTerminalImpl) Write(string)                     {}
func (f *fakeTerminalImpl) Columns() int                     { return f.columns }
func (f *fakeTerminalImpl) Rows() int                        { return f.rows }
func (f *fakeTerminalImpl) KittyProtocolActive() bool        { return false }
func (f *fakeTerminalImpl) MoveBy(int)                       {}
func (f *fakeTerminalImpl) HideCursor()                      {}
func (f *fakeTerminalImpl) ShowCursor()                      {}
func (f *fakeTerminalImpl) ClearLine()                       {}
func (f *fakeTerminalImpl) ClearFromCursor()                 {}
func (f *fakeTerminalImpl) ClearScreen()                     {}
func (f *fakeTerminalImpl) SetTitle(string)                  {}
func (f *fakeTerminalImpl) SetProgress(bool)                 {}

// handleSlashCommand is the test entry for command dispatch; production input
// resolves commands through resolveSlashCommand directly.
func (mode *InteractiveMode) handleSlashCommand(name, args string) bool {
	action, ok := mode.resolveSlashCommand(name, args)
	if ok {
		action.run()
	}
	return ok
}
