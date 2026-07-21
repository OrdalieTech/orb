package modes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/internal/jsonwire"
	"github.com/OrdalieTech/pigo/tui"

	theme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
)

type f12VisibleFixture struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Width         int                 `json:"width"`
	Commands      []f12VisibleCommand `json:"commands"`
}

type f12VisibleCommand struct {
	Name            string         `json:"name"`
	Input           string         `json:"input"`
	DispatchTrace   []string       `json:"dispatchTrace"`
	FinalEditorText string         `json:"finalEditorText"`
	Transition      *string        `json:"transition"`
	Trace           []string       `json:"trace"`
	Chat            f12VisibleChat `json:"chat"`
}

type f12VisibleChat struct {
	LineCount int         `json:"lineCount"`
	SHA256    string      `json:"sha256"`
	Raw       f12RawFrame `json:"raw"`
	Lines     []string    `json:"lines"`
	Head      []string    `json:"head"`
	Tail      []string    `json:"tail"`
}

func TestF12VisibleCommandBehaviorMatchesUpstream(t *testing.T) {
	fixture := loadF12VisibleFixture(t)
	if fixture.SchemaVersion != 2 || fixture.Width != 100 || len(fixture.Commands) != 22 {
		t.Fatalf("F12 visible fixture = version %d, width %d, commands %d", fixture.SchemaVersion, fixture.Width, len(fixture.Commands))
	}
	for _, command := range fixture.Commands {
		command := command
		t.Run(command.Name, func(t *testing.T) {
			mode, host, terminal, temporary := newF12VisibleMode(t, command.Name)
			input := strings.ReplaceAll(command.Input, "<tmp>", temporary)
			mode.editor.SetText(input)
			var editorTrace []string
			mode.editor.OnChange = func(value string) {
				editorTrace = append(editorTrace, "editor:"+strconv.Quote(value))
			}
			if command.Name == "compact" {
				_, instructions := parseSlashCommand(input)
				unsubscribe := mode.session.Subscribe(func(event any) {
					if _, ok := event.(codingagent.CompactionStartEvent); !ok {
						return
					}
					host.traceStatusClear()
					host.addTrace("compact:" + instructions)
				})
				t.Cleanup(unsubscribe)
			}
			initialThemeController := mode.themeController
			mode.editor.OnSubmit(input)

			if command.Name == "import" {
				waitF12Visible(t, func() bool {
					return strings.Contains(strings.Join(normalizeF12Lines(mode.editorContainer.Render(fixture.Width)), "\n"), "Import session")
				})
				host.addTrace(f12VisibleConfirmTrace(t, mode.editorContainer.Render(fixture.Width)))
				terminal.Send("\r")
			}
			if command.Name == "login" || command.Name == "logout" || command.Name == "new" || command.Name == "import" {
				waitF12Visible(t, func() bool { return len(host.Trace()) > 0 || len(mode.chat.Render(fixture.Width)) > 0 })
			}
			if command.Transition != nil {
				waitF12Visible(t, func() bool { return f12VisibleSelectorActive(mode, fixture.Width) })
			}
			if command.Name == "import" || command.Name == "new" {
				waitF12Visible(t, func() bool { return len(host.Trace()) == len(command.Trace) })
			}
			observeF12VisibleBehaviorTrace(t, command, mode, host, temporary, initialThemeController)
			if command.Name == "compact" {
				waitF12Visible(t, func() bool { return len(host.Trace()) == len(command.Trace) })
			}
			assertF12VisibleDispatchTrace(t, command, mode, input, editorTrace, temporary)
			gotBehaviorTrace := normalizeF12Trace(host.Trace(), temporary)
			if !reflect.DeepEqual(gotBehaviorTrace, command.Trace) {
				t.Errorf("behavior trace differs\nwant: %#v\n got: %#v", command.Trace, gotBehaviorTrace)
			}

			if got := mode.editor.GetText(); got != command.FinalEditorText {
				t.Errorf("editor text = %q, want %q", got, command.FinalEditorText)
			}
			if got := f12VisibleTransition(mode, command.Name, fixture.Width); !reflect.DeepEqual(got, command.Transition) {
				t.Errorf("transition = %v, want %v", stringPointerValue(got), stringPointerValue(command.Transition))
			}
			rawLines := replaceF12FramePaths(mode.chat.Render(fixture.Width), temporary, "<tmp>")
			assertF12RawFrame(t, command.Chat.Raw, rawLines)
			lines := normalizeF12Lines(rawLines)
			assertF12VisibleChat(t, command.Chat, lines)
		})
	}
}

func loadF12VisibleFixture(t testing.TB) f12VisibleFixture {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve F12 visible fixture path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "conformance", "fixtures", "F12-visible-commands", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture f12VisibleFixture
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func assertF12VisibleChat(t testing.TB, want f12VisibleChat, got []string) {
	t.Helper()
	digest := f12VisibleLinesDigest(t, got)
	if len(got) == want.LineCount && digest == want.SHA256 {
		return
	}
	if want.Lines != nil || len(got) <= 24 {
		t.Errorf("chat differs: lines=%d sha256=%s\nwant: %#v\n got: %#v", len(got), digest, want.Lines, got)
		return
	}
	gotHead := append([]string(nil), got[:min(20, len(got))]...)
	gotTail := append([]string(nil), got[max(0, len(got)-20):]...)
	t.Errorf("chat differs: lines=%d sha256=%s, want lines=%d sha256=%s\nwant head: %#v\n got head: %#v\nwant tail: %#v\n got tail: %#v",
		len(got), digest, want.LineCount, want.SHA256, want.Head, gotHead, want.Tail, gotTail)
}

func f12VisibleLinesDigest(t testing.TB, lines []string) string {
	t.Helper()
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(lines); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

func assertF12RawFrame(t testing.TB, want f12RawFrame, got []string) {
	t.Helper()
	digest := f12RawLinesDigest(t, got)
	if len(got) == want.LineCount && digest == want.SHA256 {
		return
	}
	if want.Lines != nil || len(got) <= 24 {
		var differences strings.Builder
		differenceCount := 0
		for index := 0; index < min(len(want.Lines), len(got)); index++ {
			if want.Lines[index] != got[index] {
				fmt.Fprintf(&differences, "\nline %d\nwant: %q\n got: %q", index, want.Lines[index], got[index])
				differenceCount++
				if differenceCount == 12 {
					break
				}
			}
		}
		t.Fatalf("raw frame differs: lines=%d sha256=%s, want lines=%d sha256=%s%s", len(got), digest, want.LineCount, want.SHA256, differences.String())
	}
	gotHead := append([]string(nil), got[:min(20, len(got))]...)
	gotTail := append([]string(nil), got[max(0, len(got)-20):]...)
	t.Fatalf("raw frame differs: lines=%d sha256=%s, want lines=%d sha256=%s\nwant head: %#v\n got head: %#v\nwant tail: %#v\n got tail: %#v",
		len(got), digest, want.LineCount, want.SHA256, want.Head, gotHead, want.Tail, gotTail)
}

func f12RawLinesDigest(t testing.TB, lines []string) string {
	t.Helper()
	encoded, err := jsonwire.Marshal(lines)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func stringPointerValue(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func f12VisibleTransition(mode *InteractiveMode, command string, width int) *string {
	if !f12VisibleSelectorActive(mode, width) {
		return nil
	}
	value := map[string]string{
		"settings": "settings-selector",
		"model":    "model-selector:fixture/model",
		"trust":    "trust-selector",
		"resume":   "session-selector",
	}[command]
	if value == "" {
		return nil
	}
	return &value
}

func f12VisibleSelectorActive(mode *InteractiveMode, width int) bool {
	return !reflect.DeepEqual(normalizeF12Lines(mode.editorContainer.Render(width)), normalizeF12Lines(mode.editor.Render(width)))
}

func waitF12Visible(t testing.TB, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for command behavior")
		}
		time.Sleep(time.Millisecond)
	}
}

func f12VisibleConfirmTrace(t testing.TB, rendered []string) string {
	t.Helper()
	lines := normalizeF12Lines(rendered)
	for index, line := range lines {
		if strings.TrimSpace(line) != "Import session" {
			continue
		}
		for message := index + 1; message < len(lines); message++ {
			value := strings.TrimSpace(lines[message])
			if value != "" {
				return "confirm:Import session:" + value
			}
		}
	}
	t.Fatalf("import confirmation was not observable in %#v", lines)
	return ""
}

func observeF12VisibleBehaviorTrace(
	t testing.TB,
	command f12VisibleCommand,
	mode *InteractiveMode,
	host *f12VisibleHost,
	temporary string,
	initialThemeController *theme.Controller,
) {
	t.Helper()
	switch command.Name {
	case "export":
		path := filepath.Join(temporary, "session.jsonl")
		encoded, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read exported JSONL: %v", err)
		}
		if len(encoded) == 0 || encoded[len(encoded)-1] != '\n' {
			t.Fatalf("exported JSONL is not newline terminated: %q", encoded)
		}
		host.addTrace("export:jsonl:" + path)
	case "name":
		name := mode.session.Manager().GetSessionName()
		if name == nil {
			t.Fatal("session name was not persisted")
			return
		}
		host.addTrace("name:" + *name)
	case "quit":
		trace := host.Trace()
		if initialThemeController == nil || mode.themeController != nil {
			t.Fatal("theme controller was not released before terminal shutdown")
		}
		trace = append([]string{"theme-stop"}, trace...)
		mode.mu.Lock()
		shutdown := mode.shutdownRequested
		mode.mu.Unlock()
		if !shutdown {
			t.Fatal("quit did not request a zero-status shutdown")
		}
		trace = append(trace, "exit:0")
		host.setTrace(trace)
	}
}

func normalizeF12Trace(trace []string, temporary string) []string {
	normalized := append([]string{}, trace...)
	for index := range normalized {
		normalized[index] = strings.ReplaceAll(normalized[index], temporary, "<tmp>")
	}
	return normalized
}

func assertF12VisibleDispatchTrace(t testing.TB, command f12VisibleCommand, mode *InteractiveMode, input string, editorTrace []string, temporary string) {
	t.Helper()
	if !reflect.DeepEqual(editorTrace, []string{`editor:""`}) {
		t.Fatalf("editor dispatch trace = %#v, want one clear", editorTrace)
	}
	_, inputArgs := parseSlashCommand(input)
	action, ok := mode.resolveSlashCommand(command.Name, inputArgs)
	if !ok {
		t.Fatalf("production dispatcher has no action for %q", command.Name)
	}
	encodedArguments, err := json.Marshal(action.arguments)
	if err != nil {
		t.Fatal(err)
	}
	actionTrace := strings.ReplaceAll("action:"+action.name+":"+string(encodedArguments), temporary, "<tmp>")
	got := []string{actionTrace, editorTrace[0]}
	if slashCommandClearsEditorFirst(command.Name) {
		got[0], got[1] = got[1], got[0]
	}
	if !reflect.DeepEqual(got, command.DispatchTrace) {
		t.Fatalf("dispatch trace differs\nwant: %#v\n got: %#v", command.DispatchTrace, got)
	}
}

type f12VisibleTerminal struct {
	mu        sync.Mutex
	onInput   func(string)
	columns   int
	rows      int
	trace     func(string)
	started   chan struct{}
	startOnce sync.Once
}

func (terminal *f12VisibleTerminal) Start(onInput func(string), _ func()) error {
	terminal.mu.Lock()
	terminal.onInput = onInput
	started := terminal.started
	terminal.mu.Unlock()
	if started != nil {
		terminal.startOnce.Do(func() { close(started) })
	}
	return nil
}
func (terminal *f12VisibleTerminal) Stop() error {
	if terminal.trace != nil {
		terminal.trace("stop")
	}
	return nil
}
func (terminal *f12VisibleTerminal) DrainInput(maxDuration, _ time.Duration) {
	if terminal.trace != nil {
		terminal.trace(fmt.Sprintf("drain:%d", maxDuration.Milliseconds()))
	}
}
func (*f12VisibleTerminal) Write(string)              {}
func (terminal *f12VisibleTerminal) Columns() int     { return terminal.columns }
func (terminal *f12VisibleTerminal) Rows() int        { return terminal.rows }
func (*f12VisibleTerminal) KittyProtocolActive() bool { return false }
func (*f12VisibleTerminal) MoveBy(int)                {}
func (*f12VisibleTerminal) HideCursor()               {}
func (*f12VisibleTerminal) ShowCursor()               {}
func (*f12VisibleTerminal) ClearLine()                {}
func (*f12VisibleTerminal) ClearFromCursor()          {}
func (*f12VisibleTerminal) ClearScreen()              {}
func (*f12VisibleTerminal) SetTitle(string)           {}
func (*f12VisibleTerminal) SetProgress(bool)          {}
func (terminal *f12VisibleTerminal) Send(value string) {
	terminal.mu.Lock()
	onInput := terminal.onInput
	terminal.mu.Unlock()
	if onInput != nil {
		onInput(value)
	}
}

type f12VisibleHost struct {
	mu      sync.Mutex
	runtime *codingagent.SessionRuntime
	cwd     string
	status  *tui.Container
	trace   []string
	cleared bool
}

func (host *f12VisibleHost) addTrace(value string) {
	host.mu.Lock()
	host.trace = append(host.trace, value)
	host.mu.Unlock()
}
func (host *f12VisibleHost) Trace() []string {
	host.mu.Lock()
	defer host.mu.Unlock()
	return append([]string(nil), host.trace...)
}
func (host *f12VisibleHost) setTrace(trace []string) {
	host.mu.Lock()
	host.trace = append([]string(nil), trace...)
	host.mu.Unlock()
}
func (host *f12VisibleHost) traceStatusClear() {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.cleared || host.status == nil || len(host.status.Render(10)) != 0 {
		return
	}
	host.cleared = true
	host.trace = append(host.trace, "clear-status")
}
func (host *f12VisibleHost) Session() *codingagent.SessionRuntime                    { return host.runtime }
func (*f12VisibleHost) SetRebindSession(func(*codingagent.SessionRuntime) error)     {}
func (*f12VisibleHost) SetBeforeSessionInvalidate(func())                            {}
func (*f12VisibleHost) SetAfterSessionStart(func(*codingagent.SessionRuntime) error) {}
func (host *f12VisibleHost) NewSession(context.Context, *extensions.NewSessionOptions) (extensions.SessionReplacementResult, error) {
	host.traceStatusClear()
	host.addTrace("new-session")
	return extensions.SessionReplacementResult{}, nil
}
func (host *f12VisibleHost) SwitchSession(context.Context, string, string, *extensions.SwitchSessionOptions) (extensions.SessionReplacementResult, error) {
	host.addTrace("switch-session")
	return extensions.SessionReplacementResult{}, nil
}
func (host *f12VisibleHost) Fork(context.Context, string, *extensions.ForkOptions) (InteractiveForkResult, error) {
	host.addTrace("fork")
	return InteractiveForkResult{}, nil
}
func (host *f12VisibleHost) ImportSession(_ context.Context, inputPath, _ string) (extensions.SessionReplacementResult, error) {
	host.traceStatusClear()
	host.addTrace("import:" + inputPath)
	return extensions.SessionReplacementResult{}, nil
}
func (host *f12VisibleHost) Reload(context.Context) error {
	host.addTrace("reload")
	return nil
}
func (*f12VisibleHost) ListProjectSessions(sessionstore.SessionListProgress) []sessionstore.SessionInfo {
	return nil
}
func (*f12VisibleHost) ListAllSessions(sessionstore.SessionListProgress) []sessionstore.SessionInfo {
	return nil
}
func (host *f12VisibleHost) TrustState() (InteractiveTrustState, error) {
	return InteractiveTrustState{CWD: host.cwd, Options: config.GetProjectTrustOptions(host.cwd, true)}, nil
}
func (*f12VisibleHost) SetProjectTrust(context.Context, []config.ProjectTrustUpdate) error {
	return nil
}
func (*f12VisibleHost) AuthOptions(context.Context) (InteractiveAuthOptions, error) {
	return InteractiveAuthOptions{}, nil
}
func (*f12VisibleHost) Login(context.Context, string, aiauth.AuthType, aiauth.AuthInteraction) error {
	return nil
}
func (*f12VisibleHost) Logout(context.Context, string) error { return nil }
func (host *f12VisibleHost) Dispose()                        { host.addTrace("dispose") }

func newF12VisibleMode(t *testing.T, command string) (*InteractiveMode, *f12VisibleHost, *f12VisibleTerminal, string) {
	t.Helper()
	initF12RawTheme(t)
	cwd, err := os.MkdirTemp("/tmp", "f12v-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cwd) })
	agentDir := filepath.Join(cwd, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	var manager *sessionstore.SessionManager
	if command == "export" || command == "share" {
		manager, err = sessionstore.Create(cwd, filepath.Join(cwd, "sessions"), sessionstore.WithSessionID("fixture-session-id"))
	} else {
		manager, err = sessionstore.InMemory(cwd, sessionstore.WithSessionID("fixture-session-id"))
	}
	if err != nil {
		t.Fatal(err)
	}
	if command == "session" {
		if _, err := manager.AppendSessionInfo("fixture session"); err != nil {
			t.Fatal(err)
		}
	}
	sessionRuntime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessionRuntime.Dispose)

	terminal := &f12VisibleTerminal{columns: 100, rows: 40}
	uiInstance := tui.NewTUI(terminal)
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	editor := NewCustomEditor(uiInstance, tui.EditorTheme{}, bindings)
	mode := &InteractiveMode{
		session: sessionRuntime, ui: uiInstance, keybindings: bindings, editor: editor,
		mdTheme: theme.MarkdownTheme(), options: InteractiveModeOptions{},
		chat: &tui.Container{}, pendingMessages: &tui.Container{}, status: &tui.Container{},
		editorContainer: &tui.Container{}, footerStatuses: map[string]string{},
		inputCh: make(chan inputEntry, 8), toolComponents: map[string]*ToolExecutionComponent{}, cwd: cwd, keyDisplayOS: "linux",
	}
	mode.status.AddChild(tui.NewText("fixture status", 0, 0, nil))
	// Fixtures were extracted in 256-color mode; pin it so the live COLORTERM cannot leak in.
	registry := theme.Load(theme.LoadOptions{CWD: cwd, AgentDir: agentDir, NoThemes: true, Mode: theme.Color256})
	mode.themeRegistry = registry
	mode.themeController = theme.Initialize(registry, "dark", theme.Dark, nil)
	host := &f12VisibleHost{runtime: sessionRuntime, cwd: cwd, status: mode.status}
	terminal.trace = host.addTrace
	mode.options.Host = host
	if command == "share" {
		mode.exportHTML = func(outputPath string) (string, error) {
			if outputPath != "" {
				t.Fatalf("share HTML output path = %q, want default", outputPath)
			}
			host.addTrace("export:html:<default>")
			return filepath.Join(cwd, "session.html"), nil
		}
	}
	mode.interactiveUI = NewInteractiveUI(mode)
	mode.editorContainer.AddChild(editor)
	mode.setupEditorSubmitHandler()
	if command == "reload" {
		mode.streaming = true
	}
	uiInstance.AddChild(mode.chat)
	uiInstance.AddChild(mode.editorContainer)
	uiInstance.SetFocus(editor)
	if err := uiInstance.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = uiInstance.Stop() })
	return mode, host, terminal, cwd
}

func initF12RawTheme(t *testing.T) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "theme", "dark.json"))
	if err != nil {
		t.Fatal("reading dark.json:", err)
	}
	parsed, err := theme.Parse("dark", data, theme.Color256)
	if err != nil {
		t.Fatal("parsing dark theme:", err)
	}
	theme.SetCurrent(parsed)
	t.Cleanup(func() { theme.SetCurrent(nil) })
}

var _ InteractiveSessionHost = (*f12VisibleHost)(nil)
var _ tui.Terminal = (*f12VisibleTerminal)(nil)

func TestF12VisibleFixtureDispatchCoversEveryVisibleCommand(t *testing.T) {
	fixture := loadF12VisibleFixture(t)
	seen := make(map[string]struct{}, len(fixture.Commands))
	for _, command := range fixture.Commands {
		if _, duplicate := seen[command.Name]; duplicate {
			t.Errorf("duplicate command %q", command.Name)
		}
		seen[command.Name] = struct{}{}
		if command.FinalEditorText != "" || len(command.DispatchTrace) < 2 {
			t.Errorf("invalid dispatch fixture for %q: editor=%q trace=%v", command.Name, command.FinalEditorText, command.DispatchTrace)
		}
	}
	if got := fmt.Sprint(len(seen)); got != "22" {
		t.Fatalf("visible command count = %s", got)
	}
}
