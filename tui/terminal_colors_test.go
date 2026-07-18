package tui

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingNotificationTerminal struct {
	*fakeTerminal
	onStarted chan struct{}
	releaseOn chan struct{}
	once      sync.Once
}

func (terminal *blockingNotificationTerminal) Write(data string) {
	if data == terminalColorSchemeNotificationsOn {
		terminal.once.Do(func() { close(terminal.onStarted) })
		<-terminal.releaseOn
	}
	terminal.fakeTerminal.Write(data)
}

func TestParseOsc11BackgroundColor(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  RgbColor
		ok    bool
	}{
		{name: "six-digit-hex-bel", input: "\x1b]11;#ffffff\x07", want: RgbColor{255, 255, 255}, ok: true},
		{name: "six-digit-hex-st", input: "\x1b]11;#0080ff\x1b\\", want: RgbColor{0, 128, 255}, ok: true},
		{name: "twelve-digit-hex", input: "\x1b]11;#00008000ffff\x07", want: RgbColor{0, 128, 255}, ok: true},
		{name: "sixteen-bit-rgb", input: "\x1b]11;rgb:0000/8000/ffff\x07", want: RgbColor{0, 128, 255}, ok: true},
		{name: "single-digit-rgba", input: "\x1b]11;rgba:f/8/0/f\x07", want: RgbColor{255, 136, 0}, ok: true},
		{name: "strict-frame-unparseable", input: "\x1b]11;not-a-color\x07", ok: false},
		{name: "wrong-osc", input: "\x1b]10;#ffffff\x07", ok: false},
		{name: "leading-data", input: "x\x1b]11;#ffffff\x07", ok: false},
		{name: "trailing-data", input: "\x1b]11;#ffffff\x07x", ok: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := ParseOsc11BackgroundColor(testCase.input)
			if ok != testCase.ok || ok && got != testCase.want {
				t.Fatalf("ParseOsc11BackgroundColor(%q) = %#v, %v; want %#v, %v", testCase.input, got, ok, testCase.want, testCase.ok)
			}
			if testCase.name == "strict-frame-unparseable" && !IsOsc11BackgroundColorResponse(testCase.input) {
				t.Fatal("strict unparseable response was not recognized for consumption")
			}
		})
	}
}

func TestParseTerminalColorSchemeReport(t *testing.T) {
	tests := map[string]TerminalColorScheme{
		"\x1b[?997;1n": TerminalColorSchemeDark,
		"\x1b[?997;2n": TerminalColorSchemeLight,
	}
	for input, want := range tests {
		if got, ok := ParseTerminalColorSchemeReport(input); !ok || got != want {
			t.Fatalf("ParseTerminalColorSchemeReport(%q) = %q, %v", input, got, ok)
		}
	}
	for _, input := range []string{"\x1b[?997;3n", "\x1b[?996n", "x\x1b[?997;1n"} {
		if got, ok := ParseTerminalColorSchemeReport(input); ok || got != "" {
			t.Fatalf("ParseTerminalColorSchemeReport(%q) = %q, %v", input, got, ok)
		}
	}
}

func TestTerminalColorQueriesConsumeBeforeListenersAndFocus(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	ui := NewTUI(terminal)
	focused := &overlayFocusRecorder{lines: []string{"INPUT"}}
	listenerInputs := make([]string, 0)
	ui.SetFocus(focused)
	ui.AddInputListener(func(data string) InputListenerResult {
		listenerInputs = append(listenerInputs, data)
		return InputListenerResult{}
	})
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()

	background := ui.QueryTerminalBackgroundColor(time.Second)
	if !strings.Contains(terminal.output(), osc11BackgroundQuery) {
		t.Fatalf("background query writes = %q", terminal.output())
	}
	terminal.send("\x1b]11;#000000\x07")
	if got := <-background; got == nil || *got != (RgbColor{0, 0, 0}) {
		t.Fatalf("background result = %#v", got)
	}
	if len(listenerInputs) != 0 || len(focused.inputs) != 0 {
		t.Fatalf("OSC reply leaked: listeners=%q focus=%q", listenerInputs, focused.inputs)
	}

	schemeEvents := make([]TerminalColorScheme, 0)
	ui.OnTerminalColorSchemeChange(func(scheme TerminalColorScheme) { schemeEvents = append(schemeEvents, scheme) })
	scheme := ui.QueryTerminalColorScheme(time.Second)
	if !strings.Contains(terminal.output(), terminalColorSchemeQuery) {
		t.Fatalf("scheme query writes = %q", terminal.output())
	}
	terminal.send("\x1b[?997;2n")
	if got := <-scheme; got != TerminalColorSchemeLight {
		t.Fatalf("scheme result = %q", got)
	}
	if !reflect.DeepEqual(schemeEvents, []TerminalColorScheme{TerminalColorSchemeLight}) {
		t.Fatalf("scheme events = %#v", schemeEvents)
	}
	if len(listenerInputs) != 0 || len(focused.inputs) != 0 {
		t.Fatalf("scheme reply leaked: listeners=%q focus=%q", listenerInputs, focused.inputs)
	}

	terminal.send("x")
	if !reflect.DeepEqual(listenerInputs, []string{"x"}) || !reflect.DeepEqual(focused.inputs, []string{"x"}) {
		t.Fatalf("normal input listeners=%q focus=%q", listenerInputs, focused.inputs)
	}
}

func TestTerminalBackgroundQueryQueueConsumesLateRepliesInOrder(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	ui := NewTUI(terminal)
	focused := &overlayFocusRecorder{lines: []string{"INPUT"}}
	listenerInputs := make([]string, 0)
	ui.SetFocus(focused)
	ui.AddInputListener(func(data string) InputListenerResult {
		listenerInputs = append(listenerInputs, data)
		return InputListenerResult{}
	})
	first := ui.QueryTerminalBackgroundColor(time.Millisecond)
	if got := <-first; got != nil {
		t.Fatalf("timed-out result = %#v", got)
	}
	second := ui.QueryTerminalBackgroundColor(time.Second)
	ui.handleInput("\x1b]11;#111111\x07")
	select {
	case got := <-second:
		t.Fatalf("first late reply resolved second query: %#v", got)
	case <-time.After(5 * time.Millisecond):
	}
	ui.handleInput("\x1b]11;rgb:ffff/0000/8000\x1b\\")
	if got := <-second; got == nil || *got != (RgbColor{255, 0, 128}) {
		t.Fatalf("second result = %#v", got)
	}
	if len(listenerInputs) != 0 || len(focused.inputs) != 0 {
		t.Fatalf("queued replies leaked: listeners=%q focus=%q", listenerInputs, focused.inputs)
	}
}

func TestTerminalColorSchemeNotificationSequencesAndTimeout(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	ui := NewTUI(terminal)
	ui.SetTerminalColorSchemeNotifications(true)
	if terminal.output() != terminalColorSchemeNotificationsOn {
		t.Fatalf("pre-start enable = %q", terminal.output())
	}
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(terminal.output(), terminalColorSchemeNotificationsOn); count != 2 {
		t.Fatalf("enable count = %d in %q", count, terminal.output())
	}
	timedOut := ui.QueryTerminalColorScheme(time.Millisecond)
	if got := <-timedOut; got != "" {
		t.Fatalf("timed-out scheme = %q", got)
	}
	terminal.send("\x1b[?997;1n")
	ui.SetTerminalColorSchemeNotifications(false)
	if err := ui.Stop(); err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(terminal.output(), terminalColorSchemeNotificationsOff); count != 1 {
		t.Fatalf("disable count = %d in %q", count, terminal.output())
	}
}

func TestTerminalColorSchemeNotificationWritesStayOrdered(t *testing.T) {
	terminal := &blockingNotificationTerminal{
		fakeTerminal: newFakeTerminal(80, 24),
		onStarted:    make(chan struct{}),
		releaseOn:    make(chan struct{}),
	}
	ui := NewTUI(terminal)
	enableDone := make(chan struct{})
	go func() {
		ui.SetTerminalColorSchemeNotifications(true)
		close(enableDone)
	}()
	select {
	case <-terminal.onStarted:
	case <-time.After(time.Second):
		t.Fatal("enable write did not start")
	}

	disableDone := make(chan struct{})
	go func() {
		ui.SetTerminalColorSchemeNotifications(false)
		close(disableDone)
	}()
	select {
	case <-disableDone:
		t.Fatal("disable overtook the blocked enable write")
	case <-time.After(10 * time.Millisecond):
	}
	close(terminal.releaseOn)
	for _, call := range []struct {
		name string
		done <-chan struct{}
	}{{name: "enable", done: enableDone}, {name: "disable", done: disableDone}} {
		select {
		case <-call.done:
		case <-time.After(time.Second):
			t.Fatalf("%s notification call did not finish", call.name)
		}
	}
	if got, want := terminal.output(), terminalColorSchemeNotificationsOn+terminalColorSchemeNotificationsOff; got != want {
		t.Fatalf("notification writes = %q, want %q", got, want)
	}
}

func TestTerminalColorSchemeStopCannotOvertakeEnable(t *testing.T) {
	terminal := &blockingNotificationTerminal{
		fakeTerminal: newFakeTerminal(80, 24),
		onStarted:    make(chan struct{}),
		releaseOn:    make(chan struct{}),
	}
	ui := NewTUI(terminal)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	terminal.resetOutput()

	enableDone := make(chan struct{})
	go func() {
		ui.SetTerminalColorSchemeNotifications(true)
		close(enableDone)
	}()
	select {
	case <-terminal.onStarted:
	case <-time.After(time.Second):
		t.Fatal("enable write did not start")
	}
	stopDone := make(chan struct{})
	go func() {
		_ = ui.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("stop overtook the blocked enable write")
	case <-time.After(10 * time.Millisecond):
	}
	close(terminal.releaseOn)
	for _, call := range []struct {
		name string
		done <-chan struct{}
	}{{name: "enable", done: enableDone}, {name: "stop", done: stopDone}} {
		select {
		case <-call.done:
		case <-time.After(time.Second):
			t.Fatalf("%s notification call did not finish", call.name)
		}
	}
	output := terminal.output()
	on := strings.Index(output, terminalColorSchemeNotificationsOn)
	off := strings.Index(output, terminalColorSchemeNotificationsOff)
	if on < 0 || off < 0 || on > off {
		t.Fatalf("notification writes out of order: %q", output)
	}
}
