package modes

import "github.com/OrdalieTech/pi-go/tui"

// AppKeybindingDefinitions are the app-level keybindings that extend TUI defaults.
var AppKeybindingDefinitions = []tui.KeybindingDefinition{
	{ID: "app.interrupt", DefaultKeys: []tui.KeyID{"escape"}, Description: "Cancel or abort"},
	{ID: "app.clear", DefaultKeys: []tui.KeyID{"ctrl+c"}, Description: "Clear editor"},
	{ID: "app.exit", DefaultKeys: []tui.KeyID{"ctrl+d"}, Description: "Exit when editor is empty"},
	{ID: "app.suspend", DefaultKeys: []tui.KeyID{"ctrl+z"}, Description: "Suspend to background"},
	{ID: "app.thinking.cycle", DefaultKeys: []tui.KeyID{"shift+tab"}, Description: "Cycle thinking level"},
	{ID: "app.model.cycleForward", DefaultKeys: []tui.KeyID{"ctrl+p"}, Description: "Cycle to next model"},
	{ID: "app.model.cycleBackward", DefaultKeys: []tui.KeyID{"shift+ctrl+p"}, Description: "Cycle to previous model"},
	{ID: "app.model.select", DefaultKeys: []tui.KeyID{"ctrl+l"}, Description: "Open model selector"},
	{ID: "app.tools.expand", DefaultKeys: []tui.KeyID{"ctrl+o"}, Description: "Toggle tool output"},
	{ID: "app.thinking.toggle", DefaultKeys: []tui.KeyID{"ctrl+t"}, Description: "Toggle thinking blocks"},
	{ID: "app.session.toggleNamedFilter", DefaultKeys: []tui.KeyID{"ctrl+n"}, Description: "Toggle named session filter"},
	{ID: "app.editor.external", DefaultKeys: []tui.KeyID{"ctrl+g"}, Description: "Open external editor"},
	{ID: "app.message.copy", DefaultKeys: []tui.KeyID{"ctrl+x"}, Description: "Copy message to clipboard"},
	{ID: "app.message.followUp", DefaultKeys: []tui.KeyID{"alt+enter"}, Description: "Queue follow-up message"},
	{ID: "app.message.dequeue", DefaultKeys: []tui.KeyID{"alt+up"}, Description: "Restore queued messages"},
	{ID: "app.clipboard.pasteImage", DefaultKeys: []tui.KeyID{"ctrl+v"}, Description: "Paste image from clipboard"},
	{ID: "app.session.new", DefaultKeys: nil, Description: "Start a new session"},
	{ID: "app.session.tree", DefaultKeys: nil, Description: "Open session tree"},
	{ID: "app.session.fork", DefaultKeys: nil, Description: "Fork current session"},
	{ID: "app.session.resume", DefaultKeys: nil, Description: "Resume a session"},
	{ID: "app.tree.foldOrUp", DefaultKeys: []tui.KeyID{"ctrl+left", "alt+left"}, Description: "Fold tree branch or move up"},
	{ID: "app.tree.unfoldOrDown", DefaultKeys: []tui.KeyID{"ctrl+right", "alt+right"}, Description: "Unfold tree branch or move down"},
	{ID: "app.tree.editLabel", DefaultKeys: []tui.KeyID{"shift+l"}, Description: "Edit tree label"},
	{ID: "app.tree.toggleLabelTimestamp", DefaultKeys: []tui.KeyID{"shift+t"}, Description: "Toggle tree label timestamps"},
	{ID: "app.session.togglePath", DefaultKeys: []tui.KeyID{"ctrl+p"}, Description: "Toggle session path display"},
	{ID: "app.session.toggleSort", DefaultKeys: []tui.KeyID{"ctrl+s"}, Description: "Toggle session sort"},
	{ID: "app.session.rename", DefaultKeys: []tui.KeyID{"ctrl+r"}, Description: "Rename session"},
	{ID: "app.session.delete", DefaultKeys: []tui.KeyID{"ctrl+d"}, Description: "Delete session"},
	{ID: "app.session.deleteNoninvasive", DefaultKeys: []tui.KeyID{"alt+d"}, Description: "Delete session (no confirm)"},
	{ID: "app.models.save", DefaultKeys: []tui.KeyID{"ctrl+s"}, Description: "Save model selection"},
	{ID: "app.models.enableAll", DefaultKeys: []tui.KeyID{"ctrl+a"}, Description: "Enable all models"},
	{ID: "app.models.clearAll", DefaultKeys: []tui.KeyID{"ctrl+x"}, Description: "Clear all models"},
	{ID: "app.models.toggleProvider", DefaultKeys: []tui.KeyID{"ctrl+p"}, Description: "Toggle provider models"},
	{ID: "app.models.reorderUp", DefaultKeys: []tui.KeyID{"alt+up"}, Description: "Move model up"},
	{ID: "app.models.reorderDown", DefaultKeys: []tui.KeyID{"alt+down"}, Description: "Move model down"},
}

// NewAppKeybindings creates a keybindings manager with all TUI + app definitions.
func NewAppKeybindings(user tui.KeybindingsConfig) *tui.KeybindingsManager {
	all := make([]tui.KeybindingDefinition, 0, len(tui.TUIKeybindingDefinitions)+len(AppKeybindingDefinitions))
	all = append(all, tui.TUIKeybindingDefinitions...)
	all = append(all, AppKeybindingDefinitions...)
	return tui.NewKeybindingsManager(all, user)
}
