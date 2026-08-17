package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInteractiveUISettingsDefaultsAndEnvironmentFallbacks(t *testing.T) {
	t.Setenv("PI_CLEAR_ON_SHRINK", "1")
	t.Setenv("PI_HARDWARE_CURSOR", "1")
	t.Setenv("VISUAL", "visual-editor --wait")
	t.Setenv("EDITOR", "ignored-editor")

	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if manager.AgentDir() != agentDir {
		t.Fatalf("AgentDir() = %q, want %q", manager.AgentDir(), agentDir)
	}
	if manager.GetQuietStartup() || manager.GetHideThinkingBlock() || manager.GetShowCacheMissNotices() {
		t.Fatal("boolean UI defaults must be false")
	}
	if !manager.GetClearOnShrink() || !manager.GetShowHardwareCursor() {
		t.Fatal("terminal environment fallbacks were not honored")
	}
	if manager.GetShowTerminalProgress() {
		t.Fatal("showTerminalProgress default must be false")
	}
	if got := manager.GetExternalEditor(); got != "visual-editor --wait" {
		t.Fatalf("GetExternalEditor() = %q", got)
	}
	if got := manager.GetDoubleEscapeAction(); got != "tree" {
		t.Fatalf("GetDoubleEscapeAction() = %q", got)
	}
	if got := manager.GetTreeFilterMode(); got != "default" {
		t.Fatalf("GetTreeFilterMode() = %q", got)
	}
	if manager.GetEditorPaddingX() != 0 || manager.GetOutputPad() != 1 || manager.GetAutocompleteMaxVisible() != 5 {
		t.Fatalf("numeric UI defaults = padding %d, output %d, autocomplete %d", manager.GetEditorPaddingX(), manager.GetOutputPad(), manager.GetAutocompleteMaxVisible())
	}

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "editor-fallback")
	if got := manager.GetExternalEditor(); got != "editor-fallback" {
		t.Fatalf("EDITOR fallback = %q", got)
	}
	t.Setenv("EDITOR", "")
	wantEditor := "nano"
	if runtime.GOOS == "windows" {
		wantEditor = "notepad"
	}
	if got := manager.GetExternalEditor(); got != wantEditor {
		t.Fatalf("platform editor fallback = %q, want %q", got, wantEditor)
	}
}

func TestInteractiveUISettingWritesClampPersistAndPreserveUnknowns(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(agentDir, "settings.json")
	initial := `{"terminal":{"showImages":true},"unknown":{"future":1}}`
	if err := os.WriteFile(settingsPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}

	manager.SetQuietStartup(true)
	manager.SetHideThinkingBlock(true)
	manager.SetShowCacheMissNotices(true)
	manager.SetDefaultProjectTrust("always")
	manager.SetDoubleEscapeAction("fork")
	manager.SetTreeFilterMode("user-only")
	manager.SetEnableSkillCommands(false)
	manager.SetShowHardwareCursor(true)
	manager.SetEditorPaddingX(99)
	manager.SetOutputPad(0)
	manager.SetAutocompleteMaxVisible(1)
	manager.SetClearOnShrink(true)
	manager.SetShowTerminalProgress(true)
	manager.SetEnabledModels([]string{"anthropic/claude-*", "openai/*"})

	reloaded, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.GetQuietStartup() || !reloaded.GetHideThinkingBlock() || !reloaded.GetShowCacheMissNotices() {
		t.Fatal("boolean UI settings did not persist")
	}
	if reloaded.GetDefaultProjectTrust() != "always" || reloaded.GetDoubleEscapeAction() != "fork" || reloaded.GetTreeFilterMode() != "user-only" {
		t.Fatalf("selection settings = trust %q, escape %q, tree %q", reloaded.GetDefaultProjectTrust(), reloaded.GetDoubleEscapeAction(), reloaded.GetTreeFilterMode())
	}
	if reloaded.GetEnableSkillCommands() {
		t.Fatal("enableSkillCommands setting did not persist")
	}
	if !reloaded.GetShowHardwareCursor() || !reloaded.GetClearOnShrink() || !reloaded.GetShowTerminalProgress() {
		t.Fatal("terminal UI settings did not persist")
	}
	if reloaded.GetEditorPaddingX() != 3 || reloaded.GetOutputPad() != 0 || reloaded.GetAutocompleteMaxVisible() != 3 {
		t.Fatalf("clamped UI settings = padding %d, output %d, autocomplete %d", reloaded.GetEditorPaddingX(), reloaded.GetOutputPad(), reloaded.GetAutocompleteMaxVisible())
	}
	if got := reloaded.GetEnabledModels(); len(got) != 2 || got[0] != "anthropic/claude-*" || got[1] != "openai/*" {
		t.Fatalf("enabled models = %#v", got)
	}

	encoded, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	unknown, _ := persisted["unknown"].(map[string]any)
	terminal, _ := persisted["terminal"].(map[string]any)
	if unknown["future"] != float64(1) || terminal["showImages"] != true || terminal["clearOnShrink"] != true || terminal["showTerminalProgress"] != true {
		t.Fatalf("preserved settings = unknown %#v, terminal %#v", unknown, terminal)
	}
}

func TestTerminalImageSettingWritesPersistAndPreserveSiblings(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(agentDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"terminal":{"clearOnShrink":true},"unrelated":"kept"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}

	manager.SetShowImages(false)
	manager.SetImageWidthCells(0)

	reloaded, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.GetShowImages() || reloaded.GetImageWidthCells() != 1 {
		t.Fatalf("terminal image settings = show %t, width %d", reloaded.GetShowImages(), reloaded.GetImageWidthCells())
	}
	encoded, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	terminal, ok := persisted["terminal"].(map[string]any)
	if !ok || terminal["clearOnShrink"] != true || terminal["showImages"] != false || terminal["imageWidthCells"] != float64(1) {
		t.Fatalf("persisted terminal settings = %#v", persisted["terminal"])
	}
	if persisted["unrelated"] != "kept" {
		t.Fatalf("unrelated setting = %#v", persisted["unrelated"])
	}
}
