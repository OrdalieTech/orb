package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImageSettingsDefaultsAndMergedOverrides(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if !manager.GetShowImages() || manager.GetImageWidthCells() != 60 || !manager.GetImageAutoResize() || manager.GetBlockImages() {
		t.Fatalf("defaults = show:%v width:%d resize:%v block:%v", manager.GetShowImages(), manager.GetImageWidthCells(), manager.GetImageAutoResize(), manager.GetBlockImages())
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"terminal":{"showImages":false,"imageWidthCells":7.9},"images":{"autoResize":false,"blockImages":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.GetShowImages() || reloaded.GetImageWidthCells() != 7 || reloaded.GetImageAutoResize() || !reloaded.GetBlockImages() {
		t.Fatalf("configured settings were not loaded")
	}
}
