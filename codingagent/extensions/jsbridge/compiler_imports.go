package jsbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// packageImportsExtensionPlugin matches the TypeScript loader used by pi: a
// package.json imports target may omit a TypeScript extension even though
// strict Node ESM resolution would require it.
func packageImportsExtensionPlugin() api.Plugin {
	return api.Plugin{
		Name: "pi-package-imports",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `^#`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				resolved, manifest, ok := resolvePackageImport(args.Importer, args.Path)
				if !ok {
					return api.OnResolveResult{}, nil
				}
				return api.OnResolveResult{Path: resolved, WatchFiles: []string{manifest}}, nil
			})
		},
	}
}

func resolvePackageImport(importer, specifier string) (string, string, bool) {
	for directory := filepath.Dir(importer); ; directory = filepath.Dir(directory) {
		manifest := filepath.Join(directory, "package.json")
		contents, err := os.ReadFile(manifest)
		if err == nil {
			var decoded struct {
				Imports map[string]json.RawMessage `json:"imports"`
			}
			if json.Unmarshal(contents, &decoded) == nil {
				if target, ok := packageImportTarget(decoded.Imports, specifier); ok {
					candidate := filepath.Clean(filepath.Join(directory, filepath.FromSlash(strings.TrimPrefix(target, "./"))))
					relative, relErr := filepath.Rel(directory, candidate)
					if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
						if resolved, found := resolveTypeScriptImport(candidate); found {
							return resolved, manifest, true
						}
					}
				}
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	return "", "", false
}

func packageImportTarget(imports map[string]json.RawMessage, specifier string) (string, bool) {
	if raw, ok := imports[specifier]; ok {
		return packageImportString(raw, "")
	}
	patterns := make([]string, 0, len(imports))
	for pattern := range imports {
		if strings.Count(pattern, "*") == 1 {
			patterns = append(patterns, pattern)
		}
	}
	sort.Slice(patterns, func(i, j int) bool { return len(patterns[i]) > len(patterns[j]) })
	for _, pattern := range patterns {
		before, after, _ := strings.Cut(pattern, "*")
		if !strings.HasPrefix(specifier, before) || !strings.HasSuffix(specifier, after) || len(specifier) < len(before)+len(after) {
			continue
		}
		wildcard := specifier[len(before) : len(specifier)-len(after)]
		if target, ok := packageImportString(imports[pattern], wildcard); ok {
			return target, true
		}
	}
	return "", false
}

func packageImportString(raw json.RawMessage, wildcard string) (string, bool) {
	var target string
	if json.Unmarshal(raw, &target) != nil || !strings.HasPrefix(target, "./") {
		return "", false
	}
	if strings.Contains(target, "*") {
		target = strings.ReplaceAll(target, "*", wildcard)
	}
	return target, true
}

func resolveTypeScriptImport(candidate string) (string, bool) {
	if regularFile(candidate) {
		return candidate, true
	}
	if filepath.Ext(candidate) == "" {
		for _, extension := range []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".json"} {
			if regularFile(candidate + extension) {
				return candidate + extension, true
			}
		}
	}
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		for _, filename := range []string{"index.ts", "index.tsx", "index.mts", "index.cts", "index.js", "index.jsx", "index.mjs", "index.cjs", "index.json"} {
			if path := filepath.Join(candidate, filename); regularFile(path) {
				return path, true
			}
		}
	}
	return "", false
}
