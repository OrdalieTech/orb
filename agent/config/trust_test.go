package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasTrustRequiringExternalProjectSkillResources(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OPENCODE_CONFIG_DIR", filepath.Join(home, "custom-opencode"))
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("COPILOT_HOME", filepath.Join(home, ".copilot"))
	cwd := filepath.Join(home, "work", "repo", "nested")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".codex", "skills"),
		filepath.Join(home, ".config", "opencode", "skills"),
		filepath.Join(home, "custom-opencode", "skills"),
		filepath.Join(home, ".gemini", "skills"),
		filepath.Join(home, ".cursor", "skills"),
		filepath.Join(home, ".copilot", "skills"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if HasTrustRequiringProjectResources(cwd) {
		t.Fatal("user-level external skill directories should not require project trust")
	}

	projectRoot := filepath.Join(home, "work", "repo")
	for _, configDir := range []string{".claude", ".codex", ".opencode", ".gemini", ".cursor", ".github"} {
		dir := filepath.Join(projectRoot, configDir, "skills")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if !HasTrustRequiringProjectResources(cwd) {
			t.Fatalf("%s/skills should require project trust", configDir)
		}
		if err := os.RemoveAll(filepath.Join(projectRoot, configDir)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSettingsManagerProjectTrustGating(t *testing.T) {
	tempDir := t.TempDir()
	agentDir := filepath.Join(tempDir, "agent")
	cwd := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(filepath.Join(cwd, ConfigDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	projectSettings := `{"packages":["npm:@project/pkg"],"skills":["proj-skills"]}`
	if err := os.WriteFile(filepath.Join(cwd, ConfigDirName, "settings.json"), []byte(projectSettings), 0o644); err != nil {
		t.Fatal(err)
	}

	manager, err := NewSettingsManager(cwd, WithAgentDir(agentDir), WithProjectTrusted(false))
	if err != nil {
		t.Fatal(err)
	}
	if manager.IsProjectTrusted() {
		t.Fatal("manager should start untrusted")
	}
	if packages := manager.GetProjectPackages(); len(packages) != 0 {
		t.Fatalf("untrusted project packages = %v", packages)
	}
	if err := manager.SetProjectPackages(nil); err == nil {
		t.Fatal("untrusted project write should fail")
	}

	manager.SetProjectTrusted(true)
	packages := manager.GetProjectPackages()
	if len(packages) != 1 || packages[0].Source != "npm:@project/pkg" {
		t.Fatalf("trusted project packages = %v", packages)
	}
	if paths := manager.GetProjectSkillPaths(); len(paths) != 1 || paths[0] != "proj-skills" {
		t.Fatalf("trusted project skills = %v", paths)
	}

	manager.SetProjectTrusted(false)
	if packages := manager.GetProjectPackages(); len(packages) != 0 {
		t.Fatalf("revoked trust should drop project settings, got %v", packages)
	}
}
