package config

import "testing"

func TestUIEnvironmentIsOrbOwned(t *testing.T) {
	t.Setenv("PI_CLEAR_ON_SHRINK", "1")
	t.Setenv("PI_HARDWARE_CURSOR", "1")
	t.Setenv("ORB_CLEAR_ON_SHRINK", "")
	t.Setenv("ORB_HARDWARE_CURSOR", "")
	manager, err := NewSettingsManager(t.TempDir(), WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetClearOnShrink() || manager.GetShowHardwareCursor() {
		t.Fatal("Pi controls affected Orb UI settings")
	}
	t.Setenv("ORB_CLEAR_ON_SHRINK", "1")
	t.Setenv("ORB_HARDWARE_CURSOR", "1")
	if !manager.GetClearOnShrink() || !manager.GetShowHardwareCursor() {
		t.Fatal("Orb controls were ignored")
	}
}
