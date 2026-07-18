package codingagent

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/config"
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

func TestParseSourceTypes(t *testing.T) {
	manager, _, _, _ := newTestPackageManager(t)
	cases := []struct {
		input   string
		kind    string
		name    string
		version string
		pinned  bool
	}{
		{"npm:@foo/bar@1.0.0", "npm", "@foo/bar", "1.0.0", true},
		{"npm:@foo/bar@^1.0.0", "npm", "@foo/bar", "^1.0.0", false},
		{"npm:pkg", "npm", "pkg", "", false},
		{"git:github.com/user/repo@v1", "git", "", "", true},
		{"https://github.com/user/repo", "git", "", "", false},
		{"/absolute/path/to/package", "local", "", "", false},
		{"./relative/path", "local", "", "", false},
		{"github.com/user/repo", "local", "", "", false},
		{"git@github.com:user/repo", "local", "", "", false},
	}
	for _, c := range cases {
		parsed := manager.parseSource(c.input)
		kind := "local"
		if parsed.npm != nil {
			kind = "npm"
		} else if parsed.git != nil {
			kind = "git"
		}
		if kind != c.kind {
			t.Errorf("parseSource(%q) kind = %s, want %s", c.input, kind, c.kind)
			continue
		}
		if c.kind == "npm" {
			if parsed.npm.name != c.name || parsed.npm.version != c.version || parsed.npm.pinned != c.pinned {
				t.Errorf("parseSource(%q) = %+v", c.input, parsed.npm)
			}
		}
		if c.kind == "git" && parsed.git.Pinned != c.pinned {
			t.Errorf("parseSource(%q) pinned = %v", c.input, parsed.git.Pinned)
		}
	}
}

func TestPackageIdentityNormalizesGitForms(t *testing.T) {
	manager, _, _, _ := newTestPackageManager(t)
	prefixed := manager.getPackageIdentity("git:git@github.com:user/repo", "")
	https := manager.getPackageIdentity("https://github.com/user/repo", "")
	ssh := manager.getPackageIdentity("ssh://git@github.com/user/repo", "")
	if prefixed != "git:github.com/user/repo" || prefixed != https || prefixed != ssh {
		t.Fatalf("identities = %q, %q, %q", prefixed, https, ssh)
	}
}

func TestAddSourceToSettingsNormalizesLocalPaths(t *testing.T) {
	manager, cwd, agentDir, settings := newTestPackageManager(t)
	pkgDir := filepath.Join(cwd, "packages", "local-package")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	changed, err := manager.AddSourceToSettings(pkgDir, false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	packages := settings.GetGlobalPackages()
	if len(packages) != 1 {
		t.Fatalf("packages = %v", packages)
	}
	resolved := filepath.Clean(filepath.Join(agentDir, packages[0].Source))
	if resolved != pkgDir {
		t.Fatalf("stored source %q resolves to %q, want %q", packages[0].Source, resolved, pkgDir)
	}

	// Re-adding the same path in an equivalent form is a no-op.
	changed, err = manager.AddSourceToSettings(pkgDir+string(filepath.Separator), false)
	if err != nil || changed {
		t.Fatalf("second add changed=%v err=%v", changed, err)
	}

	removed, err := manager.RemoveSourceFromSettings(pkgDir+string(filepath.Separator), false)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if packages := settings.GetGlobalPackages(); len(packages) != 0 {
		t.Fatalf("packages after remove = %v", packages)
	}
}

func TestAddSourceToSettingsUpdatesRefPreservingFilters(t *testing.T) {
	manager, _, _, settings := newTestPackageManager(t)
	if err := settings.SetPackages([]config.PackageSource{{
		Source: "git:github.com/user/repo@v1", IsObject: true, Skills: []string{"skills/one.md"},
	}}); err != nil {
		t.Fatal(err)
	}

	changed, err := manager.AddSourceToSettings("git:github.com/user/repo@v2", false)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	packages := settings.GetGlobalPackages()
	if len(packages) != 1 || packages[0].Source != "git:github.com/user/repo@v2" {
		t.Fatalf("packages = %+v", packages)
	}
	if len(packages[0].Skills) != 1 || packages[0].Skills[0] != "skills/one.md" {
		t.Fatalf("filters lost: %+v", packages[0])
	}

	// Same source and ref: unchanged.
	changed, err = manager.AddSourceToSettings("git:github.com/user/repo@v2", false)
	if err != nil || changed {
		t.Fatalf("noop add changed=%v err=%v", changed, err)
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

func TestTemporaryInstallPathsLiveUnderAgentTempFolder(t *testing.T) {
	manager, _, agentDir, _ := newTestPackageManager(t)
	source := &npmSource{spec: "pkg", name: "pkg"}
	path, err := manager.getManagedNpmInstallPath(source, "temporary")
	if err != nil {
		t.Fatal(err)
	}
	tempRoot := filepath.Join(agentDir, "tmp", "extensions")
	if !strings.HasPrefix(path, tempRoot+string(filepath.Separator)) {
		t.Fatalf("temporary npm path %q not under %q", path, tempRoot)
	}
	if !strings.HasSuffix(path, filepath.Join("node_modules", "pkg")) {
		t.Fatalf("temporary npm path = %q", path)
	}
	info, err := os.Stat(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("temp folder mode = %v", info.Mode().Perm())
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

func TestResolveTopLevelOverridePatterns(t *testing.T) {
	manager, _, agentDir, settings := newTestPackageManager(t)
	writeTestFile(t, filepath.Join(agentDir, "prompts", "auto.md"), "Auto prompt")
	writeTestFile(t, filepath.Join(agentDir, "prompts", "keep.md"), "Keep prompt")
	writeGlobalSettingsFile(t, agentDir, `{"prompts":["!prompts/auto.md"]}`)
	settings.Reload()

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResource(t, resolved.Prompts, filepath.Join(agentDir, "prompts", "auto.md"), false)
	assertResource(t, resolved.Prompts, filepath.Join(agentDir, "prompts", "keep.md"), true)
}

func writeGlobalSettingsFile(t *testing.T, agentDir, contents string) {
	t.Helper()
	writeTestFile(t, filepath.Join(agentDir, "settings.json"), contents)
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

func TestResolveLocalPackageWithManifest(t *testing.T) {
	manager, cwd, _, settings := newTestPackageManager(t)
	pkgDir := filepath.Join(cwd, "my-pkg")
	writeTestFile(t, filepath.Join(pkgDir, "package.json"),
		`{"name":"my-pkg","pi":{"extensions":["./extensions/clip.ts","./extensions/cost.ts"]}}`)
	writeTestFile(t, filepath.Join(pkgDir, "extensions", "clip.ts"), "export default 1")
	writeTestFile(t, filepath.Join(pkgDir, "extensions", "cost.ts"), "export default 1")
	writeTestFile(t, filepath.Join(pkgDir, "extensions", "helper.ts"), "export const x = 1")
	if err := settings.SetPackages([]config.PackageSource{{Source: pkgDir}}); err != nil {
		t.Fatal(err)
	}

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResource(t, resolved.Extensions, filepath.Join(pkgDir, "extensions", "clip.ts"), true)
	assertResource(t, resolved.Extensions, filepath.Join(pkgDir, "extensions", "cost.ts"), true)
	for _, resource := range resolved.Extensions {
		if strings.HasSuffix(resource.Path, "helper.ts") {
			t.Fatalf("helper.ts should not resolve: %+v", resource)
		}
	}
	if resolved.Extensions[0].Metadata.Origin != "package" || resolved.Extensions[0].Metadata.BaseDir != pkgDir {
		t.Fatalf("metadata = %+v", resolved.Extensions[0].Metadata)
	}
}

func TestResolvePackageFilterPatterns(t *testing.T) {
	manager, cwd, _, settings := newTestPackageManager(t)
	pkgDir := filepath.Join(cwd, "filtered-pkg")
	writeTestFile(t, filepath.Join(pkgDir, "extensions", "keep.ts"), "1")
	writeTestFile(t, filepath.Join(pkgDir, "extensions", "legacy.ts"), "1")
	writeTestFile(t, filepath.Join(pkgDir, "themes", "dark.json"), "{}")
	if err := settings.SetPackages([]config.PackageSource{{
		Source:     pkgDir,
		IsObject:   true,
		Extensions: []string{"extensions/*.ts", "!extensions/legacy.ts"},
		Themes:     []string{},
	}}); err != nil {
		t.Fatal(err)
	}

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResource(t, resolved.Extensions, filepath.Join(pkgDir, "extensions", "keep.ts"), true)
	assertResource(t, resolved.Extensions, filepath.Join(pkgDir, "extensions", "legacy.ts"), false)
	// Empty array disables all resources of that type.
	assertResource(t, resolved.Themes, filepath.Join(pkgDir, "themes", "dark.json"), false)
}

func TestResolveForceIncludeOverridesExclude(t *testing.T) {
	manager, cwd, _, settings := newTestPackageManager(t)
	pkgDir := filepath.Join(cwd, "force-pkg")
	writeTestFile(t, filepath.Join(pkgDir, "extensions", "one.ts"), "1")
	writeTestFile(t, filepath.Join(pkgDir, "extensions", "two.ts"), "1")
	if err := settings.SetPackages([]config.PackageSource{{
		Source:     pkgDir,
		IsObject:   true,
		Extensions: []string{"!extensions/*.ts", "+extensions/one.ts"},
	}}); err != nil {
		t.Fatal(err)
	}

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResource(t, resolved.Extensions, filepath.Join(pkgDir, "extensions", "one.ts"), true)
	assertResource(t, resolved.Extensions, filepath.Join(pkgDir, "extensions", "two.ts"), false)
}

func TestResolveDedupesProjectAndGlobalPackages(t *testing.T) {
	manager, cwd, _, settings := newTestPackageManager(t)
	pkgDir := filepath.Join(cwd, "shared-pkg")
	writeTestFile(t, filepath.Join(pkgDir, "extensions", "ext.ts"), "1")
	if err := settings.SetPackages([]config.PackageSource{{Source: pkgDir}}); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetProjectPackages([]config.PackageSource{{Source: pkgDir}}); err != nil {
		t.Fatal(err)
	}

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	var winner ResolvedResource
	for _, resource := range resolved.Extensions {
		if resource.Path == filepath.Join(pkgDir, "extensions", "ext.ts") {
			count++
			winner = resource
		}
	}
	if count != 1 || winner.Metadata.Scope != "project" {
		t.Fatalf("count=%d winner=%+v", count, winner)
	}
}

func TestResolveAutoloadDisabledDeltaOverGlobal(t *testing.T) {
	manager, cwd, _, settings := newTestPackageManager(t)
	pkgDir := filepath.Join(cwd, "delta-pkg")
	writeTestFile(t, filepath.Join(pkgDir, "extensions", "bar.ts"), "1")
	writeTestFile(t, filepath.Join(pkgDir, "extensions", "baz.ts"), "1")
	if err := settings.SetPackages([]config.PackageSource{{Source: pkgDir}}); err != nil {
		t.Fatal(err)
	}
	autoload := false
	if err := settings.SetProjectPackages([]config.PackageSource{{
		Source: pkgDir, IsObject: true, Autoload: &autoload, Extensions: []string{"-extensions/bar.ts"},
	}}); err != nil {
		t.Fatal(err)
	}

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResource(t, resolved.Extensions, filepath.Join(pkgDir, "extensions", "bar.ts"), false)
	assertResource(t, resolved.Extensions, filepath.Join(pkgDir, "extensions", "baz.ts"), true)
}

func TestResolveAutoDiscoveryTrustGating(t *testing.T) {
	manager, cwd, agentDir, settings := newTestPackageManager(t)
	writeTestFile(t, filepath.Join(cwd, ".pi", "prompts", "proj.md"), "p")
	writeTestFile(t, filepath.Join(cwd, ".pi", "themes", "proj.json"), "{}")
	writeTestFile(t, filepath.Join(agentDir, "prompts", "user.md"), "u")
	writeTestFile(t, filepath.Join(agentDir, "skills", "helper", "SKILL.md"), "---\nname: helper\ndescription: d\n---\nbody")

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResource(t, resolved.Prompts, filepath.Join(cwd, ".pi", "prompts", "proj.md"), true)
	assertResource(t, resolved.Themes, filepath.Join(cwd, ".pi", "themes", "proj.json"), true)
	assertResource(t, resolved.Prompts, filepath.Join(agentDir, "prompts", "user.md"), true)
	assertResource(t, resolved.Skills, filepath.Join(agentDir, "skills", "helper", "SKILL.md"), true)

	settings.SetProjectTrusted(false)
	resolved, err = manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range resolved.Prompts {
		if strings.Contains(resource.Path, ".pi") {
			t.Fatalf("untrusted project prompt resolved: %+v", resource)
		}
	}
	assertResource(t, resolved.Prompts, filepath.Join(agentDir, "prompts", "user.md"), true)
}

func TestResolveSymlinkedResourcesOnce(t *testing.T) {
	manager, cwd, agentDir, _ := newTestPackageManager(t)
	shared := filepath.Join(filepath.Dir(cwd), "shared-resources")
	writeTestFile(t, filepath.Join(shared, "prompts", "shared.md"), "Shared prompt")
	if err := os.MkdirAll(filepath.Join(cwd, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(shared, "prompts"), filepath.Join(agentDir, "prompts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(shared, "prompts"), filepath.Join(cwd, ".pi", "prompts")); err != nil {
		t.Fatal(err)
	}

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Prompts) != 1 {
		t.Fatalf("prompts = %+v", resolved.Prompts)
	}
	// Project auto-discovery outranks user auto-discovery for the same file.
	if resolved.Prompts[0].Metadata.Scope != "project" {
		t.Fatalf("winner scope = %+v", resolved.Prompts[0].Metadata)
	}
}

func TestResolveSkipsMissingPackagesWhenOffline(t *testing.T) {
	manager, _, _, settings := newTestPackageManager(t)
	t.Setenv("PI_OFFLINE", "1")
	if err := settings.SetPackages([]config.PackageSource{{Source: "npm:not-installed-pkg"}}); err != nil {
		t.Fatal(err)
	}
	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Extensions) != 0 {
		t.Fatalf("extensions = %+v", resolved.Extensions)
	}
}

func TestResolveMissingSourceActions(t *testing.T) {
	manager, _, _, settings := newTestPackageManager(t)
	if err := settings.SetPackages([]config.PackageSource{{Source: "npm:not-installed-pkg"}}); err != nil {
		t.Fatal(err)
	}
	resolved, err := manager.Resolve(func(string) (MissingSourceAction, error) { return MissingSourceSkip, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Extensions) != 0 {
		t.Fatalf("extensions = %+v", resolved.Extensions)
	}
	if _, err := manager.Resolve(func(string) (MissingSourceAction, error) { return MissingSourceError, nil }); err == nil ||
		!strings.Contains(err.Error(), "Missing source") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateSuggestsConfiguredSource(t *testing.T) {
	manager, _, _, settings := newTestPackageManager(t)
	if err := settings.SetPackages([]config.PackageSource{{Source: "npm:pi-formatter"}}); err != nil {
		t.Fatal(err)
	}
	err := manager.Update("pi-formatter")
	if err == nil || !strings.Contains(err.Error(), "Did you mean npm:pi-formatter?") {
		t.Fatalf("err = %v", err)
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

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	manager := &PackageManager{stdout: io.Discard, stderr: io.Discard}
	manager.runCommand = manager.execCommand
	if _, err := manager.runCommand(execSpec{name: "git", args: args, dir: dir}); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}
