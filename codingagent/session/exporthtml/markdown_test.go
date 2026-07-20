package exporthtml

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/session"
)

func TestMarkdownExportMatchesActiveBranchGolden(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "session.md")
	outputURL := (&url.URL{Scheme: "file", Path: output}).String()
	path, err := ExportMarkdownFromFile(fixturePath(t), outputURL)
	if err != nil {
		t.Fatal(err)
	}
	if path != output {
		t.Fatalf("markdown path = %q, want %q", path, output)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "session.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("markdown export differs from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	for _, hidden := range []string{"abandoned branch", "must not render", `<skill name=`, "</skill>"} {
		if strings.Contains(string(got), hidden) {
			t.Errorf("markdown contains hidden structural content %q", hidden)
		}
	}
	for _, visible := range []string{"visible note", "Switched to model: `openai/gpt-next`", "`````text"} {
		if !strings.Contains(string(got), visible) {
			t.Errorf("markdown is missing %q", visible)
		}
	}
}

func TestMarkdownDefaultNameStripsOnlyLowercaseJSONL(t *testing.T) {
	source := fixturePath(t)
	root := t.TempDir()
	t.Chdir(root)
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(root, "fixture.JSONL")
	if err := os.WriteFile(upper, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := ExportMarkdownFromFile(upper, "")
	if err != nil {
		t.Fatal(err)
	}
	if path != "pi-session-fixture.JSONL.md" {
		t.Fatalf("default markdown path = %q", path)
	}
}

func TestMarkdownExportErrors(t *testing.T) {
	manager, err := session.InMemory(t.TempDir(), session.WithSessionID("memory-session"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExportSessionMarkdown(manager, ""); err == nil || err.Error() != "Cannot export in-memory session to Markdown" {
		t.Fatalf("in-memory Markdown error = %v", err)
	}
}

func TestCollisionSafeMarkdownDelimiters(t *testing.T) {
	t.Parallel()
	if got, want := fencedBlock("text", "before\n````\nafter"), "`````text\nbefore\n````\nafter\n`````"; got != want {
		t.Fatalf("fenced block = %q, want %q", got, want)
	}
	if got, want := inlineCode("model`name"), "``model`name``"; got != want {
		t.Fatalf("inline code = %q, want %q", got, want)
	}
}

func TestParseSkillBlockRequiresUpstreamShape(t *testing.T) {
	t.Parallel()
	valid := "<skill name=\"demo\" location=\"/tmp/SKILL.md\">\nbody\n</skill>\n\nrun it"
	got, ok := ParseSkillBlock(valid)
	if !ok || got.Name != "demo" || got.Location != "/tmp/SKILL.md" || got.Content != "body" || got.UserMessage != "run it" {
		t.Fatalf("parseSkillBlock(valid) = %+v, %v", got, ok)
	}
	nested := "<skill name=\"demo\" location=\"/tmp/SKILL.md\">\nalpha\n</skill>literal\nomega\n</skill>"
	got, ok = ParseSkillBlock(nested)
	if !ok || got.Content != "alpha\n</skill>literal\nomega" {
		t.Fatalf("parseSkillBlock(nested close) = %+v, %v", got, ok)
	}
	for _, invalid := range []string{
		"prefix " + valid,
		"<skill name=\"demo\" location=\"/tmp/SKILL.md\">\nbody\n</skill>suffix",
		"<skill name=\"demo\" location=\"/tmp/SKILL.md\">\nbody\n</skill>\n\n",
		"<skill name=\"de\"mo\" location=\"/tmp/SKILL.md\">\nbody\n</skill>",
		"<skill name=\"demo\" location=\"/tmp/\"SKILL.md\">\nbody\n</skill>",
	} {
		if _, ok := ParseSkillBlock(invalid); ok {
			t.Errorf("parseSkillBlock accepted %q", invalid)
		}
	}
}
