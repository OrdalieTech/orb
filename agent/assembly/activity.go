package assembly

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/plugins/activity"
	"github.com/OrdalieTech/orb/tui"
)

type activityView struct {
	store      *activity.Store
	host       extensions.UIHost
	theme      extensions.Theme
	mu         sync.Mutex
	expanded   bool
	offset     int
	pageSize   int
	summaryRow int
	rows       int
	done       chan struct{}
	once       sync.Once
}

func newActivityView(store *activity.Store, host extensions.UIHost, theme extensions.Theme) activity.View {
	v := &activityView{store: store, host: host, theme: theme, done: make(chan struct{}), pageSize: 6}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-v.done:
				return
			case <-ticker.C:
				for _, r := range store.Snapshot() {
					if !r.State.Terminal() && r.State != activity.Unknown {
						host.Invalidate()
						break
					}
				}
			}
		}
	}()
	return v
}

func (v *activityView) Dispose() { v.once.Do(func() { close(v.done) }) }

func (v *activityView) Control(action string) error {
	v.mu.Lock()
	switch action {
	case "":
		v.expanded = !v.expanded
	case "close":
		v.expanded = false
	case "next":
		v.expanded = true
		v.offset += v.pageSize
	case "prev":
		v.expanded = true
		v.offset -= v.pageSize
	default:
		v.mu.Unlock()
		return fmt.Errorf("usage: /activity [next|prev|close]")
	}
	v.mu.Unlock()
	v.host.Invalidate()
	return nil
}

func activityText(text string) string {
	text = tui.StripANSI(text)
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

func activityStatus(state activity.State) (string, string, string) {
	switch state {
	case activity.Queued:
		return "·", "queued", "dim"
	case activity.Running:
		return "•", "running", "accent"
	case activity.Waiting:
		return "!", "waiting", "warning"
	case activity.Completed:
		return "✓", "done", "success"
	case activity.Failed:
		return "!", "failed", "error"
	case activity.Cancelled:
		return "−", "cancelled", "dim"
	default:
		return "?", "unknown", "warning"
	}
}

func activityAge(r activity.Record, now time.Time) string {
	if r.State == activity.Unknown {
		return "—"
	}
	if r.State.Terminal() {
		now = r.Updated
	}
	seconds := max(0, int(now.Sub(r.Started).Seconds()))
	if seconds >= 3600 {
		return fmt.Sprintf("%dh%02dm", seconds/3600, seconds/60%60)
	}
	if seconds >= 60 {
		return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func (v *activityView) color(token, text string) string {
	if v.theme == nil {
		return text
	}
	return v.theme.FG(token, text)
}

func (v *activityView) Render(width int) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	records := v.store.Snapshot()
	v.rows, v.summaryRow = 0, -1
	if width <= 0 || len(records) == 0 {
		return nil
	}
	now := time.Now()
	agents, processes, waiting, unknown, failed := 0, 0, 0, 0, 0
	for _, r := range records {
		if !r.State.Terminal() {
			if r.Kind == activity.Agent {
				agents++
			} else {
				processes++
			}
			if r.State == activity.Waiting {
				waiting++
			}
			if r.State == activity.Unknown {
				unknown++
			}
		}
		if r.State == activity.Failed {
			failed++
		}
	}
	var counts []string
	if agents > 0 {
		counts = append(counts, fmt.Sprintf("%d agent%s", agents, plural(agents)))
	}
	if processes > 0 {
		label := "processes"
		if processes == 1 {
			label = "process"
		}
		if width < 50 {
			label = "proc."
		}
		counts = append(counts, fmt.Sprintf("%d %s", processes, label))
	}
	icon, color := "•", "accent"
	summary := strings.Join(counts, " · ")
	if agents+processes == 1 && width >= 60 {
		summary += " · " + activityText(records[0].Title)
	}
	switch {
	case waiting > 0:
		icon, color = "!", "warning"
		summary += fmt.Sprintf(" · %d waiting", waiting)
	case unknown > 0:
		icon, color = "?", "warning"
		summary += fmt.Sprintf(" · %d unknown", unknown)
	case agents+processes == 0:
		icon, color = "✓", "dim"
		summary = fmt.Sprintf("%d finished", len(records))
	}
	if failed > 0 {
		icon, color = "!", "error"
		summary += fmt.Sprintf(" · %d failed", failed)
	}
	var lines []string
	if v.expanded {
		v.pageSize = min(6, max(1, v.host.Height()/3-2))
		v.offset = max(0, min(v.offset, max(0, len(records)-v.pageSize)))
		end := min(len(records), v.offset+v.pageSize)
		for _, r := range records[v.offset:end] {
			marker, state, token := activityStatus(r.State)
			label := activityText(r.Title)
			if r.Kind == activity.Process {
				label = "$ " + label
			}
			tail := state + " " + activityAge(r, now)
			if width >= 70 {
				tail = fmt.Sprintf("%s %-9s %6s", tui.TruncateToWidth(activityText(r.Source), 10, "…", true), state, activityAge(r, now))
			}
			room := max(0, width-5-tui.VisibleWidth(tail))
			text := "  " + v.color(token, marker) + " " + tui.TruncateToWidth(label, room, "…", true) + " " + v.color(token, tail)
			if room == 0 {
				text = " " + v.color(token, marker+" "+state)
			}
			lines = append(lines, tui.TruncateToWidth(text, width, "…", false))
		}
		if len(records) > v.pageSize {
			text := fmt.Sprintf("  %d–%d/%d · scroll or /activity next", v.offset+1, end, len(records))
			lines = append(lines, tui.TruncateToWidth(v.color("dim", text), width, "…", false))
		}
	}
	marker := "▸"
	if v.expanded {
		marker = "▾"
	}
	tail := " " + marker
	if width >= 50 && agents+processes == 1 {
		tail = "  " + activityAge(records[0], now) + tail
	}
	body := " " + icon + " " + summary
	body = tui.TruncateToWidth(body, max(0, width-tui.VisibleWidth(tail)), "…", true)
	lines = append(lines, tui.TruncateToWidth(v.color(color, body)+v.color("dim", tail), width, "…", false))
	v.rows, v.summaryRow = len(lines), len(lines)-1
	return lines
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (v *activityView) HandleMouse(event tui.MouseEvent) bool {
	v.mu.Lock()
	inside := event.Row >= 0 && event.Row < v.rows
	summary := event.Row == v.summaryRow
	expanded := v.expanded
	v.mu.Unlock()
	if !inside {
		return false
	}
	switch event.Type {
	case tui.MouseWheelUp, tui.MouseWheelDown:
		if !expanded {
			return false
		}
		delta := 1
		if event.Type == tui.MouseWheelUp {
			delta = -1
		}
		v.mu.Lock()
		v.offset += delta
		v.mu.Unlock()
		v.host.Invalidate()
		return true
	case tui.MousePress, tui.MouseRelease:
		if !summary || event.Button != 0 {
			return false
		}
		if event.Type == tui.MouseRelease && event.Clicks < 2 {
			_ = v.Control("")
		}
		return true
	}
	return false
}
