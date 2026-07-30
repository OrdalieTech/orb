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
	mustWriteResource(t, filepath.Join(root, "AGENTS.md"), "root")
	mustWriteResource(t, filepath.Join(project, "CLAUDE.md"), "project")
	mustWriteResource(t, filepath.Join(cwd, "CLAUDE.md"), "lower-priority")
	mustWriteResource(t, filepath.Join(cwd, "AGENTS.md"), "cwd")

	files, diagnostics := LoadProjectContextFiles(cwd, agentDir)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	wantPaths := []string{
		filepath.Join(agentDir, "CLAUDE.md"),
		filepath.Join(root, "AGENTS.md"),
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

func TestLoadProjectContextFilesUppercaseFallback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.MD")
	mustWriteResource(t, path, "upper")
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err == nil {
		t.Skip("case-insensitive filesystem cannot represent the uppercase fallback")
	}
	files, diagnostics := LoadProjectContextFiles(root, filepath.Join(root, "agent"))
	if len(diagnostics) != 0 || len(files) != 1 || files[0].Path != path || files[0].Content != "upper" {
		t.Fatalf("files = %#v, diagnostics = %#v", files, diagnostics)
	}
}

// Upstream 58c0bc2f: a directory named AGENTS.md is skipped silently (no
// unreadable-file diagnostic) and the next candidate is used instead.
func TestLoadProjectContextFilesSkipsDirectoryCandidatesSilently(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(filepath.Join(cwd, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteResource(t, filepath.Join(cwd, "CLAUDE.md"), "fallback")

	files, diagnostics := LoadProjectContextFiles(cwd, agentDir)
	if len(files) != 1 || files[0].Path != filepath.Join(cwd, "CLAUDE.md") || files[0].Content != "fallback" {
		t.Fatalf("files = %#v", files)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestLoadProjectContextFilesDedupesNestedLinkedWorktreeRoot(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	worktreeDir := filepath.Join(mainDir, "worktrees", "feature")
	cwd := filepath.Join(worktreeDir, "src")
	gitDir := filepath.Join(mainDir, ".git", "worktrees", "feature")
	mustWriteResource(t, filepath.Join(mainDir, ".git", "HEAD"), "ref: refs/heads/main\n")
	mustWriteResource(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/feature\n")
	mustWriteResource(t, filepath.Join(gitDir, "commondir"), "../..\n")
	mustWriteResource(t, filepath.Join(worktreeDir, ".git"), "gitdir: "+gitDir+"\n")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteResource(t, filepath.Join(mainDir, "AGENTS.md"), "main")
	mustWriteResource(t, filepath.Join(worktreeDir, "AGENTS.md"), "worktree")

	files, diagnostics := LoadProjectContextFiles(cwd, filepath.Join(root, "agent"))
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if len(files) != 1 || files[0].Content != "worktree" {
		t.Fatalf("files = %#v, want only worktree context", files)
	}
}

func TestLoadProjectContextFilesKeepsNestedWorktreeInheritance(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	worktreeDir := filepath.Join(mainDir, "worktrees", "feature")
	cwd := filepath.Join(worktreeDir, "src")
	gitDir := filepath.Join(mainDir, ".git", "worktrees", "feature")
	mustWriteResource(t, filepath.Join(mainDir, ".git", "HEAD"), "ref: refs/heads/main\n")
	mustWriteResource(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/feature\n")
	mustWriteResource(t, filepath.Join(gitDir, "commondir"), "../..\n")
	mustWriteResource(t, filepath.Join(worktreeDir, ".git"), "gitdir: "+gitDir+"\n")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteResource(t, filepath.Join(mainDir, "AGENTS.md"), "main")

	files, diagnostics := LoadProjectContextFiles(cwd, filepath.Join(root, "agent"))
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if len(files) != 1 || files[0].Content != "main" {
		t.Fatalf("files = %#v, want inherited main context", files)
	}
}

func TestLoadProjectContextFilesDoesNotOverDedupeWorktreeLayouts(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string) (string, []string)
	}{
		{
			name: "different filenames",
			setup: func(t *testing.T, root string) (string, []string) {
				mainDir := filepath.Join(root, "main")
				worktreeDir := filepath.Join(mainDir, "worktrees", "feature")
				cwd := filepath.Join(worktreeDir, "src")
				mustLinkContextWorktree(t, mainDir, worktreeDir, "feature")
				mustWriteResource(t, filepath.Join(mainDir, "CLAUDE.md"), "main")
				mustWriteResource(t, filepath.Join(worktreeDir, "AGENTS.md"), "worktree")
				if err := os.MkdirAll(cwd, 0o755); err != nil {
					t.Fatal(err)
				}
				return cwd, []string{"main", "worktree"}
			},
		},
		{
			name: "ancestor above nested worktree",
			setup: func(t *testing.T, root string) (string, []string) {
				outerDir := filepath.Join(root, "outer")
				mainDir := filepath.Join(outerDir, "main")
				worktreeDir := filepath.Join(mainDir, "worktrees", "feature")
				cwd := filepath.Join(worktreeDir, "src")
				mustLinkContextWorktree(t, mainDir, worktreeDir, "feature")
				mustWriteResource(t, filepath.Join(outerDir, "AGENTS.md"), "outer")
				mustWriteResource(t, filepath.Join(mainDir, "AGENTS.md"), "main")
				mustWriteResource(t, filepath.Join(worktreeDir, "AGENTS.md"), "worktree")
				if err := os.MkdirAll(cwd, 0o755); err != nil {
					t.Fatal(err)
				}
				return cwd, []string{"outer", "worktree"}
			},
		},
		{
			name: "bare repository",
			setup: func(t *testing.T, root string) (string, []string) {
				projectDir := filepath.Join(root, "project")
				bareDir := filepath.Join(projectDir, ".bare")
				worktreeDir := filepath.Join(projectDir, "main")
				gitDir := filepath.Join(bareDir, "worktrees", "main")
				mustWriteResource(t, filepath.Join(bareDir, "HEAD"), "ref: refs/heads/main\n")
				mustWriteResource(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
				mustWriteResource(t, filepath.Join(gitDir, "commondir"), "../..\n")
				mustWriteResource(t, filepath.Join(worktreeDir, ".git"), "gitdir: "+gitDir+"\n")
				mustWriteResource(t, filepath.Join(projectDir, "AGENTS.md"), "container")
				mustWriteResource(t, filepath.Join(worktreeDir, "AGENTS.md"), "worktree")
				return worktreeDir, []string{"container", "worktree"}
			},
		},
		{
			name: "sibling worktree",
			setup: func(t *testing.T, root string) (string, []string) {
				outerDir := filepath.Join(root, "outer")
				mainDir := filepath.Join(outerDir, "main")
				worktreeDir := filepath.Join(outerDir, "feature")
				cwd := filepath.Join(worktreeDir, "src")
				mustLinkContextWorktree(t, mainDir, worktreeDir, "feature")
				mustWriteResource(t, filepath.Join(outerDir, "AGENTS.md"), "outer")
				mustWriteResource(t, filepath.Join(worktreeDir, "AGENTS.md"), "worktree")
				if err := os.MkdirAll(cwd, 0o755); err != nil {
					t.Fatal(err)
				}
				return cwd, []string{"outer", "worktree"}
			},
		},
		{
			name: "submodule",
			setup: func(t *testing.T, root string) (string, []string) {
				superDir := filepath.Join(root, "super")
				submoduleDir := filepath.Join(superDir, "vendor", "lib")
				cwd := filepath.Join(submoduleDir, "src")
				gitDir := filepath.Join(superDir, ".git", "modules", "vendor", "lib")
				mustWriteResource(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
				mustWriteResource(t, filepath.Join(submoduleDir, ".git"), "gitdir: "+gitDir+"\n")
				mustWriteResource(t, filepath.Join(superDir, "AGENTS.md"), "superproject")
				mustWriteResource(t, filepath.Join(submoduleDir, "AGENTS.md"), "submodule")
				if err := os.MkdirAll(cwd, 0o755); err != nil {
					t.Fatal(err)
				}
				return cwd, []string{"superproject", "submodule"}
			},
		},
		{
			name: "ordinary repository",
			setup: func(t *testing.T, root string) (string, []string) {
				outerDir := filepath.Join(root, "outer")
				repoDir := filepath.Join(outerDir, "repo")
				cwd := filepath.Join(repoDir, "src")
				mustWriteResource(t, filepath.Join(repoDir, ".git", "HEAD"), "ref: refs/heads/main\n")
				mustWriteResource(t, filepath.Join(outerDir, "AGENTS.md"), "outer")
				mustWriteResource(t, filepath.Join(repoDir, "AGENTS.md"), "repo")
				mustWriteResource(t, filepath.Join(cwd, "AGENTS.md"), "leaf")
				return cwd, []string{"outer", "repo", "leaf"}
			},
		},
		{
			name: "missing gitdir target",
			setup: func(t *testing.T, root string) (string, []string) {
				repoDir := filepath.Join(root, "repo")
				cwd := filepath.Join(repoDir, "src")
				mustWriteResource(t, filepath.Join(repoDir, ".git"), "gitdir: "+filepath.Join(root, "missing", "gitdir")+"\n")
				mustWriteResource(t, filepath.Join(repoDir, "AGENTS.md"), "repo")
				mustWriteResource(t, filepath.Join(cwd, "AGENTS.md"), "leaf")
				return cwd, []string{"repo", "leaf"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			cwd, want := test.setup(t, root)
			files, diagnostics := LoadProjectContextFiles(cwd, filepath.Join(root, "agent"))
			if len(diagnostics) != 0 {
				t.Fatalf("diagnostics = %#v", diagnostics)
			}
			got := make([]string, 0, len(files))
			for _, file := range files {
				got = append(got, file.Content)
			}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("context = %#v, want %#v", got, want)
			}
		})
	}
}

func TestLoadResourcesDiagnosticsDoNotIncludePresentationPrefix(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
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
	t.Setenv("HOME", filepath.Join(root, "home"))
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
	t.Setenv("HOME", filepath.Join(root, "home"))
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

func mustLinkContextWorktree(t *testing.T, mainDir, worktreeDir, name string) {
	t.Helper()
	gitDir := filepath.Join(mainDir, ".git", "worktrees", name)
	mustWriteResource(t, filepath.Join(mainDir, ".git", "HEAD"), "ref: refs/heads/main\n")
	mustWriteResource(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/feature\n")
	mustWriteResource(t, filepath.Join(gitDir, "commondir"), "../..\n")
	mustWriteResource(t, filepath.Join(worktreeDir, ".git"), "gitdir: "+gitDir+"\n")
}
