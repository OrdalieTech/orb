package agent

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
)

func newTestPackageManager(t *testing.T) (*PackageManager, string, string, *config.SettingsManager) {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("HOME", filepath.Join(tempDir, "home"))
	agentDir := filepath.Join(tempDir, "agent")
	cwd := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager := NewPackageManager(PackageManagerOptions{CWD: cwd, AgentDir: agentDir, Settings: settings})
	manager.stdout = io.Discard
	manager.stderr = io.Discard
	return manager, cwd, agentDir, settings
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitInstallPathsRejectEscapes(t *testing.T) {
	manager, _, agentDir, _ := newTestPackageManager(t)
	_, err := manager.getGitInstallPath(&GitSource{Repo: "x", Host: "..", Path: "user/repo"}, "user")
	if err == nil {
		t.Fatal("expected escape rejection")
	}
	path, err := manager.getGitInstallPath(&GitSource{Repo: "x", Host: "github.com", Path: "user/repo"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(agentDir, "git", "github.com", "user", "repo") {
		t.Fatalf("git install path = %q", path)
	}
}

func TestInstallGitCleansPartialNewCheckoutOnFailure(t *testing.T) {
	manager, _, agentDir, _ := newTestPackageManager(t)
	source := &GitSource{Repo: "https://example.test/owner/repo.git", Host: "example.test", Path: "owner/repo"}
	target, err := manager.getGitInstallPath(source, "user")
	if err != nil {
		t.Fatal(err)
	}
	manager.runCommand = func(spec execSpec) (string, error) {
		if spec.name == "git" {
			if err := os.MkdirAll(filepath.Join(target, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			return "", errors.New("clone failed")
		}
		return "", nil
	}

	if err := manager.installGit(source, "user"); err == nil || !strings.Contains(err.Error(), "clone failed") {
		t.Fatalf("install error = %v", err)
	}
	if pathExists(target) {
		t.Fatalf("partial checkout still exists at %q", target)
	}
	if pathExists(filepath.Join(agentDir, "git", "example.test", "owner")) {
		t.Fatal("empty install parents were not pruned")
	}
}

func TestInstallGitCleansNewCheckoutWhenDependencyInstallFails(t *testing.T) {
	manager, _, agentDir, _ := newTestPackageManager(t)
	source := &GitSource{Repo: "https://example.test/owner/repo.git", Host: "example.test", Path: "owner/repo"}
	target, err := manager.getGitInstallPath(source, "user")
	if err != nil {
		t.Fatal(err)
	}
	manager.runCommand = func(spec execSpec) (string, error) {
		if spec.name == "git" {
			writeTestFile(t, filepath.Join(target, "package.json"), `{"dependencies":{"dep":"1.0.0"}}`)
			return "", nil
		}
		return "", errors.New("dependency install failed")
	}

	if err := manager.installGit(source, "user"); err == nil || !strings.Contains(err.Error(), "dependency install failed") {
		t.Fatalf("install error = %v", err)
	}
	if pathExists(target) {
		t.Fatalf("checkout still exists at %q", target)
	}
	if pathExists(filepath.Join(agentDir, "git", "example.test", "owner")) {
		t.Fatal("empty install parents were not pruned")
	}
}

func TestProjectScopeRequiresTrust(t *testing.T) {
	manager, _, _, settings := newTestPackageManager(t)
	settings.SetProjectTrusted(false)
	if err := manager.Install("./missing-package", true); err == nil ||
		!strings.Contains(err.Error(), "Project is not trusted") {
		t.Fatalf("err = %v", err)
	}
	if _, err := manager.getNpmInstallRoot("project", false); err == nil {
		t.Fatal("project npm root should require trust")
	}
}

func assertResource(t *testing.T, resources []ResolvedResource, path string, enabled bool) {
	t.Helper()
	for _, resource := range resources {
		if resource.Path == path {
			if resource.Enabled != enabled {
				t.Fatalf("resource %q enabled = %v, want %v", path, resource.Enabled, enabled)
			}
			return
		}
	}
	t.Fatalf("resource %q not resolved (have %+v)", path, resources)
}

func TestResolveAutoDiscoversExternalAgentSkills(t *testing.T) {
	manager, cwd, _, settings := newTestPackageManager(t)
	home := os.Getenv("HOME")
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	projectSkill := filepath.Join(cwd, ".claude", "skills", "project", "SKILL.md")
	userSkill := filepath.Join(os.Getenv("CODEX_HOME"), "skills", "user", "SKILL.md")
	writeTestFile(t, projectSkill, "---\nname: project\ndescription: project skill\n---\nbody")
	writeTestFile(t, userSkill, "---\nname: user\ndescription: user skill\n---\nbody")

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResource(t, resolved.Skills, projectSkill, true)
	assertResource(t, resolved.Skills, userSkill, true)
	for _, resource := range resolved.Skills {
		switch resource.Path {
		case projectSkill:
			if resource.Metadata.Scope != "project" {
				t.Fatalf("project metadata = %+v", resource.Metadata)
			}
		case userSkill:
			if resource.Metadata.Scope != "user" {
				t.Fatalf("user metadata = %+v", resource.Metadata)
			}
		}
	}

	settings.SetProjectTrusted(false)
	resolved, err = manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range resolved.Skills {
		if resource.Path == projectSkill {
			t.Fatalf("untrusted external project skill resolved: %+v", resource)
		}
	}
	assertResource(t, resolved.Skills, userSkill, true)
}

func TestResolveSkipsClaudeSyncedMirror(t *testing.T) {
	manager, _, _, _ := newTestPackageManager(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	claudeSkills := filepath.Join(os.Getenv("HOME"), ".claude", "skills")
	userSkill := filepath.Join(claudeSkills, "mine", "SKILL.md")
	syncedSkill := filepath.Join(claudeSkills, "synced", "org_account", "docx", "SKILL.md")
	writeTestFile(t, userSkill, "---\nname: mine\ndescription: user skill\n---\nbody")
	writeTestFile(t, syncedSkill, "---\nname: docx\ndescription: synced skill\n---\nbody")

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResource(t, resolved.Skills, userSkill, true)
	for _, resource := range resolved.Skills {
		if resource.Path == syncedSkill {
			t.Fatalf("Claude Code's synced mirror resolved: %+v", resource)
		}
	}
}

func TestInstallAndResolveGitPackage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	manager, _, agentDir, settings := newTestPackageManager(t)

	// Build a local origin repository with a prompt resource and a tag.
	origin := filepath.Join(filepath.Dir(agentDir), "origin-repo")
	writeTestFile(t, filepath.Join(origin, "prompts", "hello.md"), "Hello prompt")
	gitRun(t, origin, "init", "--quiet", "--initial-branch=main")
	gitRun(t, origin, "config", "user.email", "test@example.com")
	gitRun(t, origin, "config", "user.name", "Test")
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "--quiet", "-m", "initial")
	gitRun(t, origin, "tag", "v1")
	writeTestFile(t, filepath.Join(origin, "prompts", "later.md"), "Later prompt")
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "--quiet", "-m", "second")

	source := &GitSource{Repo: origin, Host: "localhost", Path: "user/repo", Ref: "v1", Pinned: true}
	if err := manager.installGit(source, "user"); err != nil {
		t.Fatal(err)
	}
	installedPath, err := manager.getGitInstallPath(source, "user")
	if err != nil {
		t.Fatal(err)
	}
	if !pathExists(filepath.Join(installedPath, "prompts", "hello.md")) {
		t.Fatal("cloned prompt missing")
	}
	if pathExists(filepath.Join(installedPath, "prompts", "later.md")) {
		t.Fatal("pinned checkout should not include later commit")
	}
	if !pathExists(filepath.Join(agentDir, "git", ".gitignore")) {
		t.Fatal("git install root should carry a .gitignore")
	}

	// Reconciling to a new pinned ref moves the checkout.
	gitRun(t, origin, "tag", "v2")
	updated := &GitSource{Repo: origin, Host: "localhost", Path: "user/repo", Ref: "v2", Pinned: true}
	if err := manager.installGit(updated, "user"); err != nil {
		t.Fatal(err)
	}
	if !pathExists(filepath.Join(installedPath, "prompts", "later.md")) {
		t.Fatal("reconciled checkout should include later commit")
	}

	// The clone contributes resources through resolve().
	if err := settings.SetPackages([]config.PackageSource{{Source: "git:localhost/user/repo@v2"}}); err != nil {
		t.Fatal(err)
	}
	resolved, err := manager.Resolve(func(string) (MissingSourceAction, error) { return MissingSourceSkip, nil })
	if err != nil {
		t.Fatal(err)
	}
	assertResource(t, resolved.Prompts, filepath.Join(installedPath, "prompts", "hello.md"), true)

	// Removal deletes the clone and prunes empty parents.
	if err := manager.removeGit(updated, "user"); err != nil {
		t.Fatal(err)
	}
	if pathExists(installedPath) || pathExists(filepath.Join(agentDir, "git", "localhost")) {
		t.Fatal("remove should prune empty parents")
	}
}

func TestUpdateGitPinnedShortCommitUsesInstalledObject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	manager, _, agentDir, _ := newTestPackageManager(t)

	origin := filepath.Join(filepath.Dir(agentDir), "origin-short-commit")
	writeTestFile(t, filepath.Join(origin, "prompts", "first.md"), "First prompt")
	gitRun(t, origin, "init", "--quiet", "--initial-branch=main")
	gitRun(t, origin, "config", "user.email", "test@example.com")
	gitRun(t, origin, "config", "user.name", "Test")
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "--quiet", "-m", "first")
	shortOutput, err := exec.Command("git", "-C", origin, "rev-parse", "--short=7", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	shortCommit := strings.TrimSpace(string(shortOutput))

	writeTestFile(t, filepath.Join(origin, "prompts", "later.md"), "Later prompt")
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "--quiet", "-m", "second")

	source := &GitSource{Repo: origin, Host: "localhost", Path: "user/short-commit", Ref: shortCommit, Pinned: true}
	if err := manager.installGit(source, "user"); err != nil {
		t.Fatal(err)
	}
	if err := manager.updateGit(source, "user"); err != nil {
		t.Fatalf("update pinned short commit: %v", err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	manager := &PackageManager{stdout: io.Discard, stderr: io.Discard}
	manager.runCommand = manager.execCommand
	if _, err := manager.runCommand(execSpec{name: "git", args: args, dir: dir}); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}
