package main

// `orb update`'s screen; platforms/native/selfupdate does the update itself.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/OrdalieTech/orb/internal/orbalogo"
	"github.com/OrdalieTech/orb/platforms/native/selfupdate"
)

const (

	// Twelve ticks — ten mark stages, then Orb, then update — and the process
	// rule grows on the same clock, 0% to 100% by the time the wordmark lands.
	// An update is not a session opening, so it plays a cut of the unfold, a
	// touch slow so an instant outcome (dev build, offline) still reads.
	logoRevealDelay = 42 * time.Millisecond

	// Editorial plate: the mark and wordmark sit top-left like the home
	// screen; the process line under them spans every column, its steps
	// linked by faint full-width rules. That line is the point of the screen.
	updateIndent    = "  "
	updateLockupGap = 4
	updateBarCap    = 72
	updateMinLink   = 2

	// The rules between steps recede so the steps themselves read. Bold and
	// faint are the only styling: both survive light and dark palettes.
	faintOn  = "\x1b[2m"
	boldOn   = "\x1b[1m"
	styleOff = "\x1b[22m"
)

// updateRevealStages samples the unfold down to the stages the update
// trace draws; the tail is denser because that is where the mark settles.
var updateRevealStages = [...]int{0, 6, 12, 18, 24, 30, 36, 41, 45, 47}

// selfUpdater is an update with its screen.
type selfUpdater struct {
	selfupdate.Updater
	animate   bool
	logoDelay time.Duration
	width     int
}

func runSelfUpdate(ctx context.Context, out io.Writer, offline, animate bool) int {
	// A dumb terminal has no cursor addressing, so the reveal and the spinner would
	// stack their frames instead of redrawing them.
	updater := selfUpdater{Updater: selfupdate.New(version, offline), animate: animate && os.Getenv("TERM") != "dumb", logoDelay: logoRevealDelay}
	return updater.run(ctx, out)
}

func (updater selfUpdater) run(ctx context.Context, out io.Writer) int {
	from := selfupdate.Plain(updater.CurrentVersion)
	screen := newUpdateScreen(out, updater)
	screen.reveal(from)
	verified := false
	tag, _, err := updater.Update(ctx, func(step string) func() {
		if step == "replacing binary" {
			verified = true
			return screen.spin(from, "archive verified", step)
		}
		return screen.spin(from, step)
	})
	switch {
	case errors.Is(err, selfupdate.ErrDevelopment):
		return screen.done(0, from, "development build")
	case errors.Is(err, selfupdate.ErrOffline):
		return screen.done(0, from, "offline")
	case errors.Is(err, selfupdate.ErrCurrent):
		return screen.done(0, from, "already current ✓")
	case err != nil && verified:
		return screen.done(1, from, "archive verified", err.Error(), "unchanged")
	case err != nil:
		return screen.done(1, from, err.Error(), "unchanged")
	}
	return screen.done(0, from, "archive verified", "binary replaced", selfupdate.Plain(tag)+" ✓")
}

type updateScreen struct {
	out     io.Writer
	animate bool
	styled  bool
	delay   time.Duration
	width   int
}

func newUpdateScreen(out io.Writer, updater selfUpdater) updateScreen {
	return updateScreen{out: out, animate: updater.animate, styled: updater.animate && os.Getenv("NO_COLOR") == "", delay: updater.logoDelay, width: updateBarWidth(out, updater.width)}
}

func (screen updateScreen) done(code int, cells ...string) int {
	if screen.styled {
		last := len(cells) - 1
		cells = append(append([]string{}, cells[:last]...), boldOn+cells[last]+styleOff)
	}
	if screen.animate {
		_, _ = fmt.Fprintf(screen.out, "\r\x1b[2K%s\r\n", screen.bar(cells))
	} else {
		_, _ = fmt.Fprintln(screen.out, screen.bar(cells))
	}
	_, _ = fmt.Fprint(screen.out, "\n\n")
	return code
}

func (screen updateScreen) paint(cells ...string) {
	_, _ = fmt.Fprintf(screen.out, "\r\x1b[2K%s", screen.bar(cells))
}

// bar is the process line at the screen's width; the links go faint only
// when the terminal can render it, so piped output carries no escape bytes.
func (screen updateScreen) bar(cells []string) string {
	return joinBarStyled(screen.width, cells, screen.styled)
}

func (screen updateScreen) reveal(from string) {
	revealLockup(screen.out, screen.animate, screen.delay, screen.styled, screen.width, from)
}

func (screen updateScreen) spin(held ...string) func() {
	if !screen.animate {
		return func() {}
	}
	label := held[len(held)-1]
	prefix := held[:len(held)-1]
	frames := [...]string{"·  ", "·· ", "···"}
	cell := func(frame int) string {
		dots := frames[frame]
		if screen.styled {
			dots = faintOn + dots + styleOff
		}
		return dots + " " + label
	}
	_, _ = fmt.Fprintf(screen.out, "\r\x1b[2K%s", screen.bar(append(append([]string{}, prefix...), cell(0))))
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(140 * time.Millisecond)
		defer ticker.Stop()
		for frame := 1; ; frame = (frame + 1) % len(frames) {
			select {
			case <-ticker.C:
				screen.paint(append(append([]string{}, prefix...), cell(frame))...)
			case <-stop:
				_, _ = fmt.Fprint(screen.out, "\r\x1b[2K")
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func updateBarWidth(out io.Writer, override int) int {
	if override > 0 {
		return override
	}
	if file, ok := out.(*os.File); ok {
		if width, _, err := term.GetSize(int(file.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return updateBarCap
}

func joinBarStyled(width int, cells []string, faintLinks bool) string {
	if width <= 0 {
		width = updateBarCap
	}
	gaps := len(cells) - 1
	if gaps <= 0 {
		return updateIndent + cells[0]
	}
	inner := width - len(updateIndent)
	text := 0
	for _, cell := range cells {
		text += visibleWidth(cell)
	}
	if room := inner - text - gaps*2; room >= gaps*updateMinLink {
		base, extra := room/gaps, room%gaps
		var body strings.Builder
		body.WriteString(updateIndent)
		for index, cell := range cells {
			if index > 0 {
				dashes := base
				if index <= extra {
					dashes++
				}
				rule := strings.Repeat("─", dashes)
				if faintLinks {
					rule = faintOn + rule + styleOff
				}
				body.WriteString(" " + rule + " ")
			}
			body.WriteString(cell)
		}
		return body.String()
	}
	return updateIndent + strings.Join(cells, " · ")
}

// SGR sequences style glyphs without occupying columns.
func visibleWidth(cell string) int {
	width, escape := 0, false
	for _, r := range cell {
		switch {
		case escape:
			escape = r != 'm'
		case r == '\x1b':
			escape = true
		default:
			width++
		}
	}
	return width
}

func lockupRows(index, wordmark int, styled bool) []string {
	mark := orbalogo.CompactFrame(index)
	info := [orbalogo.CompactHeight]string{}
	if wordmark >= 1 {
		info[2] = "Orb"
	}
	if wordmark >= 2 {
		info[3] = "update"
	}
	rows := make([]string, 0, orbalogo.CompactHeight+2)
	rows = append(rows, "")
	for i, row := range mark {
		pad := orbalogo.CompactWidth - utf8.RuneCountInString(row)
		line := updateIndent + row + strings.Repeat(" ", pad)
		if info[i] != "" {
			cell := info[i]
			if styled {
				if cell == "update" || wordmark == 1 {
					cell = faintOn + cell + styleOff
				} else {
					cell = boldOn + cell + styleOff
				}
			}
			line += strings.Repeat(" ", updateLockupGap) + cell
		}
		line = strings.TrimRight(line, " ")
		if styled && line != "" && info[i] == "" {
			line = boldOn + line + styleOff
		}
		rows = append(rows, line)
	}
	rows = append(rows, "")
	return rows
}

func writeLockup(out io.Writer, index, wordmark int, styled bool) {
	for _, row := range lockupRows(index, wordmark, styled) {
		_, _ = fmt.Fprintln(out, row)
	}
}

// The logo appears first, then the wordmark lands on it, then the rule
// appears under them — and from there the mark's own unfold drives the rule
// to 100%. The wordmark sets the rhythm; the growth is one motion. Explicit
// CRLF keeps redraws independent of the terminal's newline mode, and the
// frame ends on the bar row without a newline so the run's spin and done
// redraw that row in place instead of stacking a second line.
func revealLockup(out io.Writer, enabled bool, delay time.Duration, styled bool, width int, from string) {
	if !enabled {
		writeLockup(out, orbalogo.FrameCount-1, 2, styled)
		return
	}
	canvas := len(lockupRows(0, 0, styled))
	frames := len(updateRevealStages)
	full := float64(frames - 1)
	draw := func(index, wordmark int, progress float64) {
		for _, row := range lockupRows(index, wordmark, styled) {
			_, _ = fmt.Fprintf(out, "\r\x1b[2K%s\r\n", row)
		}
		bar := ""
		if progress >= 0 {
			bar = barProgress(width, from, progress, styled)
		}
		_, _ = fmt.Fprintf(out, "\r\x1b[2K%s", bar)
	}
	draw(updateRevealStages[0], 0, -1)
	for wordmark := 1; wordmark <= 2; wordmark++ {
		hold := delay
		if wordmark == 1 {
			hold = 2 * delay
		}
		time.Sleep(hold)
		_, _ = fmt.Fprintf(out, "\x1b[%dA", canvas)
		draw(updateRevealStages[0], wordmark, -1)
	}
	for stage := 1; stage < frames; stage++ {
		t := float64(stage) / full
		time.Sleep(time.Duration(float64(delay) * (0.7 + 0.9*t)))
		_, _ = fmt.Fprintf(out, "\x1b[%dA", canvas)
		draw(updateRevealStages[stage], 2, 1-(1-t)*(1-t)*(1-t))
	}
	time.Sleep(4 * delay)
}

// barProgress is the growing rule during the reveal: the from cell anchored
// left, the rule reaching for the right edge as the mark arrives.
func barProgress(width int, from string, progress float64, styled bool) string {
	if width <= 0 {
		width = updateBarCap
	}
	avail := width - len(updateIndent) - utf8.RuneCountInString(from) - 1
	if avail < 1 {
		return updateIndent + from
	}
	dashes := max(1, int(float64(avail)*progress))
	rule := strings.Repeat("─", dashes)
	if styled {
		if progress < 1 && dashes > 1 {
			rule = faintOn + strings.Repeat("─", dashes-1) + styleOff + "─"
		} else {
			rule = faintOn + rule + styleOff
		}
	}
	return updateIndent + from + " " + rule
}
