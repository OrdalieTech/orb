package agent

import (
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
	// Managed npm installs disable npm's peer auto-install (upstream
	// getNpmInstallArgs): non-pi peers are materialized natively instead.
	if specs[0].name != "npm" || strings.Join(specs[0].args, " ") != "install --omit=dev --legacy-peer-deps" || specs[0].dir != wantDir {
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
