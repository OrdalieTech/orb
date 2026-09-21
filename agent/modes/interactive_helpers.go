package modes

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
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

func menuSelectedBackground(text string) string {
	if current := theme.Current(); current != nil && current.Name == "terminal" {
		return theme.BG("selectedBg", text)
	}
	prefix := strings.Replace(theme.FGANSI("borderMuted"), "[38;", "[48;", 1)
	prefix = strings.Replace(prefix, "[39m", "[49m", 1)
	return prefix + text + "\x1b[49m"
}

func settingsListTheme() tui.SettingsListTheme {
	return tui.SettingsListTheme{
		Label: func(text string, selected bool) string {
			if selected {
				return theme.FG("text", text)
			}
			return text
		},
		Value: func(text string, selected bool) string {
			if selected {
				return theme.FG("text", text)
			}
			return theme.FG("muted", text)
		},
		Description: func(text string) string { return theme.FG("dim", text) },
		Cursor:      theme.FG("text", "› "),
		Hint:        func(text string) string { return theme.FG("dim", text) },
		SelectedBg:  func(text string) string { return menuSelectedBackground(text) },
	}
}

// newSearchInput builds the shared search field of the floating menus.
func newSearchInput() *tui.Input {
	input := tui.NewInput()
	input.Prompt = "⌕ "
	return input
}

func menuFrame(title string, child tui.Component) *tui.Frame {
	frame := tui.NewPanel(title, "",
		func(text string) string { return theme.Bold(theme.FG("text", text)) },
		func(text string) string { return theme.FG("dim", text) },
		func() string { return theme.BGANSI("toolPendingBg") }, child)
	frame.ActionSelected = func(text string) string { return menuSelectedBackground(theme.Bold(theme.FG("accent", text))) }
	return frame
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
				if ui != nil {
					ui.RequestRender()
				}
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

// commandPalette serializes the shared grid with rendering. Actions run after
// unlocking because closing an overlay synchronously transfers focus.
type commandPalette struct {
	modelShortcut bool
	mu            sync.Mutex
	list          *tui.GridList
	input         *tui.Input
	bindings      *tui.KeybindingsManager
	height        func() int
	onCancel      func()
	pending       func()
}

func newCommandPalette(rows []tui.GridRow, bindings *tui.KeybindingsManager, height func() int, selectItem func(string), cancel func()) *commandPalette {
	palette := &commandPalette{input: newSearchInput(), bindings: bindings, height: height, onCancel: cancel}
	palette.list = tui.NewGridList(rows, 10, tui.GridListTheme{
		Cell: func(row tui.GridRow, column int, text string) string {
			color := "text"
			if column > 0 || strings.HasPrefix(row.Value, "add:") {
				color = "muted"
			}
			return theme.FG(color, tui.StripANSI(text))
		},
		SelectedBg: func(s string) string { return menuSelectedBackground(s) },
		Detail:     func(s string) string { return theme.FG("muted", s) },
		ScrollInfo: func(s string) string { return theme.FG("dim", s) },
		Query:      func(s string) string { return theme.FG("text", s) },
		Cursor:     theme.FG("text", "› "),
	})
	palette.list.Searchable = true
	palette.list.DetailHeight = 1
	palette.list.OnConfirm = func(value string) {
		if value != "" {
			palette.pending = func() { selectItem(value) }
		}
	}
	return palette
}

func (palette *commandPalette) SetFocused(focused bool) {
	palette.mu.Lock()
	defer palette.mu.Unlock()
	palette.list.SetFocused(focused)
	palette.input.SetFocused(focused)
}

func (palette *commandPalette) Render(width int) []string {
	palette.mu.Lock()
	defer palette.mu.Unlock()
	palette.list.SetMaxVisible(max(1, min(10, palette.height()-11-palette.list.DetailHeight)))
	lines := palette.list.Render(width)
	if len(lines) > 0 && palette.input.GetValue() != "" {
		lines[0] = palette.input.Render(width)[0]
	}
	return lines
}

func (palette *commandPalette) unlockAndDispatch() {
	action := palette.pending
	palette.pending = nil
	palette.mu.Unlock()
	if action != nil {
		action()
	}
}

func (palette *commandPalette) HandleInput(event tui.KeyEvent) {
	palette.mu.Lock()
	defer palette.unlockAndDispatch()
	bindings := palette.bindings
	switch {
	case bindings.Matches(event.Raw, "tui.select.cancel"), bindings.Matches(event.Raw, "app.commandPalette"):
		palette.pending = palette.onCancel
	case palette.modelShortcut && event.Raw != "\r" && event.Raw != "\n" && bindings.Matches(event.Raw, "app.model.select"):
		palette.list.OnConfirm("model")
	case bindings.Matches(event.Raw, "tui.select.up"), bindings.Matches(event.Raw, "tui.select.down"),
		bindings.Matches(event.Raw, "tui.select.pageUp"), bindings.Matches(event.Raw, "tui.select.pageDown"),
		bindings.Matches(event.Raw, "tui.select.confirm"):
		palette.list.HandleInput(event)
	default:
		before := palette.input.GetValue()
		palette.input.HandleInput(event)
		if query := palette.input.GetValue(); query != before {
			palette.list.SetQuery(query)
		}
	}
}

func (*commandPalette) WantsMouseMotion() bool { return true }
func (palette *commandPalette) HandleMouse(event tui.MouseEvent) bool {
	palette.mu.Lock()
	defer palette.unlockAndDispatch()
	return palette.list.HandleMouse(event)
}

func (mode *InteractiveMode) watchTerminalBackground(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	refresh := make(chan struct{}, 1)
	refresh <- struct{}{}
	unsubscribe := mode.ui.OnTerminalColorSchemeChange(func(tui.TerminalColorScheme) {
		select {
		case refresh <- struct{}{}:
		default:
		}
	})
	mode.ui.SetTerminalColorSchemeNotifications(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-refresh:
			}
			query := mode.ui.QueryTerminalBackgroundColor(200 * time.Millisecond)
			select {
			case <-ctx.Done():
				return
			case background := <-query:
				if background == nil {
					return
				}
				mode.setTerminalBackground(*background)
			}
		}
	}()
	return func() { unsubscribe(); cancel(); <-done; mode.ui.SetTerminalColorSchemeNotifications(false) }
}

func (mode *InteractiveMode) setTerminalBackground(background tui.RgbColor) {
	mode.terminalBackgroundMu.Lock()
	defer mode.terminalBackgroundMu.Unlock()
	// Terminal appearance outlives the session and its reloadable resources.
	mode.terminalBackground = &background
	if native := theme.GetTheme("terminal"); native != nil && native.SourcePath == "" {
		native.SetTerminalBackground(background)
		if theme.Current() == native {
			mode.ui.Invalidate()
		}
	}
}
