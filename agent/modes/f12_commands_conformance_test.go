package modes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/conformance/runner"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/tui"
)

type f12CommandFixture struct {
	SchemaVersion       int                          `json:"schemaVersion"`
	Visible             []f12Command                 `json:"visible"`
	Hidden              []f12Command                 `json:"hidden"`
	Dispatch            []string                     `json:"dispatch"`
	UnexpectedArguments []f12UnexpectedArgumentProbe `json:"unexpectedArguments"`
	Behavior            struct {
		Debug         f12DebugBehavior         `json:"debug"`
		ArminSaysHi   f12ArminBehavior         `json:"arminSaysHi"`
		DementedElves f12DementedElvesBehavior `json:"dementedElves"`
	} `json:"behavior"`
}

type f12UnexpectedArgumentProbe struct {
	Name            string   `json:"name"`
	Input           string   `json:"input"`
	DispatchTrace   []string `json:"dispatchTrace"`
	FinalEditorText string   `json:"finalEditorText"`
}

type f12Command struct {
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	ArgumentHint *string `json:"argumentHint"`
	Visible      bool    `json:"visible"`
}

type f12DebugBehavior struct {
	Terminal       f12TerminalSize `json:"terminal"`
	Path           string          `json:"path"`
	Content        string          `json:"content"`
	ChatFrame      []string        `json:"chatFrame"`
	RawChatFrame   f12RawFrame     `json:"rawChatFrame"`
	RequestRenders int             `json:"requestRenders"`
}

type f12TerminalSize struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

type f12ArminBehavior struct {
	Effect             string     `json:"effect"`
	IntervalDelayMS    float64    `json:"intervalDelayMs"`
	Ticks              int        `json:"ticks"`
	ScheduledIntervals int        `json:"scheduledIntervals"`
	ClearedIntervals   int        `json:"clearedIntervals"`
	RequestRenders     int        `json:"requestRenders"`
	Frames             []f12Frame `json:"frames"`
}

type f12DementedElvesBehavior struct {
	RequestRenders int `json:"requestRenders"`
	BundledImage   struct {
		Filename           string              `json:"filename"`
		MIMEType           string              `json:"mimeType"`
		ByteLength         int                 `json:"byteLength"`
		SHA256             string              `json:"sha256"`
		Dimensions         tui.ImageDimensions `json:"dimensions"`
		ImageChildren      int                 `json:"imageChildren"`
		TerminalCapability *string             `json:"terminalCapability"`
	} `json:"bundledImage"`
	Frames []f12Frame `json:"frames"`
}

type f12Frame struct {
	ID    string      `json:"id,omitempty"`
	Width int         `json:"width"`
	Lines []string    `json:"lines"`
	Raw   f12RawFrame `json:"raw"`
}

type f12RawFrame struct {
	LineCount int      `json:"lineCount"`
	SHA256    string   `json:"sha256"`
	Lines     []string `json:"lines"`
	Head      []string `json:"head"`
	Tail      []string `json:"tail"`
}

func TestF12InteractiveCommandRegistryMatchesUpstream(t *testing.T) {
	fixture := loadF12CommandFixture(t)
	if fixture.SchemaVersion != 4 || len(fixture.Visible) != 23 || len(fixture.Hidden) != 3 || len(fixture.UnexpectedArguments) != 19 {
		t.Fatalf("F12 command fixture = version %d, visible %d, hidden %d",
			fixture.SchemaVersion, len(fixture.Visible), len(fixture.Hidden))
	}
	for index := range fixture.Visible {
		if fixture.Visible[index].Name == "quit" {
			description := "Quit orb" // D30 public-name substitution.
			fixture.Visible[index].Description = &description
		}
	}

	actual := make([]f12Command, 0, len(agent.BuiltinSlashCommands))
	registered := make(map[string]struct{}, len(agent.BuiltinSlashCommands))
	for _, command := range agent.BuiltinSlashCommands {
		registered[command.Name] = struct{}{}
		actual = append(actual, f12Command{
			Name:         command.Name,
			Description:  commandString(command.Description),
			ArgumentHint: commandString(command.ArgumentHint),
			Visible:      true,
		})
	}
	if !reflect.DeepEqual(actual, fixture.Visible) {
		wantJSON, _ := json.MarshalIndent(fixture.Visible, "", "  ")
		gotJSON, _ := json.MarshalIndent(actual, "", "  ")
		t.Fatalf("interactive command registry differs\nwant: %s\n got: %s", wantJSON, gotJSON)
	}
	for _, command := range fixture.Hidden {
		if command.Visible || command.Description != nil || command.ArgumentHint != nil {
			t.Fatalf("upstream hidden command exposes autocomplete metadata: %+v", command)
		}
		if _, exposed := registered[command.Name]; exposed {
			t.Errorf("hidden command %q is exposed in Go autocomplete", command.Name)
		}
	}
}

func TestF12UnexpectedCommandArgumentsFallThrough(t *testing.T) {
	fixture := loadF12CommandFixture(t)
	if fixture.SchemaVersion != 4 || len(fixture.UnexpectedArguments) != 19 {
		t.Fatalf("F12 unexpected-argument probes = version %d, count %d", fixture.SchemaVersion, len(fixture.UnexpectedArguments))
	}
	for _, probe := range fixture.UnexpectedArguments {
		probe := probe
		t.Run(probe.Name, func(t *testing.T) {
			mode, _, _, _ := newF12VisibleMode(t, probe.Name)
			mode.editor.SetText(probe.Input)
			mode.editor.OnSubmit(probe.Input)

			var gotTrace []string
			select {
			case submitted := <-mode.inputCh:
				gotTrace = append(gotTrace, "input:"+strconv.Quote(submitted.text))
			default:
				t.Fatal("unexpected command arguments did not fall through to normal input")
			}
			if got := mode.editor.GetText(); got != probe.FinalEditorText {
				t.Errorf("editor text = %q, want %q", got, probe.FinalEditorText)
			}
			mode.editor.SetText("")
			mode.editor.HandleInput(tui.KeyEvent{Raw: "\x1b[A"})
			if history := mode.editor.GetText(); history == probe.Input {
				gotTrace = append(gotTrace, "history:"+strconv.Quote(history))
			} else {
				t.Errorf("history text = %q, want %q", history, probe.Input)
			}
			if !reflect.DeepEqual(gotTrace, probe.DispatchTrace) {
				t.Fatalf("dispatch trace differs\nwant: %#v\n got: %#v", probe.DispatchTrace, gotTrace)
			}
		})
	}
}

func TestF12HiddenCommandBehaviorMatchesUpstream(t *testing.T) {
	fixture := loadF12CommandFixture(t)
	assertF12HiddenBehaviorFixture(t, fixture)
	snap := runner.OpenSnapshot(t, "F12-commands", "commands.json")

	t.Run("debug", func(t *testing.T) {
		initF12RawTheme(t)
		tempRoot := shortTempRoot()
		seed, err := os.MkdirTemp(tempRoot, "pi-f12-debug-")
		if err != nil {
			t.Fatal(err)
		}
		seedName := filepath.Base(seed)
		agentDir := filepath.Join(tempRoot, "pi-f12-debug-"+seedName[len(seedName)-6:])
		if err := os.Rename(seed, agentDir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(agentDir) })
		t.Setenv(config.EnvAgentDir, agentDir)
		mode := newF12DebugMode(t, fixture.Behavior.Debug.Terminal)
		if !mode.handleSlashCommand("debug", "") {
			t.Fatal(`hidden command "debug" was not handled`)
		}

		debugPath := filepath.Join(agentDir, "pi-debug.log")
		encoded, err := os.ReadFile(debugPath)
		if err != nil {
			t.Fatal(err)
		}
		content := f12DebugTimestamp.ReplaceAllString(string(encoded), "Debug output at <timestamp>")
		content = strings.ReplaceAll(content, agentDir, "<agent-dir>")
		if content != fixture.Behavior.Debug.Content {
			t.Fatalf("debug log differs\nwant: %q\n got: %q", fixture.Behavior.Debug.Content, content)
		}
		rawFrame := replaceF12FramePaths(mode.chat.Render(52), agentDir+string(filepath.Separator), "<agent-dir>/", agentDir, "<agent-dir>")
		gotFrame := normalizeF12Lines(rawFrame)
		if updateF12RawFrame(t, snap, rawFrame, "behavior", "debug", "rawChatFrame") {
			snap.Set(gotFrame, "behavior", "debug", "chatFrame")
			return
		}
		assertF12RawFrame(t, fixture.Behavior.Debug.RawChatFrame, rawFrame)
		if !reflect.DeepEqual(gotFrame, fixture.Behavior.Debug.ChatFrame) {
			t.Fatalf("debug chat frame differs\nwant: %#v\n got: %#v", fixture.Behavior.Debug.ChatFrame, gotFrame)
		}
	})

}

var (
	f12DebugTimestamp = regexp.MustCompile(`(?m)^Debug output at [^\n]*$`)
	f12TerminalCSI    = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
)

type f12LinesComponent []string

func (component f12LinesComponent) Render(int) []string {
	return append([]string(nil), component...)
}

func newF12DebugMode(t testing.TB, size f12TerminalSize) *InteractiveMode {
	t.Helper()
	cwd := t.TempDir()
	agentDir := os.Getenv(config.EnvAgentDir)
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	messages := engine.AgentMessages{
		json.RawMessage("{\"role\":\"user\",\"content\":\"debug <user>&\u2028\u2029\",\"timestamp\":0}"),
		json.RawMessage(`{"role":"custom","customType":"fixture","content":"debug custom","display":true}`),
	}
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent:          engine.NewAgent(nil, engine.WithInitialState(engine.AgentState{Messages: messages})),
		SessionManager: manager,
		Settings:       settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	mode := newF12HiddenCommandMode(size.Columns, size.Rows)
	mode.session = runtime
	mode.ui.AddChild(f12LinesComponent{"debug <root>&\u2028\u2029", "wide 界"})
	return mode
}

func newF12HiddenCommandMode(columns, rows int) *InteractiveMode {
	return &InteractiveMode{
		ui:   tui.NewTUI(newFakeTerminal(columns, rows)),
		chat: &tui.Container{},
	}
}

func normalizeF12Lines(lines []string, replacements ...string) []string {
	normalized := make([]string, len(lines))
	for index, line := range lines {
		line = f12TerminalCSI.ReplaceAllString(strings.ReplaceAll(line, "\r", ""), "")
		for replacement := 0; replacement+1 < len(replacements); replacement += 2 {
			line = strings.ReplaceAll(line, replacements[replacement], replacements[replacement+1])
		}
		normalized[index] = strings.TrimRight(line, " \t")
	}
	for len(normalized) > 0 && normalized[len(normalized)-1] == "" {
		normalized = normalized[:len(normalized)-1]
	}
	return normalized
}

// shortTempRoot returns a writable directory whose path is as short as the
// "/tmp" upstream's F12 fixtures were captured under, so rendered paths wrap
// the same: /tmp itself, else the volume root of the temp directory (Windows
// lets any user create folders there), else the temp directory.
func shortTempRoot() string {
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() && filepath.IsAbs("/tmp") {
		return "/tmp"
	}
	if volume := filepath.VolumeName(os.TempDir()); volume != "" {
		root := volume + string(filepath.Separator)
		if probe, err := os.MkdirTemp(root, "orb-f12-"); err == nil {
			_ = os.Remove(probe)
			return root
		}
	}
	return os.TempDir()
}

func replaceF12FramePaths(lines []string, replacements ...string) []string {
	replaced := make([]string, len(lines))
	copy(replaced, lines)
	for index, line := range replaced {
		for replacement := 0; replacement+1 < len(replacements); replacement += 2 {
			originalLength := len(line)
			line = strings.ReplaceAll(line, replacements[replacement], replacements[replacement+1])
			if len(line) < originalLength {
				line += strings.Repeat(" ", originalLength-len(line))
			}
		}
		replaced[index] = line
	}
	return replaced
}

func assertF12HiddenBehaviorFixture(t testing.TB, fixture f12CommandFixture) {
	t.Helper()
	debug := fixture.Behavior.Debug
	if debug.Terminal != (f12TerminalSize{Columns: 42, Rows: 17}) || debug.Path != "<agent-dir>/pi-debug.log" || debug.RequestRenders != 1 {
		t.Fatalf("debug trace metadata = %+v", debug)
	}
}

func TestF12InteractiveCommandDispatchMatchesUpstream(t *testing.T) {
	fixture := loadF12CommandFixture(t)
	// Divergence ledger: Orb drops pi's hidden easter eggs.
	want := slices.DeleteFunc(append([]string(nil), fixture.Dispatch...), func(name string) bool {
		return name == "arminsayshi" || name == "dementedelves"
	})
	got := goInteractiveCommandDispatch(t)
	sort.Strings(want)
	sort.Strings(got)
	if reflect.DeepEqual(got, want) {
		return
	}
	wantSet := make(map[string]struct{}, len(want))
	gotSet := make(map[string]struct{}, len(got))
	for _, name := range want {
		wantSet[name] = struct{}{}
	}
	for _, name := range got {
		gotSet[name] = struct{}{}
	}
	var missing, extra []string
	for _, name := range want {
		if _, ok := gotSet[name]; !ok {
			missing = append(missing, name)
		}
	}
	for _, name := range got {
		if _, ok := wantSet[name]; !ok {
			extra = append(extra, name)
		}
	}
	t.Fatalf("interactive command dispatch differs: missing=%v extra=%v", missing, extra)
}

func commandString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func loadF12CommandFixture(t testing.TB) f12CommandFixture {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve F12 command fixture path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "conformance", "fixtures", "F12-commands", "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture f12CommandFixture
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func goInteractiveCommandDispatch(t testing.TB) []string {
	t.Helper()
	mode := &InteractiveMode{}
	names := interactiveCommandNames()
	for _, name := range names {
		if _, ok := mode.resolveSlashCommand(name, ""); !ok {
			t.Errorf("production dispatcher has no action for %q", name)
		}
	}
	return names
}
