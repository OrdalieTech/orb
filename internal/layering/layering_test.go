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
	"memory":   {"memory", "engine", "ai", "internal"},
	"sandbox":  {"sandbox", "internal"},
}

// tuiImporters are the only places allowed to link the TUI: assemblies, the
// interactive mode, and plugin/extension custom UI (the D15 component
// contract is exactly for them). Everything else must stay headless so a
// binary that skips the interface contains none of its code (P1).
var tuiImporters = []string{
	"tui/", "cmd/", "agent/modes/", "agent/plugins/", "agent/extensions/", "agent/examples/",
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
			if !strings.HasPrefix(target, module) {
				continue
			}
			targetPath := strings.TrimPrefix(target, module)
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
// ponytail: agent (the product root) still links tui through modes/theme's
// MarkdownTheme/EditorTheme StyleFunc fields; split theme's tui binding when
// a headless agent-root consumer is real.
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
