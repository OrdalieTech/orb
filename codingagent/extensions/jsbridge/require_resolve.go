package jsbridge

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/grafana/sobek"
)

func installRequireResolver(runtime *sobek.Runtime, entry string) error {
	return runtime.Set("__piGoRequireResolve", func(call sobek.FunctionCall) sobek.Value {
		base := entry
		if value := call.Argument(1); present(value) {
			base = value.String()
		}
		resolved, err := resolveRequireSpecifier(base, call.Argument(0).String())
		if err != nil {
			panic(runtime.NewTypeError(err.Error()))
		}
		return runtime.ToValue(resolved)
	})
}

func resolveRequireSpecifier(base, specifier string) (string, error) {
	if strings.HasPrefix(specifier, "node:") || nodeBuiltinSpecifier(specifier) {
		return specifier, nil
	}
	base = requireBasePath(base)
	baseDirectory := filepath.Dir(base)
	if filepath.IsAbs(specifier) || strings.HasPrefix(specifier, "./") || strings.HasPrefix(specifier, "../") {
		candidate := specifier
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(baseDirectory, filepath.FromSlash(candidate))
		}
		if resolved, ok := resolveRequirePath(candidate); ok {
			return resolved, nil
		}
		return "", fmt.Errorf("Cannot find module %q", specifier) //nolint:staticcheck // Node error text.
	}

	packageName, subpath, ok := splitPackageSpecifier(specifier)
	if !ok {
		return "", fmt.Errorf("Cannot find module %q", specifier) //nolint:staticcheck // Node error text.
	}
	for directory := baseDirectory; ; directory = filepath.Dir(directory) {
		candidate := filepath.Join(directory, "node_modules", filepath.FromSlash(packageName))
		if subpath != "" {
			candidate = filepath.Join(candidate, filepath.FromSlash(subpath))
		}
		if resolved, found := resolveRequirePath(candidate); found {
			return resolved, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	return "", fmt.Errorf("Cannot find module %q", specifier) //nolint:staticcheck // Node error text.
}

func requireBasePath(base string) string {
	if parsed, err := url.Parse(base); err == nil && parsed.Scheme == "file" {
		return filepath.FromSlash(parsed.Path)
	}
	return filepath.Clean(base)
}

func nodeBuiltinSpecifier(specifier string) bool {
	switch specifier {
	case "assert", "async_hooks", "buffer", "child_process", "crypto", "diagnostics_channel", "dns", "dns/promises", "events", "fs", "fs/promises", "http", "https", "module", "net", "os", "path", "path/posix", "perf_hooks", "process", "readline", "tty", "url", "util", "zlib":
		return true
	default:
		return false
	}
}

func splitPackageSpecifier(specifier string) (string, string, bool) {
	parts := strings.Split(filepath.ToSlash(specifier), "/")
	if len(parts) == 0 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
		return "", "", false
	}
	packageParts := 1
	if strings.HasPrefix(parts[0], "@") {
		if len(parts) < 2 || parts[1] == "" {
			return "", "", false
		}
		packageParts = 2
	}
	return strings.Join(parts[:packageParts], "/"), strings.Join(parts[packageParts:], "/"), true
}

func resolveRequirePath(candidate string) (string, bool) {
	candidate = filepath.Clean(candidate)
	if regularFile(candidate) {
		return candidate, true
	}
	if filepath.Ext(candidate) == "" {
		for _, extension := range []string{".js", ".json", ".node", ".ts"} {
			if regularFile(candidate + extension) {
				return candidate + extension, true
			}
		}
	}
	if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
		return "", false
	}
	var manifest struct {
		Main string `json:"main"`
	}
	if encoded, err := os.ReadFile(filepath.Join(candidate, "package.json")); err == nil && json.Unmarshal(encoded, &manifest) == nil && manifest.Main != "" {
		if resolved, ok := resolveRequirePath(filepath.Join(candidate, filepath.FromSlash(manifest.Main))); ok {
			return resolved, true
		}
	}
	for _, filename := range []string{"index.js", "index.json", "index.node", "index.ts"} {
		if path := filepath.Join(candidate, filename); regularFile(path) {
			return path, true
		}
	}
	return "", false
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
