package modes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/session/exporthtml"
	"github.com/OrdalieTech/orb/tui"
)

func TestSkillSubmissionKeepsInvocationInPlace(t *testing.T) {
	known := func(name string) bool { return name == "docx" || name == "pdf" }
	for _, test := range []struct{ name, text, want string }{
		{name: "leading keeps upstream form", text: "/skill:docx fix the report", want: "/skill:docx fix the report"},
		{name: "inline keeps the whole text", text: "fix the report with /skill:docx please", want: "/skill:docx fix the report with /skill:docx please"},
		{name: "second line", text: "fix the report\n/skill:docx", want: "/skill:docx fix the report\n/skill:docx"},
		{name: "same skill twice", text: "use /skill:docx, then /skill:docx again", want: "/skill:docx use /skill:docx, then /skill:docx again"},
		{name: "unknown skill", text: "try /skill:nope", want: "try /skill:nope"},
		{name: "not a token", text: "see a/skill:docx", want: "see a/skill:docx"},
		{name: "unknown leading token", text: "/skill:nope then /skill:docx", want: "/skill:docx /skill:nope then /skill:docx"},
		{name: "two skills", text: "/skill:docx and /skill:pdf", want: "/skill:docx /skill:pdf /skill:docx and /skill:pdf"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := exporthtml.SkillSubmission(test.text, known); got != test.want {
				t.Fatalf("submission = %q; want %q", got, test.want)
			}
		})
	}
}

// The inline rewrite rides the unchanged kernel expansion: the envelope is
// upstream's, and the text after the block is the message exactly as typed.
func TestInlineSkillExpandsToKernelEnvelopeWithOriginalText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: docx\ndescription: Word files\n---\nEdit Word files.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join(dir, "pdf.md")
	if err := os.WriteFile(pdf, []byte("---\nname: pdf\ndescription: PDF files\n---\nRead PDF files.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skills := []agent.Skill{{Name: "docx", FilePath: path, BaseDir: dir}, {Name: "pdf", FilePath: pdf, BaseDir: dir}}
	block := "<skill name=\"docx\" location=\"" + path + "\">\nReferences are relative to " + dir + ".\n\nEdit Word files.\n</skill>"
	pdfBlock := "<skill name=\"pdf\" location=\"" + pdf + "\">\nReferences are relative to " + dir + ".\n\nRead PDF files.\n</skill>"
	known := func(name string) bool { return name == "docx" || name == "pdf" }

	for _, test := range []struct{ text, blocks, after, shown string }{
		{text: "fix the report with /skill:docx please", blocks: block, after: "fix the report with /skill:docx please", shown: "fix the report with ◆ docx please"},
		{text: "/skill:docx fix the report", blocks: block, after: "fix the report", shown: "◆ docx fix the report"},
		{text: "/skill:docx", blocks: block, shown: "◆ docx"},
		{text: "turn /skill:pdf into /skill:docx", blocks: pdfBlock + "\n\n" + block, after: "turn /skill:pdf into /skill:docx", shown: "turn ◆ pdf into ◆ docx"},
	} {
		expanded, err := agent.ExpandSkillCommand(exporthtml.SkillSubmission(test.text, known), skills)
		if err != nil {
			t.Fatal(err)
		}
		want := test.blocks
		if test.after != "" {
			want += "\n\n" + test.after
		}
		if expanded != want {
			t.Fatalf("expanded %q =\n%q\nwant\n%q", test.text, expanded, want)
		}
		if got := skillPreview(expanded); got != test.shown {
			t.Fatalf("preview = %q, want %q", got, test.shown)
		}
	}
	if got := skillPreview("plain text"); got != "plain text" {
		t.Fatalf("plain preview = %q", got)
	}
}

func TestSkillChipInEditorAndSubmit(t *testing.T) {
	initTestTheme(t)
	mode := newF12AutocompleteMode(t, true)
	mode.setupAutocomplete()
	mode.editor.SetFocused(true)
	mode.editor.SetText("Please use /skill:inspect-skill now")

	rendered := tui.StripANSI(strings.Join(mode.editor.Render(60), "\n"))
	if !strings.Contains(rendered, "Please use ◆\u00a0inspect-skill now") || strings.Contains(rendered, "/skill:") {
		t.Fatalf("editor render = %q", rendered)
	}
	if got := mode.editor.GetText(); got != "Please use /skill:inspect-skill now" {
		t.Fatalf("editor text = %q, want the canonical token", got)
	}
	mode.editor.SetText("Please use /skill:inspect-skill")
	mode.editor.HandleInput(tui.KeyEvent{Raw: "\x7f"})
	if got := mode.editor.GetText(); got != "Please use " {
		t.Fatalf("backspace after chip = %q", got)
	}

	// Several distinct skills go out in one message.
	mode.autocompleteProvider = newSkillAutocompleteProvider(mode.autocompleteProvider, []tui.AutocompleteItem{
		{Value: "@inspect-skill", Label: "[skill] inspect-skill"}, {Value: "@other", Label: "[skill] other"},
	})
	mode.inputCh = make(chan inputEntry, 1)
	mode.chat = tui.NewWindowedContainer()
	mode.setupEditorSubmitHandler()
	mode.editor.SetText("/skill:inspect-skill then /skill:other")
	mode.editor.HandleInput(tui.KeyEvent{Raw: "\r"})
	if len(mode.inputCh) != 1 {
		t.Fatal("two skills were not submitted")
	}
	if got := (<-mode.inputCh).text; got != "/skill:inspect-skill /skill:other /skill:inspect-skill then /skill:other" {
		t.Fatalf("submitted %q", got)
	}
}
