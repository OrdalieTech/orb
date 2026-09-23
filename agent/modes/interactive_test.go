package modes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/orbalogo"
	"github.com/OrdalieTech/orb/internal/themefile"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
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

func TestDroppedImageAttachesBytes(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "a picture.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	pasted := strings.ReplaceAll(path, " ", `\ `)
	if runtime.GOOS == "windows" {
		// Windows terminals quote a dropped path containing spaces instead of escaping them.
		pasted = `"` + path + `"`
	}
	dropped, mimeType := droppedImagePath(pasted)
	if dropped != path || mimeType != "image/png" {
		t.Fatalf("dropped image = %q %q", dropped, mimeType)
	}
	if plain, mime := droppedImagePath("some ordinary pasted text"); plain != "" || mime != "" {
		t.Fatalf("ordinary paste became an image: %q %q", plain, mime)
	}
	mode := &InteractiveMode{ui: tui.NewTUI(newFakeTerminal(80, 24))}
	mode.editor = NewCustomEditor(mode.ui, tui.EditorTheme{}, NewAppKeybindings(nil))
	mode.attachImage(func() ([]byte, string, error) {
		data, err := os.ReadFile(dropped)
		return data, mimeType, err
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		mode.mu.Lock()
		busy := mode.attachingImages != 0
		mode.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("image attachment did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if got := mode.editor.GetText(); got != "[Image #1]" {
		t.Fatalf("image marker = %q", got)
	}
	mode.inputCh = make(chan inputEntry, 1)
	mode.setupEditorSubmitHandler()
	mode.editor.OnSubmit(mode.editor.GetText())
	entry := <-mode.inputCh
	if entry.text != "[Image #1]" || len(entry.images) != 1 || entry.images[0].MimeType != "image/png" || entry.images[0].Data != base64.StdEncoding.EncodeToString(encoded.Bytes()) {
		t.Fatal("dropped image bytes were not attached")
	}
}

func TestExtensionSelectorAcceptsLeadingMnemonic(t *testing.T) {
	selected := ""
	component := NewExtensionSelectorItemsComponent("Permission", []tui.SelectItem{
		{Value: "y approve once"}, {Value: "s approve for this session"},
	}, func(value string) { selected = value }, nil, nil)
	component.HandleInput(tui.KeyEvent{Raw: "s"})
	if selected != "s approve for this session" {
		t.Fatalf("selected = %q", selected)
	}
}

func TestInteractiveChatRenderInvalidatesChangedChild(t *testing.T) {
	chat := tui.NewWindowedContainer()
	component := tui.NewText("before", 0, 0, nil)
	chat.AddChild(component)
	_ = chat.LineCount(80)
	component.SetText("after")

	mode := &InteractiveMode{ui: tui.NewTUI(newFakeTerminal(80, 24)), chat: chat}
	mode.requestChatRender(component)
	if got := strings.Join(chat.RenderLines(80, 0, chat.LineCount(80)), "\n"); !strings.Contains(got, "after") || strings.Contains(got, "before") {
		t.Fatalf("changed child render = %q", got)
	}
}

func TestInteractiveUIToolExpansionShowsStatus(t *testing.T) {
	initTestTheme(t)
	chat := &tui.Container{}
	component := &countedExpandableComponent{}
	chat.AddChild(component)
	mode := &InteractiveMode{
		ui:             tui.NewTUI(newFakeTerminal(80, 24)),
		header:         &tui.Container{},
		chat:           chat,
		expandables:    []expandableComponent{component},
		toolComponents: map[string]*ToolExecutionComponent{},
	}
	ui := NewInteractiveUI(mode)

	ui.SetToolsExpanded(true)
	if component.setExpandedCalls != 1 {
		t.Fatalf("SetExpanded calls = %d, want one traversal", component.setExpandedCalls)
	}
	if rendered := mode.statusNoticeText(); !strings.Contains(rendered, "Tool output: expanded") {
		t.Fatalf("expanded status = %q", rendered)
	}
	ui.SetToolsExpanded(false)
	if component.setExpandedCalls != 2 {
		t.Fatalf("SetExpanded calls = %d, want one traversal per update", component.setExpandedCalls)
	}
	if rendered := mode.statusNoticeText(); !strings.Contains(rendered, "Tool output: collapsed") {
		t.Fatalf("collapsed status = %q", rendered)
	}
}

type countedExpandableComponent struct{ setExpandedCalls int }

func (*countedExpandableComponent) Render(int) []string { return []string{"tool"} }
func (component *countedExpandableComponent) SetExpanded(bool) {
	component.setExpandedCalls++
}

type countedInteractiveComponent struct{ renders int }

func (component *countedInteractiveComponent) Render(int) []string {
	component.renders++
	return []string{"line"}
}

func TestInteractiveHostInvalidateDoesNotRebuildTranscript(t *testing.T) {
	chat := tui.NewWindowedContainer()
	component := &countedInteractiveComponent{}
	chat.AddChild(component)
	_ = chat.LineCount(80)
	body := &tui.Container{}
	body.AddChild(chat)
	uiRoot := tui.NewTUI(newFakeTerminal(80, 24))
	uiRoot.AddChild(body)

	(&InteractiveMode{ui: uiRoot, chat: chat}).Invalidate()
	_ = chat.LineCount(80)
	if component.renders != 1 {
		t.Fatalf("extension invalidate rebuilt transcript %d times", component.renders)
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
	loader, err := agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{
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
	sessionRuntime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings, ResourceLoader: loader,
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
	if !found || registered.SourcePath != loaded[0].SourcePath || registered.SourceInfo != loaded[0].SourceInfo ||
		mode.themeController.Current() != registered || theme.Current() != registered {
		t.Fatalf("resource theme: found=%t registered=%p loaded=%#v controller=%p current=%p",
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
	parse := func(name string) *agent.ResourceTheme {
		t.Helper()
		parsed, parseErr := themefile.Parse(name, []byte(strings.Replace(string(builtin), `"name": "dark"`, `"name": "`+name+`"`, 1)))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return &agent.ResourceTheme{Theme: *parsed}
	}
	themeA, themeB := parse("theme-a"), parse("theme-b")
	loaded := []*agent.ResourceTheme{themeA}
	loader, err := agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings, NoThemes: true, NoContextFiles: true,
		ThemesOverride: func(agent.ResourceThemesResult) agent.ResourceThemesResult {
			return agent.ResourceThemesResult{Themes: append([]*agent.ResourceTheme(nil), loaded...)}
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
	sessionRuntime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings, ResourceLoader: loader,
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

	loaded = []*agent.ResourceTheme{themeB}
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
	if !found || registered.Name != themeB.Name || mode.themeController.Current() != registered || theme.Current() != registered {
		t.Fatalf("refreshed theme: found=%t registered=%p controller=%p current=%p",
			found, registered, mode.themeController.Current(), theme.Current())
	}
}

func TestInteractiveModeRebindPropagatesInvalidResourceThemeName(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	newRuntime := func(loader agent.ResourceLoader) *agent.SessionRuntime {
		t.Helper()
		manager, managerErr := sessionstore.InMemory(cwd)
		if managerErr != nil {
			t.Fatal(managerErr)
		}
		created, runtimeErr := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
			Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings, ResourceLoader: loader,
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

	loader, err := agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings, NoThemes: true, NoContextFiles: true,
		ThemesOverride: func(agent.ResourceThemesResult) agent.ResourceThemesResult {
			return agent.ResourceThemesResult{Themes: []*agent.ResourceTheme{{Theme: themefile.Theme{Name: "bad/name"}}}}
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

// The default panel mounts the mark ahead of conversation content and keeps it
// in the transcript when messages arrive or a new session clears them.
func TestInteractivePanelKeepsTheMark(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	runtimeSession, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeSession.Dispose)
	mode := &InteractiveMode{
		session: runtimeSession, ui: tui.NewTUI(newFakeTerminal(80, 40)), cwd: cwd,
		keybindings: NewAppKeybindings(nil), inputCh: make(chan inputEntry, 1),
		toolComponents: make(map[string]*ToolExecutionComponent), footerStatuses: make(map[string]string),
		options: InteractiveModeOptions{SessionHeader: manager.GetHeader()},
	}
	if err := mode.init(); err != nil {
		t.Fatal(err)
	}
	if err := mode.ui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { theme.SetCurrent(nil) })
	// One synchronous render seeds ViewportBodyHeight; stopping the TUI then
	// keeps background debounce renders from racing the direct Render calls.
	mode.ui.ForceRender()
	if err := mode.ui.Stop(); err != nil {
		t.Fatal(err)
	}

	if mode.emptyState == nil {
		t.Fatal("empty state was not mounted")
	}
	if len(mode.header.Children()) != 0 || len(mode.loadedResources.Children()) != 0 {
		t.Fatalf("the default panel carries built-in bands: header=%d resources=%d",
			len(mode.header.Children()), len(mode.loadedResources.Children()))
	}

	mode.emptyState.frame.Store(orbalogo.FrameCount - 1)
	mark := orbalogo.Frame(orbalogo.FrameCount - 1)[4]
	bodyComponent := mode.ui.Children()[0]
	body := func() string { return strings.Join(bodyComponent.Render(79), "\n") }
	if count := strings.Count(body(), mark); count != 1 {
		t.Fatalf("the fresh panel drew the mark %d times, want one: %q", count, body())
	}
	mode.chat.AddChild(lifecycleText("assistant answer"))
	if count := strings.Count(body(), mark); count != 1 {
		t.Fatalf("visible chat removed or duplicated the mark: %q", body())
	}
	mode.chat.Clear()
	if count := strings.Count(body(), mark); count != 1 {
		t.Fatalf("clearing the chat drew the mark %d times, want it back once: %q", count, body())
	}
}

// LOG-m3: status indicators render the raw runtime sources like upstream
// oauth-selector.ts formatStatusIndicator ("stored", "models_json_key",
// "runtime", env names), not invented friendly labels.
func TestLOGm3AuthStatusIndicatorDescribesConfiguredTypeAndSource(t *testing.T) {
	tests := []struct {
		name   string
		option InteractiveAuthProvider
		want   string
	}{
		{
			name: "different auth type",
			option: InteractiveAuthProvider{Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth,
				Status: &InteractiveAuthStatus{Type: aiauth.AuthTypeAPIKey, Source: "stored credential"}},
			want: " • API key configured",
		},
		{
			name: "environment source",
			option: InteractiveAuthProvider{Name: "Groq", AuthType: aiauth.AuthTypeAPIKey,
				Status: &InteractiveAuthStatus{Type: aiauth.AuthTypeAPIKey, Source: "GROQ_API_KEY"}},
			want: " ✓ env: GROQ_API_KEY",
		},
		{
			name:   "unconfigured",
			option: InteractiveAuthProvider{Name: "Google", AuthType: aiauth.AuthTypeAPIKey},
			want:   " • unconfigured",
		},
		{
			name: "raw stored source",
			option: InteractiveAuthProvider{Name: "Anthropic", AuthType: aiauth.AuthTypeAPIKey,
				Status: &InteractiveAuthStatus{Type: aiauth.AuthTypeAPIKey, Source: "stored"}},
			want: " ✓ stored",
		},
		{
			name: "raw models.json source",
			option: InteractiveAuthProvider{Name: "Custom", AuthType: aiauth.AuthTypeAPIKey,
				Status: &InteractiveAuthStatus{Type: aiauth.AuthTypeAPIKey, Source: "models_json_key"}},
			want: " ✓ models_json_key",
		},
		{
			name: "stored credential collapses to configured",
			option: InteractiveAuthProvider{Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth,
				Status: &InteractiveAuthStatus{Type: aiauth.AuthTypeOAuth, Source: "stored credential"}},
			want: " ✓ configured",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := selectorANSI.ReplaceAllString(formatAuthStatusIndicator(test.option), ""); got != test.want {
				t.Fatalf("formatAuthStatusIndicator() = %q, want %q", got, test.want)
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
	var selected []InteractiveAuthProvider
	component := NewOAuthSelectorComponent(oauthSelectorLogin, matched,
		func(provider InteractiveAuthProvider) { selected = append(selected, provider) }, func() {}, "")
	component.HandleInput(tui.KeyEvent{Raw: "\r"})
	component.HandleInput(tui.KeyEvent{Raw: "\x1b[B"})
	component.HandleInput(tui.KeyEvent{Raw: "\r"})
	if len(selected) != 2 || selected[0].ID != "first" || selected[1].ID != "second" {
		t.Fatalf("selector identities = %#v", selected)
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
	comp := NewUserMessageComponent("hello", theme.MarkdownTheme(), 0, nil)
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
	comp := NewAssistantMessageComponent(msg, false, theme.MarkdownTheme(), "", 0, nil)
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
	comp := NewAssistantMessageComponent(msg, false, theme.MarkdownTheme(), "", 0, nil)
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
	comp.SetExpanded(true)
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

func TestCompactStatusRender(t *testing.T) {
	status := &tui.Container{}
	status.AddChild(IdleStatus{})
	if lines := (compactStatus{Component: status}).Render(20); len(lines) != 0 {
		t.Fatalf("compact idle status = %#v, want no rows", lines)
	}

	indicator := NewWorkingStatusIndicator(&fakeRenderRequester{}, "Working...", &extensions.WorkingIndicatorOptions{Frames: []string{"*"}})
	defer indicator.Dispose()
	status.Clear()
	status.AddChild(indicator)
	if lines := (compactStatus{Component: status}).Render(20); len(lines) != 1 || !strings.Contains(lines[0], "Working...") {
		t.Fatalf("compact working status = %#v, want one row", lines)
	}
	if lines := (compactStatus{Component: status, Inline: func(int, string) bool { return true }}).Render(20); len(lines) != 0 {
		t.Fatalf("inlined compact status = %#v, want no rows", lines)
	}
	mode := &InteractiveMode{ui: tui.NewTUI(newFakeTerminal(80, 24)), chat: &tui.Container{}}
	t.Cleanup(func() { mode.showStatusMessage("") })
	lane := compactStatus{Component: status, Notice: mode.animatedStatusNoticeText}
	mode.showStatusMessage("Model changed")
	mode.statusMessageMu.Lock()
	mode.statusNoticeStarted = time.Now().Add(-45 * time.Millisecond)
	mode.statusMessageMu.Unlock()
	opening := lane.Render(80)
	mode.statusMessageMu.Lock()
	mode.statusNoticeStarted = time.Now().Add(-300 * time.Millisecond)
	mode.statusMessageMu.Unlock()
	full := lane.Render(80)
	if len(opening) != 1 || tui.VisibleWidth(opening[0]) >= tui.VisibleWidth(full[0]) {
		t.Fatalf("notice did not open gradually: opening=%q full=%q", opening, full)
	}
	mode.showStatusMessage("Copied to clipboard")
	if got := tui.VisibleWidth(mode.animatedStatusNoticeText()); got != len("Copied to clipboard") {
		t.Fatalf("replacement closed the notice gap: width=%d", got)
	}
	lines := lane.Render(80)
	if len(mode.chat.Children()) != 0 || len(lines) != 1 || !strings.Contains(tui.StripANSI(lines[0]), "* Working...") || !strings.Contains(lines[0], "Copied to clipboard") || strings.Contains(lines[0], "Model changed") {
		t.Fatalf("notice changed the transcript or loader: %q", lines)
	}
	mode.statusMessageMu.Lock()
	mode.statusNoticeStarted = time.Now().Add(-2950 * time.Millisecond)
	mode.statusMessageMu.Unlock()
	closing := lane.Render(80)
	if len(closing) != 1 || tui.VisibleWidth(closing[0]) >= tui.VisibleWidth(lines[0]) {
		t.Fatalf("notice gap did not close gradually: closing=%q full=%q", closing, lines)
	}
	mode.statusMessageMu.Lock()
	mode.statusNoticeTimer.Reset(time.Millisecond)
	mode.statusMessageMu.Unlock()
	deadline := time.Now().Add(time.Second)
	for mode.statusNoticeText() != "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if mode.statusNoticeText() != "" || strings.Contains(strings.Join(lane.Render(80), ""), "Copied") {
		t.Fatal("notice did not expire")
	}
}

func TestComposeEditorTopBorderStatusAndSessionName(t *testing.T) {
	initTestTheme(t)
	const width = 48
	border := func(text string) string { return "\x1b[34m" + text + "\x1b[39m" }
	base := border(strings.Repeat("─", width))
	projection := composeEditorTopBorder(base, width, "\x1b[36m⠋\x1b[39m Working...", "A deliberately long conversation title", border)
	if !projection.StatusInline || !projection.TitleShown || tui.VisibleWidth(projection.Line) != width {
		t.Fatalf("editor chrome projection = %#v", projection)
	}
	if !strings.Contains(projection.Line, "Working...") || !strings.Contains(projection.Line, "A deliberately") || !strings.Contains(projection.Line, "\x1b[48;2;") {
		t.Fatalf("editor chrome omitted status or themed title: %q", projection.Line)
	}
	plain := selectorANSI.ReplaceAllString(projection.Line, "")
	if !strings.HasPrefix(plain, "╭") || !strings.HasSuffix(plain, "╮") {
		t.Fatalf("editor chrome lost rounded corners: %q", plain)
	}
	for testWidth := 0; testWidth <= 32; testWidth++ {
		projection := composeEditorTopBorder(border(strings.Repeat("─", testWidth)), testWidth, "⠋ Working...", "会話 🙂 title", border)
		if got := tui.VisibleWidth(projection.Line); got != testWidth {
			t.Fatalf("editor chrome width %d rendered %d cells: %q", testWidth, got, projection.Line)
		}
	}
}

func TestEditorChromePreservesScrollHintAndUsesStatusLane(t *testing.T) {
	initTestTheme(t)
	ui := tui.NewTUI(newFakeTerminal(20, 24))
	status := &tui.Container{}
	indicator := NewWorkingStatusIndicator(ui, "Working...", &extensions.WorkingIndicatorOptions{Frames: []string{"*"}})
	t.Cleanup(indicator.Dispose)
	status.AddChild(indicator)
	mode := &InteractiveMode{ui: ui, status: status, editorContainer: &tui.Container{}}
	mode.editor = NewCustomEditor(ui, theme.EditorTheme(), NewAppKeybindings(nil))
	mode.editor.setTopBorderDecorator(func(width int, base string, border tui.StyleFunc) string {
		return mode.editorTopBorder(width, base, border).Line
	})
	mode.editorContainer.AddChild(mode.editor)
	mode.editor.SetText(strings.TrimRight(strings.Repeat("long draft line\n", 20), "\n"))
	lane := compactStatus{Component: status, Inline: mode.statusInEditor}
	if lines := lane.Render(20); len(lines) != 1 || !strings.Contains(lines[0], "Working...") {
		t.Fatalf("scrolled editor did not retain status lane: %#v", lines)
	}
	lines := mode.editor.Render(20)
	if len(lines) == 0 || !strings.Contains(lines[0], "↑") || strings.Contains(lines[0], "Working...") ||
		!strings.Contains(selectorANSI.ReplaceAllString(lines[0], ""), "╭") {
		t.Fatalf("scrolled editor chrome replaced its scroll hint: %#v", lines)
	}
}

func TestRetryStatusIndicatorCountsDown(t *testing.T) {
	indicator := NewRetryStatusIndicator(&fakeRenderRequester{}, 2, 4, 1500)
	t.Cleanup(indicator.Dispose)
	if rendered := strings.Join(indicator.Render(80), "\n"); !strings.Contains(rendered, "in 2s") {
		t.Fatalf("initial retry status = %q", rendered)
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for {
		rendered := strings.Join(indicator.Render(80), "\n")
		if strings.Contains(rendered, "in 1s") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retry status did not count down: %q", rendered)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestQueueUpdateRendersTruncatedMessagesCountAndConfiguredHint(t *testing.T) {
	initTestTheme(t)
	ui := tui.NewTUI(newFakeTerminal(80, 24))
	mode := &InteractiveMode{
		ui:              ui,
		pendingMessages: &tui.Container{},
		keybindings: NewAppKeybindings(tui.KeybindingsConfig{
			"app.message.dequeue": {"ctrl+u"},
		}),
		keyDisplayOS: "linux",
	}
	mode.handleEvent(agent.QueueUpdateEvent{
		Steering: []string{"inspect a very long queued steering message that must truncate\nnot rendered"},
		FollowUp: []string{"summarize the result"},
	})
	wide := mode.pendingMessages.Render(80)
	if len(wide) != 4 {
		t.Fatalf("queued rows = %#v", wide)
	}
	joined := strings.Join(wide, "\n")
	for _, want := range []string{"Steering: inspect", "Follow-up: summarize", "↳ 2 queued", "Ctrl+U", "edit all"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("queued display omitted %q: %q", want, joined)
		}
	}
	if strings.Contains(joined, "not rendered") {
		t.Fatalf("queued display rendered a second logical line: %q", joined)
	}
	if narrow := mode.pendingMessages.Render(24); len(narrow) != 4 || !strings.Contains(narrow[1], "...") {
		t.Fatalf("queued display was not one-row truncated: %#v", narrow)
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

	si4 := NewBranchSummaryStatusIndicator(fake)
	if si4.Kind != StatusBranchSummary {
		t.Errorf("expected StatusBranchSummary, got %s", si4.Kind)
	}
	si4.Dispose()
}

func TestSummarizationRetryEventsRestoreUnderlyingStatus(t *testing.T) {
	initTestTheme(t)
	modeUI := tui.NewTUI(newFakeTerminal(80, 24))
	mode := &InteractiveMode{ui: modeUI, status: &tui.Container{}, chat: &tui.Container{}}

	mode.handleEvent(agent.SummarizationRetryScheduledEvent{
		Attempt: 1, MaxAttempts: 3, DelayMS: 2000, ErrorMessage: "terminated",
	})
	if status, ok := mode.statusIndicator.(*StatusIndicator); !ok || status.Kind != StatusRetry {
		t.Fatalf("scheduled status = %#v", mode.statusIndicator)
	}
	if rendered := strings.Join(mode.chat.Render(80), "\n"); !strings.Contains(rendered, "terminated") {
		t.Fatalf("scheduled error was not rendered: %q", rendered)
	}

	mode.handleEvent(agent.SummarizationRetryAttemptStartEvent{Source: "compaction", Reason: "overflow"})
	if status, ok := mode.statusIndicator.(*StatusIndicator); !ok || status.Kind != StatusCompaction {
		t.Fatalf("compaction attempt status = %#v", mode.statusIndicator)
	}

	mode.handleEvent(agent.SummarizationRetryScheduledEvent{
		Attempt: 2, MaxAttempts: 3, DelayMS: 4000, ErrorMessage: "terminated",
	})
	mode.handleEvent(agent.SummarizationRetryAttemptStartEvent{Source: "branchSummary"})
	if status, ok := mode.statusIndicator.(*StatusIndicator); !ok || status.Kind != StatusBranchSummary {
		t.Fatalf("branch attempt status = %#v", mode.statusIndicator)
	}

	mode.handleEvent(agent.SummarizationRetryScheduledEvent{
		Attempt: 3, MaxAttempts: 3, DelayMS: 8000, ErrorMessage: "terminated",
	})
	mode.handleEvent(agent.SummarizationRetryFinishedEvent{})
	if mode.statusIndicator != nil {
		t.Fatalf("finished retry status = %#v", mode.statusIndicator)
	}
}

func TestAvailableProviderCountUsesScopedModels(t *testing.T) {
	mode := newF12AutocompleteMode(t, true)
	if count := mode.AvailableProviderCount(); count != 3 {
		t.Fatalf("available provider count = %d, want 3", count)
	}
	mode.session.SetScopedModels([]agent.ScopedModel{
		{Model: ai.Model{Provider: "anthropic", ID: "first"}},
		{Model: ai.Model{Provider: "anthropic", ID: "second"}},
	})
	if count := mode.AvailableProviderCount(); count != 1 {
		t.Fatalf("scoped provider count = %d, want 1", count)
	}
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

func TestSkillAtAutocompleteInvokesCanonicalCommand(t *testing.T) {
	initTestTheme(t)
	mode := newF12AutocompleteMode(t, true)
	mode.interactiveUI = NewInteractiveUI(mode)
	mode.setupAutocomplete()

	result := mode.autocompleteProvider.GetSuggestions(t.Context(), []string{"@inspect"}, 0, 8, false)
	if result == nil || result.Prefix != "@inspect" || len(result.Items) == 0 {
		t.Fatalf("@ skill suggestions = %#v", result)
	}
	item := result.Items[0]
	if item.Value != "@inspect-skill" || item.Label != "[skill] inspect-skill" || item.Description != "[t] Inspect the workspace" {
		t.Fatalf("first @ suggestion = %#v", item)
	}
	styler, ok := mode.autocompleteProvider.(tui.AutocompleteItemStyler)
	if !ok {
		t.Fatal("skill autocomplete provider has no item styler")
	}
	styled := styler.StyleAutocompleteItem(item, item.Label, false)
	if !strings.Contains(styled, theme.FG("accent", theme.Bold("[skill]"))) || !strings.Contains(styled, theme.FG("mdLink", " inspect-skill")) {
		t.Fatalf("styled skill = %q", styled)
	}
	applied := mode.autocompleteProvider.ApplyCompletion([]string{"@inspect review this"}, 0, 8, item, result.Prefix)
	if applied.Lines[0] != "/skill:inspect-skill review this" || applied.CursorCol != len([]rune("/skill:inspect-skill")) {
		t.Fatalf("applied @ skill = %#v", applied)
	}
	midToken := mode.autocompleteProvider.GetSuggestions(t.Context(), []string{"@inspect"}, 0, 5, false)
	if midToken == nil || len(midToken.Items) == 0 {
		t.Fatalf("mid-token suggestions = %#v", midToken)
	}
	applied = mode.autocompleteProvider.ApplyCompletion([]string{"@inspect"}, 0, 5, midToken.Items[0], midToken.Prefix)
	if applied.Lines[0] != "/skill:inspect-skill " {
		t.Fatalf("mid-token completion = %#v", applied)
	}
	if blankFirst := mode.autocompleteProvider.GetSuggestions(t.Context(), []string{"", "@inspect"}, 1, 8, false); blankFirst == nil || blankFirst.Items[0].Label != "[skill] inspect-skill" {
		t.Fatalf("skill after an empty first line = %#v", blankFirst)
	}
	if slash := mode.autocompleteProvider.GetSuggestions(t.Context(), []string{"Please /"}, 0, len("Please /"), false); slash == nil || len(slash.Items) != 1 || slash.Items[0].Label != "[skill] inspect-skill" {
		t.Fatalf("inline slash exposed ordinary commands: %#v", slash)
	}
	inline := mode.autocompleteProvider.GetSuggestions(t.Context(), []string{"Please /insp continue"}, 0, len("Please /insp"), false)
	if inline == nil || inline.Prefix != "/insp" || len(inline.Items) != 1 || inline.Items[0].Label != "[skill] inspect-skill" {
		t.Fatalf("inline slash suggestions = %#v", inline)
	}
	applied = mode.autocompleteProvider.ApplyCompletion([]string{"Please /insp continue"}, 0, len("Please /insp"), inline.Items[0], inline.Prefix)
	if applied.Lines[0] != "Please /skill:inspect-skill continue" {
		t.Fatalf("inline skill completion = %#v", applied)
	}
	if got := mode.autocompleteProvider.(*skillAutocompleteProvider).promoteInlineSkill(applied.Lines[0]); got != "/skill:inspect-skill Please continue" {
		t.Fatalf("inline skill was not promoted to the existing skill resolver: %q", got)
	}
	mode.inputCh = make(chan inputEntry, 1)
	mode.setupEditorSubmitHandler()
	mode.editor.SetText(applied.Lines[0])
	mode.editor.HandleInput(tui.KeyEvent{Raw: "\r"})
	if len(mode.inputCh) != 1 {
		t.Fatal("inline skill was not submitted")
	}
	if entry := <-mode.inputCh; entry.text != "/skill:inspect-skill Please continue" {
		t.Fatalf("submitted inline skill = %q", entry.text)
	}
	inlineAt := mode.autocompleteProvider.GetSuggestions(t.Context(), []string{"Please use @insp now"}, 0, len("Please use @insp"), false)
	if inlineAt == nil || len(inlineAt.Items) == 0 || inlineAt.Items[0].Label != "[skill] inspect-skill" {
		t.Fatalf("inline @ skill suggestions = %#v", inlineAt)
	}
	inserted := mode.autocompleteProvider.ApplyCompletion([]string{"Please use @insp now"}, 0, len("Please use @insp"), inlineAt.Items[0], inlineAt.Prefix)
	if inserted.Lines[0] != "Please use /skill:inspect-skill now" {
		t.Fatalf("inline @ skill completion = %#v", inserted)
	}
	mode.editor.SetText(inserted.Lines[0])
	mode.editor.HandleInput(tui.KeyEvent{Raw: "\r"})
	if entry := <-mode.inputCh; entry.text != "/skill:inspect-skill Please use now" {
		t.Fatalf("submitted inline @ skill = %q", entry.text)
	}
	secondLine := mode.autocompleteProvider.GetSuggestions(t.Context(), []string{"Please help", "with @insp"}, 1, len("with @insp"), false)
	if secondLine == nil || len(secondLine.Items) == 0 || secondLine.Items[0].Label != "[skill] inspect-skill" {
		t.Fatalf("second-line @ skill suggestions = %#v", secondLine)
	}

	for _, test := range []struct {
		name     string
		line     string
		col      int
		disabled bool
	}{
		{name: "quoted", line: `@"inspect`, col: 9},
		{name: "disabled", line: "@inspect", col: 8, disabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := mode.autocompleteProvider
			if test.disabled {
				provider = newF12AutocompleteMode(t, false).autocompleteProvider
			}
			got := provider.GetSuggestions(t.Context(), []string{test.line}, 0, test.col, false)
			if got == nil {
				return
			}
			for _, suggestion := range got.Items {
				if strings.HasPrefix(suggestion.Label, "[skill] ") {
					t.Fatalf("unexpected skill suggestion: %#v", suggestion)
				}
			}
		})
	}
}

func TestSkillAtAutocompleteOmitsExtensionCollision(t *testing.T) {
	mode := newF12AutocompleteMode(t, true, "skill:inspect-skill")
	result := mode.autocompleteProvider.GetSuggestions(t.Context(), []string{"@inspect"}, 0, 8, false)
	if result == nil {
		return
	}
	for _, item := range result.Items {
		if strings.HasPrefix(item.Label, "[skill] ") {
			t.Fatalf("extension collision exposed skill alias: %#v", item)
		}
	}
}

func TestSkillAtAutocompleteKeepsSameNamedFile(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv(modesTestHelperEnv, "stdout")
	t.Setenv(modesTestStdoutEnv, "inspect\n")
	provider := newSkillAutocompleteProvider(
		tui.NewCombinedAutocompleteProvider(nil, baseDir, os.Args[0]),
		[]tui.AutocompleteItem{{Value: "@inspect", Label: "[skill] inspect", Description: "Inspect things"}},
	)
	result := provider.GetSuggestions(t.Context(), []string{"@insp"}, 0, 5, false)
	if result == nil || len(result.Items) != 2 || result.Items[0].Label != "[skill] inspect" || result.Items[1].Label != "inspect" {
		t.Fatalf("mixed @ suggestions = %#v", result)
	}
	if applied := provider.ApplyCompletion([]string{"@insp"}, 0, 5, result.Items[0], result.Prefix); applied.Lines[0] != "/skill:inspect " {
		t.Fatalf("skill completion = %#v", applied)
	}
	if applied := provider.ApplyCompletion([]string{"@insp"}, 0, 5, result.Items[1], result.Prefix); applied.Lines[0] != "@inspect " {
		t.Fatalf("file completion = %#v", applied)
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

func TestRestrainedToolComponentHeights(t *testing.T) {
	initTestTheme(t)
	for _, width := range []int{28, 88} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			tool := NewToolExecutionComponent("read", "call", nil, false, nil, &fakeRenderRequester{}, "/")
			if lines := tool.Render(width); len(lines) != 2 {
				t.Fatalf("pending tool lines = %d, want one separator and one title: %#v", len(lines), lines)
			}
			tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: "ok"}}, false, nil, false)
			if lines := tool.Render(width); len(lines) != 2 {
				t.Fatalf("finished tool lines = %d, want a compact action header: %#v", len(lines), lines)
			}

			bash := NewBashExecutionComponent("printf ok", &fakeRenderRequester{}, false)
			bash.AppendOutput("ok")
			exitCode := 0
			bash.SetComplete(&exitCode, false)
			if lines := bash.Render(width); len(lines) != 4 {
				t.Fatalf("finished bash lines = %d, want a gap above the output: %#v", len(lines), lines)
			}
		})
	}
}

func TestToolResultsCollapseAndToggleIndividually(t *testing.T) {
	initTestTheme(t)
	output := "one\ntwo\nthree\nfour\nfive\nsix"
	tool := NewToolExecutionComponent("read", "call", nil, false, nil, &fakeRenderRequester{}, "/")
	tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: output}}, false, nil, false)
	collapsed := strings.Join(tool.Render(60), "\n")
	if strings.Contains(collapsed, "one") || strings.Contains(collapsed, "six") || !strings.Contains(collapsed, "›") {
		t.Fatalf("completed tool did not hide output behind its disclosure: %s", collapsed)
	}
	if !tool.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: 1}) || collapsed == strings.Join(tool.Render(60), "\n") {
		t.Fatal("tool hover did not highlight the status marker")
	}
	tool.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: -1})
	tool.HandleMouse(tui.MouseEvent{Type: tui.MouseRelease, Button: 0})
	if expanded := strings.Join(tool.Render(60), "\n"); !strings.Contains(expanded, "one") || !strings.Contains(expanded, "six") {
		t.Fatalf("tool click did not expand the result: %s", expanded)
	}

	bash := NewBashExecutionComponent("printf", &fakeRenderRequester{}, false)
	bash.AppendOutput(output)
	if collapsed := strings.Join(bash.Render(60), "\n"); strings.Contains(collapsed, "one") || !strings.Contains(collapsed, "click to expand") {
		t.Fatalf("shell did not render a short tail: %s", collapsed)
	}
	bash.HandleMouse(tui.MouseEvent{Type: tui.MouseRelease, Button: 0})
	if expanded := strings.Join(bash.Render(60), "\n"); !strings.Contains(expanded, "one") {
		t.Fatalf("shell click did not expand the result: %s", expanded)
	}
}

func TestLongReasoningStreamsAsBoundedPreview(t *testing.T) {
	initTestTheme(t)
	message := &ai.AssistantMessage{Content: ai.AssistantContent{
		&ai.ThinkingContent{Thinking: "start of reasoning\n" + strings.Repeat("long reasoning paragraph.\n", 6_000) + "end of reasoning"},
		&ai.TextContent{Text: "final answer"},
	}}
	component := NewAssistantMessageComponent(nil, false, theme.MarkdownTheme(), "", 0, nil)
	component.UpdateContentStreaming(message, true)
	if lines := component.Render(80); len(lines) > 10 || strings.Contains(strings.Join(lines, "\n"), "start of reasoning") {
		t.Fatalf("streamed reasoning was not bounded: %d rows", len(lines))
	}
	component.UpdateContentStreaming(message, false)
	lines := component.Render(80)
	if !strings.Contains(strings.Join(lines, "\n"), "click to expand") {
		t.Fatal("completed reasoning has no expand control")
	}
	if !component.HandleMouse(tui.MouseEvent{Type: tui.MouseRelease, Button: 0, Row: component.toggleStart}) {
		t.Fatal("reasoning expand control ignored click")
	}
	if expanded := strings.Join(component.Render(80), "\n"); !strings.Contains(expanded, "start of reasoning") || !strings.Contains(expanded, "end of reasoning") {
		t.Fatal("expanded reasoning lost its full content")
	}
}

type layoutFooterSession struct{}

func (layoutFooterSession) State() engine.AgentState {
	return engine.AgentState{Model: &ai.Model{ID: "fixture-model", Provider: "fixture", ContextWindow: 8192}}
}

func TestFooterComponentCompactAndVerboseLayouts(t *testing.T) {
	initTestTheme(t)
	provider := &fakeFooterDataProvider{cwd: "/workspace", branch: "main"}
	compact := NewFooterComponent(layoutFooterSession{}, provider, false)
	for _, width := range []int{28, 88} {
		lines := compact.Render(width)
		if len(lines) != 1 || tui.VisibleWidth(lines[0]) != width {
			t.Fatalf("compact footer at %d = %#v", width, lines)
		}
		if width == 88 {
			plain := normalizeWP450Lines(lines)[0]
			if !strings.Contains(plain, "fixture-model") || !strings.Contains(plain, "/workspace") {
				t.Fatalf("compact footer omits the model or current session directory: %q", plain)
			}
		}
	}
	provider.cwd = "/another/project"
	if line := normalizeWP450Lines(compact.Render(88))[0]; !strings.Contains(line, "/another/project") || strings.Contains(line, "/workspace") {
		t.Fatalf("compact footer did not follow the current session directory: %q", line)
	}
	provider.cwd = "/workspace"

	verbose := normalizeWP450Lines(NewFooterComponent(layoutFooterSession{}, provider, true).Render(88))
	if len(verbose) != 2 || verbose[0] != " /workspace (main)" ||
		verbose[1] != " ?/8.2k (auto)                                                            fixture-model" {
		t.Fatalf("verbose footer = %#v", verbose)
	}
}

func TestCompactFooterKeepsStatusAndModel(t *testing.T) {
	initTestTheme(t)
	for _, cwd := range []string{"", "/workspace"} {
		lines := normalizeWP450Lines(NewFooterComponent(
			layoutFooterSession{}, &fakeFooterDataProvider{cwd: cwd, statuses: map[string]string{"extension": "active"}}, false,
		).Render(80))
		if len(lines) != 1 || !strings.Contains(lines[0], "active") {
			t.Fatalf("compact status footer for cwd %q = %#v", cwd, lines)
		}
		if !strings.Contains(lines[0], "fixture-model") || (cwd != "" && !strings.Contains(lines[0], "/workspace")) {
			t.Fatalf("compact footer omitted the model or current session directory for cwd %q: %#v", cwd, lines)
		}
	}
	narrow := normalizeWP450Lines(NewFooterComponent(
		layoutFooterSession{}, &fakeFooterDataProvider{cwd: "/workspace", statuses: map[string]string{"extension": "active"}}, false,
	).Render(40))
	if len(narrow) != 1 || !strings.Contains(narrow[0], "active") {
		t.Fatalf("narrow footer dropped the higher-priority status: %#v", narrow)
	}
	long := normalizeWP450Lines(NewFooterComponent(
		layoutFooterSession{}, &fakeFooterDataProvider{cwd: "/a/very/long/project", statuses: map[string]string{"extension": "active"}}, false,
	).Render(40))
	if len(long) != 1 || !strings.Contains(long[0], "project") || !strings.Contains(long[0], "active") {
		t.Fatalf("narrow footer hid the current directory or status: %#v", long)
	}
}

func TestCompactFooterCapsPathAndPinsIndicatorsRight(t *testing.T) {
	initTestTheme(t)
	dot := theme.FG("success", "●")
	line := NewFooterComponent(layoutFooterSession{}, &fakeFooterDataProvider{
		cwd: "/private/tmp/claude-501/a-very-long-generated-directory/scratchpad", statuses: map[string]string{"bridge": dot},
	}, false).Render(160)[0]
	if plain := tui.StripANSI(line); !strings.Contains(plain, "…/scratchpad") || !strings.HasSuffix(strings.TrimRight(plain, " "), " ●") {
		t.Fatalf("want a capped cwd and the indicator at the far right: %q", plain)
	}
	if !strings.Contains(line, dot[:strings.Index(dot, "\x1b[39m")]+theme.FGANSI("dim")) {
		t.Fatalf("footer text after a colored status lost dim: %q", line)
	}
}

func TestThinkingFooterSlotHasFixedWidth(t *testing.T) {
	want := map[ai.ModelThinkingLevel]string{
		ai.ModelThinkingOff: "·", ai.ModelThinkingMinimal: "○", ai.ModelThinkingLow: "◔",
		ai.ModelThinkingMedium: "◑", ai.ModelThinkingHigh: "◕",
		ai.ModelThinkingXHigh: "●", ai.ModelThinkingMax: "◉",
	}
	width := 0
	for level, meter := range want {
		if got := thinkingMeter(string(level)); got != meter || tui.VisibleWidth(got) != 1 {
			t.Errorf("thinking meter %q = %q, want %q", level, got, meter)
		}
		forms := modelFooterForms(engine.AgentDisplayState{
			HasModel: true, ModelID: "model", Reasoning: true, ThinkingLevel: level,
		}, 1)
		got := tui.VisibleWidth(forms[0])
		if width == 0 {
			width = got
		} else if got != width {
			t.Fatalf("thinking footer width at %s = %d, want fixed %d: %q", level, got, width, forms[0])
		}
	}
}

type footerTelemetryProbe struct {
	statsCalls, contextCalls, autoCalls int
}

func (probe *footerTelemetryProbe) State() engine.AgentState {
	return engine.AgentState{Model: &ai.Model{ID: "probe", ContextWindow: 8192}}
}

func (probe *footerTelemetryProbe) GetContextUsage() *harness.ContextUsage {
	probe.contextCalls++
	percent := 25.0
	return &harness.ContextUsage{ContextWindow: 8192, Percent: &percent}
}

func (probe *footerTelemetryProbe) GetSessionStats() agent.SessionStats {
	probe.statsCalls++
	return agent.SessionStats{ContextUsage: probe.GetContextUsage()}
}

func (probe *footerTelemetryProbe) AutoCompactionEnabled() bool {
	probe.autoCalls++
	return true
}

func TestCompactFooterSkipsTelemetryCollection(t *testing.T) {
	initTestTheme(t)
	probe := &footerTelemetryProbe{}
	provider := &fakeFooterDataProvider{}
	compact := normalizeWP450Lines(NewFooterComponent(probe, provider, false).Render(40))
	if probe.statsCalls != 0 || probe.autoCalls != 0 || probe.contextCalls != 1 {
		t.Fatalf("compact calls = stats %d, context %d, auto %d", probe.statsCalls, probe.contextCalls, probe.autoCalls)
	}
	if len(compact) != 1 || !strings.Contains(compact[0], "?|25%") {
		t.Fatalf("compact footer = %#v", compact)
	}

	_ = NewFooterComponent(probe, provider, true).Render(40)
	if probe.statsCalls != 1 || probe.autoCalls != 1 {
		t.Fatalf("verbose calls = stats %d, context %d, auto %d", probe.statsCalls, probe.contextCalls, probe.autoCalls)
	}
}

func TestFooterMetadataDoesNotBlockRender(t *testing.T) {
	initTestTheme(t)
	provider := &blockingFooterDataProvider{
		started: make(chan struct{}), release: make(chan struct{}), invalidated: make(chan struct{}, 1),
	}
	footer := NewFooterComponent(&fakeFooterSession{}, provider, false)
	footer.Render(80)
	select {
	case <-provider.started:
		t.Fatal("compact footer started an unused Git probe")
	default:
	}
	footer.verbose = true
	rendered := make(chan []string, 1)
	go func() { rendered <- footer.Render(80) }()
	select {
	case <-rendered:
	case <-time.After(time.Second):
		close(provider.release)
		<-rendered
		t.Fatal("footer render blocked on Git metadata")
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("footer did not start its metadata refresh")
	}
	close(provider.release)
	select {
	case <-provider.invalidated:
	case <-time.After(time.Second):
		t.Fatal("footer metadata refresh did not request a redraw")
	}
	if lines := strings.Join(footer.Render(80), "\n"); !strings.Contains(lines, "async") {
		t.Fatalf("refreshed footer = %q, want async branch", lines)
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
// only git itself can resolve the branch. orb always delegates to git, so
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

type blockingDrainTerminal struct {
	*fakeTerminalImpl
	started chan struct{}
	release chan struct{}
}

func (terminal *blockingDrainTerminal) DrainInput(time.Duration, time.Duration) {
	close(terminal.started)
	<-terminal.release
}

func TestCtrlCWithDraftClearsWithoutExiting(t *testing.T) {
	ui := tui.NewTUI(newFakeTerminal(80, 24))
	keybindings := NewAppKeybindings(nil)
	mode := &InteractiveMode{
		ui: ui, keybindings: keybindings,
		editor: NewCustomEditor(ui, tui.EditorTheme{}, keybindings),
	}
	mode.setupKeyHandlers()
	mode.editor.SetText("draft")
	mode.editor.HandleInput(tui.KeyEvent{Raw: "\x03"})

	mode.mu.Lock()
	shuttingDown := mode.shutdownRequested
	mode.mu.Unlock()
	if mode.editor.GetText() != "" || shuttingDown {
		t.Fatal("Ctrl-C with a draft must clear without exiting")
	}
}

func TestDoubleEscapeUsesActiveEditorAndResetsAfterOpeningTree(t *testing.T) {
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(json.RawMessage(`{"role":"user","content":"root"}`)); err != nil {
		t.Fatal(err)
	}
	runtime := newCacheStatsRuntime(t, manager)
	t.Cleanup(runtime.Dispose)
	modeUI := tui.NewTUI(newFakeTerminal(80, 24))
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	mode := &InteractiveMode{
		session: runtime, ui: modeUI, keybindings: bindings,
		editorContainer: &tui.Container{}, chat: &tui.Container{},
	}
	mode.editor = NewCustomEditor(modeUI, tui.EditorTheme{}, bindings)
	replacement := &f12LifecycleReplacementEditor{text: "draft"}
	mode.setExtensionEditor(replacement)
	mode.editorContainer.AddChild(replacement)
	mode.interactiveUI = NewInteractiveUI(mode)
	mode.setupKeyHandlers()

	mode.editor.OnEscape()
	if !mode.lastEscape.IsZero() || replacement.text != "draft" {
		t.Fatal("Escape with active editor text armed the double-Escape action")
	}
	replacement.text = " "
	mode.editor.OnEscape()
	if mode.lastEscape.IsZero() {
		t.Fatal("whitespace-only active editor was not treated as empty")
	}

	runtime.SetDoubleEscapeAction("none")
	mode.lastEscape = time.Time{}
	mode.editor.OnEscape()
	if !mode.lastEscape.IsZero() {
		t.Fatal("disabled double-Escape action retained timing state")
	}

	runtime.SetDoubleEscapeAction("tree")
	mode.lastEscape = time.Now()
	mode.editor.OnEscape()
	if !mode.lastEscape.IsZero() {
		t.Fatal("completed double-Escape sequence was not reset")
	}
	children := mode.editorContainer.Children()
	if len(children) != 1 {
		t.Fatalf("tree editor children = %d", len(children))
	}
	if _, ok := children[0].(*TreeSelectorComponent); !ok {
		t.Fatalf("double-Escape installed %T, want tree selector", children[0])
	}
}

func TestTreeSelectionChecksCurrentLeafAtCommitTime(t *testing.T) {
	initTestTheme(t)
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(json.RawMessage(`{"role":"user","content":"first"}`)); err != nil {
		t.Fatal(err)
	}
	runtime := newCacheStatsRuntime(t, manager)
	t.Cleanup(runtime.Dispose)
	modeUI := tui.NewTUI(newFakeTerminal(80, 24))
	bindings := NewAppKeybindings(nil)
	mode := &InteractiveMode{
		session: runtime, ui: modeUI, keybindings: bindings,
		editorContainer: &tui.Container{}, chat: &tui.Container{},
	}
	mode.editor = NewCustomEditor(modeUI, tui.EditorTheme{}, bindings)
	mode.showTreeSelector()
	children := mode.editorContainer.Children()
	if len(children) != 1 {
		t.Fatalf("tree selector children = %#v", children)
	}
	selector, ok := children[0].(*TreeSelectorComponent)
	if !ok {
		t.Fatalf("tree selector child = %T", children[0])
	}

	currentLeaf, err := manager.AppendMessage(json.RawMessage(`{"role":"user","content":"streamed later"}`))
	if err != nil {
		t.Fatal(err)
	}
	selector.onSelect(currentLeaf)
	if rendered := mode.statusNoticeText(); !strings.Contains(rendered, "Already at this point") {
		t.Fatalf("status = %q, want current-leaf no-op", rendered)
	}
}

func TestQuitKeysDoNotBlockTerminalInputReader(t *testing.T) {
	for name, ctrlC := range map[string]bool{"ctrl-d": false, "single-ctrl-c": true} {
		t.Run(name, func(t *testing.T) {
			manager, err := sessionstore.InMemory(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			terminal := &blockingDrainTerminal{
				fakeTerminalImpl: newFakeTerminal(80, 24),
				started:          make(chan struct{}),
				release:          make(chan struct{}),
			}
			ui := tui.NewTUI(terminal)
			keybindings := NewAppKeybindings(nil)
			mode := &InteractiveMode{
				session: newCacheStatsRuntime(t, manager), ui: ui, keybindings: keybindings,
				editor: NewCustomEditor(ui, tui.EditorTheme{}, keybindings), inputCh: make(chan inputEntry, 1),
			}
			mode.setupKeyHandlers()

			returned := make(chan struct{})
			go func() {
				if ctrlC {
					mode.editor.HandleInput(tui.KeyEvent{Raw: "\x03"})
				} else {
					mode.editor.OnCtrlD()
				}
				close(returned)
			}()
			select {
			case <-terminal.started:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not start")
			}
			select {
			case <-returned:
				close(terminal.release)
			case <-time.After(100 * time.Millisecond):
				close(terminal.release)
				<-returned
				t.Fatal("quit key blocked the terminal input reader during teardown")
			}
			select {
			case <-mode.inputCh:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not finish")
			}
		})
	}
}

func TestFooterComponentStatuses(t *testing.T) {
	initTestTheme(t)
	session := &fakeFooterSession{}
	provider := &fakeFooterDataProvider{
		branch:   "dev",
		statuses: map[string]string{"ext": "active"},
	}
	footer := NewFooterComponent(session, provider, false)
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
	message := "Model: kimi-coding/" + strings.Repeat("k", 120)
	if got := tui.VisibleWidth(message); got != 139 {
		t.Fatalf("fixture message width = %d, want 139", got)
	}
	const width = 86
	lines := newStyledText("dim", message).Render(width)
	if len(lines) < 2 {
		t.Fatalf("rendered lines = %d, want wrapped output", len(lines))
	}
	for index, line := range lines {
		if got := tui.VisibleWidth(line); got > width {
			t.Fatalf("rendered line %d width = %d, want <= %d", index, got, width)
		}
	}
}

type fakeRenderRequester struct{}

func (f *fakeRenderRequester) RequestRender() {}

type fakeFooterSession struct{}

func (f *fakeFooterSession) State() engine.AgentState {
	return engine.AgentState{}
}

type fakeFooterDataProvider struct {
	cwd      string
	branch   string
	statuses map[string]string
}

type blockingFooterDataProvider struct {
	started     chan struct{}
	release     chan struct{}
	invalidated chan struct{}
	startOnce   sync.Once
}

func (provider *blockingFooterDataProvider) GitBranch() string {
	provider.startOnce.Do(func() { close(provider.started) })
	<-provider.release
	return "async"
}
func (*blockingFooterDataProvider) Statuses() map[string]string { return nil }
func (provider *blockingFooterDataProvider) Invalidate() {
	select {
	case provider.invalidated <- struct{}{}:
	default:
	}
}

func (f *fakeFooterDataProvider) GitBranch() string  { return f.branch }
func (f *fakeFooterDataProvider) CurrentCWD() string { return f.cwd }
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

func TestPaletteSearchSettingsDraftAndCancellation(t *testing.T) {
	initTestTheme(t)
	mode := newF12AutocompleteMode(t, true)
	previous := tui.GetKeybindings()
	tui.SetKeybindings(mode.keybindings)
	t.Cleanup(func() { tui.SetKeybindings(previous) })
	rows := mode.commandPaletteRows()
	var chosen string
	cancelled := false
	palette := newCommandPalette(rows, mode.keybindings, func() int { return 24 }, func(value string) { chosen = value }, func() { cancelled = true })
	initial := palette.Render(70)
	palette.HandleInput(tui.KeyEvent{Raw: "\x1b[200~auto-resize images\x1b[201~"})
	filtered := strings.Join(palette.Render(70), "\n")
	if !strings.Contains(filtered, "Auto-resize images") {
		t.Fatalf("setting missing: %q", filtered)
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Value, "/skill:") {
			t.Fatalf("skill leaked into palette: %q", row.Value)
		}
	}
	if len(palette.Render(70)) != len(initial) {
		t.Fatal("filtering moved the palette")
	}
	palette.HandleInput(tui.KeyEvent{Raw: "\r"})
	if chosen != "setting:auto-resize-images" {
		t.Fatalf("selected %q", chosen)
	}
	mode.editor.SetText("review this carefully")
	mode.runPaletteCommand("/template")
	if got := mode.editor.GetText(); got != "/template review this carefully" {
		t.Fatalf("draft = %q", got)
	}
	palette.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	if !cancelled {
		t.Fatal("Escape did not close filtered palette")
	}
}

func TestPaletteNoMatchResizeAndConcurrentRender(t *testing.T) {
	initTestTheme(t)
	bindings := NewAppKeybindings(nil)
	previous := tui.GetKeybindings()
	tui.SetKeybindings(bindings)
	t.Cleanup(func() { tui.SetKeybindings(previous) })
	confirmed := false
	palette := newCommandPalette([]tui.GridRow{{Value: "model", Cells: []string{"Choose model"}}}, bindings, func() int { return 14 }, func(string) { confirmed = true }, func() {})
	palette.HandleInput(tui.KeyEvent{Raw: "no-such-action"})
	palette.HandleInput(tui.KeyEvent{Raw: "\r"})
	if confirmed {
		t.Fatal("empty search confirmed an action")
	}
	if !strings.Contains(strings.Join(palette.Render(24), "\n"), "no matches") {
		t.Fatal("no empty state")
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for range 100 {
			palette.HandleInput(tui.KeyEvent{Raw: "\x7f"})
			palette.HandleInput(tui.KeyEvent{Raw: "x"})
		}
	}()
	for range 100 {
		for _, width := range []int{12, 24, 80} {
			frame := menuFrame("Commands", palette).Render(width)
			if len(frame) > 14 {
				t.Errorf("palette exceeds terminal: %d", len(frame))
			}
			for _, line := range frame {
				if tui.VisibleWidth(line) > width {
					t.Errorf("row exceeds %d columns", width)
				}
			}
		}
	}
	workers.Wait()
}

func TestComposerSlashShowsCommandsAndSkills(t *testing.T) {
	mode := newF12AutocompleteMode(t, true)
	provider := &composerAutocompleteProvider{AutocompleteProvider: mode.autocompleteProvider}
	result := provider.GetSuggestions(t.Context(), []string{"/"}, 0, 1, false)
	if result == nil {
		t.Fatal("lost resource commands")
	}
	skill, model := false, false
	for _, item := range result.Items {
		model = model || strings.TrimPrefix(item.Value, "/") == "model"
		skill = skill || item.Value == "skill:inspect-skill"
	}
	if !skill || !model {
		t.Fatal("lost command or canonical skill completion")
	}
	// Extension editors still receive the complete compatibility surface.
	canonical := mode.autocompleteProvider.GetSuggestions(t.Context(), []string{"/model"}, 0, 6, false)
	if canonical == nil || len(canonical.Items) == 0 {
		t.Fatal("extension completion contract lost model")
	}
}

func BenchmarkCommandPaletteRender(b *testing.B) {
	for _, count := range []int{100, 10000} {
		b.Run(strconv.Itoa(count)+"-commands", func(b *testing.B) {
			rows := make([]tui.GridRow, count)
			for index := range rows {
				rows[index] = tui.GridRow{Value: strconv.Itoa(index), Cells: []string{"Skill " + strconv.Itoa(index), "skill"}, Detail: []string{"A compatible skill"}}
			}
			palette := newCommandPalette(rows, NewAppKeybindings(nil), func() int { return 40 }, func(string) {}, func() {})
			palette.Render(80)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				palette.Render(80)
			}
		})
	}
}

type clickableFooterData struct {
	fakeFooterDataProvider
	clicked         int
	thinkingClicked int
}

func (data *clickableFooterData) StatusAction(key string) func() {
	if key == "orb:thinking" {
		return func() { data.thinkingClicked++ }
	}
	if key == "quota" || key == "bridge" {
		return func() { data.clicked++ }
	}
	return nil
}

func (data *clickableFooterData) StatusLabel(key string) string {
	if key == "bridge" {
		return "Bridge"
	}
	return ""
}

func TestFooterHoverRevealsIndicatorLabelInPlace(t *testing.T) {
	initTestTheme(t)
	data := &clickableFooterData{fakeFooterDataProvider: fakeFooterDataProvider{cwd: "/workspace", statuses: map[string]string{"bridge": "●"}}}
	footer := NewFooterComponent(layoutFooterSession{}, data, false)
	dot := func() (string, int) {
		line := tui.StripANSI(footer.Render(80)[0])
		return line, tui.VisibleWidth(line[:strings.LastIndex(line, "●")])
	}
	line, column := dot()
	if strings.Contains(line, "Bridge") {
		t.Fatalf("label shown without hover: %q", line)
	}
	if !footer.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Column: column}) {
		t.Fatal("hovering the dot did not change the frame")
	}
	if hovered, hoveredColumn := dot(); !strings.Contains(hovered, "Bridge ●") || hoveredColumn != column {
		t.Fatalf("hover label missing or dot moved: %q (column %d, was %d)", hovered, hoveredColumn, column)
	}
	if !footer.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: -1}) {
		t.Fatal("leaving the footer did not clear hover")
	}
	if line, _ := dot(); strings.Contains(line, "Bridge") {
		t.Fatalf("label stayed after the pointer left: %q", line)
	}
}
func TestFooterStatusClickTracksResizeAndIgnoresOtherCells(t *testing.T) {
	initTestTheme(t)
	data := &clickableFooterData{fakeFooterDataProvider: fakeFooterDataProvider{cwd: "/workspace", statuses: map[string]string{"quota": "Codex 5h 82% · 7d 46% left"}}}
	footer := NewFooterComponent(layoutFooterSession{}, data, false)
	for _, width := range []int{120, 48, 80} {
		line := tui.StripANSI(footer.Render(width)[0])
		column := strings.Index(line, "Codex")
		if column < 0 {
			t.Fatalf("status disappeared at width %d: %q", width, line)
		}
		before := data.clicked
		if !footer.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Column: column + 1}) || data.clicked != before+1 {
			t.Fatal("status click did not activate")
		}
		if footer.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Column: 1}) {
			t.Fatal("model label activated account switcher")
		}
	}
	data.statuses = nil
	footer.Render(80)
	if footer.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Column: 1}) {
		t.Fatal("removed status kept a click target")
	}
}

func TestCompactFooterQuotaAndContextAtNarrowWidths(t *testing.T) {
	percent := 4.1
	display := engine.AgentDisplayState{HasModel: true, ModelID: "gpt-5.6-luna", Provider: "openai-codex", Reasoning: true, ThinkingLevel: ai.ModelThinkingHigh}
	tokens := int64(11152)
	context := &harness.ContextUsage{Tokens: &tokens, ContextWindow: 272000, Percent: &percent}
	for _, width := range []int{20, 36, 48, 80, 140} {
		line := compactFooterLine(display, context, []string{"Codex 69% left"}, width, "")
		if tui.VisibleWidth(line) > width {
			t.Fatalf("overflow at %d: %q", width, line)
		}
		for _, noise := range []string{"openai-codex", "ctx ", "$"} {
			if strings.Contains(line, noise) {
				t.Fatalf("footer contains %q", noise)
			}
		}
		if width >= 36 && (!strings.Contains(line, "gpt-5.6-luna") || !strings.Contains(line, "Codex 69% left")) {
			t.Fatalf("lost model or quota at %d: %q", width, line)
		}
		if width >= 80 && (!strings.Contains(line, "gpt-5.6-luna ◕") || !strings.Contains(line, "11k|4%")) {
			t.Fatalf("lost useful detail: %q", line)
		}
	}
}

func TestTerminalThemeDefaultAndBackdrop(t *testing.T) {
	mode := &InteractiveMode{}
	if got := mode.themeSettingOr(""); got != "terminal" {
		t.Fatalf("default = %q", got)
	}
	if got := mode.themeSettingOr("light/dark"); got != "light/dark" {
		t.Fatalf("persisted = %q", got)
	}
	mode.themeSetting = "custom"
	if got := mode.themeSettingOr("terminal"); got != "custom" {
		t.Fatalf("override = %q", got)
	}
	previous := theme.Current()
	t.Cleanup(func() { theme.SetCurrent(previous) })
	native, _ := theme.Load(theme.LoadOptions{NoThemes: true}).Get("terminal")
	theme.SetCurrent(native)
	if got := menuSelectedBackground("choice"); got != "\x1b[4mchoice\x1b[24m" {
		t.Fatalf("selection = %q", got)
	}
	if got := backdropStyle()("behind"); got != theme.BG("modalBackdropBg", theme.FG("modalBackdropText", "behind")) {
		t.Fatalf("backdrop = %q", got)
	}
	registry := theme.Load(theme.LoadOptions{NoThemes: true})
	data, err := os.ReadFile("theme/dark.json")
	if err != nil {
		t.Fatal(err)
	}
	custom, err := theme.Parse("custom", []byte(strings.Replace(string(data), `"name": "dark"`, `"name": "custom"`, 1)), theme.TrueColor)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(custom); err != nil {
		t.Fatal(err)
	}
	for _, initial := range []string{"terminal", "dark", "light", "custom"} {
		value, _ := registry.Get(initial)
		theme.SetCurrent(value)
		backdrop := backdropStyle()
		for _, next := range []string{"light", "dark", "custom", "terminal", "light"} {
			value, _ = registry.Get(next)
			theme.SetCurrent(value)
			if got, want := backdrop("behind"), backdropStyle()("behind"); got != want {
				t.Fatalf("open modal %s -> %s: got %q, want %q", initial, next, got, want)
			}
		}
	}
}

func TestTerminalPaletteSurvivesSessionReload(t *testing.T) {
	previous := theme.Current()
	t.Cleanup(func() { theme.SetCurrent(previous) })
	cwd := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	runtimeSession, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeSession.Dispose)
	mode := &InteractiveMode{session: runtimeSession, ui: tui.NewTUI(newFakeTerminal(80, 24)), cwd: cwd}
	if err := mode.initializeTheme(); err != nil {
		t.Fatal(err)
	}
	for _, background := range []tui.RgbColor{{R: 255, G: 252, B: 239}, {R: 24, G: 27, B: 32}} {
		mode.setTerminalBackground(&background)
		palette := func() string {
			return theme.BG("toolPendingBg", "panel") + menuSelectedBackground("selected") + backdropStyle()("behind") + theme.FG("muted", "hint")
		}
		want := palette()
		for range 2 {
			// Usage toggles and session replacement both rebuild this registry.
			if err := mode.initializeTheme(); err != nil {
				t.Fatal(err)
			}
			if got := palette(); got != want {
				t.Fatalf("reload lost terminal palette for %+v:\ngot %q\nwant %q", background, got, want)
			}
		}
	}
	settings.SetTheme("light")
	if err := mode.initializeTheme(); err != nil {
		t.Fatal(err)
	}
	if theme.Current().Name != "light" {
		t.Fatal("terminal appearance replaced the explicit theme setting")
	}
}

func TestOpenPaletteRecolorsHints(t *testing.T) {
	previous := theme.Current()
	t.Cleanup(func() { theme.SetCurrent(previous) })
	native, _ := theme.Load(theme.LoadOptions{NoThemes: true, Mode: theme.TrueColor}).Get("terminal")
	theme.SetCurrent(native)
	native.SetTerminalBackground(tui.RgbColor{R: 255, G: 252, B: 239})
	palette := newCommandPalette([]tui.GridRow{
		{Value: "model", Cells: []string{"Choose model", theme.FG("muted", "ctrl+m")}},
	}, NewAppKeybindings(nil), func() int { return 32 }, func(string) {}, func() {})
	native.SetTerminalBackground(tui.RgbColor{R: 24, G: 27, B: 32})
	rendered := strings.Join(palette.Render(60), "\n")
	if !strings.Contains(rendered, theme.FG("muted", "ctrl+m")) {
		t.Fatalf("stale menu colors: %q", rendered)
	}
}

func TestFooterReasoningBarsClickAtNarrowWidths(t *testing.T) {
	initTestTheme(t)
	for _, level := range []ai.ModelThinkingLevel{ai.ModelThinkingOff, ai.ModelThinkingHigh} {
		for _, width := range []int{36, 80} {
			data := &clickableFooterData{fakeFooterDataProvider: fakeFooterDataProvider{statuses: map[string]string{"quota": "Codex 69% left"}}}
			session := wp450FooterSession{state: engine.AgentState{Model: &ai.Model{ID: "model", Reasoning: true}, ThinkingLevel: level}}
			footer := NewFooterComponent(&session, data, false)
			line := tui.StripANSI(footer.Render(width)[0])
			index := strings.Index(line, thinkingMeter(string(level)))
			if index < 0 {
				t.Fatalf("reasoning bars missing: %q", line)
			}
			col := tui.VisibleWidth(line[:index])
			footer.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Column: col, Clicks: 1})
			footer.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Column: col, Clicks: 2})
			if data.thinkingClicked != 1 || data.clicked != 0 {
				t.Fatalf("reasoning=%d quota=%d", data.thinkingClicked, data.clicked)
			}
			quota := strings.Index(line, "Codex")
			if quota >= 0 {
				footer.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Column: tui.VisibleWidth(line[:quota]), Clicks: 1})
				if data.clicked != 1 {
					t.Fatal("reasoning target replaced quota target")
				}
			}
		}
	}
}

func TestPaletteOpensManagementPagesWithoutChangingDraft(t *testing.T) {
	initTestTheme(t)
	mode := newF12AutocompleteMode(t, true)
	cwd := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan string, 3)
	registry := extensions.NewRegistry(cwd)
	err = registry.Register("management", func(api extensions.API) error {
		for _, name := range []string{"plugins", "bridge", "external-session"} {
			api.RegisterCommand(name, extensions.Command{SettingsLabel: name, Handler: func(context.Context, string, extensions.CommandContext) error { opened <- name; return nil }})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings, ExtensionRegistry: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	mode.session = runtime
	mode.setupAutocomplete()
	mode.editor.SetText("keep my draft")
	for _, name := range []string{"plugins", "bridge", "external-session"} {
		found := false
		for _, row := range mode.commandPaletteRows() {
			if row.Value == name {
				found = true
			}
			if row.Value == "/"+name {
				t.Fatalf("%s still inserts a slash command", name)
			}
		}
		if !found {
			t.Fatalf("%s page missing", name)
		}
		mode.runPaletteCommand(name)
		select {
		case got := <-opened:
			if got != name {
				t.Fatal(got)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not open", name)
		}
		if mode.editor.GetText() != "keep my draft" {
			t.Fatal("page navigation changed draft")
		}
	}
	found := false
	for _, item := range mode.settingItems() {
		if item.ID == "bridge" {
			found = true
		}
	}
	if !found {
		t.Fatal("Bridge is missing from Settings")
	}
}

func TestTranscriptRecolorsAcrossTerminalAppearanceChanges(t *testing.T) {
	previous := theme.Current()
	t.Cleanup(func() { theme.SetCurrent(previous) })
	registry := theme.Load(theme.LoadOptions{NoThemes: true})
	native, _ := registry.Get("terminal")
	theme.SetCurrent(native)
	makeComponents := func() []tui.Component {
		tool := NewToolExecutionComponent("read", "call", nil, false, nil, &fakeRenderRequester{}, "/")
		tool.UpdateResult(ai.ToolResultContent{&ai.TextContent{Text: "result"}}, false, nil, false)
		bash := NewBashExecutionComponent("echo result", &fakeRenderRequester{}, false)
		bash.AppendOutput("result")
		bash.SetComplete(nil, false)
		assistant := NewAssistantMessageComponent(&ai.AssistantMessage{Content: ai.AssistantContent{
			&ai.ThinkingContent{Thinking: strings.Repeat("reasoning ", 600)},
			&ai.TextContent{Text: "**Answer** with `code`"},
		}}, false, theme.MarkdownTheme(), "", 0, nil)
		return []tui.Component{tool, bash, assistant}
	}
	components := makeComponents()
	for _, step := range []string{"light", "dark", "light", "timeout", "dark", "explicit-light", "explicit-dark"} {
		switch step {
		case "light":
			native.SetTerminalBackground(tui.RgbColor{R: 255, G: 252, B: 239})
		case "dark":
			native.SetTerminalBackground(tui.RgbColor{R: 24, G: 27, B: 32})
		case "timeout":
			native.ClearTerminalBackground()
			if got := theme.BGANSI("toolSuccessBg"); got != "\x1b[49m" {
				t.Fatalf("timeout retained an explicit background: %q", got)
			}
		default:
			value, _ := registry.Get(strings.TrimPrefix(step, "explicit-"))
			theme.SetCurrent(value)
		}
		fresh := makeComponents()
		for i, component := range components {
			component.(interface{ Invalidate() }).Invalidate()
			if got, want := strings.Join(component.Render(70), "\n"), strings.Join(fresh[i].Render(70), "\n"); got != want {
				t.Fatalf("%s component %d kept stale colors:\n%q\nwant:\n%q", step, i, got, want)
			}
		}
	}
}

func TestAssistantStreamingOwnsPendingTextAndReusesCompletedMarkdown(t *testing.T) {
	calls := 0
	streaming := true
	transformer := func(text string, ctx extensions.MarkdownTransformContext) string {
		calls++
		if ctx.IsStreaming != streaming {
			t.Fatalf("streaming = %v, want %v", ctx.IsStreaming, streaming)
		}
		return text
	}
	first, tail := &ai.TextContent{Text: "completed"}, &ai.TextContent{Text: "pending"}
	thought := &ai.ThinkingContent{Thinking: "reasoning"}
	message := &ai.AssistantMessage{Content: ai.AssistantContent{first, thought, tail}}
	c := NewAssistantMessageComponent(nil, false, tui.MarkdownTheme{}, "", 0, []extensions.MarkdownTransformer{transformer})
	c.UpdateContentStreaming(message, true)
	tail.Text, thought.Thinking = "mutated", "changed"
	if calls != 0 {
		t.Fatal("prepared a frame before rendering")
	}
	rendered := strings.Join(c.Render(80), "\n")
	if !strings.Contains(rendered, "pending") || strings.Contains(rendered, "mutated") || !strings.Contains(rendered, "reasoning") {
		t.Fatalf("pending message changed: %s", rendered)
	}
	thought.Thinking = "reasoning"
	c.UpdateContentStreaming(message, true)
	c.Render(80)
	if calls != 4 {
		t.Fatalf("completed blocks were re-rendered: %d transformer calls, want 4", calls)
	}
	streaming = false
	c.UpdateContentStreaming(message, false)
	c.Render(80)
	if calls != 7 {
		t.Fatalf("completion did not refresh transformer context: %d", calls)
	}
	c.Invalidate()
	c.Render(80)
	if calls != 10 {
		t.Fatalf("explicit invalidation retained stale markdown: %d", calls)
	}
}

func BenchmarkAssistantStreaming(b *testing.B) {
	for _, size := range []int{64 << 10, 256 << 10} {
		for _, scenario := range []string{"active", "completed", "burst"} {
			b.Run(fmt.Sprintf("%d/%s", size, scenario), func(b *testing.B) {
				text := strings.Repeat("A paragraph with **bold** and `code`, followed by words.\n\n", size/56)
				first, tail := &ai.TextContent{Text: text}, &ai.TextContent{Text: "tail"}
				message := &ai.AssistantMessage{Content: ai.AssistantContent{first}}
				if scenario != "active" {
					message.Content = append(message.Content, tail)
				} else {
					tail = first
				}
				c := NewAssistantMessageComponent(nil, false, tui.MarkdownTheme{}, "", 0, nil)
				c.UpdateContentStreaming(message, true)
				c.Render(120)
				updates := 1
				if scenario == "burst" {
					updates = 16
				}
				b.ReportAllocs()
				n := 0
				for b.Loop() {
					for i := 0; i < updates; i++ {
						n++
						suffix := strconv.Itoa(n)
						if scenario == "active" {
							tail.Text = text + suffix
						} else {
							tail.Text = "tail " + suffix
						}
						c.UpdateContentStreaming(message, true)
					}
					c.Render(120)
				}
			})
		}
	}
}
