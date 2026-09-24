// Package layering makes the P1 layer map executable: the allowed dependency
// edges between orb's layers are asserted over every non-test file's imports,
// so an illegal edge fails make check instead of surviving as architecture
// prose. See DECISIONS.md "Constitution" P1.
package layering

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/OrdalieTech/orb/"

// allowedImports maps a top-level layer to the layers it may import from this
// module. Layers absent from the map (agent, chat, cmd, conformance) are
// assemblies or the product runtime and may import anything below them; a new
// top-level capability package should get an entry here (sandbox is the model).
var allowedImports = map[string][]string{
	"internal": {"internal"},
	"ai":       {"ai", "internal"},
	"engine":   {"engine", "ai", "internal"},
	"tui":      {"tui", "internal"},
	"sandbox":  {"sandbox", "internal"},
	"host":     {"host", "engine", "ai", "storage", "internal"},
}

// tuiImporters are the only places allowed to link the TUI: assemblies, the
// interactive mode, and plugin/extension custom UI (the D15 component
// contract is exactly for them). Everything else must stay headless so a
// binary that skips the interface contains none of its code (P1).
var tuiImporters = []string{
	"tui/", "cmd/", "agent/modes/", "agent/assembly/", "plugins/tasks/", "plugins/questions/", "plugins/permissions/", "plugins/mcp/", "agent/extensions/", "agent/examples/",
}

var skipDirs = map[string]bool{
	".git": true, ".upstream": true, ".tools": true, ".claude": true,
	"node_modules": true, "testdata": true,
}

func TestLayerEdges(t *testing.T) {
	root := moduleRoot(t)
	fileSet := token.NewFileSet()
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		layer, _, _ := strings.Cut(relative, "/")
		for _, spec := range parsed.Imports {
			target := strings.Trim(spec.Path.Value, `"`)
			if (strings.HasPrefix(target, "github.com/tailscale/") || strings.HasPrefix(target, "tailscale.com/")) && !strings.HasPrefix(relative, "plugins/bridge/transports/tailcat/") {
				violations = append(violations, relative+" imports Tailcat outside its native transport adapter")
			}
			if !strings.HasPrefix(target, module) {
				continue
			}
			targetPath := strings.TrimPrefix(target, module)
			if strings.HasPrefix(targetPath, "platforms/") && layer != "platforms" && layer != "cmd" {
				violations = append(violations, relative+" imports a platform assembly into a reusable layer")
			}
			if (layer == "agent" || layer == "ai" || layer == "engine") && (strings.HasPrefix(targetPath, "plugins/bridge") || strings.HasPrefix(targetPath, "connect")) {
				violations = append(violations, relative+" imports optional bridge assembly")
			}
			if (strings.HasPrefix(relative, "connect/") && !strings.HasPrefix(relative, "connect/agent/")) || (strings.HasPrefix(relative, "plugins/bridge/") && !strings.HasPrefix(relative, "plugins/bridge/agent/") && !strings.HasPrefix(relative, "plugins/bridge/extension/") && !strings.HasPrefix(relative, "plugins/bridge/hosts/") && !strings.HasPrefix(relative, "plugins/bridge/transports/")) {
				if strings.HasPrefix(targetPath, "agent/") || targetPath == "agent" || strings.HasPrefix(targetPath, "plugins/bridge/hosts/") || strings.HasPrefix(targetPath, "plugins/bridge/transports/") {
					violations = append(violations, relative+" imports host-specific code into portable core")
				}
			}
			if (relative == "plugins/memory/memory.go" || strings.HasPrefix(relative, "plugins/memory/agent/")) &&
				!hasAnyPrefix(targetPath, []string{"plugins/memory", "engine", "ai", "internal"}) {
				violations = append(violations, relative+" imports "+targetPath+" outside the memory SDK layers")
			}
			targetLayer, _, _ := strings.Cut(targetPath, "/")
			if allowed, restricted := allowedImports[layer]; restricted && !slices.Contains(allowed, targetLayer) {
				violations = append(violations, relative+" imports "+targetPath+" ("+layer+" may only import "+strings.Join(allowed, ", ")+")")
			}
			if targetLayer == "tui" && !hasAnyPrefix(relative, tuiImporters) {
				violations = append(violations, relative+" imports "+targetPath+" (tui is presentation: only "+strings.Join(tuiImporters, " ")+" may link it)")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Error(violation)
	}
}

// TestEngineIsHeadless proves the P1 linkage promise at the layer served at
// scale: no package under engine/ or ai/ links TUI code, transitively.
func TestEngineIsHeadless(t *testing.T) {
	root := moduleRoot(t)
	command := exec.Command("go", "list", "-deps", "./engine/...", "./ai/...")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./engine/... ./ai/...: %v\n%s", err, output)
	}
	if strings.Contains(string(output), module+"tui") {
		t.Errorf("engine or ai transitively links %stui", module)
	}
}

// TestProductCoreIsHeadless extends the P1 promise to the product root: an
// embedder of agent gets the full AgentSession without the interactive
// driver, the TUI, or syntax highlighting. Themes reach the core as neutral
// resources (internal/themefile); agent/modes/theme renders them.
func TestProductCoreIsHeadless(t *testing.T) {
	packages := []string{"./agent", "./agent/config", "./agent/session", "./agent/extensions", "./agent/tools"}
	forbidden := []string{module + "agent/modes", module + "tui", module + "internal/chromalexers", module + "internal/cjksegment", "github.com/alecthomas/chroma"}
	command := exec.Command("go", append([]string{"list", "-deps"}, packages...)...)
	command.Dir = moduleRoot(t)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", strings.Join(packages, " "), err, output)
	}
	for dep := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		for _, prefix := range forbidden {
			if dep == prefix || strings.HasPrefix(dep, prefix+"/") {
				t.Errorf("product core transitively links %s", dep)
			}
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("cannot find module root")
		}
		directory = parent
	}
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// These transitive checks keep optional adapters out of reusable capabilities.
func TestCapabilityDependencies(t *testing.T) {
	for _, tc := range []struct {
		path      string
		forbidden []string
	}{
		{"plugins/memory", []string{"/agent", "/engine", "/tui", "/plugins/memory/filestore", "/storage/sqlite"}},
		{"plugins/memory/agent", []string{"/agent", "/tui", "/plugins/memory/filestore", "/plugins/memory/extension", "/storage/sqlite"}},
		{"plugins/memory/extension", []string{"/agent/assembly", "/plugins/memory/filestore", "/storage/sqlite"}},
		{"plugins/usage", []string{"/agent", "/engine", "/tui", "/plugins/usage/footer", "/storage/sqlite"}},
		{"plugins/bridge/agent", []string{"/agent", "/tui", "/plugins/bridge/hosts", "/plugins/bridge/transports", "/storage/sqlite"}},
		{"plugins/bridge/transports/websocket", []string{"/agent", "/tui", "/plugins/bridge/hosts", "/plugins/bridge/transports/tailcat", "/storage/sqlite"}},
		{"plugins/bridge", []string{"/agent", "/tui", "/plugins/bridge/hosts", "/plugins/bridge/transports", "/storage/sqlite"}},
		{"plugins/tasks", []string{"/agent/assembly", "/plugins/subagents", "/plugins/websearch", "/plugins/mcp"}},
		{"platforms/worker/peer", []string{"/agent/assembly", "/agent/modes", "/tui", "/storage/sqlite", "/plugins/bridge/hosts", "/plugins/bridge/transports"}},
		{"platforms/worker", []string{"/agent/assembly", "/agent/modes", "/tui", "/storage/sqlite", "/plugins"}},
		{"platforms/browser", []string{"/agent/config", "/agent/assembly", "/agent/modes", "/agent/extensions", "/tui", "/storage/sqlite", "/plugins/bridge/hosts", "/plugins/bridge/transports"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "./"+tc.path)
			cmd.Dir = moduleRoot(t)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("dependencies: %v\n%s", err, output)
			}
			for dep := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
				for _, forbidden := range tc.forbidden {
					prefix := strings.TrimSuffix(module, "/") + forbidden
					if dep == prefix || strings.HasPrefix(dep, prefix+"/") {
						t.Errorf("%s links forbidden dependency %s", tc.path, dep)
					}
				}
				if strings.HasPrefix(dep, "github.com/tailscale/") || strings.HasPrefix(dep, "tailscale.com/") {
					t.Errorf("%s links native transport dependency %s", tc.path, dep)
				}
			}
		})
	}
}

func TestBrowserAssemblyDependencies(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "./cmd/orb-wasm")
	cmd.Dir = moduleRoot(t)
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser dependencies: %v\n%s", err, output)
	}
	for dep := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		if hasAnyPrefix(dep, []string{module + "tui", module + "agent/extensions", module + "agent/modes", module + "storage/sqlite", module + "plugins/bridge/hosts", module + "plugins/bridge/transports/tailcat", "tailscale.com/", "github.com/tailscale/"}) {
			t.Errorf("browser assembly links %s", dep)
		}
	}
}
