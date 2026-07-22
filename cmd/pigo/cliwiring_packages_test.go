package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	extensionhost "github.com/OrdalieTech/pigo/codingagent/extensions/host"
)

const packageToolExtension = `export default function (pi) {
  pi.registerTool({
    name: "parse_duration",
    label: "Parse duration",
    description: "package tool",
    parameters: { type: "object", properties: {} },
    async execute() { return { content: [{ type: "text", text: "ok" }] }; },
  });
}
`

func writeJSExtension(t *testing.T, dir, source string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "index.ts")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadedToolNames(t *testing.T, registry *extensions.Registry) []string {
	t.Helper()
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{})
	var names []string
	for _, tool := range runner.AllRegisteredTools() {
		names = append(names, tool.Definition.Name)
	}
	return names
}

func requireExtensionHostRuntime(t *testing.T) {
	t.Helper()
	if _, err := extensionhost.DiscoverRuntime(t.Context()); err != nil {
		t.Skip("extension-host CLI test requires Node.js >=22.6 or Bun on PATH")
	}
}

// Finding 2: extensions provided by installed pi packages must load. cmd/pigo now
// forwards resolvedPaths.Extensions into the host's package-path fields; a
// user-scope package extension therefore reaches the loaded tool set.
func TestLoadCompiledExtensionsLoadsPackageProvidedExtensions(t *testing.T) {
	requireExtensionHostRuntime(t)
	t.Cleanup(func() { replaceActiveExtensionHost(nil) })
	cwd := t.TempDir()
	agentDir := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	extPath := writeJSExtension(t, filepath.Join(t.TempDir(), "pkg"), packageToolExtension)
	packages := &codingagent.ResolvedPaths{
		Extensions: []codingagent.ResolvedResource{{
			Path: extPath, Enabled: true,
			Metadata: codingagent.PathMetadata{Source: "npm:pkg", Scope: "user", Origin: "package"},
		}},
	}
	registry, diagnostics := loadCompiledExtensions(cwd, agentDir, CLIArgs{}, settings, packages)
	if registry == nil {
		t.Fatalf("no registry; diagnostics=%v", diagnostics)
	}
	if names := loadedToolNames(t, registry); !containsString(names, "parse_duration") {
		t.Fatalf("package tool not loaded: tools=%v diagnostics=%v", names, diagnostics)
	}
}

func TestLoadCompiledExtensionsUsesExtensionHost(t *testing.T) {
	requireExtensionHostRuntime(t)
	t.Cleanup(func() { replaceActiveExtensionHost(nil) })
	cwd := t.TempDir()
	agentDir := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	extPath := writeJSExtension(t, filepath.Join(t.TempDir(), "pkg"), packageToolExtension)
	packages := &codingagent.ResolvedPaths{
		Extensions: []codingagent.ResolvedResource{{
			Path: extPath, Enabled: true,
			Metadata: codingagent.PathMetadata{Source: "npm:pkg", Scope: "user", Origin: "package"},
		}},
	}
	registry, diagnostics := loadCompiledExtensions(cwd, agentDir, CLIArgs{}, settings, packages)
	if registry == nil || len(diagnostics) != 0 {
		t.Fatalf("registry = %#v, diagnostics = %v", registry, diagnostics)
	}
	if names := loadedToolNames(t, registry); !containsString(names, "parse_duration") {
		t.Fatalf("package tool did not load through host: %v", names)
	}
	extensionHostMu.Lock()
	manager := activeExtensionHost
	extensionHostMu.Unlock()
	if manager == nil {
		t.Fatal("extension host manager was not retained")
	}
}

func TestLoadCompiledExtensionsKeepsNativeExtensionsWithoutJSRuntime(t *testing.T) {
	t.Cleanup(func() { replaceActiveExtensionHost(nil) })
	cwd := t.TempDir()
	agentDir := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	writeJSExtension(t, filepath.Join(agentDir, "extensions", "local"), packageToolExtension)
	t.Setenv("PATH", t.TempDir())
	registry, diagnostics := loadCompiledExtensions(cwd, agentDir, CLIArgs{}, settings, nil)
	if registry == nil {
		t.Fatal("native extension registry was lost without a JavaScript runtime")
	}
	want := "JS extensions require Node.js ≥22.6 or Bun; skills, prompt templates, MCP servers and built-in tools work without it"
	if len(diagnostics) != 1 || diagnostics[0] != want {
		t.Fatalf("diagnostics = %#v, want only %q", diagnostics, want)
	}
}

// Finding 2, trust gate: a project-scope package extension stays invisible until
// the project is trusted (the host gates ProjectResolvedPackagePaths behind
// ProjectTrusted).
func TestLoadCompiledExtensionsHidesProjectPackageExtensionsUntilTrusted(t *testing.T) {
	requireExtensionHostRuntime(t)
	t.Cleanup(func() { replaceActiveExtensionHost(nil) })
	cwd := t.TempDir()
	agentDir := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir), config.WithProjectTrusted(false))
	if err != nil {
		t.Fatal(err)
	}
	extPath := writeJSExtension(t, filepath.Join(t.TempDir(), "proj-pkg"), packageToolExtension)
	packages := &codingagent.ResolvedPaths{
		Extensions: []codingagent.ResolvedResource{{
			Path: extPath, Enabled: true,
			Metadata: codingagent.PathMetadata{Source: "npm:pkg", Scope: "project", Origin: "package"},
		}},
	}
	registry, _ := loadCompiledExtensions(cwd, agentDir, CLIArgs{}, settings, packages)
	if registry != nil && containsString(loadedToolNames(t, registry), "parse_duration") {
		t.Fatal("untrusted project-scope package extension was loaded")
	}

	settings.SetProjectTrusted(true)
	registry, diagnostics := loadCompiledExtensions(cwd, agentDir, CLIArgs{}, settings, packages)
	if registry == nil || !containsString(loadedToolNames(t, registry), "parse_duration") {
		t.Fatalf("trusted project-scope package extension did not load: diagnostics=%v", diagnostics)
	}
}

// Finding 3: isPackageSourceSpec routes npm:/git:/http(s)/ssh specs through the
// package resolver instead of treating them as literal file paths, while plain
// paths continue straight to the extension host.
func TestIsPackageSourceSpecClassification(t *testing.T) {
	for _, spec := range []string{"npm:pi-skillful", "git:github.com/u/r", "github:u/r", "https://x/y.git", "ssh://git@h/r"} {
		if !isPackageSourceSpec(spec) {
			t.Fatalf("%q should be a package source", spec)
		}
	}
	for _, path := range []string{"./local.ts", "/abs/ext.ts", "ext", "../up/index.js"} {
		if isPackageSourceSpec(path) {
			t.Fatalf("%q should be a local path", path)
		}
	}
}

// Finding 3, end-to-end: `-e npm:<pkg>` installs the package to a temporary dir
// and loads its extension, rather than failing on a literal `<cwd>/npm:<pkg>`
// path. A fake registry (via npm_config_registry) keeps the test offline.
func TestExtensionFlagResolvesNpmSourceInsteadOfLiteralPath(t *testing.T) {
	requireExtensionHostRuntime(t)
	t.Cleanup(func() { replaceActiveExtensionHost(nil) })
	registry := newInlineNpmRegistry(t)
	registry.add("pi-fixture-ext", "1.0.0", map[string]string{
		"package.json": `{"name":"pi-fixture-ext","version":"1.0.0","pi":{"extensions":["index.ts"]}}`,
		"index.ts":     packageToolExtension,
	})
	t.Setenv("npm_config_registry", registry.server.URL)

	cwd := t.TempDir()
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("HOME", t.TempDir())
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	reg, diagnostics := loadCompiledExtensions(cwd, agentDir, CLIArgs{Extensions: []string{"npm:pi-fixture-ext"}}, settings, nil)
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, "npm:pi-fixture-ext") && strings.Contains(diagnostic, "resolve") {
			t.Fatalf("npm spec was treated as a literal path: %q", diagnostic)
		}
	}
	if reg == nil || !containsString(loadedToolNames(t, reg), "parse_duration") {
		t.Fatalf("`-e npm:` package tool not loaded: diagnostics=%v", diagnostics)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// inlineNpmRegistry is a compact offline npm registry for cmd/pigo package tests.
type inlineNpmRegistry struct {
	server   *httptest.Server
	packages map[string]map[string]map[string]string
}

func newInlineNpmRegistry(t *testing.T) *inlineNpmRegistry {
	t.Helper()
	registry := &inlineNpmRegistry{packages: map[string]map[string]map[string]string{}}
	registry.server = httptest.NewServer(http.HandlerFunc(registry.handle))
	t.Cleanup(registry.server.Close)
	return registry
}

func (registry *inlineNpmRegistry) add(name, version string, files map[string]string) {
	if registry.packages[name] == nil {
		registry.packages[name] = map[string]map[string]string{}
	}
	registry.packages[name][version] = files
}

func (registry *inlineNpmRegistry) handle(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/")
	if tarballName, found := strings.CutPrefix(path, "tarballs/"); found {
		name, version, _ := strings.Cut(strings.TrimSuffix(tarballName, ".tgz"), "@@")
		files, ok := registry.packages[name][version]
		if !ok {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(inlineNpmTarball(files))
		return
	}
	name := strings.ReplaceAll(path, "%2F", "/")
	versions, ok := registry.packages[name]
	if !ok {
		http.NotFound(writer, request)
		return
	}
	versionsJSON := map[string]any{}
	latest := ""
	for version, files := range versions {
		digest := sha512.Sum512(inlineNpmTarball(files))
		versionsJSON[version] = map[string]any{
			"version": version,
			"dist": map[string]any{
				"tarball":   registry.server.URL + "/tarballs/" + name + "@@" + version + ".tgz",
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(digest[:]),
			},
		}
		latest = version
	}
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"dist-tags": map[string]string{"latest": latest},
		"versions":  versionsJSON,
	})
}

func inlineNpmTarball(files map[string]string) []byte {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		content := files[name]
		header := &tar.Header{Name: "package/" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		_ = tarWriter.WriteHeader(header)
		_, _ = tarWriter.Write([]byte(content))
	}
	_ = tarWriter.Close()
	_ = gzipWriter.Close()
	return buffer.Bytes()
}
