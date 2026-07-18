package config

import "os"

// Getters mirror upstream settings-manager.ts UI accessors, including their
// defaults and env fallbacks.

func (manager *SettingsManager) boolValue(key string) (bool, bool) {
	value, _ := manager.value(key)
	result, ok := value.(bool)
	return result, ok
}

func (manager *SettingsManager) GetQuietStartup() bool {
	value, _ := manager.boolValue("quietStartup")
	return value
}

func (manager *SettingsManager) GetDoubleEscapeAction() string {
	if action := manager.stringValue("doubleEscapeAction"); action != "" {
		return action
	}
	return "tree"
}

// Settings value takes precedence, then PI_CLEAR_ON_SHRINK, then false.
func (manager *SettingsManager) GetClearOnShrink() bool {
	if value, ok := manager.objectValue("terminal")["clearOnShrink"].(bool); ok {
		return value
	}
	return os.Getenv("PI_CLEAR_ON_SHRINK") == "1"
}

func (manager *SettingsManager) GetHideThinkingBlock() bool {
	value, _ := manager.boolValue("hideThinkingBlock")
	return value
}

func (manager *SettingsManager) GetShowCacheMissNotices() bool {
	value, _ := manager.boolValue("showCacheMissNotices")
	return value
}

// Settings value takes precedence, then PI_HARDWARE_CURSOR, then false.
func (manager *SettingsManager) GetShowHardwareCursor() bool {
	if value, ok := manager.boolValue("showHardwareCursor"); ok {
		return value
	}
	return os.Getenv("PI_HARDWARE_CURSOR") == "1"
}

func (manager *SettingsManager) GetEditorPaddingX() int {
	if padding := optionalInt(manager.GetSettings(), "editorPaddingX"); padding != nil {
		return *padding
	}
	return 0
}

func (manager *SettingsManager) GetAutocompleteMaxVisible() int {
	if visible := optionalInt(manager.GetSettings(), "autocompleteMaxVisible"); visible != nil {
		return *visible
	}
	return 5
}
