package jsbridge

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrdalieTech/pigo/codingagent/config"
)

const configDirName = ".pi"

// DiscoveryOptions contains local paths after settings and package resolution.
// WP-360 supplies resolved package paths; this package does not install packages.
type DiscoveryOptions struct {
	CWD                         string
	AgentDir                    string
	ProjectTrusted              bool
	NoDiscovery                 bool
	ConfiguredPaths             []string
	ProjectConfiguredPaths      []string
	ResolvedPackagePaths        []string
	ProjectResolvedPackagePaths []string
	ExplicitPaths               []string
}

// Discover returns extension entry points in upstream load order with the first
// spelling of an absolute path winning deduplication.
func Discover(options DiscoveryOptions) []string {
	cwd := absoluteOrDot(options.CWD)
	agentDir := options.AgentDir
	if agentDir == "" {
		if configured, err := config.GetAgentDir(); err == nil {
			agentDir = configured
		}
	}
	agentDir = absoluteOrDot(agentDir)
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	add := func(candidates []string) {
		for _, candidate := range candidates {
			resolved := resolvePath(candidate, cwd)
			key := filepath.Clean(resolved)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			paths = append(paths, resolved)
		}
	}

	if !options.NoDiscovery {
		if options.ProjectTrusted {
			add(discoverDirectory(filepath.Join(cwd, configDirName, "extensions")))
		}
		add(discoverDirectory(filepath.Join(agentDir, "extensions")))
		configuredPaths := append([]string(nil), options.ConfiguredPaths...)
		if options.ProjectTrusted {
			configuredPaths = append(configuredPaths, options.ProjectConfiguredPaths...)
		}
		configuredPaths = append(configuredPaths, options.ResolvedPackagePaths...)
		if options.ProjectTrusted {
			configuredPaths = append(configuredPaths, options.ProjectResolvedPackagePaths...)
		}
		for _, configured := range configuredPaths {
			add(resolveConfiguredPath(configured, cwd))
		}
	}
	for _, explicit := range options.ExplicitPaths {
		add(resolveConfiguredPath(explicit, cwd))
	}
	return paths
}

func resolveConfiguredPath(input, cwd string) []string {
	resolved := resolvePath(input, cwd)
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return []string{resolved}
	}
	if entries := resolveExtensionEntries(resolved); len(entries) > 0 {
		return entries
	}
	return discoverDirectory(resolved)
}

func discoverDirectory(directory string) []string {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	discovered := make([]string, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		isSymlink := entry.Type()&os.ModeSymlink != 0
		if (entry.Type().IsRegular() || isSymlink) && isExtensionFile(entry.Name()) {
			discovered = append(discovered, path)
			continue
		}
		if entry.IsDir() || isSymlink {
			discovered = append(discovered, resolveExtensionEntries(path)...)
		}
	}
	return discovered
}

func resolveExtensionEntries(directory string) []string {
	manifestPath := filepath.Join(directory, "package.json")
	if content, err := os.ReadFile(manifestPath); err == nil {
		var manifest struct {
			Pi struct {
				Extensions []string `json:"extensions"`
			} `json:"pi"`
		}
		if json.Unmarshal(content, &manifest) == nil && len(manifest.Pi.Extensions) > 0 {
			entries := make([]string, 0, len(manifest.Pi.Extensions))
			for _, entry := range manifest.Pi.Extensions {
				path := filepath.Clean(filepath.Join(directory, filepath.FromSlash(entry)))
				if _, err := os.Stat(path); err == nil {
					entries = append(entries, path)
				}
			}
			if len(entries) > 0 {
				return entries
			}
		}
	}
	for _, name := range []string{"index.ts", "index.js"} {
		path := filepath.Join(directory, name)
		if _, err := os.Stat(path); err == nil {
			return []string{path}
		}
	}
	return nil
}

func isExtensionFile(name string) bool {
	return strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".js")
}

func resolvePath(input, base string) string {
	input = normalizeUnicodeSpaces(input)
	if input == "~" || strings.HasPrefix(input, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			input = filepath.Join(home, strings.TrimPrefix(input, "~/"))
		}
	}
	if strings.HasPrefix(input, "file://") {
		if parsed, err := url.Parse(input); err == nil {
			input = parsed.Path
		}
	}
	if !filepath.IsAbs(input) {
		input = filepath.Join(base, input)
	}
	if absolute, err := filepath.Abs(input); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(input)
}

func normalizeUnicodeSpaces(input string) string {
	return strings.Map(func(character rune) rune {
		switch {
		case character == '\u00a0', character >= '\u2000' && character <= '\u200a', character == '\u202f', character == '\u205f', character == '\u3000':
			return ' '
		default:
			return character
		}
	}, input)
}

func absoluteOrDot(path string) string {
	if path == "" {
		path = "."
	}
	return resolvePath(path, ".")
}
