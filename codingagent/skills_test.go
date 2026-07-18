package codingagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillsFromDirValidationDiscoveryAndIgnore(t *testing.T) {
	root := t.TempDir()
	mustWriteResource(t, filepath.Join(root, ".gitignore"), "ignored\n")
	mustWriteResource(t, filepath.Join(root, "valid", "SKILL.md"), "---\nname: different-name\ndescription: |\n  A multiline skill.\n  Use it carefully.\nallowed-tools: read bash\ndisable-model-invocation: true\n---\nUse this skill.\n")
	mustWriteResource(t, filepath.Join(root, "missing", "SKILL.md"), "---\nname: missing\n---\nNo description")
	mustWriteResource(t, filepath.Join(root, "invalid", "SKILL.md"), "---\nname: Bad--Name\ndescription: Still loads\n---\nInvalid name")
	mustWriteResource(t, filepath.Join(root, "ignored", "bad", "SKILL.md"), "---\nname: ignored\ndescription: Hidden\n---\nHidden")
	mustWriteResource(t, filepath.Join(root, "preferred", "SKILL.md"), "---\nname: preferred\ndescription: Root wins\n---\nRoot")
	mustWriteResource(t, filepath.Join(root, "preferred", "nested", "SKILL.md"), "---\nname: nested\ndescription: Must lose\n---\nNested")

	result := LoadSkillsFromDir(LoadSkillsFromDirOptions{Dir: root, Source: "test"})
	byName := make(map[string]Skill)
	for _, skill := range result.Skills {
		byName[skill.Name] = skill
	}
	if len(result.Skills) != 3 || byName["different-name"].AllowedTools != "read bash" || !byName["different-name"].DisableModelInvocation {
		t.Fatalf("skills = %#v", result.Skills)
	}
	if byName["different-name"].Content != "Use this skill." || !strings.Contains(byName["different-name"].Description, "\n") {
		t.Fatalf("valid skill = %#v", byName["different-name"])
	}
	if _, found := byName["nested"]; found {
		t.Fatalf("nested skill below root SKILL.md was loaded: %#v", result.Skills)
	}
	messages := make([]string, 0, len(result.Diagnostics))
	for _, diagnostic := range result.Diagnostics {
		messages = append(messages, diagnostic.Message)
	}
	joined := strings.Join(messages, "\n")
	for _, expected := range []string{"description is required", "invalid characters", "consecutive hyphens"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("diagnostics %q omit %q", joined, expected)
		}
	}
}

func TestLoadSkillsFollowsSymlinksAndKeepsFirstCollision(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	mustWriteResource(t, filepath.Join(first, "one", "SKILL.md"), "---\nname: same\ndescription: First\n---\nOne")
	mustWriteResource(t, filepath.Join(second, "two", "SKILL.md"), "---\nname: same\ndescription: Second\n---\nTwo")
	link := filepath.Join(root, "linked")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	result := LoadSkills(LoadSkillsOptions{CWD: root, AgentDir: root, SkillPaths: []string{link, second}})
	if len(result.Skills) != 1 || result.Skills[0].Description != "First" || !strings.HasPrefix(result.Skills[0].FilePath, link) {
		t.Fatalf("skills = %#v", result.Skills)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Type != "collision" || result.Diagnostics[0].Collision == nil {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
}

func TestFormatSkillsForPromptExactAndHidden(t *testing.T) {
	skills := []Skill{
		{Name: "visible", Description: `Use <read> & "care".`, FilePath: "/skills/visible/SKILL.md"},
		{Name: "hidden", Description: "Explicit only", FilePath: "/skills/hidden/SKILL.md", DisableModelInvocation: true},
	}
	want := "\n\nThe following skills provide specialized instructions for specific tasks.\n" +
		"Use the read tool to load a skill's file when the task matches its description.\n" +
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.\n\n" +
		"<available_skills>\n  <skill>\n    <name>visible</name>\n" +
		"    <description>Use &lt;read&gt; &amp; &quot;care&quot;.</description>\n" +
		"    <location>/skills/visible/SKILL.md</location>\n  </skill>\n</available_skills>"
	if got := FormatSkillsForPrompt(skills); got != want {
		t.Fatalf("skill prompt mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if got := FormatSkillsForPrompt(skills[1:]); got != "" {
		t.Fatalf("hidden-only prompt = %q", got)
	}
}

func TestBuildSystemPromptIncludesSkillsOnlyWithRead(t *testing.T) {
	skill := Skill{Name: "inspect", Description: "Inspect", FilePath: "/skills/inspect/SKILL.md"}
	withRead := BuildSystemPrompt(SystemPromptOptions{SelectedTools: []string{"read"}, Skills: []Skill{skill}, CWD: "/cwd", PackageDir: t.TempDir()})
	if !strings.Contains(withRead, "<available_skills>") || !strings.HasSuffix(withRead, "\nCurrent working directory: /cwd") {
		t.Fatalf("skill block placement mismatch: %q", withRead)
	}
	withoutRead := BuildSystemPrompt(SystemPromptOptions{SelectedTools: []string{"bash"}, Skills: []Skill{skill}, CWD: "/cwd", PackageDir: t.TempDir()})
	if strings.Contains(withoutRead, "<available_skills>") {
		t.Fatalf("skills visible without read: %q", withoutRead)
	}
}
