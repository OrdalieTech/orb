package layering

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// corePackages is the P10 portable core. Subdirectories are included unless
// listed in coreExcluded: those are drivers, native capabilities or tooling.
var corePackages = []string{"ai", "engine", "agent", "internal/themefile"}

var coreExcluded = []string{
	"ai/models/cmd", "ai/models/internal",
	"agent/modes", "agent/examples", "agent/clipboard",
	"agent/extensions/host", "agent/extensions/examples",
}

// Platform access that must reach the core through host ports (P10). Entries
// cover their subpackages, except "net", whose subpackages (http, url) are
// portable types.
var forbiddenCoreImports = []string{
	"os/exec", "os/signal", "syscall", "net", "golang.org/x/sys",
	"github.com/gofrs/flock", "github.com/creack/pty", module + "internal/filelock",
}

// allowedOS are the os selectors that describe errors and file metadata
// rather than touching the process, environment or real filesystem.
var allowedOS = map[string]bool{
	"ErrNotExist": true, "ErrExist": true, "ErrPermission": true, "ErrClosed": true,
	"ErrInvalid": true, "ErrDeadlineExceeded": true, "ErrProcessDone": true,
	"IsNotExist": true, "IsExist": true, "IsPermission": true, "IsTimeout": true,
	"FileMode": true, "FileInfo": true, "DirEntry": true, "PathError": true, "LinkError": true,
	"SyscallError": true, "PathSeparator": true, "PathListSeparator": true, "DevNull": true,
	"ModePerm": true, "ModeDir": true, "ModeSymlink": true, "ModeType": true, "ModeNamedPipe": true,
	"ModeSocket": true, "ModeDevice": true, "ModeCharDevice": true, "ModeIrregular": true,
	"ModeAppend": true, "ModeExclusive": true, "ModeTemporary": true, "ModeSetuid": true,
	"ModeSetgid": true, "ModeSticky": true,
	"O_RDONLY": true, "O_WRONLY": true, "O_RDWR": true, "O_APPEND": true, "O_CREATE": true,
	"O_EXCL": true, "O_SYNC": true, "O_TRUNC": true,
}

// Selectors outside os that consult the process-wide network client or the
// real filesystem and working directory.
var forbiddenSelectors = map[string]map[string]bool{
	"net/http":      {"DefaultClient": true, "DefaultTransport": true, "Get": true, "Head": true, "Post": true, "PostForm": true, "ProxyFromEnvironment": true},
	"path/filepath": {"Abs": true, "EvalSymlinks": true, "Glob": true, "Walk": true, "WalkDir": true},
}

const ratchetFile = "testdata/core_ratchet.txt"

// TestPortableCoreRatchet enforces P10: core packages reach the platform only
// through host ports. Existing violations are recorded per package and symbol
// and may only shrink; ORB_UPDATE_CORE_RATCHET=1 records a reduction and
// refuses to record any growth.
func TestPortableCoreRatchet(t *testing.T) {
	root := moduleRoot(t)
	current := coreViolations(t, root)
	recorded := readRatchet(t)
	var grown, shrunk []string
	for _, key := range slices.Sorted(maps.Keys(current)) {
		if current[key] > recorded[key] {
			grown = append(grown, fmt.Sprintf("%s: %d > recorded %d", key, current[key], recorded[key]))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(recorded)) {
		if current[key] < recorded[key] {
			shrunk = append(shrunk, fmt.Sprintf("%s: %d < recorded %d", key, current[key], recorded[key]))
		}
	}
	if len(grown) > 0 {
		t.Fatalf("portable core gained platform access; route it through a host port (DECISIONS.md P10):\n%s", strings.Join(grown, "\n"))
	}
	if len(shrunk) == 0 {
		return
	}
	if os.Getenv("ORB_UPDATE_CORE_RATCHET") != "1" {
		t.Fatalf("portable core shrank; lock the gain with ORB_UPDATE_CORE_RATCHET=1:\n%s", strings.Join(shrunk, "\n"))
	}
	var out strings.Builder
	out.WriteString("# P10 portable-core ratchet: package<TAB>symbol<TAB>count. Counts only shrink.\n")
	for _, key := range slices.Sorted(maps.Keys(current)) {
		fmt.Fprintf(&out, "%s\t%d\n", key, current[key])
	}
	if err := os.WriteFile(filepath.Join(root, "internal/layering", ratchetFile), []byte(out.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func coreViolations(t *testing.T, root string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	fileSet := token.NewFileSet()
	for _, top := range corePackages {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if entry.IsDir() {
				if skipDirs[entry.Name()] || slices.ContainsFunc(coreExcluded, func(excluded string) bool { return rel == excluded }) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
			if parseErr != nil {
				return parseErr
			}
			pkg := filepath.ToSlash(filepath.Dir(rel))
			names := map[string]string{}
			for _, spec := range file.Imports {
				importPath, _ := strconv.Unquote(spec.Path.Value)
				for _, forbidden := range forbiddenCoreImports {
					if importPath == forbidden || (forbidden != "net" && strings.HasPrefix(importPath, forbidden+"/")) {
						counts[pkg+"\timport "+importPath]++
					}
				}
				name := importPath[strings.LastIndex(importPath, "/")+1:]
				if spec.Name != nil {
					name = spec.Name.Name
				}
				names[name] = importPath
			}
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch importPath := names[ident.Name]; {
				case importPath == "os" && !allowedOS[selector.Sel.Name]:
					counts[pkg+"\tos."+selector.Sel.Name]++
				case forbiddenSelectors[importPath][selector.Sel.Name]:
					counts[pkg+"\t"+importPath+"."+selector.Sel.Name]++
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func readRatchet(t *testing.T) map[string]int {
	t.Helper()
	data, err := os.ReadFile(ratchetFile)
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]int{}
	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cut := strings.LastIndexByte(line, '\t')
		count, convErr := strconv.Atoi(line[cut+1:])
		if cut < 0 || convErr != nil {
			t.Fatalf("malformed ratchet line %q", line)
		}
		recorded[line[:cut]] = count
	}
	return recorded
}
