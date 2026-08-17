package modes

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OrdalieTech/orb/agent/modes/theme"
	"github.com/OrdalieTech/orb/internal/orbalogo"
	"github.com/OrdalieTech/orb/tui"
)

// The mark is on screen whole from the first frame; what plays over the next
// second is the rest of its dots arriving and six percent of growth. It runs
// off the render loop and owns neither the viewport nor input, so a message
// typed over it cuts it short.
const (
	logoUnfoldDelay = 20 * time.Millisecond
	emptyLockupGap  = 5
)

type emptyChatState struct {
	bodyHeight func() int
	left       int
	frame      atomic.Int32
}

// infoRows places the lockup text on rows 4 and 6: they straddle the mark's
// centre row, and they are two of the three rows that fill the canvas to its
// widest ink, so both clear the same edge and read as one gap. The hint is the
// bare slash because that is the key that opens the command list; there is no
// /help command to point at.
func (empty *emptyChatState) infoRows() [orbalogo.Height]string {
	rows := [orbalogo.Height]string{}
	rows[4] = theme.Bold(theme.FG("accent", "Orb"))
	rows[6] = theme.FG("dim", "/") + "  " + theme.FG("muted", "commands")
	return rows
}

func (empty *emptyChatState) markRows() [orbalogo.Height]string {
	if frame := empty.frame.Load(); frame < orbalogo.FrameCount-1 {
		return orbalogo.Frame(int(max(0, frame)))
	}
	return orbalogo.Frame(orbalogo.FrameCount - 1)
}

func (empty *emptyChatState) Render(width int) []string {
	height := empty.bodyHeight()
	left := max(0, empty.left)
	if width < left+orbalogo.Width || height < orbalogo.Height {
		return nil
	}
	mark := empty.markRows()
	info := empty.infoRows()
	infoWidth := 0
	for _, row := range info {
		infoWidth = max(infoWidth, tui.VisibleWidth(row))
	}
	showInfo := width >= left+orbalogo.Width+emptyLockupGap+infoWidth
	top, bottom := 0, 0
	if height > orbalogo.Height {
		bottom = 1
	}
	if height > orbalogo.Height+bottom {
		top = 1
	}
	indent := strings.Repeat(" ", left)
	lines := make([]string, top, top+orbalogo.Height+bottom)
	for index, row := range mark {
		if row == "" && (!showInfo || info[index] == "") {
			lines = append(lines, "")
			continue
		}
		line := indent + theme.FG("accent", row)
		if showInfo && info[index] != "" {
			line += strings.Repeat(" ", orbalogo.Width-tui.VisibleWidth(row)+emptyLockupGap) + info[index]
		}
		lines = append(lines, line)
	}
	if bottom > 0 {
		lines = append(lines, "")
	}
	return lines
}

func (mode *InteractiveMode) logoAnimationEnabled() bool {
	if mode.emptyState == nil || !mode.options.OutputTTY || os.Getenv("TERM") == "dumb" {
		return false
	}
	if !mode.options.Verbose && mode.session != nil && mode.session.InteractiveSettings().QuietStartup {
		return false
	}
	terminal := mode.ui.Terminal()
	return terminal.Columns() >= mode.emptyState.left+orbalogo.Width && terminal.Rows() >= orbalogo.Height+1
}

func (mode *InteractiveMode) startLogoUnfold(ctx context.Context, between time.Duration) {
	if !mode.logoAnimationEnabled() {
		if mode.emptyState != nil {
			mode.emptyState.frame.Store(orbalogo.FrameCount - 1)
		}
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	mode.mu.Lock()
	mode.logoCancel, mode.logoDone = cancel, done
	mode.mu.Unlock()
	go func() {
		defer close(done)
		mode.playLogoUnfold(ctx, between)
	}()
}

func (mode *InteractiveMode) stopLogoUnfold() {
	mode.mu.Lock()
	cancel, done := mode.logoCancel, mode.logoDone
	mode.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// playLogoUnfold walks the frames the mark has not reached yet, so a caller
// that seeded a later frame resumes from there instead of folding back.
func (mode *InteractiveMode) playLogoUnfold(ctx context.Context, between time.Duration) {
	for index := mode.emptyState.frame.Load() + 1; index < orbalogo.FrameCount; index++ {
		if !logoPause(ctx, between) {
			return
		}
		mode.emptyState.frame.Store(index)
		mode.ui.RequestRender()
	}
}

func logoPause(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
