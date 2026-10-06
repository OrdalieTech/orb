// Package layering makes the P1 layer map executable: the allowed dependency
// edges between orb's layers are asserted over every non-test file's imports,
// so an illegal edge fails make check instead of surviving as architecture
// prose. See DECISIONS.md "Constitution" P1.
package layering

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/OrdalieTech/orb/"

// allowedImports maps a layer, or a package directory inside one, to the
// module paths it may import; the most specific entry covering a file applies.
// Layers absent from the map (agent, chat, cmd, conformance) are assemblies or
// the product runtime and may import anything below them; a new self-contained
// package should get an entry here (platforms/native/sandbox is the model).
// Layers rank every package; an import points to its own layer or below.
const (
	library    = iota // internal and tui: leaf code
	providers         // ai
	loop              // engine
	core              // agent, bridge, host: the closed core (P10)
	capability        // plugins (P3)
	driver            // interfaces: TUI mode, RPC, ACP, chat platforms (P1)
	hostLayer         // platforms: port implementations
	assembly          // binaries, catalogs, examples: the only layer that composes and configures
)

// layers maps package paths to their layer; the longest prefix wins, and a
// package no entry covers fails the test.
var layers = map[string]int{
	".": library, "internal": library, "tui": library,
	"ai": providers, "engine": loop,
	"agent": core, "bridge": core, "host": core,
	"plugins":     capability,
	"agent/modes": driver, "agent/rpc": driver, "agent/acp": driver, "agent/bridge": driver,
	"agent/clipboard": driver, "agent/extensions/host": driver, "chat": driver,
	"platforms": hostLayer,
	"cmd":       assembly, "agent/assembly": assembly, "agent/examples": assembly, "chat/examples": assembly, "chat/platforms": assembly,
	"platforms/agent": assembly, "conformance": assembly,
}

// processState is the process's environment, home, working directory and
// host name, which only hosts and assemblies read or change: everything below
// takes them as values (P3, P10).
var processState = map[string]bool{
	"Getenv": true, "LookupEnv": true, "Environ": true, "Setenv": true, "Unsetenv": true, "Clearenv": true, "ExpandEnv": true,
	"UserHomeDir": true, "UserConfigDir": true, "UserCacheDir": true, "Getwd": true, "Chdir": true, "Hostname": true,
}

func layerOf(pkg string) (int, bool) {
	best, rank, found := "", 0, false
	for prefix, layer := range layers {
		if (pkg == prefix || prefix == "." || strings.HasPrefix(pkg, prefix+"/")) && (!found || len(prefix) > len(best)) {
			best, rank, found = prefix, layer, true
		}
	}
	return rank, found
}

var tuiImporters = []string{
	"tui/", "cmd/", "agent/modes/", "agent/assembly/", "plugins/tasks/", "plugins/questions/", "plugins/permissions/", "plugins/mcp/", "agent/extensions/", "agent/examples/",
}

var skipDirs = map[string]bool{
	".git": true, ".upstream": true, ".tools": true, ".claude": true,
	"node_modules": true, "testdata": true,
}

const layerRatchet = "testdata/layer_ratchet.txt"

// TestLayerEdges enforces the layers: imports point down, process state and
// init-time registration stay in hosts and assemblies, Tailcat in its
// adapter, tui with its importers. Existing violations are recorded in
// layerRatchet and may only shrink (ORB_UPDATE_CORE_RATCHET=1 records a gain).
func TestLayerEdges(t *testing.T) {
	root := moduleRoot(t)
	fileSet := token.NewFileSet()
	counts := map[string]int{}
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
		pkg := filepath.ToSlash(filepath.Dir(relative))
		source, ok := layerOf(pkg)
		if !ok {
			t.Errorf("%s is in no layer; add it to layers", pkg)
			return nil
		}
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		names := map[string]string{}
		for _, spec := range file.Imports {
			target, _ := strconv.Unquote(spec.Path.Value)
			names[target[strings.LastIndex(target, "/")+1:]] = target
			if spec.Name != nil {
				names[spec.Name.Name] = target
			}
			if (strings.HasPrefix(target, "github.com/tailscale/") || strings.HasPrefix(target, "tailscale.com/")) && !strings.HasPrefix(relative, "platforms/native/tailcat/") {
				t.Errorf("%s imports Tailcat outside its native transport adapter", relative)
			}
			targetPath, internal := strings.CutPrefix(target, module)
			if !internal {
				continue
			}
			if rank, _ := layerOf(targetPath); rank > source {
				counts[pkg+"\timports "+targetPath]++
			}
			if (targetPath == "tui" || strings.HasPrefix(targetPath, "tui/")) && !hasAnyPrefix(relative, tuiImporters) {
				t.Errorf("%s imports %s (tui is presentation: only %s may link it)", relative, targetPath, strings.Join(tuiImporters, " "))
			}
		}
		if source >= hostLayer || isCore(pkg) {
			return nil // the core's platform access has its own ratchet
		}
		for _, declaration := range file.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok && function.Recv == nil && function.Name.Name == "init" && source < assembly {
				counts[pkg+"\tfunc init"]++
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok {
				if ident, ok := selector.X.(*ast.Ident); ok && names[ident.Name] == "os" && processState[selector.Sel.Name] {
					counts[pkg+"\tos."+selector.Sel.Name]++
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkRatchet(t, root, layerRatchet, counts, "layers: imports point down, and only hosts and assemblies read the environment or register at init")
}

// isCore reports whether pkg is in the P10 portable core.
func isCore(pkg string) bool {
	return slices.ContainsFunc(corePackages, func(top string) bool { return pkg == top || strings.HasPrefix(pkg, top+"/") }) &&
		!slices.ContainsFunc(coreExcluded, func(excluded string) bool { return pkg == excluded || strings.HasPrefix(pkg, excluded+"/") })
}

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
		{"plugins/memory", []string{"/agent", "/engine", "/tui", "/plugins/memory/filestore", "/platforms/native/sqlite"}},
		{"plugins/memory/agent", []string{"/agent", "/tui", "/plugins/memory/filestore", "/plugins/memory/extension", "/platforms/native/sqlite"}},
		{"plugins/memory/extension", []string{"/agent/assembly", "/plugins/memory/filestore", "/platforms/native/sqlite"}},
		{"plugins/usage", []string{"/agent", "/engine", "/tui", "/plugins/usage/footer", "/platforms/native/sqlite"}},
		{"agent/bridge/tool", []string{"/agent/session", "/agent/extensions", "/agent/config", "/agent/modes", "/tui", "/platforms", "/plugins"}},
		{"platforms/websocket", []string{"/agent", "/tui", "/platforms/native", "/plugins"}},
		{"bridge", []string{"/agent", "/ai", "/engine", "/tui", "/platforms", "/plugins"}},
		{"plugins/tasks", []string{"/agent/assembly", "/plugins/subagents", "/plugins/websearch", "/plugins/mcp"}},
		{"platforms/worker/peer", []string{"/agent/assembly", "/agent/modes", "/tui", "/platforms/native", "/platforms/websocket"}},
		{"platforms/worker", []string{"/agent/assembly", "/agent/modes", "/tui", "/platforms/native/sqlite", "/plugins"}},
		{"platforms/browser", []string{"/agent/config", "/agent/assembly", "/agent/modes", "/agent/extensions", "/tui", "/platforms/native", "/platforms/websocket"}},
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
		if hasAnyPrefix(dep, []string{module + "tui", module + "agent/extensions", module + "agent/modes", module + "platforms/native/", "tailscale.com/", "github.com/tailscale/"}) {
			t.Errorf("browser assembly links %s", dep)
		}
	}
}
