package modes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/tui"

	theme "github.com/OrdalieTech/pi-go/codingagent/modes/theme"
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
	border := NewDynamicBorder()
	lines := border.Render(10)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "─") {
		t.Error("expected border character")
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
	text := keyText("app.interrupt")
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
