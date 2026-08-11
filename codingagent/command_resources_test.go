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
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
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

func TestCommandResourceDiscoveryImportsExternalAgentSkills(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude-home"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex-home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("OPENCODE_CONFIG_DIR", filepath.Join(root, "opencode-custom"))
	t.Setenv("GEMINI_CLI_HOME", filepath.Join(root, "gemini-home"))
	t.Setenv("COPILOT_HOME", filepath.Join(root, "copilot-home"))
	copilotExtra := filepath.Join(root, "copilot-extra")
	t.Setenv("COPILOT_SKILLS_DIRS", copilotExtra)

	agentDir := filepath.Join(home, ".pi", "agent")
	repo := filepath.Join(root, "repo")
	cwd := filepath.Join(repo, "packages", "app")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill := func(path, name, description string) {
		mustWriteResource(t, path, "---\nname: "+name+"\ndescription: "+description+"\n---\nBody for "+name)
	}

	writeSkill(filepath.Join(cwd, ".claude", "skills", "project-claude", "SKILL.md"), "project-claude", "cwd Claude skill")
	writeSkill(filepath.Join(repo, ".opencode", "skills", "project-opencode", "SKILL.md"), "project-opencode", "repo OpenCode skill")
	sharedDir := filepath.Join(home, ".agents", "skills", "shared")
	writeSkill(filepath.Join(sharedDir, "SKILL.md"), "shared", "standard user skill")
	claudeAlias := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "skills", "shared")
	if err := os.MkdirAll(filepath.Dir(claudeAlias), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sharedDir, claudeAlias); err != nil {
		t.Fatal(err)
	}
	writeSkill(filepath.Join(os.Getenv("CODEX_HOME"), "skills", "ponytail", "SKILL.md"), "ponytail", "Codex skill wins")
	writeSkill(filepath.Join(os.Getenv("OPENCODE_CONFIG_DIR"), "skills", "custom-open", "SKILL.md"), "custom-open", "custom OpenCode skill")
	writeSkill(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "skills", "ponytail", "SKILL.md"), "ponytail", "duplicate OpenCode skill")
	writeSkill(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "skills", "global-open", "SKILL.md"), "global-open", "global OpenCode skill")
	writeSkill(filepath.Join(os.Getenv("GEMINI_CLI_HOME"), ".gemini", "skills", "gemini", "SKILL.md"), "gemini", "Gemini skill")
	writeSkill(filepath.Join(home, ".cursor", "skills", "cursor", "SKILL.md"), "cursor", "Cursor skill")
	writeSkill(filepath.Join(os.Getenv("COPILOT_HOME"), "skills", "copilot", "SKILL.md"), "copilot", "Copilot skill")
	writeSkill(filepath.Join(copilotExtra, "copilot-extra", "SKILL.md"), "copilot-extra", "additional Copilot skill")

	trusted := true
	resources := LoadResources(ResourceOptions{CWD: cwd, AgentDir: agentDir, ProjectTrusted: &trusted, NoContextFiles: true})
	names := make([]string, len(resources.Skills))
	for index, skill := range resources.Skills {
		names[index] = skill.Name
	}
	want := []string{"project-claude", "project-opencode", "shared", "ponytail", "custom-open", "global-open", "gemini", "cursor", "copilot", "copilot-extra"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("external skill order = %#v, want %#v", names, want)
	}
	if resources.Skills[3].Description != "Codex skill wins" {
		t.Fatalf("duplicate winner = %#v", resources.Skills[3])
	}
	collisions := 0
	for _, diagnostic := range resources.Diagnostics {
		if diagnostic.Type == "collision" {
			collisions++
			if diagnostic.Collision == nil || diagnostic.Collision.Name != "ponytail" {
				t.Fatalf("unexpected collision: %#v", diagnostic)
			}
		}
	}
	if collisions != 1 {
		t.Fatalf("collisions = %d, want one; the symlink alias must be silent", collisions)
	}

	trusted = false
	resources = LoadResources(ResourceOptions{CWD: cwd, AgentDir: agentDir, ProjectTrusted: &trusted, NoContextFiles: true})
	for _, skill := range resources.Skills {
		if skill.Name == "project-claude" || skill.Name == "project-opencode" {
			t.Fatalf("untrusted external project skill loaded: %#v", skill)
		}
	}
}

func TestExplicitCommandResourcesRemainAdditiveWhenDiscoveryDisabled(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	agentDir := filepath.Join(root, "agent")
	explicitSkill := filepath.Join(root, "explicit", "SKILL.md")
	mustWriteResource(t, explicitSkill, "---\nname: explicit\ndescription: Explicit skill\n---\nBody")
	explicitPrompt := filepath.Join(root, "prompt.md")
	mustWriteResource(t, explicitPrompt, "Explicit $1")
	mustWriteResource(t, filepath.Join(agentDir, "skills", "hidden", "SKILL.md"), "---\nname: hidden\ndescription: Hidden\n---\nBody")
	mustWriteResource(t, filepath.Join(os.Getenv("CODEX_HOME"), "skills", "also-hidden", "SKILL.md"), "---\nname: also-hidden\ndescription: Hidden\n---\nBody")

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
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
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
