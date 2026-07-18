package codingagent

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCommandResourceDiscoveryLocationsPrecedenceAndTrust(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	agentDir := filepath.Join(home, ".pi", "agent")
	repo := filepath.Join(root, "repo")
	cwd := filepath.Join(repo, "packages", "app")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill := func(path, name, description string) {
		mustWriteResource(t, path, "---\nname: "+name+"\ndescription: "+description+"\n---\nBody for "+name)
	}
	writeSkill(filepath.Join(agentDir, "skills", "global", "SKILL.md"), "global", "global")
	writeSkill(filepath.Join(home, ".agents", "skills", "home", "SKILL.md"), "home", "home")
	writeSkill(filepath.Join(repo, ".agents", "skills", "repo", "SKILL.md"), "repo", "repo")
	writeSkill(filepath.Join(repo, "packages", ".agents", "skills", "nested", "SKILL.md"), "nested", "nested")
	writeSkill(filepath.Join(cwd, ".agents", "skills", "cwd", "SKILL.md"), "cwd", "cwd")
	writeSkill(filepath.Join(root, ".agents", "skills", "above", "SKILL.md"), "above", "above")
	writeSkill(filepath.Join(cwd, ".pi", "skills", "project", "SKILL.md"), "project", "project")
	writeSkill(filepath.Join(cwd, ".pi", "skills", "collision", "SKILL.md"), "global", "project wins")
	mustWriteResource(t, filepath.Join(agentDir, "prompts", "same.md"), "Global prompt")
	mustWriteResource(t, filepath.Join(cwd, ".pi", "prompts", "same.md"), "Project prompt")
	mustWriteResource(t, filepath.Join(cwd, ".pi", "prompts", "project.md"), "Project only")

	trusted := true
	resources := LoadResources(ResourceOptions{CWD: cwd, AgentDir: agentDir, ProjectTrusted: &trusted, NoContextFiles: true})
	names := make([]string, len(resources.Skills))
	for index, skill := range resources.Skills {
		names[index] = skill.Name
	}
	wantNames := []string{"global", "project", "cwd", "nested", "repo", "home"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("skill order = %#v, want %#v", names, wantNames)
	}
	if resources.Skills[0].Description != "project wins" {
		t.Fatalf("project collision did not win: %#v", resources.Skills[0])
	}
	if len(resources.PromptTemplates) != 2 || resources.PromptTemplates[0].Name != "project" || resources.PromptTemplates[1].Content != "Project prompt" {
		t.Fatalf("prompts = %#v", resources.PromptTemplates)
	}
	for _, name := range names {
		if name == "above" {
			t.Fatalf("skill above git root was discovered: %#v", names)
		}
	}

	trusted = false
	untrusted := LoadResources(ResourceOptions{CWD: cwd, AgentDir: agentDir, ProjectTrusted: &trusted, NoContextFiles: true})
	for _, skill := range untrusted.Skills {
		if skill.SourceInfo.Scope == "project" {
			t.Fatalf("untrusted project skill loaded: %#v", skill)
		}
	}
	if len(untrusted.PromptTemplates) != 1 || untrusted.PromptTemplates[0].Content != "Global prompt" {
		t.Fatalf("untrusted prompts = %#v", untrusted.PromptTemplates)
	}
}

func TestExplicitCommandResourcesRemainAdditiveWhenDiscoveryDisabled(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	explicitSkill := filepath.Join(root, "explicit", "SKILL.md")
	mustWriteResource(t, explicitSkill, "---\nname: explicit\ndescription: Explicit skill\n---\nBody")
	explicitPrompt := filepath.Join(root, "prompt.md")
	mustWriteResource(t, explicitPrompt, "Explicit $1")
	mustWriteResource(t, filepath.Join(agentDir, "skills", "hidden", "SKILL.md"), "---\nname: hidden\ndescription: Hidden\n---\nBody")

	resources := LoadResources(ResourceOptions{
		CWD: root, AgentDir: agentDir, NoContextFiles: true,
		NoSkills: true, NoPromptTemplates: true,
		SkillPaths: []string{explicitSkill}, PromptTemplatePaths: []string{explicitPrompt},
	})
	if len(resources.Skills) != 1 || resources.Skills[0].Name != "explicit" || len(resources.PromptTemplates) != 1 || resources.PromptTemplates[0].Name != "prompt" {
		t.Fatalf("explicit resources = %#v / %#v", resources.Skills, resources.PromptTemplates)
	}
}

func TestPackageCommandResourcesCarryPackageProvenance(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	skillPath := filepath.Join(root, "package", "skills", "packaged", "SKILL.md")
	mustWriteResource(t, skillPath, "---\nname: packaged\ndescription: Package skill\n---\nBody")
	promptPath := filepath.Join(root, "package", "prompts", "package.md")
	mustWriteResource(t, promptPath, "Package prompt $1")

	resources := LoadResources(ResourceOptions{
		CWD: root, AgentDir: filepath.Join(root, "agent"), NoContextFiles: true,
		PackageSkillPaths:          []string{filepath.Dir(filepath.Dir(skillPath))},
		PackagePromptTemplatePaths: []string{filepath.Dir(promptPath)},
	})
	if len(resources.Skills) != 1 || resources.Skills[0].SourceInfo.Source != "package" || len(resources.PromptTemplates) != 1 || resources.PromptTemplates[0].SourceInfo.Source != "package" {
		t.Fatalf("package resources = %#v / %#v", resources.Skills, resources.PromptTemplates)
	}
}
