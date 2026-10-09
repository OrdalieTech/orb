package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOrbDirectoriesIgnorePiEnvironmentAndSettings(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ORB_AGENT_DIR", "")
	t.Setenv("ORB_SESSION_DIR", "")
	piDir := filepath.Join(home, ".pi", "agent")
	t.Setenv("PI_CODING_AGENT_DIR", piDir)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", filepath.Join(piDir, "sessions"))
	piGlobal, piProject := filepath.Join(piDir, "settings.json"), filepath.Join(project, ".pi", "settings.json")
	sentinel := `{"defaultModel":"pi-sentinel"}`
	writeRaw(t, piGlobal, sentinel)
	writeRaw(t, piProject, sentinel)
	want := filepath.Join(home, ".orb", "agent")
	if got, err := GetAgentDir(); err != nil || got != want {
		t.Fatalf("agent directory = %q, %v; want %q", got, err, want)
	}
	if got, err := ResolveSessionDir("", nil); err != nil || got != "" {
		t.Fatalf("inherited Pi session directory = %q, %v", got, err)
	}
	manager, err := NewSettingsManager(project)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.GetDefaultModel(); got != "" {
		t.Fatalf("discovered Pi settings: %q", got)
	}
	manager.SetDefaultModelAndProvider("orb-provider", "orb-model")
	if err := manager.SetProjectPromptTemplatePaths([]string{"prompts/own.md"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(want, "settings.json"), filepath.Join(project, ".orb", "settings.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Orb settings were not written to %s: %v", path, err)
		}
	}
	for _, path := range []string{piGlobal, piProject} {
		if got, err := os.ReadFile(path); err != nil || string(got) != sentinel {
			t.Fatalf("Pi sentinel changed: %s: %q, %v", path, got, err)
		}
	}
	t.Setenv("ORB_AGENT_DIR", filepath.Join(home, "custom"))
	if got, err := GetAgentDir(); err != nil || got != filepath.Join(home, "custom") {
		t.Fatalf("explicit Orb directory = %q, %v", got, err)
	}
	t.Setenv("ORB_SESSION_DIR", filepath.Join(home, "journals"))
	if got, err := ResolveSessionDir("", nil); err != nil || got != filepath.Join(home, "journals") {
		t.Fatalf("explicit Orb session directory = %q, %v", got, err)
	}
}
