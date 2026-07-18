package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectTrustStoreInheritsFromParents(t *testing.T) {
	tempDir := t.TempDir()
	agentDir := filepath.Join(tempDir, "agent")
	parentDir := filepath.Join(tempDir, "trusted-parent")
	childDir := filepath.Join(parentDir, "project")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewProjectTrustStore(agentDir)

	if decision, err := store.Get(childDir); err != nil || decision != nil {
		t.Fatalf("initial decision = %v, %v", decision, err)
	}
	if err := store.Set(parentDir, boolPtr(true)); err != nil {
		t.Fatal(err)
	}
	if decision, _ := store.Get(childDir); decision == nil || !*decision {
		t.Fatalf("child should inherit parent trust, got %v", decision)
	}
	if err := store.Set(childDir, boolPtr(false)); err != nil {
		t.Fatal(err)
	}
	if decision, _ := store.Get(childDir); decision == nil || *decision {
		t.Fatalf("child should be untrusted, got %v", decision)
	}
	if err := store.Set(childDir, nil); err != nil {
		t.Fatal(err)
	}
	if decision, _ := store.Get(childDir); decision == nil || !*decision {
		t.Fatalf("child should fall back to parent trust, got %v", decision)
	}
}

func TestProjectTrustStoreFileFormat(t *testing.T) {
	tempDir := t.TempDir()
	agentDir := filepath.Join(tempDir, "agent")
	store := NewProjectTrustStore(agentDir)
	pathB := filepath.Join(tempDir, "b")
	pathA := filepath.Join(tempDir, "a")
	if err := os.MkdirAll(pathA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pathB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(pathB, boolPtr(false)); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(pathA, boolPtr(true)); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(agentDir, "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	realA, realB := canonicalizeTrustPath(pathA), canonicalizeTrustPath(pathB)
	// JSON.stringify(sorted, null, 2) + "\n": sorted keys, two-space indent.
	want := "{\n  \"" + realA + "\": true,\n  \"" + realB + "\": false\n}\n"
	if string(contents) != want {
		t.Fatalf("trust.json = %q, want %q", contents, want)
	}
}

func TestProjectTrustStoreRejectsInvalidFile(t *testing.T) {
	tempDir := t.TempDir()
	agentDir := filepath.Join(tempDir, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "trust.json"), []byte(`{"/x": "yes"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewProjectTrustStore(agentDir)
	if _, err := store.Get(tempDir); err == nil {
		t.Fatal("expected invalid trust store error")
	}
}

func TestHasTrustRequiringProjectResources(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	cwd := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".pi", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	// ~/.agents/skills is a user resource, never trust-requiring, even when
	// cwd is $HOME; ~/.pi/agent is not a project config dir.
	if HasTrustRequiringProjectResources(tempDir) {
		t.Fatal("home dir should not require trust")
	}
	if HasTrustRequiringProjectResources(cwd) {
		t.Fatal("plain project should not require trust")
	}

	if err := os.WriteFile(filepath.Join(tempDir, ".pi", "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasTrustRequiringProjectResources(tempDir) {
		t.Fatal(".pi/settings.json should require trust")
	}
	if err := os.Remove(filepath.Join(tempDir, ".pi", "settings.json")); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(cwd, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".pi", "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasTrustRequiringProjectResources(cwd) {
		t.Fatal("project .pi/settings.json should require trust")
	}
	if err := os.RemoveAll(filepath.Join(cwd, ".pi")); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(cwd, ".agents", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasTrustRequiringProjectResources(cwd) {
		t.Fatal("project .agents/skills should require trust")
	}
}

func TestGetProjectTrustOptions(t *testing.T) {
	tempDir := t.TempDir()
	parent := filepath.Join(tempDir, "parent")
	cwd := filepath.Join(parent, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	realCwd := normalizeTrustCwd(cwd)
	realParent := filepath.Dir(realCwd)

	options := GetProjectTrustOptions(cwd, true)
	labels := make([]string, 0, len(options))
	for _, option := range options {
		labels = append(labels, option.Label)
	}
	want := []string{
		"Trust",
		"Trust parent folder (" + realParent + ")",
		"Trust (this session only)",
		"Do not trust",
		"Do not trust (this session only)",
	}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v", labels)
	}
	for index := range want {
		if labels[index] != want[index] {
			t.Fatalf("labels[%d] = %q, want %q", index, labels[index], want[index])
		}
	}
	// Trust-parent clears the child decision (null update).
	parentOption := options[1]
	if len(parentOption.Updates) != 2 || parentOption.Updates[0].Decision == nil || !*parentOption.Updates[0].Decision ||
		parentOption.Updates[1].Decision != nil || parentOption.Updates[1].Path != realCwd {
		t.Fatalf("parent option updates = %+v", parentOption.Updates)
	}
	// Session-only options persist nothing.
	if len(options[2].Updates) != 0 || len(options[4].Updates) != 0 {
		t.Fatalf("session-only options should have no updates")
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

func TestSetProjectPackagesWritesUpstreamShape(t *testing.T) {
	tempDir := t.TempDir()
	agentDir := filepath.Join(tempDir, "agent")
	cwd := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSettingsManager(cwd, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	autoload := false
	err = manager.SetProjectPackages([]PackageSource{
		{Source: "npm:pi-tools", IsObject: true, Autoload: &autoload, Extensions: []string{"-extensions/bar.ts"}},
		{Source: "npm:simple"},
	})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(cwd, ConfigDirName, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "packages": [
    {
      "source": "npm:pi-tools",
      "autoload": false,
      "extensions": [
        "-extensions/bar.ts"
      ]
    },
    "npm:simple"
  ]
}`
	if string(contents) != want {
		t.Fatalf("project settings = %s\nwant %s", contents, want)
	}
}
