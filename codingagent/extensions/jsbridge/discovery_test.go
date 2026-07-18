package jsbridge

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	conformance "github.com/OrdalieTech/pi-go/conformance/runner"
)

func TestDiscoveryFixtureMatchesUpstream(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	configuredDir := filepath.Join(root, "configured")
	missing := filepath.Join(root, "missing.ts")
	extension := "export default function () {}\n"
	mustWrite(t, filepath.Join(cwd, ".pi", "extensions", "a.ts"), extension)
	mustWrite(t, filepath.Join(cwd, ".pi", "extensions", "bundle", "index.ts"), extension)
	mustWrite(t, filepath.Join(agentDir, "extensions", "global.js"), extension)
	mustWrite(t, filepath.Join(configuredDir, "src", "configured.ts"), extension)
	mustWrite(t, filepath.Join(configuredDir, "package.json"), `{"pi":{"extensions":["src/configured.ts","missing.ts"]}}`)

	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: agentDir, ProjectTrusted: true, ConfiguredPaths: []string{configuredDir, missing},
	})
	t.Cleanup(loader.Close)
	loaded := loader.Load(t.Context())
	gotPaths := make([]string, 0, len(loaded.Registry.Extensions()))
	for _, value := range loaded.Registry.Extensions() {
		gotPaths = append(gotPaths, normalizeFixtureRoot(value.ResolvedPath, root))
	}
	gotErrors := make([]struct {
		Path   string `json:"path"`
		Prefix string `json:"prefix"`
	}, 0, len(loaded.Errors))
	for _, value := range loaded.Errors {
		gotErrors = append(gotErrors, struct {
			Path   string `json:"path"`
			Prefix string `json:"prefix"`
		}{normalizeFixtureRoot(value.Path, root), strings.Split(value.Error, ":")[0]})
	}
	var want struct {
		Paths  []string `json:"paths"`
		Errors []struct {
			Path   string `json:"path"`
			Prefix string `json:"prefix"`
		} `json:"errors"`
	}
	conformance.LoadJSON(t, "F11-jsbridge", "discovery.json", &want)
	if !reflect.DeepEqual(gotPaths, want.Paths) || !reflect.DeepEqual(gotErrors, want.Errors) {
		t.Fatalf("discovery fixture\n got paths: %#v\nwant paths: %#v\n got errors: %#v\nwant errors: %#v", gotPaths, want.Paths, gotErrors, want.Errors)
	}
}

func TestDiscoverMatchesUpstreamOrderAndDirectoryRules(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	projectDir := filepath.Join(cwd, ".pi", "extensions")
	globalDir := filepath.Join(agentDir, "extensions")
	configuredDir := filepath.Join(root, "configured")
	packageDir := filepath.Join(root, "package")
	explicit := filepath.Join(root, "explicit.ts")

	mustWrite(t, filepath.Join(projectDir, "a.ts"), "export default () => {}")
	mustWrite(t, filepath.Join(projectDir, "b.js"), "module.exports = () => {}")
	mustWrite(t, filepath.Join(projectDir, "bundle", "index.ts"), "export default () => {}")
	mustWrite(t, filepath.Join(projectDir, "bundle", "index.js"), "module.exports = () => {}")
	mustWrite(t, filepath.Join(projectDir, "deep", "nested", "index.ts"), "export default () => {}")
	mustWrite(t, filepath.Join(globalDir, "global.ts"), "export default () => {}")
	mustWrite(t, filepath.Join(configuredDir, "configured.js"), "module.exports = () => {}")
	mustWrite(t, filepath.Join(packageDir, "src", "first.ts"), "export default () => {}")
	mustWrite(t, filepath.Join(packageDir, "second.js"), "module.exports = () => {}")
	mustWrite(t, filepath.Join(packageDir, "index.ts"), "export default () => {}")
	mustWrite(t, filepath.Join(packageDir, "package.json"), `{"pi":{"extensions":["src/first.ts","missing.ts","second.js"]}}`)
	mustWrite(t, explicit, "export default () => {}")

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
	mustWrite(t, project, "export default () => {}")
	mustWrite(t, projectConfigured, "export default () => {}")
	mustWrite(t, global, "export default () => {}")

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
	mustWrite(t, filepath.Join(target, "index.ts"), "export default () => {}")
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

func normalizeFixtureRoot(value, root string) string {
	return strings.ReplaceAll(filepath.ToSlash(value), filepath.ToSlash(root), "<root>")
}
