package codingagent

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for the package-manager last mile: npm/git package
// dependencies are installed via the npmCommand setting, and git subprocess
// chatter stays out of the CLI output.

func TestInstallNpmRunsDependencyInstall(t *testing.T) {
	registry := newFakeNpmRegistry(t)
	manager, _, agentDir, _ := newTestPackageManager(t)
	manager.registryBaseURL = registry.server.URL
	registry.add(fakeNpmPackage{name: "pi-deps", version: "1.0.0", files: map[string]string{
		"package.json": `{"name":"pi-deps","version":"1.0.0","dependencies":{"ndjson":"^2.0.0"}}`,
		"index.js":     "module.exports = () => {}",
	}})

	var specs []execSpec
	manager.runCommand = func(spec execSpec) (string, error) {
		specs = append(specs, spec)
		return "", nil
	}
	if err := manager.Install("npm:pi-deps", false); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected one npm invocation, got %d: %+v", len(specs), specs)
	}
	wantDir := filepath.Join(agentDir, "npm", "node_modules", "pi-deps")
	if specs[0].name != "npm" || strings.Join(specs[0].args, " ") != "install --omit=dev" || specs[0].dir != wantDir {
		t.Fatalf("npm invocation = %+v", specs[0])
	}
}

func TestInstallNpmSkipsDependencyInstallWhenBundledOrAbsent(t *testing.T) {
	registry := newFakeNpmRegistry(t)
	manager, _, _, _ := newTestPackageManager(t)
	manager.registryBaseURL = registry.server.URL
	registry.add(fakeNpmPackage{name: "pi-bundled", version: "1.0.0", files: map[string]string{
		"package.json":                     `{"name":"pi-bundled","version":"1.0.0","dependencies":{"ndjson":"^2.0.0"}}`,
		"node_modules/ndjson/package.json": `{"name":"ndjson","version":"2.0.0"}`,
		"node_modules/ndjson/index.js":     "module.exports = null",
	}})
	registry.add(fakeNpmPackage{name: "pi-no-deps", version: "1.0.0", files: map[string]string{
		"package.json": `{"name":"pi-no-deps","version":"1.0.0"}`,
	}})

	manager.runCommand = func(spec execSpec) (string, error) {
		t.Fatalf("unexpected subprocess: %+v", spec)
		return "", nil
	}
	if err := manager.Install("npm:pi-bundled", false); err != nil {
		t.Fatal(err)
	}
	if err := manager.Install("npm:pi-no-deps", false); err != nil {
		t.Fatal(err)
	}
}

func TestInstallNpmHonorsNpmCommandSetting(t *testing.T) {
	registry := newFakeNpmRegistry(t)
	manager, _, agentDir, settings := newTestPackageManager(t)
	manager.registryBaseURL = registry.server.URL
	writeTestFile(t, filepath.Join(agentDir, "settings.json"), `{"npmCommand":["mise","exec","node@20","--","npm"]}`)
	settings.Reload()
	registry.add(fakeNpmPackage{name: "pi-deps", version: "1.0.0", files: map[string]string{
		"package.json": `{"name":"pi-deps","version":"1.0.0","dependencies":{"ndjson":"^2.0.0"}}`,
	}})

	var specs []execSpec
	manager.runCommand = func(spec execSpec) (string, error) {
		specs = append(specs, spec)
		return "", nil
	}
	if err := manager.Install("npm:pi-deps", false); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected one npmCommand invocation, got %d", len(specs))
	}
	// Custom npmCommand values skip the npm-specific --omit=dev flag.
	if specs[0].name != "mise" || strings.Join(specs[0].args, " ") != "exec node@20 -- npm install" {
		t.Fatalf("npmCommand invocation = %+v", specs[0])
	}
}

func TestInstallNpmWarnsWhenNpmMissing(t *testing.T) {
	registry := newFakeNpmRegistry(t)
	manager, _, agentDir, _ := newTestPackageManager(t)
	manager.registryBaseURL = registry.server.URL
	var stderr bytes.Buffer
	manager.stderr = &stderr
	registry.add(fakeNpmPackage{name: "pi-deps", version: "1.0.0", files: map[string]string{
		"package.json": `{"name":"pi-deps","version":"1.0.0","dependencies":{"ndjson":"^2.0.0"}}`,
	}})

	manager.runCommand = func(spec execSpec) (string, error) {
		return "", &exec.Error{Name: spec.name, Err: exec.ErrNotFound}
	}
	if err := manager.Install("npm:pi-deps", false); err != nil {
		t.Fatalf("install should degrade gracefully, got %v", err)
	}
	if !pathExists(filepath.Join(agentDir, "npm", "node_modules", "pi-deps", "package.json")) {
		t.Fatal("package should stay installed when npm is missing")
	}
	if !strings.Contains(stderr.String(), "npm not found") {
		t.Fatalf("expected npm-missing warning, got %q", stderr.String())
	}
}

func TestInstallGitRunsDependencyInstall(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	manager, _, agentDir, _ := newTestPackageManager(t)

	origin := filepath.Join(filepath.Dir(agentDir), "origin-deps-repo")
	writeTestFile(t, filepath.Join(origin, "package.json"), `{"name":"gitpack","version":"1.0.0","dependencies":{"ndjson":"^2.0.0"}}`)
	gitRun(t, origin, "init", "--quiet", "--initial-branch=main")
	gitRun(t, origin, "config", "user.email", "test@example.com")
	gitRun(t, origin, "config", "user.name", "Test")
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "--quiet", "-m", "initial")
	gitRun(t, origin, "tag", "v1")

	realRun := manager.execCommand
	var npmSpecs []execSpec
	manager.runCommand = func(spec execSpec) (string, error) {
		if spec.name == "git" {
			return realRun(spec)
		}
		npmSpecs = append(npmSpecs, spec)
		return "", nil
	}

	source := &GitSource{Repo: origin, Host: "localhost", Path: "user/deps-repo", Ref: "v1", Pinned: true}
	if err := manager.installGit(source, "user"); err != nil {
		t.Fatal(err)
	}
	targetDir, err := manager.getGitInstallPath(source, "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(npmSpecs) != 1 {
		t.Fatalf("expected one npm invocation after clone, got %d", len(npmSpecs))
	}
	if npmSpecs[0].name != "npm" || strings.Join(npmSpecs[0].args, " ") != "install --omit=dev" || npmSpecs[0].dir != targetDir {
		t.Fatalf("npm invocation = %+v", npmSpecs[0])
	}

	// Reconciling to a new ref cleans the checkout and reinstalls deps.
	writeTestFile(t, filepath.Join(origin, "extra.md"), "extra")
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "--quiet", "-m", "second")
	gitRun(t, origin, "tag", "v2")
	updated := &GitSource{Repo: origin, Host: "localhost", Path: "user/deps-repo", Ref: "v2", Pinned: true}
	if err := manager.installGit(updated, "user"); err != nil {
		t.Fatal(err)
	}
	if len(npmSpecs) != 2 {
		t.Fatalf("expected npm reinstall after reconcile, got %d invocations", len(npmSpecs))
	}
	if npmSpecs[1].dir != targetDir {
		t.Fatalf("reconcile npm invocation = %+v", npmSpecs[1])
	}
}

func TestGitInstallAndUpdateAreQuiet(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	manager, _, agentDir, _ := newTestPackageManager(t)
	var output bytes.Buffer
	manager.stdout = &output
	manager.stderr = &output

	origin := filepath.Join(filepath.Dir(agentDir), "origin-quiet-repo")
	writeTestFile(t, filepath.Join(origin, "prompts", "hello.md"), "Hello prompt")
	gitRun(t, origin, "init", "--quiet", "--initial-branch=main")
	gitRun(t, origin, "config", "user.email", "test@example.com")
	gitRun(t, origin, "config", "user.name", "Test")
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "--quiet", "-m", "initial")
	gitRun(t, origin, "tag", "v1")

	// Fresh install of a pinned ref: clone + detached checkout.
	source := &GitSource{Repo: origin, Host: "localhost", Path: "user/quiet-repo", Ref: "v1", Pinned: true}
	if err := manager.installGit(source, "user"); err != nil {
		t.Fatal(err)
	}

	// Reconcile to a new ref: fetch + reset + clean.
	writeTestFile(t, filepath.Join(origin, "prompts", "later.md"), "Later prompt")
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "--quiet", "-m", "second")
	gitRun(t, origin, "tag", "v2")
	updated := &GitSource{Repo: origin, Host: "localhost", Path: "user/quiet-repo", Ref: "v2", Pinned: true}
	if err := manager.installGit(updated, "user"); err != nil {
		t.Fatal(err)
	}

	captured := output.String()
	for _, chatter := range []string{"detached HEAD", "You are in", "Cloning into", "FETCH_HEAD"} {
		if strings.Contains(captured, chatter) {
			t.Fatalf("git chatter %q leaked into output:\n%s", chatter, captured)
		}
	}
}
