// TestF13UserShapedInstall exercises the release path the goldens harness
// deliberately does not: `orb install npm:@quintinshaw/pi-dynamic-workflows@3.5.1`
// through the real PackageManager (native registry client), then the real
// extension host loading the extension entries Resolve reports — the exact
// user-shaped flow. The installer must materialize the plugin's non-pi peer
// dependencies natively (typebox — upstream pi serves it in-process, orb's
// out-of-process host does not) and must never fetch @earendil-works/pi-* or
// @mariozechner/pi-* packages, which the embedded orb-extension-sdk serves.
//
// The goldens harness (f13_orb_harness_test.go) keeps its integrity-pinned
// npm ci install instead of this path on purpose: the plugin's typebox peer
// range is "*", so a product install floats with the registry while goldens
// require the pinned 1.3.7 the extractor ran against.
package runner

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	extensionhost "github.com/OrdalieTech/orb/agent/extensions/host"
)

func TestF13UserShapedInstall(t *testing.T) {
	runtime, err := extensionhost.DiscoverRuntime(context.Background())
	if err != nil {
		t.Skip("F13 user-shaped install requires Node.js >=22.6 on PATH")
	}
	// The plugin declares a regular dependency (acorn); the product installs
	// declared dependencies through the npmCommand setting (default npm).
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("F13 user-shaped install requires npm on PATH")
	}
	probe, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(probe, http.MethodGet, "https://registry.npmjs.org/-/ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Skipf("F13 user-shaped install requires registry.npmjs.org: %v", err)
	}
	_ = response.Body.Close()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	agentDir := filepath.Join(home, ".pi", "agent")
	project := filepath.Join(root, "project")
	for _, dir := range []string{agentDir, project} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv(config.EnvAgentDir, agentDir)

	settings, err := config.NewSettingsManager(project, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager := agent.NewPackageManager(agent.PackageManagerOptions{
		CWD: project, AgentDir: agentDir, Settings: settings,
	})
	source := "npm:" + f13PluginName + "@" + f13PluginVersion
	if err := manager.InstallAndPersist(source, false); err != nil {
		t.Fatalf("orb install %s: %v", source, err)
	}

	pluginDir := filepath.Join(agentDir, "npm", "node_modules", filepath.FromSlash(f13PluginName))
	var installed struct {
		Version string `json:"version"`
	}
	manifest, err := os.ReadFile(filepath.Join(pluginDir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifest, &installed); err != nil {
		t.Fatal(err)
	}
	if installed.Version != f13PluginVersion {
		t.Fatalf("installed plugin version = %q, want %s", installed.Version, f13PluginVersion)
	}

	// The typebox peer is materialized natively; the pi-* SDK peers are never
	// fetched anywhere under the managed install root.
	if _, err := os.Stat(filepath.Join(pluginDir, "node_modules", "typebox", "package.json")); err != nil {
		t.Fatalf("typebox peer dependency was not materialized: %v", err)
	}
	if err := filepath.WalkDir(filepath.Join(agentDir, "npm"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == "@earendil-works" || entry.Name() == "@mariozechner") {
			t.Fatalf("real pi SDK scope materialized at %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Resolve reports the plugin's pi.extensions entry; the real host must
	// load it — this is where the missing peer previously failed with
	// "Cannot find package 'typebox'".
	resolved, err := manager.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var extensionPaths []string
	for _, resource := range resolved.Extensions {
		if resource.Enabled && strings.HasPrefix(resource.Path, pluginDir+string(filepath.Separator)) {
			extensionPaths = append(extensionPaths, resource.Path)
		}
	}
	if len(extensionPaths) == 0 {
		t.Fatalf("no extension entries resolved from %s: %+v", pluginDir, resolved.Extensions)
	}

	hostManager := extensionhost.NewManager(extensionhost.Options{
		AgentDir: agentDir, CWD: project, Version: "test", Runtime: &runtime,
		RequestTimeout: 2 * time.Minute, ShutdownTimeout: 2 * time.Second,
	})
	t.Cleanup(func() {
		if err := hostManager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	registry := extensions.NewRegistry(project)
	result := hostManager.RegisterInto(context.Background(), registry, extensionPaths)
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("extension load result = %#v", result)
	}
	modelRegistry, err := config.NewOfflineModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: project, ModelRegistry: modelRegistry})
	for _, tool := range []string{"workflow", "workflow_control"} {
		if runner.ToolDefinition(tool) == nil {
			t.Fatalf("tool %q was not registered by the loaded extension", tool)
		}
	}
}
