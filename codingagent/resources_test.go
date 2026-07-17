package codingagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectContextFilesGlobalThenAncestorsThenCWD(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent-home")
	project := filepath.Join(root, "project")
	cwd := filepath.Join(project, "nested")
	mustWriteResource(t, filepath.Join(agentDir, "CLAUDE.md"), "global")
	mustWriteResource(t, filepath.Join(root, "AGENTS.MD"), "root")
	mustWriteResource(t, filepath.Join(project, "CLAUDE.md"), "project")
	mustWriteResource(t, filepath.Join(cwd, "CLAUDE.md"), "lower-priority")
	mustWriteResource(t, filepath.Join(cwd, "AGENTS.md"), "cwd")

	files, diagnostics := LoadProjectContextFiles(cwd, agentDir)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	wantPaths := []string{
		filepath.Join(agentDir, "CLAUDE.md"),
		filepath.Join(root, "AGENTS.MD"),
		filepath.Join(project, "CLAUDE.md"),
		filepath.Join(cwd, "AGENTS.md"),
	}
	wantContents := []string{"global", "root", "project", "cwd"}
	if len(files) != len(wantPaths) {
		t.Fatalf("files = %#v, want %d entries", files, len(wantPaths))
	}
	for index := range wantPaths {
		if files[index].Path != wantPaths[index] || files[index].Content != wantContents[index] {
			t.Fatalf("files[%d] = %#v, want path %q content %q", index, files[index], wantPaths[index], wantContents[index])
		}
	}
}

func TestLoadProjectContextFilesFallsThroughUnreadableCandidate(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(filepath.Join(cwd, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteResource(t, filepath.Join(cwd, "CLAUDE.md"), "fallback")

	files, diagnostics := LoadProjectContextFiles(cwd, agentDir)
	if len(files) != 1 || files[0].Path != filepath.Join(cwd, "CLAUDE.md") {
		t.Fatalf("files = %#v", files)
	}
	if len(diagnostics) != 1 || diagnostics[0].Path != filepath.Join(cwd, "AGENTS.md") || strings.HasPrefix(diagnostics[0].Message, "Warning:") {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestLoadResourcesDiagnosticsDoNotIncludePresentationPrefix(t *testing.T) {
	root := t.TempDir()
	unreadable := filepath.Join(root, "prompt")
	if err := os.MkdirAll(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	resources := LoadResources(ResourceOptions{CWD: root, AgentDir: filepath.Join(root, "agent"), SystemPrompt: &unreadable})
	if len(resources.Diagnostics) != 1 || strings.HasPrefix(resources.Diagnostics[0].Message, "Warning:") {
		t.Fatalf("diagnostics = %#v", resources.Diagnostics)
	}
}

func TestLoadResourcesPromptPrecedenceTrustAndNoContext(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	mustWriteResource(t, filepath.Join(cwd, "AGENTS.md"), "context")
	mustWriteResource(t, filepath.Join(cwd, ".pi", "SYSTEM.md"), "project system")
	mustWriteResource(t, filepath.Join(cwd, ".pi", "APPEND_SYSTEM.md"), "project append")
	mustWriteResource(t, filepath.Join(agentDir, "SYSTEM.md"), "global system")
	mustWriteResource(t, filepath.Join(agentDir, "APPEND_SYSTEM.md"), "global append")

	trusted := true
	resources := LoadResources(ResourceOptions{CWD: cwd, AgentDir: agentDir, ProjectTrusted: &trusted})
	if resources.SystemPrompt == nil || *resources.SystemPrompt != "project system" {
		t.Fatalf("trusted system = %#v", resources.SystemPrompt)
	}
	if strings.Join(resources.AppendSystemPrompt, "|") != "project append" {
		t.Fatalf("trusted append = %#v", resources.AppendSystemPrompt)
	}
	if len(resources.ContextFiles) != 1 || resources.ContextFiles[0].Content != "context" {
		t.Fatalf("context = %#v", resources.ContextFiles)
	}

	trusted = false
	resources = LoadResources(ResourceOptions{CWD: cwd, AgentDir: agentDir, ProjectTrusted: &trusted})
	if len(resources.ContextFiles) != 1 || resources.ContextFiles[0].Content != "context" {
		t.Fatalf("project context files should load independently of project trust: %#v", resources.ContextFiles)
	}

	resources = LoadResources(ResourceOptions{CWD: cwd, AgentDir: agentDir, ProjectTrusted: &trusted, NoContextFiles: true})
	if resources.SystemPrompt == nil || *resources.SystemPrompt != "global system" {
		t.Fatalf("untrusted system = %#v", resources.SystemPrompt)
	}
	if strings.Join(resources.AppendSystemPrompt, "|") != "global append" {
		t.Fatalf("untrusted append = %#v", resources.AppendSystemPrompt)
	}
	if resources.ContextFiles == nil || len(resources.ContextFiles) != 0 {
		t.Fatalf("no-context files = %#v, want non-nil empty", resources.ContextFiles)
	}
}

func TestDefaultAgentDirNormalizesEnvironmentOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("PI_CODING_AGENT_DIR", "~/custom-agent")
	if got, want := DefaultAgentDir(), filepath.Join(root, "custom-agent"); got != want {
		t.Fatalf("tilde agent directory = %q, want %q", got, want)
	}

	t.Setenv("PI_CODING_AGENT_DIR", "file://"+filepath.ToSlash(root)+"/encoded%20agent")
	if got, want := DefaultAgentDir(), filepath.Join(root, "encoded agent"); got != want {
		t.Fatalf("file URL agent directory = %q, want %q", got, want)
	}
}

func TestLoadResourcesCLIOverridesFileLiteralAndExplicitEmpty(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	systemPath := filepath.Join(root, "cli-system.md")
	appendPath := filepath.Join(root, "cli-append.md")
	mustWriteResource(t, systemPath, "system file")
	mustWriteResource(t, appendPath, "append file")
	mustWriteResource(t, filepath.Join(cwd, ".pi", "SYSTEM.md"), "discovered system")
	mustWriteResource(t, filepath.Join(cwd, ".pi", "APPEND_SYSTEM.md"), "discovered append")

	resources := LoadResources(ResourceOptions{
		CWD:                cwd,
		AgentDir:           agentDir,
		SystemPrompt:       &systemPath,
		AppendSystemPrompt: []string{appendPath, "literal", ""},
	})
	if resources.SystemPrompt == nil || *resources.SystemPrompt != "system file" {
		t.Fatalf("system override = %#v", resources.SystemPrompt)
	}
	if strings.Join(resources.AppendSystemPrompt, "|") != "append file|literal" {
		t.Fatalf("append overrides = %#v", resources.AppendSystemPrompt)
	}
	if joined := resources.JoinedAppendSystemPrompt(); joined == nil || *joined != "append file\n\nliteral" {
		t.Fatalf("joined append = %#v", joined)
	}

	empty := ""
	resources = LoadResources(ResourceOptions{
		CWD:                cwd,
		AgentDir:           agentDir,
		SystemPrompt:       &empty,
		AppendSystemPrompt: []string{},
	})
	if resources.SystemPrompt != nil {
		t.Fatalf("explicit empty system = %#v, want nil", resources.SystemPrompt)
	}
	if resources.AppendSystemPrompt == nil || len(resources.AppendSystemPrompt) != 0 {
		t.Fatalf("explicit empty append = %#v, want non-nil empty", resources.AppendSystemPrompt)
	}
}

func TestResourceFilesUseNodeUTF8Replacement(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	path := filepath.Join(cwd, "AGENTS.md")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{0xff, 0xff, 0xe2, 0x82}, 0o644); err != nil {
		t.Fatal(err)
	}
	files, diagnostics := LoadProjectContextFiles(cwd, agentDir)
	if len(diagnostics) != 0 || len(files) != 1 || files[0].Content != "���" {
		t.Fatalf("files = %#v, diagnostics = %#v", files, diagnostics)
	}
}

func mustWriteResource(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
