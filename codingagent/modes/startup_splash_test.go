package modes

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/internal/orbalogo"
	"github.com/OrdalieTech/orb/tui"
)

func emptyChatFixture(bodyHeight int) *emptyChatState {
	empty := &emptyChatState{bodyHeight: func() int { return bodyHeight }, left: 3}
	empty.frame.Store(orbalogo.FrameCount - 1)
	return empty
}

func TestEmptyChatStateAnchorsTheLockupToConversationText(t *testing.T) {
	initTestTheme(t)
	empty := emptyChatFixture(19)
	lines := empty.Render(79)
	if len(lines) != orbalogo.Height+2 || lines[0] != "" || lines[len(lines)-1] != "" {
		t.Fatalf("top-left lockup rows = %#v", lines)
	}
	body := selectorANSI.ReplaceAllString(strings.Join(lines, "\n"), "")
	for _, text := range []string{"Orb", "/  commands"} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(text)) {
			t.Fatalf("top-left lockup omitted %q: %q", text, body)
		}
	}
	for _, duplicate := range []string{"model", "thinking"} {
		if strings.Contains(strings.ToLower(body), duplicate) {
			t.Fatalf("top-left lockup duplicates footer state %q: %q", duplicate, body)
		}
	}
	// Drawn rows come from markRows, which at rest carries the shimmer; the
	// lockup geometry below is measured against the canonical art instead.
	mark, canonical := empty.markRows(), orbalogo.Frame(orbalogo.FrameCount-1)
	leftmost := math.MaxInt
	for index, row := range mark {
		if row == "" {
			continue
		}
		plain := selectorANSI.ReplaceAllString(lines[index+1], "")
		if !strings.HasPrefix(plain, "   "+row) {
			t.Fatalf("mark row %d is not drawn on the canvas edge: %q", index, plain)
		}
		leftmost = min(leftmost, len(plain)-len(strings.TrimLeft(plain, " ")))
	}
	// The canvas is cropped to the mark, so its ink starts at canvas column 0
	// and lands on the conversation text column (outputPad+2 = 3 by default).
	if leftmost != 3 {
		t.Fatalf("mark ink starts at column %d, want the conversation text column 3", leftmost)
	}
	// Both lockup rows sit on equally wide mark rows, so the gap between the
	// mark and the text reads as one gutter rather than two.
	if utf8.RuneCountInString(canonical[4]) != utf8.RuneCountInString(canonical[6]) {
		t.Fatalf("lockup rows clear different ink edges: %q vs %q", canonical[4], canonical[6])
	}
}

func TestEmptyChatStateFallsBackToTheMarkAtNarrowWidths(t *testing.T) {
	empty := emptyChatFixture(orbalogo.Height + 1)
	lines := empty.Render(empty.left + orbalogo.Width)
	if len(lines) != orbalogo.Height+1 || lines[len(lines)-1] != "" || strings.Contains(strings.Join(lines, "\n"), "commands") {
		t.Fatalf("narrow lockup = %#v", lines)
	}
	if lines := empty.Render(empty.left + orbalogo.Width - 1); len(lines) != 0 {
		t.Fatalf("clipped mark rendered at narrow width: %#v", lines)
	}
	short := emptyChatFixture(orbalogo.Height - 1)
	if lines := short.Render(79); len(lines) != 0 {
		t.Fatalf("clipped mark rendered at short height: %#v", lines)
	}
}

type countingChrome struct{ renders int }

func (chrome *countingChrome) Render(int) []string {
	chrome.renders++
	return []string{"editor"}
}

func TestEmptyChatStateUsesTheViewportHeightWithoutRenderingChromeAgain(t *testing.T) {
	terminal := newLifecycleTerminal(80, 24)
	ui := tui.NewTUI(terminal)
	body := &tui.Container{}
	chrome := &countingChrome{}
	empty := emptyChatFixture(19)
	empty.bodyHeight = ui.ViewportBodyHeight
	body.AddChild(empty)
	ui.SetViewport(body, chrome)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()
	chrome.renders = 0
	ui.ForceRender()
	if chrome.renders != 1 {
		t.Fatalf("chrome rendered %d times in one empty frame, want once", chrome.renders)
	}
}

func TestLogoUnfoldAnimationGate(t *testing.T) {
	tests := []struct {
		name           string
		tty            bool
		term           string
		width, rows    int
		quiet, verbose bool
		want           bool
	}{
		{name: "interactive", tty: true, term: "xterm-256color", width: 80, rows: 24, want: true},
		{name: "redirected", term: "xterm-256color", width: 80, rows: 24},
		{name: "dumb", tty: true, term: "dumb", width: 80, rows: 24},
		{name: "narrow", tty: true, term: "xterm-256color", width: 19, rows: 24},
		{name: "short", tty: true, term: "xterm-256color", width: 80, rows: 8},
		{name: "quiet", tty: true, term: "xterm-256color", width: 80, rows: 24, quiet: true},
		{name: "verbose overrides quiet", tty: true, term: "xterm-256color", width: 80, rows: 24, quiet: true, verbose: true, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TERM", test.term)
			mode, _, _, _ := newF12ShutdownMode(t)
			mode.ui = tui.NewTUI(newLifecycleTerminal(test.width, test.rows))
			mode.options.OutputTTY = test.tty
			mode.options.Verbose = test.verbose
			mode.session.SetQuietStartup(test.quiet)
			mode.emptyState = emptyChatFixture(19)
			if got := mode.logoAnimationEnabled(); got != test.want {
				t.Fatalf("animation enabled = %v, want %v", got, test.want)
			}
		})
	}
}

// The mark opens from its folded first frame and draws every frame on the way
// to rest: skipping any of them is what made the old four-frame settle read as
// a snap rather than a growth.
func TestLogoUnfoldDrawsEveryFrameFromTheFold(t *testing.T) {
	mode := &InteractiveMode{ui: tui.NewTUI(newLifecycleTerminal(80, 24))}
	mode.emptyState = emptyChatFixture(19)
	mode.emptyState.frame.Store(0)
	mode.playLogoUnfold(context.Background(), 0)
	if got := mode.emptyState.frame.Load(); got != orbalogo.FrameCount-1 {
		t.Fatalf("unfold stopped on frame %d, want %d", got, orbalogo.FrameCount-1)
	}
	// The mark travels about forty dot rows as it opens. Fewer steps than this
	// and each one crosses more than a dot, which is what reads as stepping.
	if orbalogo.FrameCount < 32 {
		t.Fatalf("unfold is %d frames; too few to read as continuous", orbalogo.FrameCount)
	}
}

func TestLogoUnfoldCancellationDoesNotWaitForDecoration(t *testing.T) {
	mode := &InteractiveMode{ui: tui.NewTUI(newLifecycleTerminal(80, 24))}
	mode.emptyState = emptyChatFixture(19)
	first := int32(0)
	mode.emptyState.frame.Store(first)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		mode.playLogoUnfold(ctx, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled logo unfold blocked shutdown")
	}
	if got := mode.emptyState.frame.Load(); got != first {
		t.Fatalf("cancelled unfold advanced to frame %d", got)
	}
}
