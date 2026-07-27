package modes

import (
	"strings"

	"github.com/OrdalieTech/pigo/codingagent/modes/theme"
	"github.com/OrdalieTech/pigo/tui"
)

// CustomEditor wraps tui.Editor with app-level keybinding dispatch.
type CustomEditor struct {
	*tui.Editor
	keybindings        *tui.KeybindingsManager
	actionHandlers     map[string]func()
	topBorderDecorator func(width int, base string, border tui.StyleFunc) string

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
	ce.actionHandlers[action] = handler
}

func (ce *CustomEditor) setTopBorderDecorator(decorator func(width int, base string, border tui.StyleFunc) string) {
	ce.topBorderDecorator = decorator
}

func (ce *CustomEditor) Render(width int) []string {
	lines := ce.Editor.Render(width)
	if len(lines) == 0 || ce.topBorderDecorator == nil {
		return lines
	}
	lines[0] = ce.topBorderDecorator(width, lines[0], ce.GetBorderColor())
	return lines
}

type editorTopBorderProjection struct {
	Line         string
	StatusInline bool
	TitleShown   bool
}

const maxEditorSessionTitleWidth = 28

func composeEditorTopBorder(base string, width int, status, title string, border tui.StyleFunc) editorTopBorderProjection {
	if width <= 0 {
		return editorTopBorderProjection{}
	}
	if border == nil {
		border = func(text string) string { return text }
	}
	base = tui.TruncateToWidth(base, width, "", true)
	if width == 1 {
		return editorTopBorderProjection{Line: tui.SliceByColumn(base, 0, 1, true)}
	}

	status = strings.Join(strings.Fields(status), " ")
	title = strings.Join(strings.Fields(title), " ")
	interiorWidth := width - 2
	titleBudget := min(maxEditorSessionTitleWidth, width/2, interiorWidth-2)
	titleDisplay := truncateEditorSessionTitle(title, titleBudget)
	titleShown := titleDisplay != ""
	badge := ""
	badgeWidth := 0
	if titleShown {
		badgeText := " " + titleDisplay + " "
		badge = theme.BG("selectedBg", theme.FG("text", badgeText))
		badgeWidth = tui.VisibleWidth(badgeText)
	}

	statusWidth := tui.VisibleWidth(status)
	statusSpace := statusWidth + 2 + badgeWidth
	if titleShown {
		statusSpace++
	}
	statusInline := status != "" && statusSpace <= interiorWidth
	if !statusInline {
		if !titleShown {
			return editorTopBorderProjection{Line: base}
		}
		prefixWidth := width - 1 - badgeWidth
		line := tui.SliceByColumn(base, 0, prefixWidth, true) + badge + tui.SliceByColumn(base, width-1, 1, true)
		return editorTopBorderProjection{Line: line, TitleShown: true}
	}

	left := " " + status + " "
	gapWidth := max(0, interiorWidth-tui.VisibleWidth(left)-badgeWidth)
	line := border("─") + left + border(strings.Repeat("─", gapWidth)) + badge + border("─")
	return editorTopBorderProjection{Line: line, StatusInline: true, TitleShown: titleShown}
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

	for action, handler := range ce.actionHandlers {
		if action == "app.interrupt" || action == "app.exit" {
			continue
		}
		if ce.keybindings.Matches(data, action) {
			handler()
			return true
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
