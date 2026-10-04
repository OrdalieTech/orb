package modes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/tui"
)

func TestSkillSubmissionKeepsInvocationInPlace(t *testing.T) {
	known := func(name string) bool { return name == "docx" || name == "pdf" }
	for _, test := range []struct{ name, text, want, err string }{
		{name: "leading keeps upstream form", text: "/skill:docx fix the report", want: "/skill:docx fix the report"},
		{name: "inline keeps the whole text", text: "fix the report with /skill:docx please", want: "/skill:docx fix the report with /skill:docx please"},
		{name: "second line", text: "fix the report\n/skill:docx", want: "/skill:docx fix the report\n/skill:docx"},
		{name: "same skill twice", text: "use /skill:docx, then /skill:docx again", want: "/skill:docx use /skill:docx, then /skill:docx again"},
		{name: "unknown skill", text: "try /skill:nope", want: "try /skill:nope"},
		{name: "not a token", text: "see a/skill:docx", want: "see a/skill:docx"},
		{name: "unknown leading token", text: "/skill:nope then /skill:docx", want: "/skill:docx /skill:nope then /skill:docx"},
		{name: "two skills", text: "/skill:docx and /skill:pdf", err: "one skill per message: ◆ docx, ◆ pdf; send them separately"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := skillSubmission(test.text, known)
			if test.err != "" {
				if err == nil || err.Error() != test.err {
					t.Fatalf("error = %v, want %q", err, test.err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("submission = %q, %v; want %q", got, err, test.want)
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
	skills := []agent.Skill{{Name: "docx", FilePath: path, BaseDir: dir}}
	block := "<skill name=\"docx\" location=\"" + path + "\">\nReferences are relative to " + dir + ".\n\nEdit Word files.\n</skill>"
	known := func(name string) bool { return name == "docx" }

	for _, test := range []struct{ text, after, shown string }{
		{text: "fix the report with /skill:docx please", after: "fix the report with /skill:docx please", shown: "fix the report with ◆ docx please"},
		{text: "/skill:docx fix the report", after: "fix the report", shown: "◆ docx fix the report"},
		{text: "/skill:docx", shown: "◆ docx"},
	} {
		prompt, err := skillSubmission(test.text, known)
		if err != nil {
			t.Fatal(err)
		}
		expanded, err := agent.ExpandSkillCommand(prompt, skills)
		if err != nil {
			t.Fatal(err)
		}
		want := block
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

	// Several distinct skills are refused and the draft is kept.
	mode.autocompleteProvider = newSkillAutocompleteProvider(mode.autocompleteProvider, []tui.AutocompleteItem{
		{Value: "@inspect-skill", Label: "[skill] inspect-skill"}, {Value: "@other", Label: "[skill] other"},
	})
	mode.inputCh = make(chan inputEntry, 1)
	mode.chat = tui.NewWindowedContainer()
	mode.setupEditorSubmitHandler()
	mode.editor.SetText("/skill:inspect-skill then /skill:other")
	mode.editor.HandleInput(tui.KeyEvent{Raw: "\r"})
	if len(mode.inputCh) != 0 {
		t.Fatalf("two skills were submitted: %q", (<-mode.inputCh).text)
	}
	if got := mode.editor.GetText(); got != "/skill:inspect-skill then /skill:other" {
		t.Fatalf("draft after refusal = %q", got)
	}
	if warning := tui.StripANSI(strings.Join(mode.chat.Render(120), "\n")); !strings.Contains(warning, "one skill per message") {
		t.Fatalf("refusal not shown: %q", warning)
	}
}
