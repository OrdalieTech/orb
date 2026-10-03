package modes

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/OrdalieTech/orb/tui"
)

func TestFooterTooltipExpiresWithoutMouseLeave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		initTestTheme(t)
		data := &clickableFooterData{fakeFooterDataProvider: fakeFooterDataProvider{cwd: "/workspace", statuses: map[string]string{"bridge": "●"}}}
		footer := NewFooterComponent(layoutFooterSession{}, data, false)
		label := ""
		footer.tooltip = func(text string, _, _ int) { label = text }
		tooltip := func() string {
			footer.hitMu.Lock()
			defer footer.hitMu.Unlock()
			return label
		}
		line := tui.StripANSI(footer.Render(80)[0])
		column := tui.VisibleWidth(line[:strings.LastIndex(line, "●")])
		hover := tui.MouseEvent{Type: tui.MouseMove, Column: column}
		footer.HandleMouse(hover)
		if got := tooltip(); got != "Bridge" {
			t.Fatalf("tooltip = %q", got)
		}
		// Leaving a terminal pane produces no mouse-leave report.
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if got := tooltip(); got != "" {
			t.Fatalf("tooltip survived absent mouse reports: %q", got)
		}
		if !footer.HandleMouse(hover) || tooltip() != "Bridge" {
			t.Fatal("expired hover could not show its tooltip again")
		}
		footer.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: -1})
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if got := tooltip(); got != "" {
			t.Fatalf("leave retained tooltip: %q", got)
		}
		footer.HandleMouse(hover)
		time.Sleep(time.Second)
		footer.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: -1})
		footer.HandleMouse(hover)
		time.Sleep(1100 * time.Millisecond)
		synctest.Wait()
		if got := tooltip(); got != "Bridge" {
			t.Fatalf("previous hover expiry hid a newer tooltip: %q", got)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := tooltip(); got != "" {
			t.Fatalf("new hover never expired: %q", got)
		}
	})
}
