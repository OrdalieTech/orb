package config

import (
	"fmt"
	"os"
)

// Getters mirror upstream settings-manager.ts UI accessors, including their
// defaults and env fallbacks.

func (manager *SettingsManager) boolValue(key string) (bool, bool) {
	value, _ := manager.value(key)
	result, ok := value.(bool)
	return result, ok
}

// GetQuietStartup returns "true" (no startup header), "header" (header only,
// no startup details) or "false".
func (manager *SettingsManager) GetQuietStartup() string {
	if value, _ := manager.value("quietStartup"); value == true || value == "header" {
		return fmt.Sprint(value)
	}
	return "false"
}

func (manager *SettingsManager) GetDoubleEscapeAction() string {
	if action := manager.stringValue("doubleEscapeAction"); action != "" {
		return action
	}
	return "tree"
}

// Settings value takes precedence, then ORB_CLEAR_ON_SHRINK, then false.
func (manager *SettingsManager) GetClearOnShrink() bool {
	if value, ok := manager.objectValue("terminal")["clearOnShrink"].(bool); ok {
		return value
	}
	return os.Getenv("ORB_CLEAR_ON_SHRINK") == "1"
}

func (manager *SettingsManager) GetHideThinkingBlock() bool {
	value, _ := manager.boolValue("hideThinkingBlock")
	return value
}

type NoticeSettings struct {
	CacheMisses     bool `json:"cacheMisses"`
	CacheWarming    bool `json:"cacheWarming"`
	CompactionCosts bool `json:"compactionCosts"`
	ReasoningDrops  bool `json:"reasoningDrops"`
}

func (manager *SettingsManager) GetNotices() NoticeSettings {
	notices := manager.objectValue("notices")
	boolField := func(key string) bool {
		value, _ := notices[key].(bool)
		return value
	}
	return NoticeSettings{
		CacheMisses:     boolField("cacheMisses"),
		CacheWarming:    boolField("cacheWarming"),
		CompactionCosts: boolField("compactionCosts"),
		ReasoningDrops:  boolField("reasoningDrops"),
	}
}

func (manager *SettingsManager) GetShowCacheMissNotices() bool {
	return manager.GetNotices().CacheMisses
}

// Settings value takes precedence, then ORB_HARDWARE_CURSOR, then false.
func (manager *SettingsManager) GetShowHardwareCursor() bool {
	if value, ok := manager.boolValue("showHardwareCursor"); ok {
		return value
	}
	return os.Getenv("ORB_HARDWARE_CURSOR") == "1"
}

func (manager *SettingsManager) GetEditorPaddingX() int {
	if padding := manager.intValue("editorPaddingX"); padding != nil {
		return *padding
	}
	return 0
}

func (manager *SettingsManager) GetAutocompleteMaxVisible() int {
	if visible := manager.intValue("autocompleteMaxVisible"); visible != nil {
		return *visible
	}
	return 5
}
