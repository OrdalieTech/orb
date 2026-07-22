package host

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestDiscoverMatchesUpstreamOrderAndDirectoryRules(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	projectDir := filepath.Join(cwd, ".pi", "extensions")
	globalDir := filepath.Join(agentDir, "extensions")
	configuredDir := filepath.Join(root, "configured")
	packageDir := filepath.Join(root, "package")
	explicit := filepath.Join(root, "explicit.ts")

	writeFile(t, filepath.Join(projectDir, "a.ts"), "export default () => {}", 0o644)
	writeFile(t, filepath.Join(projectDir, "b.js"), "module.exports = () => {}", 0o644)
	writeFile(t, filepath.Join(projectDir, "bundle", "index.ts"), "export default () => {}", 0o644)
	writeFile(t, filepath.Join(projectDir, "bundle", "index.js"), "module.exports = () => {}", 0o644)
	writeFile(t, filepath.Join(projectDir, "deep", "nested", "index.ts"), "export default () => {}", 0o644)
	writeFile(t, filepath.Join(globalDir, "global.ts"), "export default () => {}", 0o644)
	writeFile(t, filepath.Join(configuredDir, "configured.js"), "module.exports = () => {}", 0o644)
	writeFile(t, filepath.Join(packageDir, "src", "first.ts"), "export default () => {}", 0o644)
	writeFile(t, filepath.Join(packageDir, "second.js"), "module.exports = () => {}", 0o644)
	writeFile(t, filepath.Join(packageDir, "index.ts"), "export default () => {}", 0o644)
	writeFile(t, filepath.Join(packageDir, "package.json"), `{"pi":{"extensions":["src/first.ts","missing.ts","second.js"]}}`, 0o644)
	writeFile(t, explicit, "export default () => {}", 0o644)

	got := Discover(DiscoveryOptions{
		CWD:                  cwd,
		AgentDir:             agentDir,
		ProjectTrusted:       true,
		ConfiguredPaths:      []string{configuredDir},
		ResolvedPackagePaths: []string{packageDir},
		ExplicitPaths:        []string{explicit, filepath.Join(projectDir, "a.ts")},
	})
	want := []string{
		filepath.Join(projectDir, "a.ts"),
		filepath.Join(projectDir, "b.js"),
		filepath.Join(projectDir, "bundle", "index.ts"),
		filepath.Join(globalDir, "global.ts"),
		filepath.Join(configuredDir, "configured.js"),
		filepath.Join(packageDir, "src", "first.ts"),
		filepath.Join(packageDir, "second.js"),
		explicit,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discovered paths\n got: %#v\nwant: %#v", got, want)
	}
}

func TestDiscoverTrustAndMissingExplicitPath(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	project := filepath.Join(cwd, ".pi", "extensions", "project.ts")
	projectConfigured := filepath.Join(cwd, "project-configured.ts")
	global := filepath.Join(agentDir, "extensions", "global.ts")
	missing := filepath.Join(root, "missing.ts")
	writeFile(t, project, "export default () => {}", 0o644)
	writeFile(t, projectConfigured, "export default () => {}", 0o644)
	writeFile(t, global, "export default () => {}", 0o644)

	got := Discover(DiscoveryOptions{
		CWD: cwd, AgentDir: agentDir, ProjectTrusted: false,
		ProjectConfiguredPaths: []string{projectConfigured}, ExplicitPaths: []string{missing},
	})
	want := []string{global, missing}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("untrusted discovery = %#v, want %#v", got, want)
	}
}

func TestDiscoverFollowsDirectorySymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available")
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	target := filepath.Join(root, "target")
	writeFile(t, filepath.Join(target, "index.ts"), "export default () => {}", 0o644)
	link := filepath.Join(cwd, ".pi", "extensions", "linked")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got := Discover(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(root, "agent"), ProjectTrusted: true})
	want := []string{filepath.Join(link, "index.ts")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symlink discovery = %#v, want %#v", got, want)
	}
}
