package harness

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadSkillsUsesEnvironmentAndIgnoreRules(t *testing.T) {
	root := t.TempDir()
	writeHarnessSkillFile(t, root, "skills/.gitignore", "ignored/\n")
	writeHarnessSkillFile(t, root, "skills/alpha/SKILL.md", "---\nname: alpha\ndescription: Alpha skill.\n---\nAlpha body.")
	writeHarnessSkillFile(t, root, "skills/alpha/nested/SKILL.md", "---\nname: nested\ndescription: Must not load.\n---\nNested.")
	writeHarnessSkillFile(t, root, "skills/ignored/SKILL.md", "---\nname: ignored\ndescription: Ignored.\n---\nIgnored.")
	writeHarnessSkillFile(t, root, "skills/root.md", "---\nname: root\ndescription: Root file.\n---\nRoot body.")

	result := LoadSkills(LocalExecutionEnv{CWD: root}, "skills")
	names := make([]string, len(result.Skills))
	for index, skill := range result.Skills {
		names[index] = skill.Name
	}
	if !reflect.DeepEqual(names, []string{"alpha", "root"}) {
		t.Fatalf("skill names = %v", names)
	}
	if result.Skills[0].Content != "Alpha body." || result.Skills[1].Content != "Root body." {
		t.Fatalf("skill bodies = %+v", result.Skills)
	}
}

func TestLoadSkillsReportsInvalidMetadataAndSkipsMissingDescription(t *testing.T) {
	root := t.TempDir()
	writeHarnessSkillFile(t, root, "different/SKILL.md", "---\nname: valid-name\ndescription: Present.\n---\nBody.")
	writeHarnessSkillFile(t, root, "missing/SKILL.md", "---\nname: missing\n---\nBody.")

	result := LoadSkills(LocalExecutionEnv{CWD: root}, root)
	if len(result.Skills) != 1 || result.Skills[0].Name != "valid-name" {
		t.Fatalf("skills = %+v", result.Skills)
	}
	var invalid int
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Type != "warning" {
			t.Fatalf("diagnostic type = %q", diagnostic.Type)
		}
		if diagnostic.Code == SkillDiagnosticInvalidMeta {
			invalid++
		}
	}
	if invalid != 2 {
		t.Fatalf("invalid metadata diagnostics = %d: %+v", invalid, result.Diagnostics)
	}
}

func TestLoadSourcedSkillsAndInvocation(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "inspect", "SKILL.md")
	writeHarnessSkillFile(t, root, "inspect/SKILL.md", "---\nname: inspect\ndescription: Inspect.\ndisable-model-invocation: true\n---\nRead files.")

	skills, diagnostics := LoadSourcedSkills(LocalExecutionEnv{CWD: root}, []SourcedSkillInput[string]{{Path: root, Source: "project"}})
	if len(diagnostics) != 0 || len(skills) != 1 || skills[0].Source != "project" || !skills[0].Skill.DisableModelInvocation {
		t.Fatalf("sourced result = %+v, %+v", skills, diagnostics)
	}
	want := "<skill name=\"inspect\" location=\"" + filePath + "\">\nReferences are relative to " + filepath.Dir(filePath) + ".\n\nRead files.\n</skill>\n\nFocus on errors."
	if got := FormatSkillInvocation(skills[0].Skill, "Focus on errors."); got != want {
		t.Fatalf("invocation\nwant: %q\n got: %q", want, got)
	}
}

func writeHarnessSkillFile(t testing.TB, root, relativePath, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", relativePath, err)
	}
}
