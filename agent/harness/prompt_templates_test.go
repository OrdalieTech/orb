package harness

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadPromptTemplatesThroughEnvironment(t *testing.T) {
	root := t.TempDir()
	writeHarnessSkillFile(t, root, "a/one.md", "---\ndescription: One template\n---\nHello $1")
	writeHarnessSkillFile(t, root, "a/nested/ignored.md", "Ignored")
	writeHarnessSkillFile(t, root, "b/two.md", "First line description\nBody")
	target := filepath.Join(root, "target.md")
	writeHarnessSkillFile(t, root, "target.md", "---\ndescription: Target\n---\nTarget body")
	if err := os.Symlink(target, filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}

	result := LoadPromptTemplates(LocalExecutionEnv{CWD: root}, "a", "b", "link.md")
	want := []PromptTemplate{
		{Name: "one", Description: "One template", Content: "Hello $1"},
		{Name: "two", Description: "First line description", Content: "First line description\nBody"},
		{Name: "link", Description: "Target", Content: "Target body"},
	}
	if len(result.Diagnostics) != 0 || !reflect.DeepEqual(result.PromptTemplates, want) {
		t.Fatalf("templates = %+v, diagnostics = %+v", result.PromptTemplates, result.Diagnostics)
	}
}

func TestLoadSourcedPromptTemplatesAttachesDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeHarnessSkillFile(t, root, "broken.md", "---\ndescription: [unterminated\n---\nBody")
	templates, diagnostics := LoadSourcedPromptTemplates(LocalExecutionEnv{CWD: root}, []SourcedPromptTemplateInput[string]{{Path: "broken.md", Source: "project"}})
	if len(templates) != 0 || len(diagnostics) != 1 || diagnostics[0].Source != "project" || diagnostics[0].Code != PromptTemplateDiagnosticParseFailed {
		t.Fatalf("sourced templates = %+v, diagnostics = %+v", templates, diagnostics)
	}
}

func TestFormatPromptTemplateInvocation(t *testing.T) {
	template := PromptTemplate{Name: "one", Content: "$1 ${@:2} $ARGUMENTS ${4:-fallback}"}
	if got := FormatPromptTemplateInvocation(template, []string{"hello world", "test", "$1"}); got != "hello world test $1 hello world test $1 ${4:-fallback}" {
		t.Fatalf("formatted = %q", got)
	}
}
