package codingagent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/config"
)

type fakeNpmPackage struct {
	name    string
	version string
	files   map[string]string
}

type fakeNpmRegistry struct {
	server   *httptest.Server
	packages map[string][]fakeNpmPackage
	corrupt  bool
}

func newFakeNpmRegistry(t *testing.T) *fakeNpmRegistry {
	t.Helper()
	registry := &fakeNpmRegistry{packages: map[string][]fakeNpmPackage{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", registry.handle)
	registry.server = httptest.NewServer(mux)
	t.Cleanup(registry.server.Close)
	return registry
}

func (registry *fakeNpmRegistry) add(pkg fakeNpmPackage) {
	registry.packages[pkg.name] = append(registry.packages[pkg.name], pkg)
}

func (registry *fakeNpmRegistry) handle(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/")
	if tarballName, found := strings.CutPrefix(path, "tarballs/"); found {
		name, version, _ := strings.Cut(strings.TrimSuffix(tarballName, ".tgz"), "@@")
		for _, pkg := range registry.packages[name] {
			if pkg.version != version {
				continue
			}
			tarball := npmTarballFromFiles(pkg.files)
			if registry.corrupt {
				tarball = append(tarball, 0)
			}
			_, _ = writer.Write(tarball)
			return
		}
		http.NotFound(writer, request)
		return
	}

	name, err := unescapePackageName(path)
	if err != nil || len(registry.packages[name]) == 0 {
		http.NotFound(writer, request)
		return
	}
	versions := map[string]any{}
	latest := ""
	for _, pkg := range registry.packages[name] {
		tarball := npmTarballFromFiles(pkg.files)
		digest := sha512.Sum512(tarball)
		versions[pkg.version] = map[string]any{
			"version": pkg.version,
			"dist": map[string]any{
				"tarball":   registry.server.URL + "/tarballs/" + pkg.name + "@@" + pkg.version + ".tgz",
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(digest[:]),
			},
		}
		latest = pkg.version
	}
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"dist-tags": map[string]string{"latest": latest},
		"versions":  versions,
	})
}

func unescapePackageName(path string) (string, error) {
	return strings.ReplaceAll(path, "%2F", "/"), nil
}

func npmTarballFromFiles(files map[string]string) []byte {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	// Deterministic entry order keeps digests stable per files map.
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

func packageJSONContent(name, version string) string {
	return fmt.Sprintf(`{"name":%q,"version":%q}`, name, version)
}

func TestInstallNpmFromRegistry(t *testing.T) {
	manager, _, agentDir, _ := newTestPackageManager(t)
	registry := newFakeNpmRegistry(t)
	manager.registryBaseURL = registry.server.URL
	registry.add(fakeNpmPackage{name: "@scope/pkg", version: "1.0.0", files: map[string]string{
		"package.json":      packageJSONContent("@scope/pkg", "1.0.0"),
		"prompts/hello.md":  "Hello prompt",
		"skills/a/SKILL.md": "---\nname: a\ndescription: d\n---\nbody",
	}})

	parsed := manager.parseSource("npm:@scope/pkg@1.0.0")
	if err := manager.installNpm(parsed.npm, "user", false); err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(agentDir, "npm")
	installedPath := filepath.Join(installRoot, "node_modules", "@scope/pkg")
	if getInstalledNpmVersion(installedPath) != "1.0.0" {
		t.Fatalf("installed version = %q", getInstalledNpmVersion(installedPath))
	}
	if !pathExists(filepath.Join(installedPath, "prompts", "hello.md")) {
		t.Fatal("extracted file missing")
	}
	// The managed npm root is initialized like upstream's ensureNpmProject.
	if !pathExists(filepath.Join(installRoot, "package.json")) || !pathExists(filepath.Join(installRoot, ".gitignore")) {
		t.Fatal("npm project root not initialized")
	}

	if err := manager.uninstallNpm(parsed.npm, "user"); err != nil {
		t.Fatal(err)
	}
	if pathExists(installedPath) {
		t.Fatal("uninstall should remove the package directory")
	}
}

func TestInstallNpmSelectsRangeAndLatest(t *testing.T) {
	manager, _, agentDir, _ := newTestPackageManager(t)
	registry := newFakeNpmRegistry(t)
	manager.registryBaseURL = registry.server.URL
	for _, version := range []string{"1.0.0", "1.2.0", "2.0.0"} {
		registry.add(fakeNpmPackage{name: "pkg", version: version, files: map[string]string{
			"package.json": packageJSONContent("pkg", version),
		}})
	}

	ranged := manager.parseSource("npm:pkg@^1.0.0")
	if err := manager.installNpm(ranged.npm, "user", false); err != nil {
		t.Fatal(err)
	}
	installedPath := filepath.Join(agentDir, "npm", "node_modules", "pkg")
	if version := getInstalledNpmVersion(installedPath); version != "1.2.0" {
		t.Fatalf("range install picked %q, want 1.2.0", version)
	}

	latest := manager.parseSource("npm:pkg")
	if err := manager.installNpm(latest.npm, "user", false); err != nil {
		t.Fatal(err)
	}
	if version := getInstalledNpmVersion(installedPath); version != "2.0.0" {
		t.Fatalf("latest install picked %q, want 2.0.0", version)
	}
}

func TestInstallNpmRejectsCorruptTarball(t *testing.T) {
	manager, _, agentDir, _ := newTestPackageManager(t)
	registry := newFakeNpmRegistry(t)
	registry.corrupt = true
	manager.registryBaseURL = registry.server.URL
	registry.add(fakeNpmPackage{name: "pkg", version: "1.0.0", files: map[string]string{
		"package.json": packageJSONContent("pkg", "1.0.0"),
	}})

	parsed := manager.parseSource("npm:pkg@1.0.0")
	err := manager.installNpm(parsed.npm, "user", false)
	if err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("err = %v", err)
	}
	if pathExists(filepath.Join(agentDir, "npm", "node_modules", "pkg")) {
		t.Fatal("corrupt tarball must not be installed")
	}
}

func TestExtractNpmTarballRejectsEscapes(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "dest")
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	content := "evil"
	_ = tarWriter.WriteHeader(&tar.Header{Name: "package/../../evil.txt", Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
	_, _ = tarWriter.Write([]byte(content))
	_ = tarWriter.Close()
	_ = gzipWriter.Close()

	if err := extractNpmTarball(buffer.Bytes(), destination); err == nil {
		t.Fatal("expected escape rejection")
	}
	if pathExists(filepath.Join(filepath.Dir(destination), "evil.txt")) {
		t.Fatal("escaped file was written")
	}
}

func TestResolveInstallsMissingNpmPackage(t *testing.T) {
	manager, _, agentDir, settings := newTestPackageManager(t)
	registry := newFakeNpmRegistry(t)
	manager.registryBaseURL = registry.server.URL
	registry.add(fakeNpmPackage{name: "pi-pack", version: "1.0.0", files: map[string]string{
		"package.json":     packageJSONContent("pi-pack", "1.0.0"),
		"prompts/greet.md": "Greet",
	}})
	if err := settings.SetPackages([]config.PackageSource{{Source: "npm:pi-pack"}}); err != nil {
		t.Fatal(err)
	}

	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	installedPath := filepath.Join(agentDir, "npm", "node_modules", "pi-pack")
	assertResource(t, resolved.Prompts, filepath.Join(installedPath, "prompts", "greet.md"), true)

	// Installed and unpinned: resolve must not hit the registry again.
	registry.server.Close()
	if _, err := manager.Resolve(nil); err != nil {
		t.Fatalf("resolve after install: %v", err)
	}
}

func TestUpdateReinstallsWhenVersionDiffers(t *testing.T) {
	manager, _, agentDir, settings := newTestPackageManager(t)
	registry := newFakeNpmRegistry(t)
	manager.registryBaseURL = registry.server.URL
	registry.add(fakeNpmPackage{name: "pkg", version: "1.0.0", files: map[string]string{
		"package.json": packageJSONContent("pkg", "1.0.0"),
	}})
	if err := settings.SetPackages([]config.PackageSource{{Source: "npm:pkg"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(nil); err != nil {
		t.Fatal(err)
	}
	installedPath := filepath.Join(agentDir, "npm", "node_modules", "pkg")
	if version := getInstalledNpmVersion(installedPath); version != "1.0.0" {
		t.Fatalf("version = %q", version)
	}

	registry.add(fakeNpmPackage{name: "pkg", version: "1.1.0", files: map[string]string{
		"package.json": packageJSONContent("pkg", "1.1.0"),
	}})
	updates := manager.CheckForAvailableUpdates()
	if len(updates) != 1 || updates[0].Source != "npm:pkg" || updates[0].Type != "npm" || updates[0].Scope != "user" {
		t.Fatalf("updates = %+v", updates)
	}
	if err := manager.Update(""); err != nil {
		t.Fatal(err)
	}
	if version := getInstalledNpmVersion(installedPath); version != "1.1.0" {
		t.Fatalf("updated version = %q", version)
	}

	// Pinned packages are skipped by updates.
	if err := settings.SetPackages([]config.PackageSource{{Source: "npm:pkg@1.1.0"}}); err != nil {
		t.Fatal(err)
	}
	if updates := manager.CheckForAvailableUpdates(); len(updates) != 0 {
		t.Fatalf("pinned updates = %+v", updates)
	}
}

func TestOfflineModeSkipsUpdates(t *testing.T) {
	manager, _, _, settings := newTestPackageManager(t)
	t.Setenv("PI_OFFLINE", "1")
	if err := settings.SetPackages([]config.PackageSource{{Source: "npm:pkg"}}); err != nil {
		t.Fatal(err)
	}
	if updates := manager.CheckForAvailableUpdates(); updates != nil {
		t.Fatalf("offline updates = %+v", updates)
	}
	if err := manager.Update(""); err != nil {
		t.Fatal(err)
	}
}
