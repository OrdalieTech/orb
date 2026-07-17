package codingagent

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemPromptDefaultIsByteOrdered(t *testing.T) {
	packageDir := t.TempDir()
	prompt := BuildSystemPrompt(SystemPromptOptions{
		SelectedTools:    []string{"read", "bash", "hidden"},
		ToolSnippets:     map[string]string{"read": "Read file contents", "bash": "Execute bash commands"},
		PromptGuidelines: []string{"  Extra rule.  ", "Be concise in your responses", "Extra rule.", "   "},
		CWD:              `/work\tree`,
		PackageDir:       packageDir,
	})

	want := fmt.Sprintf(`You are an expert coding assistant operating inside pi, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.

Available tools:
- read: Read file contents
- bash: Execute bash commands

In addition to the tools above, you may have access to other custom tools depending on the project.

Guidelines:
- Use bash for file operations like ls, rg, find
- Extra rule.
- Be concise in your responses
- Show file paths clearly when working with files

Pi documentation (read only when the user asks about pi itself, its SDK, extensions, themes, skills, or TUI):
- Main documentation: %s
- Additional docs: %s
- Examples: %s (extensions, custom tools, SDK)
- When reading pi docs or examples, resolve docs/... under Additional docs and examples/... under Examples, not the current working directory
- When asked about: extensions (docs/extensions.md, examples/extensions/), themes (docs/themes.md), skills (docs/skills.md), prompt templates (docs/prompt-templates.md), TUI components (docs/tui.md), keybindings (docs/keybindings.md), SDK integrations (docs/sdk.md), custom providers (docs/custom-provider.md), adding models (docs/models.md), pi packages (docs/packages.md)
- When working on pi topics, read the docs and examples, and follow .md cross-references before implementing
- Always read pi .md files completely and follow links to related docs (e.g., tui.md for TUI API details)
Current working directory: /work/tree`, filepath.Join(packageDir, "README.md"), filepath.Join(packageDir, "docs"), filepath.Join(packageDir, "examples"))
	if prompt != want {
		t.Fatalf("prompt mismatch\n--- got ---\n%s\n--- want ---\n%s", prompt, want)
	}
}

func TestBuildSystemPromptCustomAppendContextAndEmptyTools(t *testing.T) {
	custom := "custom"
	appendPrompt := "append-a\n\nappend-b"
	prompt := BuildSystemPrompt(SystemPromptOptions{
		CustomPrompt:       &custom,
		SelectedTools:      []string{},
		AppendSystemPrompt: &appendPrompt,
		CWD:                `C:\repo\work`,
		ContextFiles: []ContextFile{
			{Path: `/one/AGENTS"&.md`, Content: "first<&"},
			{Path: "/two/CLAUDE.md", Content: "second"},
		},
	})
	want := "custom\n\nappend-a\n\nappend-b" +
		"\n\n<project_context>\n\n" +
		"Project-specific instructions and guidelines:\n\n" +
		"<project_instructions path=\"/one/AGENTS\"&.md\">\nfirst<&\n</project_instructions>\n\n" +
		"<project_instructions path=\"/two/CLAUDE.md\">\nsecond\n</project_instructions>\n\n" +
		"</project_context>\n" +
		"\nCurrent working directory: C:/repo/work"
	if prompt != want {
		t.Fatalf("custom prompt mismatch\n--- got ---\n%s\n--- want ---\n%s", prompt, want)
	}

	empty := ""
	defaultPrompt := BuildSystemPrompt(SystemPromptOptions{
		CustomPrompt:       &empty,
		AppendSystemPrompt: &empty,
		SelectedTools:      []string{},
		CWD:                "/cwd",
		PackageDir:         t.TempDir(),
	})
	if !strings.HasPrefix(defaultPrompt, "You are an expert coding assistant") {
		t.Fatalf("empty custom prompt did not select default prompt: %q", defaultPrompt)
	}
	if !strings.Contains(defaultPrompt, "Available tools:\n(none)") {
		t.Fatalf("explicit empty tools were not preserved: %q", defaultPrompt)
	}
}

func TestBuiltInToolPromptDataUsesActiveOrder(t *testing.T) {
	snippets, guidelines := BuiltInToolPromptData([]string{"write", "missing", "read", "edit"})
	if snippets["write"] != "Create or overwrite files" || snippets["read"] != "Read file contents" {
		t.Fatalf("unexpected snippets: %#v", snippets)
	}
	want := []string{
		"Use write only for new files or complete rewrites.",
		"Use read to examine files instead of cat or sed.",
		"Use edit for precise changes (edits[].oldText must match exactly)",
		"When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls",
		"Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
		"Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
	}
	if strings.Join(guidelines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("guidelines = %#v, want %#v", guidelines, want)
	}
}

func TestBuildSystemPromptNormalizesPackageDirectoryEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("PI_PACKAGE_DIR", "~/pi-package")

	prompt := BuildSystemPrompt(SystemPromptOptions{CWD: root, SelectedTools: []string{}})
	wantReadme := filepath.Join(root, "pi-package", "README.md")
	if !strings.Contains(prompt, "- Main documentation: "+wantReadme+"\n") {
		t.Fatalf("tilde package directory was not normalized in prompt: %q", prompt)
	}

	fileURL := "file://" + filepath.ToSlash(root) + "/encoded%20package"
	if filepath.Separator == '\\' {
		fileURL = "file:///" + strings.TrimPrefix(filepath.ToSlash(root), "/") + "/encoded%20package"
	}
	t.Setenv("PI_PACKAGE_DIR", fileURL)
	prompt = BuildSystemPrompt(SystemPromptOptions{CWD: root, SelectedTools: []string{}})
	wantReadme = filepath.Join(root, "encoded package", "README.md")
	if !strings.Contains(prompt, "- Main documentation: "+wantReadme+"\n") {
		t.Fatalf("file URL package directory was not normalized in prompt: %q", prompt)
	}
}

func TestSystemPromptGuidelinesUseJavaScriptTrimSet(t *testing.T) {
	prompt := BuildSystemPrompt(SystemPromptOptions{
		SelectedTools:    []string{},
		PromptGuidelines: []string{"\ufefftrimmed\ufeff", "\u0085preserved\u0085"},
		CWD:              "/cwd",
		PackageDir:       t.TempDir(),
	})
	if !strings.Contains(prompt, "- trimmed\n- \u0085preserved\u0085\n") {
		t.Fatalf("unexpected guideline trimming: %q", prompt)
	}
}
