package modes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/modes/theme"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/tui"
)

type f12ApplicationFixture struct {
	SchemaVersion      int                   `json:"schemaVersion"`
	Frames             []f12ApplicationFrame `json:"frames"`
	NotificationFrames []f12ApplicationFrame `json:"notificationFrames"`
	DialogFrames       []f12ApplicationFrame `json:"dialogFrames"`
	DialogResults      []f12DialogResult     `json:"dialogResults"`
	ExternalEditor     f12ExternalEditor     `json:"externalEditor"`
	Autocomplete       struct {
		Enabled               []f12AutocompleteReplay `json:"enabled"`
		SkillCommandsDisabled []f12AutocompleteReplay `json:"skillCommandsDisabled"`
		ProviderTransfer      struct {
			DefaultAssigned     bool `json:"defaultAssigned"`
			ReplacementAssigned bool `json:"replacementAssigned"`
			SameProvider        bool `json:"sameProvider"`
			StoredProvider      bool `json:"storedProvider"`
		} `json:"providerTransfer"`
	} `json:"autocomplete"`
}

type f12AutocompleteReplay struct {
	Input  string                      `json:"input"`
	Result *f12AutocompleteSuggestions `json:"result"`
}

type f12AutocompleteSuggestions struct {
	Prefix string                 `json:"prefix"`
	Items  []tui.AutocompleteItem `json:"items"`
}

type f12ExternalEditor struct {
	KeyData     string   `json:"keyData"`
	HintVisible bool     `json:"hintVisible"`
	InitialText string   `json:"initialText"`
	FinalText   string   `json:"finalText"`
	Lifecycle   []string `json:"lifecycle"`
}

type f12DialogResult struct {
	Width              int    `json:"width"`
	Selected           string `json:"selected"`
	SelectorCancelled  bool   `json:"selectorCancelled"`
	InputValue         string `json:"inputValue"`
	InputCancelled     bool   `json:"inputCancelled"`
	PlaceholderVisible bool   `json:"placeholderVisible"`
}

type f12ApplicationFrame struct {
	ID    string   `json:"id"`
	Width int      `json:"width"`
	Lines []string `json:"lines"`
}

func TestF12ApplicationStatusFramesMatchUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	fixture := loadF12ApplicationFixture(t)
	if fixture.SchemaVersion != 6 || len(fixture.Frames) != 8 || len(fixture.NotificationFrames) != 6 || len(fixture.DialogFrames) != 10 {
		t.Fatalf("F12 application fixture = version %d, frames %d", fixture.SchemaVersion, len(fixture.Frames))
	}

	for _, width := range []int{48, 88} {
		width := width
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			terminal := newFakeTerminal(width, 40)
			ui := tui.NewTUI(terminal)
			chat := &tui.Container{}
			mode := &InteractiveMode{ui: ui, chat: chat}
			capture := func(id string) f12ApplicationFrame {
				return f12ApplicationFrame{ID: id, Width: width, Lines: chat.Render(width)}
			}

			got := make([]f12ApplicationFrame, 0, 4)
			mode.showStatusMessage("STATUS_ONE")
			got = append(got, capture("status-first"))
			mode.showStatusMessage("STATUS_TWO")
			got = append(got, capture("status-replaced"))
			chat.AddChild(tui.NewText(theme.FG("accent", "OTHER"), 1, 0, nil))
			mode.showStatusMessage("STATUS_THREE")
			got = append(got, capture("status-after-content"))
			mode.showStatusMessage("STATUS_FOUR")
			got = append(got, capture("status-after-content-replaced"))

			want := make([]f12ApplicationFrame, 0, 4)
			for _, frame := range fixture.Frames {
				if frame.Width == width {
					want = append(want, frame)
				}
			}
			if !reflect.DeepEqual(got, want) {
				wantJSON, _ := json.MarshalIndent(want, "", "  ")
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("application status frames differ\nwant: %s\n got: %s", wantJSON, gotJSON)
			}
		})
	}
}

type f12AutocompleteEditor struct {
	provider extensions.AutocompleteProvider
}

func (*f12AutocompleteEditor) Render(int) []string { return nil }
func (*f12AutocompleteEditor) HandleInput(string)  {}
func (editor *f12AutocompleteEditor) SetAutocompleteProvider(provider extensions.AutocompleteProvider) {
	editor.provider = provider
}

func TestF12ApplicationAutocompleteProviderTransferMatchesUpstream(t *testing.T) {
	fixture := loadF12ApplicationFixture(t).Autocomplete.ProviderTransfer
	if !fixture.DefaultAssigned || !fixture.ReplacementAssigned || !fixture.SameProvider || !fixture.StoredProvider {
		t.Fatalf("upstream autocomplete provider transfer = %+v", fixture)
	}

	mode := newF12AutocompleteMode(t, true)
	replacement := &f12AutocompleteEditor{}
	mode.extensionEditor = replacement
	mode.setupAutocomplete()
	if replacement.provider == nil {
		t.Fatal("active replacement editor did not receive autocomplete provider")
	}
	adapter, ok := replacement.provider.(tuiAutocompleteAdapter)
	if !ok || !reflect.DeepEqual(adapter.provider, mode.autocompleteProvider) {
		t.Fatal("active replacement editor did not receive the stored autocomplete provider")
	}
}

func TestF12ApplicationAutocompleteMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	fixture := loadF12ApplicationFixture(t)
	assertMatches := func(t *testing.T, mode *InteractiveMode, want []f12AutocompleteReplay) {
		t.Helper()
		got := make([]f12AutocompleteReplay, 0, len(want))
		for _, replay := range want {
			result := mode.autocompleteProvider.GetSuggestions(
				t.Context(), []string{replay.Input}, 0, len([]rune(replay.Input)), false,
			)
			var normalized *f12AutocompleteSuggestions
			if result != nil {
				normalized = &f12AutocompleteSuggestions{Prefix: result.Prefix, Items: result.Items}
			}
			got = append(got, f12AutocompleteReplay{Input: replay.Input, Result: normalized})
		}
		if !reflect.DeepEqual(got, want) {
			wantJSON, _ := json.MarshalIndent(want, "", "  ")
			gotJSON, _ := json.MarshalIndent(got, "", "  ")
			t.Fatalf("application autocomplete differs\nwant: %s\n got: %s", wantJSON, gotJSON)
		}
	}
	tests := []struct {
		name    string
		enabled bool
		want    []f12AutocompleteReplay
	}{
		{name: "enabled", enabled: true, want: fixture.Autocomplete.Enabled},
		{name: "skill-commands-disabled", want: fixture.Autocomplete.SkillCommandsDisabled},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			mode := newF12AutocompleteMode(t, test.enabled)
			assertMatches(t, mode, test.want)
			if !test.enabled {
				return
			}
			t.Run("post-session-start-refresh", func(t *testing.T) {
				mode.autocompleteProvider = tui.NewCombinedAutocompleteProvider(nil, mode.cwd, "")
				if err := mode.refreshResourcesAfterSessionStart(mode.session); err != nil {
					t.Fatal(err)
				}
				assertMatches(t, mode, test.want)
			})
		})
	}
}

func newF12AutocompleteMode(t *testing.T, enableSkillCommands bool) *InteractiveMode {
	t.Helper()
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	settings.SetEnableSkillCommands(enableSkillCommands)
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(cwd)
	userSource := extensions.SourceInfo{
		Path: "/fixture/extensions/commands.ts", Source: "local",
		Scope: extensions.SourceScopeUser, Origin: extensions.SourceOriginTopLevel,
	}
	registerCommand := func(path, name, description string) {
		t.Helper()
		if registerErr := registry.Register(path, func(api extensions.API) error {
			api.RegisterCommand(name, extensions.Command{Description: description})
			return nil
		}, extensions.WithSourceInfo(userSource)); registerErr != nil {
			t.Fatal(registerErr)
		}
	}
	registerCommand("extension-command", "extension-command", "Run extension command")
	registerCommand("model-one", "model", "Conflicts with a built-in")
	registerCommand("model-two", "model", "Conflicts with a built-in")

	prompts := []codingagent.PromptTemplate{{
		Name: "review-prompt", Description: "Review a path", ArgumentHint: "<path>",
		FilePath:   "/fixture/prompts/review-prompt.md",
		SourceInfo: codingagent.SourceInfo{Path: "/fixture/prompts/review-prompt.md", Source: "local", Scope: "project", Origin: "top-level"},
	}}
	skills := []codingagent.Skill{{
		Name: "inspect-skill", Description: "Inspect the workspace",
		FilePath: "/fixture/skills/inspect-skill/SKILL.md", BaseDir: "/fixture/skills/inspect-skill",
		SourceInfo: codingagent.SourceInfo{Path: "/fixture/skills/inspect-skill/SKILL.md", Source: "cli", Scope: "temporary", Origin: "top-level"},
	}}
	loader, err := codingagent.NewDefaultResourceLoader(codingagent.DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings,
		NoSkills: true, NoPromptTemplates: true, NoThemes: true, NoContextFiles: true,
		SkillsOverride: func(codingagent.ResourceSkillsResult) codingagent.ResourceSkillsResult {
			return codingagent.ResourceSkillsResult{Skills: skills, Diagnostics: []codingagent.ResourceDiagnostic{}}
		},
		PromptsOverride: func(codingagent.ResourcePromptsResult) codingagent.ResourcePromptsResult {
			return codingagent.ResourcePromptsResult{Prompts: prompts, Diagnostics: []codingagent.ResourceDiagnostic{}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = loader.Reload(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, ResourceLoader: loader,
		SlashResolver: &codingagent.SlashResolver{PromptTemplates: prompts, Skills: skills},
		AvailableModels: func() []ai.Model {
			return []ai.Model{
				{ID: "claude-sonnet-4-5", Provider: "anthropic", Name: "Claude Sonnet 4.5"},
				{ID: "gpt-5.1", Provider: "openai", Name: "GPT 5.1"},
				{ID: "openai/gpt-5", Provider: "openrouter", Name: "GPT 5 via OpenRouter"},
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	modeUI := tui.NewTUI(newFakeTerminal(88, 40))
	bindings := NewAppKeybindings(nil)
	host := &f12AutocompleteHost{authOptions: InteractiveAuthOptions{Login: []InteractiveAuthProvider{
		{ID: "openai", Name: "OpenAI", AuthType: aiauth.AuthTypeOAuth},
		{ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth},
		{ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeAPIKey},
		{ID: "google", Name: "Google", AuthType: aiauth.AuthTypeAPIKey},
	}}}
	mode := &InteractiveMode{session: runtime, cwd: cwd, ui: modeUI, keybindings: bindings, options: InteractiveModeOptions{Host: host}}
	mode.editor = NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode.setupAutocomplete()
	return mode
}

type f12AutocompleteHost struct {
	InteractiveSessionHost
	authOptions InteractiveAuthOptions
}

func (host *f12AutocompleteHost) AuthOptions(context.Context) (InteractiveAuthOptions, error) {
	return host.authOptions, nil
}

func TestF12ExtensionEditorExternalLifecycleMatchesUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	fixture := loadF12ApplicationFixture(t).ExternalEditor
	lifecyclePath := filepath.Join(t.TempDir(), "lifecycle.txt")
	t.Setenv("PI_GO_F12_EXTERNAL_EDITOR_LOG", lifecyclePath)
	externalEditorCommand := strings.Join([]string{os.Args[0], "-test.run=TestF12ExternalEditorHelper", "--"}, " ")
	t.Setenv("VISUAL", "pi-go-f12-visual-must-not-run")
	t.Setenv("EDITOR", "")

	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	terminal := &f12ExternalEditorTerminal{fakeTerminalImpl: newFakeTerminal(88, 40), lifecyclePath: lifecyclePath}
	modeUI := tui.NewTUI(terminal)
	editor := newExtensionEditorComponent(modeUI, bindings, "Edit value", fixture.InitialText, func(string) {}, func() {}, externalEditorCommand)
	terminal.component = editor
	modeUI.AddChild(editor)
	if err := modeUI.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = modeUI.Stop() })
	terminal.record = true
	baselineRedraws := modeUI.FullRedraws()

	if got := editor.editor.GetText(); got != fixture.InitialText {
		t.Fatalf("initial editor text = %q, want %q", got, fixture.InitialText)
	}
	if got := strings.Contains(strings.Join(editor.Render(88), "\n"), "external editor"); got != fixture.HintVisible {
		t.Fatalf("external editor hint visible = %t, want %t", got, fixture.HintVisible)
	}
	editor.HandleInput(tui.KeyEvent{Raw: fixture.KeyData})

	deadline := time.Now().Add(5 * time.Second)
	for {
		lifecycle, _ := os.ReadFile(lifecyclePath)
		if strings.Contains(string(lifecycle), "start:") && modeUI.FullRedraws() > baselineRedraws {
			if got := editor.editor.GetText(); got != fixture.FinalText {
				t.Fatalf("external editor text = %q, want %q", got, fixture.FinalText)
			}
			got := append(strings.Split(strings.TrimSpace(string(lifecycle)), "\n"), "render:true")
			if !reflect.DeepEqual(got, fixture.Lifecycle) {
				t.Fatalf("external editor lifecycle = %#v, want %#v", got, fixture.Lifecycle)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("external editor lifecycle did not complete: lifecycle=%q redraws=%d", lifecycle, modeUI.FullRedraws()-baselineRedraws)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestF12ExternalEditorHelper(t *testing.T) {
	logPath := os.Getenv("PI_GO_F12_EXTERNAL_EDITOR_LOG")
	if logPath == "" {
		return
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString("edit\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if len(os.Args) == 0 {
		t.Fatal("missing external editor path")
	}
	if err = os.WriteFile(os.Args[len(os.Args)-1], []byte("edited externally\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

type f12ExternalEditorTerminal struct {
	*fakeTerminalImpl
	component     *extensionEditorComponent
	lifecyclePath string
	record        bool
}

func (terminal *f12ExternalEditorTerminal) Start(func(string), func()) error {
	if terminal.record {
		terminal.appendLifecycle("start:" + terminal.component.editor.GetText())
	}
	return nil
}

func (terminal *f12ExternalEditorTerminal) Stop() error {
	if terminal.record {
		terminal.appendLifecycle("stop")
	}
	return nil
}

func (terminal *f12ExternalEditorTerminal) appendLifecycle(event string) {
	file, err := os.OpenFile(terminal.lifecyclePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.WriteString(event + "\n")
	_ = file.Close()
}

func TestF12ExtensionDialogFramesMatchUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	fixture := loadF12ApplicationFixture(t)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)

	for _, expectedResult := range fixture.DialogResults {
		expectedResult := expectedResult
		t.Run(strconv.Itoa(expectedResult.Width), func(t *testing.T) {
			width := expectedResult.Width
			var selected string
			selectorCancelled := false
			selector := newExtensionSelectorComponent(
				"Pick one",
				[]string{"alpha", "beta", "gamma"},
				func(value string) { selected = value },
				func() { selectorCancelled = true },
				nil,
			)
			got := []f12ApplicationFrame{{ID: "selector-initial", Width: width, Lines: selector.Render(width)}}
			selector.HandleInput(tui.KeyEvent{Raw: "\x1b[B"})
			got = append(got, f12ApplicationFrame{ID: "selector-down", Width: width, Lines: selector.Render(width)})
			selector.HandleInput(tui.KeyEvent{Raw: "\r"})

			var inputValue string
			inputCancelled := false
			input := newExtensionInputComponent(
				"Enter value",
				"PLACEHOLDER_IS_IGNORED",
				func(value string) { inputValue = value },
				func() { inputCancelled = true },
				nil,
			)
			got = append(got, f12ApplicationFrame{ID: "input-initial", Width: width, Lines: input.Render(width)})
			input.HandleInput(tui.KeyEvent{Raw: "abc"})
			got = append(got, f12ApplicationFrame{ID: "input-typed", Width: width, Lines: input.Render(width)})
			input.HandleInput(tui.KeyEvent{Raw: "\r"})

			terminal := newFakeTerminal(width, 40)
			modeUI := tui.NewTUI(terminal)
			editor := newExtensionEditorComponent(modeUI, bindings, "Edit value", "alpha\nbeta", func(string) {}, func() {}, "false")
			got = append(got, f12ApplicationFrame{ID: "editor-prefill", Width: width, Lines: editor.Render(width)})

			var want []f12ApplicationFrame
			for _, frame := range fixture.DialogFrames {
				if frame.Width == width {
					want = append(want, frame)
				}
			}
			if !reflect.DeepEqual(got, want) {
				wantJSON, _ := json.MarshalIndent(want, "", "  ")
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("extension dialog frames differ\nwant: %s\n got: %s", wantJSON, gotJSON)
			}
			actualResult := f12DialogResult{
				Width: width, Selected: selected, SelectorCancelled: selectorCancelled,
				InputValue: inputValue, InputCancelled: inputCancelled, PlaceholderVisible: false,
			}
			if actualResult != expectedResult {
				t.Fatalf("extension dialog result = %+v, want %+v", actualResult, expectedResult)
			}
		})
	}
}

func TestF12InteractiveUISelectUsesEditorSlot(t *testing.T) {
	initF12ApplicationTheme(t)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	modeUI := tui.NewTUI(newFakeTerminal(48, 40))
	mode := &InteractiveMode{
		ui:              modeUI,
		keybindings:     bindings,
		editorContainer: &tui.Container{},
		widgetAbove:     &tui.Container{},
		footerStatuses:  make(map[string]string),
	}
	mode.editor = NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode.editorContainer.AddChild(mode.editor)
	interactiveUI := NewInteractiveUI(mode)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = interactiveUI.Select(ctx, "Pick one", []string{"alpha", "beta", "gamma"}, nil)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		lines := mode.editorContainer.Render(48)
		if len(lines) > 0 && strings.Contains(lines[0], "─") {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("extension selector was not installed in the editor slot; editor=%#v widget=%#v", lines, mode.widgetAbove.Render(48))
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

func TestInteractiveUISelectItemsPreservesDisplayLabels(t *testing.T) {
	initF12ApplicationTheme(t)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	modeUI := tui.NewTUI(newFakeTerminal(48, 40))
	mode := &InteractiveMode{
		ui:              modeUI,
		keybindings:     bindings,
		editorContainer: &tui.Container{},
		widgetAbove:     &tui.Container{},
		footerStatuses:  make(map[string]string),
	}
	mode.editor = NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode.editorContainer.AddChild(mode.editor)
	interactiveUI := NewInteractiveUI(mode)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = interactiveUI.selectItems(ctx, "Choose provider", []tui.SelectItem{{
			Value: "0", Label: "Provider One ✓ configured",
		}}, nil)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		lines := strings.Join(mode.editorContainer.Render(48), "\n")
		if strings.Contains(lines, "✓ configured") {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("selector rendered item identity instead of display label: %q", lines)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

func TestF12ApplicationNotificationsMatchUpstream(t *testing.T) {
	initF12ApplicationTheme(t)
	fixture := loadF12ApplicationFixture(t)
	for _, width := range []int{48, 88} {
		width := width
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			mode := &InteractiveMode{
				ui:             tui.NewTUI(newFakeTerminal(width, 40)),
				chat:           &tui.Container{},
				footerStatuses: make(map[string]string),
			}
			interactiveUI := NewInteractiveUI(mode)
			got := make([]f12ApplicationFrame, 0, 3)
			capture := func(id string) {
				got = append(got, f12ApplicationFrame{ID: id, Width: width, Lines: mode.chat.Render(width)})
			}
			interactiveUI.Notify("NOTICE", extensions.NotifyInfo)
			capture("notify-info")
			interactiveUI.Notify("CAUTION", extensions.NotifyWarning)
			capture("notify-warning")
			interactiveUI.Notify("BROKEN", extensions.NotifyError)
			capture("notify-error")

			want := make([]f12ApplicationFrame, 0, 3)
			for _, frame := range fixture.NotificationFrames {
				if frame.Width == width {
					want = append(want, frame)
				}
			}
			if !reflect.DeepEqual(got, want) {
				wantJSON, _ := json.MarshalIndent(want, "", "  ")
				gotJSON, _ := json.MarshalIndent(got, "", "  ")
				t.Fatalf("application notification frames differ\nwant: %s\n got: %s", wantJSON, gotJSON)
			}
		})
	}
}

func initF12ApplicationTheme(t testing.TB) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve F12 application theme path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "theme", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := theme.Parse("dark", encoded, theme.Color256)
	if err != nil {
		t.Fatal(err)
	}
	theme.SetCurrent(parsed)
	t.Cleanup(func() { theme.SetCurrent(nil) })
}

func loadF12ApplicationFixture(t testing.TB) f12ApplicationFixture {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve F12 application fixture path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "conformance", "fixtures", "F12-app", "status-frames.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture f12ApplicationFixture
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}
