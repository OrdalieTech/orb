package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResourceDiscoveryDoesNotLoadPiDirectories(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ORB_AGENT_DIR", "")
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
	for _, dir := range []string{filepath.Join(home, ".pi", "agent"), filepath.Join(cwd, ".pi")} {
		mustWriteResource(t, filepath.Join(dir, "SYSTEM.md"), "Pi system sentinel")
		mustWriteResource(t, filepath.Join(dir, "APPEND_SYSTEM.md"), "Pi append sentinel")
		mustWriteResource(t, filepath.Join(dir, "prompts", "pi-sentinel.md"), "Pi prompt sentinel")
		mustWriteResource(t, filepath.Join(dir, "skills", "pi-sentinel", "SKILL.md"), "---\nname: pi-sentinel\ndescription: Pi skill sentinel\n---\nDo not load")
	}
	if got, want := DefaultAgentDir(), filepath.Join(home, ".orb", "agent"); got != want {
		t.Fatalf("default agent directory = %q, want %q", got, want)
	}
	trusted := true
	resources := LoadResources(ResourceOptions{CWD: cwd, ProjectTrusted: &trusted, NoContextFiles: true})
	if resources.SystemPrompt != nil || len(resources.AppendSystemPrompt) != 0 {
		t.Fatalf("Pi system prompt discovered: %+v", resources)
	}
	for _, skill := range resources.Skills {
		if skill.Name == "pi-sentinel" {
			t.Fatal("Pi skill discovered")
		}
	}
	for _, prompt := range resources.PromptTemplates {
		if prompt.Name == "pi-sentinel" {
			t.Fatal("Pi prompt discovered")
		}
	}
	mustWriteResource(t, filepath.Join(cwd, ".orb", "SYSTEM.md"), "Orb system")
	resources = LoadResources(ResourceOptions{CWD: cwd, ProjectTrusted: &trusted, NoContextFiles: true})
	if resources.SystemPrompt == nil || *resources.SystemPrompt != "Orb system" {
		t.Fatal("Orb system prompt not discovered")
	}
	for _, dir := range []string{filepath.Join(home, ".pi", "agent"), filepath.Join(cwd, ".pi")} {
		if got, err := os.ReadFile(filepath.Join(dir, "SYSTEM.md")); err != nil || string(got) != "Pi system sentinel" {
			t.Fatalf("Pi sentinel changed: %q, %v", got, err)
		}
	}
}
