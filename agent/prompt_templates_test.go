package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPromptTemplateLoadingAndExpansion(t *testing.T) {
	root := t.TempDir()
	prompts := filepath.Join(root, "prompts")
	if err := os.MkdirAll(filepath.Join(prompts, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteResource(t, filepath.Join(prompts, "review.md"), "---\ndescription: Review staged changes\nargument-hint: \"<path>\"\n---\nReview $1 with ${@:2}")
	mustWriteResource(t, filepath.Join(prompts, "fallback.md"), "\nFirst line description that is deliberately longer than sixty characters to truncate\nBody")
	mustWriteResource(t, filepath.Join(prompts, "nested", "ignored.md"), "Ignored")

	templates, _ := LoadPromptTemplates(LoadPromptTemplatesOptions{CWD: root, AgentDir: root, PromptPaths: []string{prompts}})
	if len(templates) != 2 || templates[0].Name != "fallback" || templates[1].Name != "review" {
		t.Fatalf("templates = %#v", templates)
	}
	if templates[1].ArgumentHint != "<path>" || templates[1].Description != "Review staged changes" {
		t.Fatalf("review metadata = %#v", templates[1])
	}
	if got := ExpandPromptTemplate(`/review src/main.go "focus here"`, templates); got != "Review src/main.go with focus here" {
		t.Fatalf("expanded = %q", got)
	}
	if got := ExpandPromptTemplate("/review\nfile.go", templates); got != "Review file.go with " {
		t.Fatalf("newline expansion = %q", got)
	}
	if got := ExpandPromptTemplate("/missing x", templates); got != "/missing x" {
		t.Fatalf("unknown template = %q", got)
	}
}
