package modes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/tui"
)

type sessionSelectorFixture struct {
	SchemaVersion int `json:"schemaVersion"`
	Width         int `json:"width"`
	Searches      []struct {
		ID         string   `json:"id"`
		Query      string   `json:"query"`
		SortMode   string   `json:"sortMode"`
		NameFilter string   `json:"nameFilter"`
		Result     []string `json:"result"`
	} `json:"searches"`
	Frames []struct {
		ID    string   `json:"id"`
		Lines []string `json:"lines"`
	} `json:"frames"`
	Callbacks struct {
		Selected      []string `json:"selected"`
		Cancellations int      `json:"cancellations"`
	} `json:"callbacks"`
	Lifetime []selectorLifetimeTrace `json:"lifetime"`
}

type selectorLifetimeTrace struct {
	ID                       string   `json:"id"`
	Events                   []string `json:"events"`
	ScheduledTimeouts        int      `json:"scheduledTimeouts"`
	ClearedTimeouts          int      `json:"clearedTimeouts"`
	PendingTimeoutsAfterExit int      `json:"pendingTimeoutsAfterExit"`
	FiredTimeoutsAfterExit   int      `json:"firedTimeoutsAfterExit"`
	RenderCallbacksAfterExit int      `json:"renderCallbacksAfterExit"`
	StatusVisibleBeforeExit  bool     `json:"statusVisibleBeforeExit"`
	StatusVisibleAfterExit   bool     `json:"statusVisibleAfterExit"`
}

var selectorANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

const selectorDisplayRoot = "/tmp/pi-session-selector-fixdir"

func loadSessionSelectorFixture(t *testing.T) sessionSelectorFixture {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "WP450-session-selector", "selector.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture sessionSelectorFixture
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func selectorSession(root, id, first, allText string, modified time.Time, name *string, parent *string) session.SessionInfo {
	project := "project"
	if id == "incident" {
		project = "other"
	}
	return session.SessionInfo{
		Path: filepath.Join(root, id+".jsonl"), ID: id, CWD: filepath.Join(selectorDisplayRoot, project), Name: name,
		ParentSessionPath: parent, Created: modified.Add(-time.Hour), Modified: modified,
		MessageCount: 2, FirstMessage: first, AllMessagesText: allText,
	}
}

func sessionSelectorSessions(t *testing.T, now time.Time) (string, []session.SessionInfo, []session.SessionInfo) {
	t.Helper()
	seed, err := os.MkdirTemp("", "pi-selector-seed-")
	if err != nil {
		t.Fatal(err)
	}
	seedName := filepath.Base(seed)
	if err := os.Remove(seed); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(os.TempDir(), "pi-session-selector-"+seedName[len(seedName)-6:])
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	rootName, incidentName := "Root plan", "Incident"
	rootSession := selectorSession(root, "root", "Plan the alpha rollout", "alpha rollout details", now.Add(-20*time.Minute), &rootName, nil)
	rootPath := rootSession.Path
	child := selectorSession(root, "child", "Investigate Node CVE", "node cve remediation", now.Add(-5*time.Minute), nil, &rootPath)
	incident := selectorSession(root, "incident", "Alpha failure", "alpha fatal error", now.Add(-time.Minute), &incidentName, nil)
	misc := selectorSession(root, "misc", "Misc notes", "unrelated notes", now.Add(-30*time.Minute), nil, nil)
	for _, info := range []session.SessionInfo{rootSession, child, incident, misc} {
		if writeErr := os.WriteFile(info.Path, []byte("{}\n"), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	return root, []session.SessionInfo{child, rootSession, misc}, []session.SessionInfo{incident, child, rootSession, misc}
}

func selectorKey(raw string) tui.KeyEvent {
	return tui.KeyEvent{Raw: raw, Key: tui.ParseKey(raw), Type: tui.KeyEventTypeOf(raw)}
}

func normalizeSelectorFrame(lines []string, root string) []string {
	result := make([]string, len(lines))
	for index, line := range lines {
		line = selectorANSI.ReplaceAllString(line, "")
		line = strings.ReplaceAll(line, root, "<fixture>")
		line = strings.ReplaceAll(line, selectorDisplayRoot, "<fixture>")
		result[index] = strings.TrimRight(line, " \t")
	}
	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	return result
}

func waitForSelector(t *testing.T, selector *SessionSelectorComponent, contains string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(selector.Render(100), "\n"), contains) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("selector never rendered %q:\n%s", contains, strings.Join(selector.Render(100), "\n"))
}

func TestSessionSelectorMatchesUpstreamFixture(t *testing.T) {
	fixture := loadSessionSelectorFixture(t)
	now := time.Date(2026, 7, 18, 22, 0, 0, 0, time.UTC)
	root, current, all := sessionSelectorSessions(t, now)
	for _, search := range fixture.Searches {
		values := append([]session.SessionInfo(nil), all...)
		if search.NameFilter == string(sessionNamesNamed) {
			values = slices.DeleteFunc(values, func(info session.SessionInfo) bool { return info.Name == nil })
		}
		mode := sessionSelectorSort(search.SortMode)
		if mode == "relevance" {
			mode = sessionSortRelevance
		}
		got := filterAndSortSelectorSessions(values, search.Query, mode)
		gotIDs := make([]string, len(got))
		for index := range got {
			gotIDs[index] = got[index].ID
		}
		if !reflect.DeepEqual(gotIDs, search.Result) {
			t.Fatalf("search %s result = %#v, want %#v", search.ID, gotIDs, search.Result)
		}
	}
	release := make(chan struct{})
	currentLoader := func(progress session.SessionListProgress) []session.SessionInfo {
		progress(1, len(current))
		<-release
		progress(len(current), len(current))
		return existingSelectorSessions(current)
	}
	allLoader := func(progress session.SessionListProgress) []session.SessionInfo {
		progress(len(all), len(all))
		return existingSelectorSessions(all)
	}
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	selector := NewSessionSelectorComponent(SessionSelectorOptions{
		CurrentSessions: currentLoader,
		AllSessions:     allLoader,
		Keybindings:     bindings,
		Now:             func() time.Time { return now },
		DeleteSession: func(path string) (SessionDeleteMethod, error) {
			return SessionDeleteUnlink, os.Remove(path)
		},
	}, nil, nil)

	expected := make(map[string][]string, len(fixture.Frames))
	for _, frame := range fixture.Frames {
		expected[frame.ID] = frame.Lines
	}
	assertFrame := func(id string) {
		t.Helper()
		got := normalizeSelectorFrame(selector.Render(fixture.Width), root)
		if !reflect.DeepEqual(got, expected[id]) {
			t.Fatalf("frame %s mismatch\n got: %#v\nwant: %#v", id, got, expected[id])
		}
	}

	waitForSelector(t, selector, "Loading 1/3")
	assertFrame("loading-progress")
	close(release)
	waitForSelector(t, selector, "Root plan")
	assertFrame("current-threaded")
	selector.HandleInput(selectorKey("\t"))
	waitForSelector(t, selector, "Incident")
	assertFrame("all-threaded")
	selector.HandleInput(selectorKey("\x13"))
	assertFrame("all-recent")
	selector.HandleInput(selectorKey("\x13"))
	assertFrame("all-relevance")
	selector.HandleInput(selectorKey("ndcv"))
	assertFrame("fuzzy-search")
	selector.HandleInput(selectorKey("\x15"))
	selector.HandleInput(selectorKey(`"node cve"`))
	assertFrame("exact-search")
	selector.HandleInput(selectorKey("\x15"))
	selector.HandleInput(selectorKey("re:alpha.*error"))
	assertFrame("regex-search")
	selector.HandleInput(selectorKey("\x15"))
	selector.HandleInput(selectorKey("re:["))
	assertFrame("invalid-regex")
	selector.HandleInput(selectorKey("\x15"))
	selector.HandleInput(selectorKey("\x0e"))
	assertFrame("named-filter")
	selector.HandleInput(selectorKey("\x10"))
	assertFrame("path-toggle")
	selector.HandleInput(selectorKey("\x04"))
	assertFrame("delete-confirmation")
	selector.HandleInput(selectorKey("\x1b"))
	assertFrame("delete-cancelled")
	selector.HandleInput(selectorKey("\x04"))
	selector.HandleInput(selectorKey("\r"))
	waitForSelector(t, selector, "Root plan")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && strings.Contains(strings.Join(selector.Render(fixture.Width), "\n"), "Incident") {
		time.Sleep(time.Millisecond)
	}
	if rendered := strings.Join(selector.Render(fixture.Width), "\n"); strings.Contains(rendered, "Incident") {
		t.Fatalf("deleted session remains visible:\n%s", rendered)
	}
	waitForSelector(t, selector, "◉ All")
	assertFrame("after-delete")
}

func existingSelectorSessions(sessions []session.SessionInfo) []session.SessionInfo {
	result := make([]session.SessionInfo, 0, len(sessions))
	for _, info := range sessions {
		if _, err := os.Stat(info.Path); err == nil {
			result = append(result, info)
		}
	}
	return result
}

func TestSessionSelectorSelectionCancellationAndKeybindings(t *testing.T) {
	fixture := loadSessionSelectorFixture(t)
	now := time.Date(2026, 7, 18, 22, 0, 0, 0, time.UTC)
	_, current, all := sessionSelectorSessions(t, now)
	loader := func(values []session.SessionInfo) SessionSelectorLoader {
		return func(session.SessionListProgress) []session.SessionInfo { return values }
	}
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	selected := make(chan string, 1)
	selector := NewSessionSelectorComponent(SessionSelectorOptions{
		CurrentSessions: loader(current), AllSessions: loader(all), Keybindings: bindings, Now: func() time.Time { return now },
	}, func(path string) { selected <- path }, nil)
	waitForSelector(t, selector, "Root plan")
	selector.HandleInput(selectorKey("\x1b[B"))
	selector.HandleInput(selectorKey("\r"))
	got := <-selected
	if len(fixture.Callbacks.Selected) != 1 {
		t.Fatalf("upstream selected callbacks = %#v, want one path", fixture.Callbacks.Selected)
	}
	want := strings.ReplaceAll(fixture.Callbacks.Selected[0], "<fixture>", filepath.Dir(current[0].Path))
	if got != want {
		t.Fatalf("selected path = %q, want upstream callback %q", got, want)
	}

	cancelled := make(chan struct{}, 1)
	selector = NewSessionSelectorComponent(SessionSelectorOptions{
		CurrentSessions: loader(current), AllSessions: loader(all), Keybindings: bindings, Now: func() time.Time { return now },
	}, nil, func() { cancelled <- struct{}{} })
	waitForSelector(t, selector, "Root plan")
	selector.HandleInput(selectorKey("\x1b"))
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("escape did not cancel session selection")
	}
	if fixture.Callbacks.Cancellations != 1 {
		t.Fatalf("upstream cancellation callbacks = %d, want 1", fixture.Callbacks.Cancellations)
	}

	overridden := NewAppKeybindings(tui.KeybindingsConfig{"app.session.toggleSort": {"ctrl+x"}})
	tui.SetKeybindings(overridden)
	selector = NewSessionSelectorComponent(SessionSelectorOptions{
		CurrentSessions: loader(current), AllSessions: loader(all), Keybindings: overridden, Now: func() time.Time { return now },
	}, nil, nil)
	waitForSelector(t, selector, "Sort: Threaded")
	selector.HandleInput(selectorKey("\x13"))
	if rendered := strings.Join(selector.Render(100), "\n"); !strings.Contains(rendered, "Sort: Threaded") {
		t.Fatalf("overridden ctrl+s unexpectedly toggled sort:\n%s", rendered)
	}
	selector.HandleInput(selectorKey("\x18"))
	if rendered := strings.Join(selector.Render(100), "\n"); !strings.Contains(rendered, "Sort: Recent") {
		t.Fatalf("custom ctrl+x did not toggle sort:\n%s", rendered)
	}
}

func TestSessionSelectorClearsStatusLifetimeOnSelectionCancellationAndExit(t *testing.T) {
	fixture := loadSessionSelectorFixture(t)
	if fixture.SchemaVersion != 2 || len(fixture.Lifetime) != 3 {
		t.Fatalf("upstream selector lifetime fixture = version %d, cases %d", fixture.SchemaVersion, len(fixture.Lifetime))
	}
	now := time.Date(2026, 7, 18, 22, 0, 0, 0, time.UTC)
	_, current, all := sessionSelectorSessions(t, now)
	loader := func(values []session.SessionInfo) SessionSelectorLoader {
		return func(session.SessionListProgress) []session.SessionInfo { return values }
	}

	for _, expected := range fixture.Lifetime {
		expected := expected
		t.Run(expected.ID, func(t *testing.T) {
			bindings := NewAppKeybindings(nil)
			tui.SetKeybindings(bindings)
			var renders atomic.Int32
			events := make([]string, 0, 1)
			selector := NewSessionSelectorComponent(SessionSelectorOptions{
				CurrentSessions: loader(current),
				AllSessions:     loader(all),
				Keybindings:     bindings,
				RequestRender:   func() { renders.Add(1) },
				Now:             func() time.Time { return now },
			}, func(string) { events = append(events, "select") }, func() { events = append(events, "cancel") })
			waitForSelector(t, selector, "Root plan")
			time.Sleep(10 * time.Millisecond)
			renders.Store(0)

			selector.mu.Lock()
			selector.setStatusLocked("info", "lifetime-status", 100*time.Millisecond)
			selector.mu.Unlock()
			statusVisibleBeforeExit := strings.Contains(strings.Join(selector.Render(100), "\n"), "lifetime-status")

			switch expected.ID {
			case "select":
				selector.HandleInput(selectorKey("\r"))
			case "cancel":
				selector.HandleInput(selectorKey("\x1b"))
			case "exit":
				selector.clearStatus()
				events = append(events, "exit")
			default:
				t.Fatalf("unexpected component lifetime case %q", expected.ID)
			}

			selector.mu.Lock()
			pendingTimeoutsAfterExit := 0
			if selector.statusTimer != nil {
				pendingTimeoutsAfterExit = 1
			}
			selector.mu.Unlock()
			statusVisibleAfterExit := strings.Contains(strings.Join(selector.Render(100), "\n"), "lifetime-status")
			time.Sleep(150 * time.Millisecond)
			renderCallbacksAfterExit := int(renders.Load())
			actual := selectorLifetimeTrace{
				ID:                       expected.ID,
				Events:                   events,
				ScheduledTimeouts:        1,
				ClearedTimeouts:          1 - pendingTimeoutsAfterExit,
				PendingTimeoutsAfterExit: pendingTimeoutsAfterExit,
				FiredTimeoutsAfterExit:   renderCallbacksAfterExit,
				RenderCallbacksAfterExit: renderCallbacksAfterExit,
				StatusVisibleBeforeExit:  statusVisibleBeforeExit,
				StatusVisibleAfterExit:   statusVisibleAfterExit,
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("selector %s lifetime = %+v, want upstream %+v", expected.ID, actual, expected)
			}
		})
	}
}

type selectorLifetimeProbe struct {
	_ byte
}

type selectorCancellationTerminal struct {
	loaded     <-chan struct{}
	session    string
	cancel     context.CancelFunc
	startCount atomic.Int32
	stopCount  atomic.Int32
}

func (terminal *selectorCancellationTerminal) Start(handleInput func(string), _ func()) error {
	terminal.startCount.Add(1)
	go func() {
		<-terminal.loaded
		time.Sleep(25 * time.Millisecond)
		handleInput("\x04")
		handleInput("\r")
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(terminal.session); errors.Is(err, os.ErrNotExist) {
				time.Sleep(25 * time.Millisecond)
				terminal.cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
		terminal.cancel()
	}()
	return nil
}

func (terminal *selectorCancellationTerminal) Stop() error {
	terminal.stopCount.Add(1)
	return nil
}
func (*selectorCancellationTerminal) Write(string)                            {}
func (*selectorCancellationTerminal) DrainInput(time.Duration, time.Duration) {}
func (*selectorCancellationTerminal) Columns() int                            { return 100 }
func (*selectorCancellationTerminal) Rows() int                               { return 24 }
func (*selectorCancellationTerminal) KittyProtocolActive() bool               { return false }
func (*selectorCancellationTerminal) MoveBy(int)                              {}
func (*selectorCancellationTerminal) HideCursor()                             {}
func (*selectorCancellationTerminal) ShowCursor()                             {}
func (*selectorCancellationTerminal) ClearLine()                              {}
func (*selectorCancellationTerminal) ClearFromCursor()                        {}
func (*selectorCancellationTerminal) ClearScreen()                            {}
func (*selectorCancellationTerminal) SetTitle(string)                         {}
func (*selectorCancellationTerminal) SetProgress(bool)                        {}

func runCancelledSelectorWithStatusTimer(t *testing.T) <-chan struct{} {
	t.Helper()
	finalized := make(chan struct{}, 1)
	probe := &selectorLifetimeProbe{}
	runtime.SetFinalizer(probe, func(*selectorLifetimeProbe) { finalized <- struct{}{} })
	root := t.TempDir()
	sessionPath := filepath.Join(root, "pending-status.jsonl")
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded := make(chan struct{})
	var loadedOnce sync.Once
	loader := func(session.SessionListProgress) []session.SessionInfo {
		runtime.KeepAlive(probe)
		loadedOnce.Do(func() { close(loaded) })
		return []session.SessionInfo{{
			Path: sessionPath, ID: "pending-status", CWD: root, Created: time.Now(), Modified: time.Now(),
			MessageCount: 1, FirstMessage: "pending status", AllMessagesText: "pending status",
		}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	terminal := &selectorCancellationTerminal{loaded: loaded, session: sessionPath, cancel: cancel}
	_, _, err := RunSessionSelectorWithTerminal(ctx, loader, loader, terminal)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runner cancellation error = %v, want %v", err, context.Canceled)
	}
	if terminal.startCount.Load() != 1 || terminal.stopCount.Load() != 1 {
		t.Fatalf("runner terminal starts=%d stops=%d", terminal.startCount.Load(), terminal.stopCount.Load())
	}
	runtime.KeepAlive(probe)
	return finalized
}

func TestRunSessionSelectorContextCancellationClearsStatusLifetime(t *testing.T) {
	fixture := loadSessionSelectorFixture(t)
	expected := fixture.Lifetime[2]
	if expected.ID != "exit" || expected.PendingTimeoutsAfterExit != 0 || expected.FiredTimeoutsAfterExit != 0 {
		t.Fatalf("unexpected upstream exit lifetime: %+v", expected)
	}
	finalized := runCancelledSelectorWithStatusTimer(t)
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		runtime.GC()
		select {
		case <-finalized:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("runner context cancellation retained the selector through its status timeout; upstream clears it on exit")
}

type selectorLifecycleTerminal struct {
	mu      sync.Mutex
	input   string
	start   int
	stop    int
	stopErr error
	handle  func(string)
}

func (terminal *selectorLifecycleTerminal) Start(handleInput func(string), _ func()) error {
	terminal.mu.Lock()
	terminal.start++
	terminal.handle = handleInput
	input := terminal.input
	terminal.mu.Unlock()
	go func() {
		time.Sleep(5 * time.Millisecond)
		handleInput(input)
	}()
	return nil
}
func (terminal *selectorLifecycleTerminal) Stop() error {
	terminal.mu.Lock()
	terminal.stop++
	terminal.mu.Unlock()
	return terminal.stopErr
}
func (*selectorLifecycleTerminal) Write(string)                            {}
func (*selectorLifecycleTerminal) DrainInput(time.Duration, time.Duration) {}
func (*selectorLifecycleTerminal) Columns() int                            { return 100 }
func (*selectorLifecycleTerminal) Rows() int                               { return 24 }
func (*selectorLifecycleTerminal) KittyProtocolActive() bool               { return false }
func (*selectorLifecycleTerminal) MoveBy(int)                              {}
func (*selectorLifecycleTerminal) HideCursor()                             {}
func (*selectorLifecycleTerminal) ShowCursor()                             {}
func (*selectorLifecycleTerminal) ClearLine()                              {}
func (*selectorLifecycleTerminal) ClearFromCursor()                        {}
func (*selectorLifecycleTerminal) ClearScreen()                            {}
func (*selectorLifecycleTerminal) SetTitle(string)                         {}
func (*selectorLifecycleTerminal) SetProgress(bool)                        {}

func TestRunSessionSelectorTerminalLifecycle(t *testing.T) {
	now := time.Now()
	_, current, all := sessionSelectorSessions(t, now)
	loader := func(values []session.SessionInfo) SessionSelectorLoader {
		return func(session.SessionListProgress) []session.SessionInfo { return values }
	}
	terminal := &selectorLifecycleTerminal{input: "\r"}
	path, selected, err := RunSessionSelectorWithTerminal(context.Background(), loader(current), loader(all), terminal)
	if err != nil || !selected || path != current[1].Path {
		t.Fatalf("path=%q selected=%t err=%v", path, selected, err)
	}
	terminal.mu.Lock()
	starts, stops := terminal.start, terminal.stop
	terminal.mu.Unlock()
	if starts != 1 || stops != 1 {
		t.Fatalf("terminal starts=%d stops=%d", starts, stops)
	}

	stopFailure := errors.New("restore failed")
	terminal = &selectorLifecycleTerminal{input: "\x1b", stopErr: stopFailure}
	_, _, err = RunSessionSelectorWithTerminal(context.Background(), loader(current), loader(all), terminal)
	if !errors.Is(err, stopFailure) {
		t.Fatalf("stop error = %v", err)
	}
}
