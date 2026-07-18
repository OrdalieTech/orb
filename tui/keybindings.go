package tui

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
)

type KeybindingDefinition struct {
	ID          string
	DefaultKeys []KeyID
	Description string
}

type KeybindingsConfig map[string][]KeyID

type KeybindingConflict struct {
	Key         KeyID
	Keybindings []string
}

var TUIKeybindingDefinitions = []KeybindingDefinition{
	{ID: "tui.editor.cursorUp", DefaultKeys: []KeyID{"up"}, Description: "Move cursor up"},
	{ID: "tui.editor.cursorDown", DefaultKeys: []KeyID{"down"}, Description: "Move cursor down"},
	{ID: "tui.editor.cursorLeft", DefaultKeys: []KeyID{"left", "ctrl+b"}, Description: "Move cursor left"},
	{ID: "tui.editor.cursorRight", DefaultKeys: []KeyID{"right", "ctrl+f"}, Description: "Move cursor right"},
	{ID: "tui.editor.cursorWordLeft", DefaultKeys: []KeyID{"alt+left", "ctrl+left", "alt+b"}, Description: "Move cursor word left"},
	{ID: "tui.editor.cursorWordRight", DefaultKeys: []KeyID{"alt+right", "ctrl+right", "alt+f"}, Description: "Move cursor word right"},
	{ID: "tui.editor.cursorLineStart", DefaultKeys: []KeyID{"home", "ctrl+a"}, Description: "Move to line start"},
	{ID: "tui.editor.cursorLineEnd", DefaultKeys: []KeyID{"end", "ctrl+e"}, Description: "Move to line end"},
	{ID: "tui.editor.jumpForward", DefaultKeys: []KeyID{"ctrl+]"}, Description: "Jump forward to character"},
	{ID: "tui.editor.jumpBackward", DefaultKeys: []KeyID{"ctrl+alt+]"}, Description: "Jump backward to character"},
	{ID: "tui.editor.pageUp", DefaultKeys: []KeyID{"pageUp"}, Description: "Page up"},
	{ID: "tui.editor.pageDown", DefaultKeys: []KeyID{"pageDown"}, Description: "Page down"},
	{ID: "tui.editor.deleteCharBackward", DefaultKeys: []KeyID{"backspace"}, Description: "Delete character backward"},
	{ID: "tui.editor.deleteCharForward", DefaultKeys: []KeyID{"delete", "ctrl+d"}, Description: "Delete character forward"},
	{ID: "tui.editor.deleteWordBackward", DefaultKeys: []KeyID{"ctrl+w", "alt+backspace"}, Description: "Delete word backward"},
	{ID: "tui.editor.deleteWordForward", DefaultKeys: []KeyID{"alt+d", "alt+delete"}, Description: "Delete word forward"},
	{ID: "tui.editor.deleteToLineStart", DefaultKeys: []KeyID{"ctrl+u"}, Description: "Delete to line start"},
	{ID: "tui.editor.deleteToLineEnd", DefaultKeys: []KeyID{"ctrl+k"}, Description: "Delete to line end"},
	{ID: "tui.editor.yank", DefaultKeys: []KeyID{"ctrl+y"}, Description: "Yank"},
	{ID: "tui.editor.yankPop", DefaultKeys: []KeyID{"alt+y"}, Description: "Yank pop"},
	{ID: "tui.editor.undo", DefaultKeys: []KeyID{"ctrl+-"}, Description: "Undo"},
	{ID: "tui.input.newLine", DefaultKeys: []KeyID{"shift+enter", "ctrl+j"}, Description: "Insert newline"},
	{ID: "tui.input.submit", DefaultKeys: []KeyID{"enter"}, Description: "Submit input"},
	{ID: "tui.input.tab", DefaultKeys: []KeyID{"tab"}, Description: "Tab / autocomplete"},
	{ID: "tui.input.copy", DefaultKeys: []KeyID{"ctrl+c"}, Description: "Copy selection"},
	{ID: "tui.select.up", DefaultKeys: []KeyID{"up"}, Description: "Move selection up"},
	{ID: "tui.select.down", DefaultKeys: []KeyID{"down"}, Description: "Move selection down"},
	{ID: "tui.select.pageUp", DefaultKeys: []KeyID{"pageUp"}, Description: "Selection page up"},
	{ID: "tui.select.pageDown", DefaultKeys: []KeyID{"pageDown"}, Description: "Selection page down"},
	{ID: "tui.select.confirm", DefaultKeys: []KeyID{"enter"}, Description: "Confirm selection"},
	{ID: "tui.select.cancel", DefaultKeys: []KeyID{"escape", "ctrl+c"}, Description: "Cancel selection"},
}

var legacyTUIKeybindings = map[string]string{
	"cursorUp": "tui.editor.cursorUp", "cursorDown": "tui.editor.cursorDown", "cursorLeft": "tui.editor.cursorLeft",
	"cursorRight": "tui.editor.cursorRight", "cursorWordLeft": "tui.editor.cursorWordLeft", "cursorWordRight": "tui.editor.cursorWordRight",
	"cursorLineStart": "tui.editor.cursorLineStart", "cursorLineEnd": "tui.editor.cursorLineEnd", "jumpForward": "tui.editor.jumpForward",
	"jumpBackward": "tui.editor.jumpBackward", "pageUp": "tui.editor.pageUp", "pageDown": "tui.editor.pageDown",
	"deleteCharBackward": "tui.editor.deleteCharBackward", "deleteCharForward": "tui.editor.deleteCharForward",
	"deleteWordBackward": "tui.editor.deleteWordBackward", "deleteWordForward": "tui.editor.deleteWordForward",
	"deleteToLineStart": "tui.editor.deleteToLineStart", "deleteToLineEnd": "tui.editor.deleteToLineEnd", "yank": "tui.editor.yank",
	"yankPop": "tui.editor.yankPop", "undo": "tui.editor.undo", "newLine": "tui.input.newLine", "submit": "tui.input.submit",
	"tab": "tui.input.tab", "copy": "tui.input.copy", "selectUp": "tui.select.up", "selectDown": "tui.select.down",
	"selectPageUp": "tui.select.pageUp", "selectPageDown": "tui.select.pageDown", "selectConfirm": "tui.select.confirm", "selectCancel": "tui.select.cancel",
}

type KeybindingsManager struct {
	mu          sync.RWMutex
	definitions map[string]KeybindingDefinition
	order       []string
	user        KeybindingsConfig
	resolved    KeybindingsConfig
	conflicts   []KeybindingConflict
	configPath  string
}

func NewKeybindingsManager(definitions []KeybindingDefinition, user KeybindingsConfig) *KeybindingsManager {
	manager := &KeybindingsManager{definitions: make(map[string]KeybindingDefinition), user: cloneConfig(user)}
	for _, definition := range definitions {
		manager.definitions[definition.ID] = definition
		manager.order = append(manager.order, definition.ID)
	}
	manager.rebuild()
	return manager
}

func NewDefaultKeybindings(user KeybindingsConfig) *KeybindingsManager {
	return NewKeybindingsManager(TUIKeybindingDefinitions, user)
}

func NewKeybindingsFromFile(definitions []KeybindingDefinition, path string) *KeybindingsManager {
	manager := NewKeybindingsManager(definitions, LoadKeybindingsFile(path))
	manager.configPath = path
	return manager
}

func normalizeBinding(keys []KeyID) []KeyID {
	seen := make(map[KeyID]bool, len(keys))
	result := make([]KeyID, 0, len(keys))
	for _, key := range keys {
		if !seen[key] {
			seen[key] = true
			result = append(result, key)
		}
	}
	return result
}

func (manager *KeybindingsManager) rebuild() {
	manager.resolved = make(KeybindingsConfig, len(manager.definitions))
	manager.conflicts = nil
	claims := make(map[KeyID][]string)
	userIDs := make([]string, 0, len(manager.user))
	for id := range manager.user {
		userIDs = append(userIDs, id)
	}
	sort.Strings(userIDs)
	for _, id := range userIDs {
		if _, ok := manager.definitions[id]; ok {
			for _, key := range normalizeBinding(manager.user[id]) {
				claims[key] = append(claims[key], id)
			}
		}
	}
	keys := make([]string, 0, len(claims))
	for key := range claims {
		keys = append(keys, string(key))
	}
	sort.Strings(keys)
	for _, value := range keys {
		key := KeyID(value)
		if len(claims[key]) > 1 {
			manager.conflicts = append(manager.conflicts, KeybindingConflict{Key: key, Keybindings: append([]string(nil), claims[key]...)})
		}
	}
	for _, id := range manager.order {
		keys, overridden := manager.user[id]
		if !overridden {
			keys = manager.definitions[id].DefaultKeys
		}
		manager.resolved[id] = normalizeBinding(keys)
	}
}

func (manager *KeybindingsManager) Matches(data, id string) bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	for _, key := range manager.resolved[id] {
		if MatchesKey(data, key) {
			return true
		}
	}
	return false
}

func (manager *KeybindingsManager) Keys(id string) []KeyID {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return append([]KeyID(nil), manager.resolved[id]...)
}

func (manager *KeybindingsManager) Definition(id string) (KeybindingDefinition, bool) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	definition, ok := manager.definitions[id]
	return definition, ok
}

func (manager *KeybindingsManager) Conflicts() []KeybindingConflict {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	result := make([]KeybindingConflict, len(manager.conflicts))
	for index, conflict := range manager.conflicts {
		result[index] = KeybindingConflict{Key: conflict.Key, Keybindings: append([]string(nil), conflict.Keybindings...)}
	}
	return result
}

func (manager *KeybindingsManager) SetUserBindings(user KeybindingsConfig) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.user = cloneConfig(user)
	manager.rebuild()
}

func (manager *KeybindingsManager) UserBindings() KeybindingsConfig {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return cloneConfig(manager.user)
}
func (manager *KeybindingsManager) ResolvedBindings() KeybindingsConfig {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return cloneConfig(manager.resolved)
}

func (manager *KeybindingsManager) Reload() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.configPath == "" {
		return
	}
	manager.user = LoadKeybindingsFile(manager.configPath)
	manager.rebuild()
}

func cloneConfig(config KeybindingsConfig) KeybindingsConfig {
	result := make(KeybindingsConfig, len(config))
	for id, keys := range config {
		result[id] = append([]KeyID(nil), keys...)
	}
	return result
}

// LoadKeybindingsFile mirrors upstream's forgiving keybindings.json parser:
// absent, malformed, and non-object files all resolve to an empty override.
func LoadKeybindingsFile(path string) KeybindingsConfig {
	contents, err := os.ReadFile(path)
	if err != nil {
		return KeybindingsConfig{}
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(contents, &raw) != nil || raw == nil {
		return KeybindingsConfig{}
	}
	config := make(KeybindingsConfig)
	for original, encoded := range raw {
		id := original
		if migrated := legacyTUIKeybindings[original]; migrated != "" {
			id = migrated
			if _, canonical := raw[id]; canonical {
				continue
			}
		}
		var single string
		if json.Unmarshal(encoded, &single) == nil {
			config[id] = []KeyID{KeyID(single)}
			continue
		}
		var many []string
		if json.Unmarshal(encoded, &many) == nil {
			keys := make([]KeyID, len(many))
			for index, key := range many {
				keys[index] = KeyID(key)
			}
			config[id] = keys
		}
	}
	return config
}

var globalKeybindings struct {
	sync.RWMutex
	manager *KeybindingsManager
}

func SetKeybindings(manager *KeybindingsManager) {
	globalKeybindings.Lock()
	globalKeybindings.manager = manager
	globalKeybindings.Unlock()
}
func GetKeybindings() *KeybindingsManager {
	globalKeybindings.RLock()
	manager := globalKeybindings.manager
	globalKeybindings.RUnlock()
	if manager != nil {
		return manager
	}
	globalKeybindings.Lock()
	defer globalKeybindings.Unlock()
	if globalKeybindings.manager == nil {
		globalKeybindings.manager = NewDefaultKeybindings(nil)
	}
	return globalKeybindings.manager
}
