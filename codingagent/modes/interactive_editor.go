package modes

import "github.com/OrdalieTech/pi-go/tui"

// CustomEditor wraps tui.Editor with app-level keybinding dispatch.
type CustomEditor struct {
	*tui.Editor
	keybindings    *tui.KeybindingsManager
	actionHandlers map[string]func()

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
