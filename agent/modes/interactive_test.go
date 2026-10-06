package modes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if openTreeSelector(t, mode) == nil {
		t.Fatal("double-Escape did not open the tree modal")
	}
}

// openTreeSelector returns the tree selector shown in the modal, if any.
func openTreeSelector(t *testing.T, mode *InteractiveMode) *TreeSelectorComponent {
	t.Helper()
	for _, component := range mode.ui.VisibleOverlayComponents() {
		if frame, ok := component.(*tui.Frame); ok {
			if selector, ok := frame.Child.(*TreeSelectorComponent); ok {
				return selector
			}
		}
	}
	return nil
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
	selector := openTreeSelector(t, mode)
	if selector == nil {
		t.Fatal("tree modal did not open")
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
	settings.SetTheme("light/dark")
	for _, background := range []tui.RgbColor{{R: 255, G: 252, B: 239}, {R: 24, G: 27, B: 32}} {
		mode.setTerminalBackground(&background)
		if err := mode.initializeTheme(); err != nil {
			t.Fatal(err)
		}
		if got, want := theme.Current().Name, string(theme.BackgroundAppearance(background)); got != want {
			t.Fatalf("reload resolved auto pair from stale environment: %s, want %s", got, want)
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
		return []tui.Component{tool, bash, assistant, NewUserMessageComponent("**Question** with `code`", theme.MarkdownTheme(), 0, nil)}
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
