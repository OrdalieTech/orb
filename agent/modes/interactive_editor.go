package modes

import (
	"strings"
	"sync/atomic"

	"github.com/OrdalieTech/orb/agent/modes/theme"
	"github.com/OrdalieTech/orb/tui"
)

// CustomEditor wraps tui.Editor with app-level keybinding dispatch.
type CustomEditor struct {
	*tui.Editor
	keybindings        *tui.KeybindingsManager
	actionHandlers     map[string]func()
	actionOrder        []string
	topBorderDecorator func(width int, base string, border tui.StyleFunc) string
	framed             atomic.Bool
	popupLayout        atomic.Uint64

	OnEscape            func()
	OnCtrlD             func()
	OnPasteImage        func()
	OnExtensionShortcut func(string) bool
}

func NewCustomEditor(ui *tui.TUI, editorTheme tui.EditorTheme, kb *tui.KeybindingsManager) *CustomEditor {
	editor := tui.NewEditor(ui, editorTheme)
	ce := &CustomEditor{
		Editor:         editor,
		keybindings:    kb,
		actionHandlers: make(map[string]func()),
	}
	editor.InputInterceptor = ce.interceptInput
	return ce
}

func (ce *CustomEditor) OnAction(action string, handler func()) {
	if _, exists := ce.actionHandlers[action]; !exists {
		ce.actionOrder = append(ce.actionOrder, action)
	}
	ce.actionHandlers[action] = handler
}

func (ce *CustomEditor) setTopBorderDecorator(decorator func(width int, base string, border tui.StyleFunc) string) {
	ce.topBorderDecorator = decorator
}

// The rounded frame belongs to the conversation composer only: selector and
// dialog editors keep tui.Editor's plain rails. A one-column interior has no
// spare cell for the block cursor past the last character, so below
// editorFrameMinWidth the frame is dropped and the plain rails stay.
const (
	editorFrameInset    = 2
	editorFrameMinWidth = 4
)

// editorContentWidth is the width the wrapped tui.Editor lays out at. Callers
// that ask it about scroll state must use this width, not the viewport width.
func editorContentWidth(width int) int {
	if width < editorFrameMinWidth {
		return width
	}
	return width - editorFrameInset
}

func (ce *CustomEditor) Render(width int) []string {
	if width < editorFrameMinWidth {
		ce.framed.Store(false)
		ce.popupLayout.Store(0)
		lines := ce.Editor.Render(width)
		if len(lines) > 0 && ce.topBorderDecorator != nil {
			lines[0] = ce.topBorderDecorator(width, lines[0], ce.GetBorderColor())
		}
		return lines
	}
	ce.framed.Store(true)
	lines := ce.Editor.Render(editorContentWidth(width))
	if len(lines) < 2 {
		return lines
	}
	border := ce.GetBorderColor()
	top := border("╭") + lines[0] + border("╮")
	if ce.topBorderDecorator != nil {
		top = ce.topBorderDecorator(width, top, border)
	}
	// Native suggestions sit above the composer; the reusable editor keeps
	// its original layout for extension compatibility.
	bottom := min(1+ce.RenderedContentRows(), len(lines)-1)
	framed := make([]string, 0, len(lines))
	popupRows := len(lines) - bottom - 1
	ce.popupLayout.Store(uint64(popupRows)<<32 | uint64(bottom+1))
	for _, line := range lines[bottom+1:] {
		framed = append(framed, " "+line+" ")
	}
	framed = append(framed, top)
	for _, line := range lines[1:bottom] {
		framed = append(framed, border("│")+line+border("│"))
	}
	framed = append(framed, border("╰")+lines[bottom]+border("╯"))
	return framed
}

// HandleMouse rebases a click past the frame's left rail so the wrapped editor
// still lands the cursor on the character under the pointer.
func (ce *CustomEditor) HandleMouse(event tui.MouseEvent) bool {
	if ce.framed.Load() {
		layout := ce.popupLayout.Load()
		rows, originalTop := int(layout>>32), int(layout&0xffffffff)
		if event.Row < rows {
			event.Row += originalTop
		} else {
			event.Row -= rows
		}
		event.Column = max(0, event.Column-1)
	}
	return ce.Editor.HandleMouse(event)
}

type editorTopBorderProjection struct {
	Line         string
	StatusInline bool
	TitleShown   bool
}

const maxEditorSessionTitleWidth = 28

// composeEditorTopBorder keeps transient work status and the optional session
// name in the top rail. Durable model and usage metadata stays in the footer.
func composeEditorTopBorder(base string, width int, status, title string, border tui.StyleFunc) editorTopBorderProjection {
	if width <= 0 {
		return editorTopBorderProjection{}
	}
	if width < editorFrameMinWidth {
		return editorTopBorderProjection{Line: base}
	}
	status = strings.Join(strings.Fields(status), " ")
	title = strings.Join(strings.Fields(title), " ")
	interior := width - 2
	titleText := truncateEditorSessionTitle(title, min(maxEditorSessionTitleWidth, width/2, interior-2))
	titleBadge := ""
	if titleText != "" {
		titleBadge = theme.BG("selectedBg", theme.FG("text", " "+titleText+" "))
	}
	titleWidth := tui.VisibleWidth(titleBadge)
	statusWidth := tui.VisibleWidth(status)
	statusInline := status != "" && statusWidth+2+titleWidth <= interior

	if !statusInline && titleBadge == "" {
		return editorTopBorderProjection{Line: base}
	}
	left := ""
	if statusInline {
		left = " " + status + " "
	}
	fill := border(strings.Repeat("─", max(0, interior-tui.VisibleWidth(left)-titleWidth)))
	line := border("╭") + left + fill + titleBadge + border("╮")
	return editorTopBorderProjection{
		Line:         line,
		StatusInline: statusInline,
		TitleShown:   titleBadge != "",
	}
}

func truncateEditorSessionTitle(title string, width int) string {
	if width <= 0 || title == "" {
		return ""
	}
	if tui.VisibleWidth(title) <= width {
		return title
	}
	if width < 4 {
		return ""
	}
	return tui.SliceByColumn(title, 0, width-3, true) + "..."
}

func (ce *CustomEditor) interceptInput(event tui.KeyEvent) bool {
	data := event.Raw

	// Legacy Shift+Enter can arrive as Escape+Return, also used by Alt+Enter.
	// The composer always gives newline precedence over app and extension actions.
	if data == "\x1b\r" || data == "\n" || data == "\x1b[13;2~" || tui.MatchesKey(data, "shift+enter") {
		ce.InsertTextAtCursor("\n")
		return true
	}

	if ce.OnExtensionShortcut != nil && ce.OnExtensionShortcut(data) {
		return true
	}

	if ce.keybindings.Matches(data, "app.clipboard.pasteImage") {
		if ce.OnPasteImage != nil {
			ce.OnPasteImage()
		}
		return true
	}

	if ce.keybindings.Matches(data, "app.interrupt") {
		if !ce.IsShowingAutocomplete() {
			handler := ce.OnEscape
			if handler == nil {
				handler = ce.actionHandlers["app.interrupt"]
			}
			if handler != nil {
				handler()
				return true
			}
		}
		return false
	}

	if ce.keybindings.Matches(data, "app.exit") {
		if ce.GetText() == "" {
			handler := ce.OnCtrlD
			if handler == nil {
				handler = ce.actionHandlers["app.exit"]
			}
			if handler != nil {
				handler()
				return true
			}
		}
		return false
	}

	// User overrides win on every dispatch, including after keybindings reload.
	for _, explicit := range []bool{true, false} {
		for _, action := range ce.actionOrder {
			if action == "app.interrupt" || action == "app.exit" || ce.keybindings.IsUserDefined(action) != explicit {
				continue
			}
			// Legacy terminals send the same byte for Ctrl+M and Enter.
			if action == "app.model.select" && (data == "\r" || data == "\n") {
				continue
			}
			if ce.keybindings.Matches(data, action) {
				ce.actionHandlers[action]()
				return true
			}
		}
	}

	return false
}

// bridgeEditorBase adapts the built-in editor to the extension seam's
// string-input contract for the JS bridge's CustomEditor base class.
type bridgeEditorBase struct{ *CustomEditor }

func (base bridgeEditorBase) HandleInput(data string) {
	base.CustomEditor.HandleInput(tui.KeyEvent{Raw: data})
}
