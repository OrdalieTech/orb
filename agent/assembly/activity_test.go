package assembly

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/plugins/activity"
	"github.com/OrdalieTech/orb/tui"
)

type activityHost struct{ redraws atomic.Int64 }

func (*activityHost) Width() int    { return 80 }
func (*activityHost) Height() int   { return 24 }
func (h *activityHost) Invalidate() { h.redraws.Add(1) }

func testActivityView(t *testing.T, count int) (*activity.Store, *activityView) {
	t.Helper()
	store := activity.NewStore("session")
	// Distinct start times keep the order the records were made in: a clock
	// as coarse as Windows' would tie them, and ties sort by source.
	started := time.Now().Add(-time.Minute)
	for i := range count {
		store.Apply(activity.Record{SessionID: "session", Source: []string{"Claude", "Codex", "Orb"}[i%3], ID: fmt.Sprint(i), Kind: activity.Agent, Title: "Trace external source viewer", State: activity.Running, Sequence: 1, Started: started.Add(time.Duration(i) * time.Millisecond)})
	}
	view := newActivityView(store, &activityHost{}, nil).(*activityView)
	t.Cleanup(view.Dispose)
	return store, view
}

func TestActivityBarCompactExpandAndPage(t *testing.T) {
	_, view := testActivityView(t, 12)
	lines := view.Render(80)
	if len(lines) != 1 || !strings.Contains(lines[0], "12 agents") || strings.Contains(lines[0], "alt+") {
		t.Fatal(lines)
	}
	if !view.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Row: 0}) || len(view.Render(80)) != 1 {
		t.Fatal("expanded on press instead of click")
	}
	view.HandleMouse(tui.MouseEvent{Type: tui.MouseRelease, Button: 0, Row: 0})
	lines = view.Render(80)
	if len(lines) != 8 || !strings.Contains(lines[6], "1–6/12") {
		t.Fatal(lines)
	}
	joined := strings.Join(lines, "\n")
	for _, name := range []string{"Claude", "Codex", "Orb", "running"} {
		if !strings.Contains(joined, name) {
			t.Fatalf("missing %s: %s", name, joined)
		}
	}
	if err := view.Control("next"); err != nil {
		t.Fatal(err)
	}
	lines = view.Render(80)
	if !strings.Contains(lines[6], "7–12/12") {
		t.Fatal(lines)
	}
	view.HandleMouse(tui.MouseEvent{Type: tui.MouseWheelUp, Row: 0})
	if !strings.Contains(view.Render(80)[6], "6–11/12") {
		t.Fatal("wheel did not page")
	}
	if err := view.Control("close"); err != nil || len(view.Render(80)) != 1 {
		t.Fatal(err)
	}
	if err := view.Control("invalid"); err == nil {
		t.Fatal("accepted invalid control")
	}
	view.Dispose()
	view.Dispose()
}

func TestActivityBarAllWidthsAndUntrustedLabels(t *testing.T) {
	store, view := testActivityView(t, 1)
	records := store.Snapshot()
	record := records[0]
	record.Sequence++
	record.Title = "\x1b]52;c;secret\a\x1b[31m界界\x1b[0m\n👩‍💻 e\u0301\r\t" + strings.Repeat("title", 100)
	store.Apply(record)
	for _, expanded := range []bool{false, true} {
		view.expanded = expanded
		for width := range 170 {
			lines := view.Render(width)
			for _, line := range lines {
				// Truncation may add its own reset; no source-supplied controls survive.
				plain := strings.ReplaceAll(line, "\x1b[0m", "")
				if tui.VisibleWidth(line) > width || strings.ContainsAny(plain, "\n\r\t\x1b\a") {
					t.Fatalf("width %d: %q", width, line)
				}
			}
		}
	}
}

func TestActivityBarStatesAndSourceNeutrality(t *testing.T) {
	var collapsed string
	for _, source := range []string{"Claude", "Codex", "Orb"} {
		store, view := testActivityView(t, 0)
		store.Apply(activity.Record{SessionID: "session", Source: source, ID: "1", Kind: activity.Process, Title: "Tests", State: activity.Waiting, Sequence: 1, Started: time.Now()})
		line := view.Render(80)[0]
		if !strings.Contains(line, "waiting") || !strings.Contains(line, "1 process") {
			t.Fatal(line)
		}
		if collapsed != "" && collapsed != line {
			t.Fatalf("provider-specific layout: %q / %q", collapsed, line)
		}
		collapsed = line
		_ = view.Control("")
		if !strings.Contains(view.Render(80)[0], source) {
			t.Fatal("missing source")
		}
	}
	for _, state := range []activity.State{activity.Queued, activity.Running, activity.Waiting, activity.Unknown, activity.Completed, activity.Failed, activity.Cancelled} {
		store, view := testActivityView(t, 0)
		r := activity.Record{SessionID: "session", Source: "Orb", ID: "1", Kind: activity.Agent, Title: "Work", State: state, Sequence: 1, Started: time.Now().Add(-time.Minute), Updated: time.Now()}
		store.Apply(r)
		_ = view.Control("")
		_, label, _ := activityStatus(state)
		if !strings.Contains(view.Render(80)[0], label) {
			t.Fatalf("missing %s", label)
		}
	}
}

func TestActivityViewConcurrentUpdatesAndDisposal(t *testing.T) {
	store, view := testActivityView(t, 1)
	r := store.Snapshot()[0]
	var group sync.WaitGroup
	group.Go(func() {
		for i := range 100 {
			r.Sequence = uint64(i + 2)
			store.Apply(r)
		}
	})
	group.Go(func() {
		for range 100 {
			_ = view.Render(60)
		}
	})
	group.Go(func() {
		for range 100 {
			_ = view.Control("")
		}
		view.Dispose()
	})
	group.Wait()
}

var _ extensions.DisposableComponent = (*activityView)(nil)
var _ tui.MouseHandler = (*activityView)(nil)
