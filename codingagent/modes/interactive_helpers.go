package modes

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/tui"

	theme "github.com/OrdalieTech/pi-go/codingagent/modes/theme"
)

// DynamicBorder renders a horizontal line that stretches to fit the terminal width.
type DynamicBorder struct {
	colorFn func(string) string
}

func NewDynamicBorder() *DynamicBorder { return &DynamicBorder{} }

func NewDynamicBorderWithColor(colorFn func(string) string) *DynamicBorder {
	return &DynamicBorder{colorFn: colorFn}
}

func (border *DynamicBorder) Invalidate() {}

func (border *DynamicBorder) Render(width int) []string {
	line := strings.Repeat("─", max(1, width))
	if border.colorFn != nil {
		line = border.colorFn(line)
	} else {
		line = theme.FG("border", line)
	}
	return []string{line}
}

func settingsListTheme() tui.SettingsListTheme {
	return tui.SettingsListTheme{
		Label: func(text string, selected bool) string {
			if selected {
				return theme.FG("accent", text)
			}
			return text
		},
		Value: func(text string, selected bool) string {
			if selected {
				return theme.FG("accent", text)
			}
			return theme.FG("muted", text)
		},
		Description: func(text string) string { return theme.FG("dim", text) },
		Cursor:      theme.FG("accent", "→ "),
		Hint:        func(text string) string { return theme.FG("dim", text) },
	}
}

// CountdownTimer ticks each second and fires a callback on expiry.
type CountdownTimer struct {
	mu       sync.Mutex
	done     chan struct{}
	onTick   func(int)
	onExpire func()
	stopped  bool
}

func NewCountdownTimer(durationMS int64, ui tui.RenderRequester, onTick func(int), onExpire func()) *CountdownTimer {
	ct := &CountdownTimer{done: make(chan struct{}), onTick: onTick, onExpire: onExpire}
	remaining := int((durationMS + 999) / 1000)
	if ct.onTick != nil {
		ct.onTick(remaining)
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ct.done:
				return
			case <-ticker.C:
				ct.mu.Lock()
				if ct.stopped {
					ct.mu.Unlock()
					return
				}
				remaining--
				ct.mu.Unlock()
				if ct.onTick != nil {
					ct.onTick(remaining)
				}
				ui.RequestRender()
				if remaining <= 0 {
					ct.mu.Lock()
					if ct.stopped {
						ct.mu.Unlock()
						return
					}
					ct.stopped = true
					ct.mu.Unlock()
					if ct.onExpire != nil {
						ct.onExpire()
					}
					return
				}
			}
		}
	}()
	return ct
}

func (ct *CountdownTimer) Dispose() {
	ct.mu.Lock()
	if !ct.stopped {
		ct.stopped = true
		close(ct.done)
	}
	ct.mu.Unlock()
}

// KeyText formats the keys currently bound to a keybinding id (falling back
// to the id itself), mirroring upstream's exported keyText helper.
func KeyText(binding string) string {
	kb := tui.GetKeybindings()
	keys := kb.Keys(binding)
	if len(keys) == 0 {
		return binding
	}
	formatted := make([]string, len(keys))
	for index, key := range keys {
		formatted[index] = formatKeyText(string(key))
	}
	return strings.Join(formatted, "/")
}

func formatKeyText(key string) string {
	return formatKeyTextForOS(key, runtime.GOOS)
}

func formatKeyTextForOS(key, goos string) string {
	if goos == "darwin" {
		parts := strings.Split(key, "+")
		for index, part := range parts {
			if strings.EqualFold(part, "alt") {
				parts[index] = "option"
			}
		}
		return strings.Join(parts, "+")
	}
	return key
}

// formatTokens renders a token count in human-readable form.
func formatTokens(count int64) string {
	if count < 1_000 {
		return fmt.Sprintf("%d", count)
	}
	if count < 10_000 {
		return fmt.Sprintf("%.1fk", float64(count)/1_000)
	}
	if count < 1_000_000 {
		return fmt.Sprintf("%.0fk", float64(count)/1_000)
	}
	if count < 10_000_000 {
		return fmt.Sprintf("%.1fM", float64(count)/1_000_000)
	}
	return fmt.Sprintf("%.0fM", float64(count)/1_000_000)
}

func formatInteger(count int64) string {
	digits := fmt.Sprintf("%d", count)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", strings.TrimPrefix(digits, "-")
	}
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return sign + digits
}
