package themefile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Diagnostic struct {
	Type      string
	Path      string
	Message   string
	Collision *Collision
}

type Collision struct {
	ResourceType string
	Name         string
	WinnerPath   string
	LoserPath    string
}

// Loader walks theme paths in pi's discovery order. Each root is walked once;
// unreadable, missing, or invalid entries become warnings, and every parsed
// theme goes to the caller, which owns name collisions.
type Loader struct {
	warnings []Diagnostic
	seen     map[string]bool
}

// Warnings returns the load warnings in discovery order.
func (loader *Loader) Warnings() []Diagnostic {
	return append([]Diagnostic(nil), loader.warnings...)
}

func (loader *Loader) LoadPaths(paths []string, add func(*Theme)) {
	if loader.seen == nil {
		loader.seen = map[string]bool{}
	}
	for _, path := range paths {
		path = CleanPath(path)
		if path == "" {
			continue
		}
		if loader.seen[path] {
			continue
		}
		loader.seen[path] = true
		info, err := os.Stat(path)
		if err != nil {
			loader.warnings = append(loader.warnings, Diagnostic{Type: "warning", Path: path, Message: "theme path does not exist"})
			continue
		}
		if info.IsDir() {
			entries, readErr := os.ReadDir(path)
			if readErr != nil {
				loader.warnings = append(loader.warnings, Diagnostic{Type: "warning", Path: path, Message: readErr.Error()})
				continue
			}
			for _, entry := range entries {
				if !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				isFile := entry.Type().IsRegular()
				if entry.Type()&os.ModeSymlink != 0 {
					if target, statErr := os.Stat(filepath.Join(path, entry.Name())); statErr == nil {
						isFile = target.Mode().IsRegular()
					}
				}
				if isFile {
					loader.loadFile(filepath.Join(path, entry.Name()), add)
				}
			}
			continue
		}
		if !strings.HasSuffix(path, ".json") {
			loader.warnings = append(loader.warnings, Diagnostic{Type: "warning", Path: path, Message: "theme path is not a json file"})
			continue
		}
		loader.loadFile(path, add)
	}
}

// LoadDefaultDirectory walks path only when it is an existing directory.
func (loader *Loader) LoadDefaultDirectory(path string, add func(*Theme)) {
	path = CleanPath(path)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}
	loader.LoadPaths([]string{path}, add)
}

func (loader *Loader) loadFile(path string, add func(*Theme)) {
	data, err := os.ReadFile(path)
	if err != nil {
		loader.warnings = append(loader.warnings, Diagnostic{Type: "warning", Path: path, Message: err.Error()})
		return
	}
	theme, err := Parse(path, data)
	if err != nil {
		loader.warnings = append(loader.warnings, Diagnostic{Type: "warning", Path: path, Message: err.Error()})
		return
	}
	theme.SourcePath = path
	add(theme)
}

// CollisionDiagnostic reports loser losing its name to an earlier winner.
func CollisionDiagnostic(name, winnerPath, loserPath string) Diagnostic {
	return Diagnostic{
		Type: "collision", Message: fmt.Sprintf("name %q collision", name), Path: loserPath,
		Collision: &Collision{ResourceType: "theme", Name: name, WinnerPath: winnerPath, LoserPath: loserPath},
	}
}

// Discover loads the themes under paths (relative ones resolved against cwd):
// first name wins, and diagnostics list load warnings before collisions.
func Discover(cwd string, paths []string) ([]*Theme, []Diagnostic) {
	var loader Loader
	themes := []*Theme{}
	byName := map[string]*Theme{}
	var collisions []Diagnostic
	loader.LoadPaths(ResolvePaths(paths, CleanPath(cwd)), func(theme *Theme) {
		if winner, exists := byName[theme.Name]; exists {
			collisions = append(collisions, CollisionDiagnostic(theme.Name, winner.SourcePath, theme.SourcePath))
			return
		}
		byName[theme.Name] = theme
		themes = append(themes, theme)
	})
	return themes, append(loader.Warnings(), collisions...)
}

// CleanPath trims, expands a leading ~, and makes path absolute and clean.
func CleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, path[2:])
			}
		}
	}
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

// ResolvePaths joins relative, non-home paths onto base.
func ResolvePaths(paths []string, base string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path != "" && !filepath.IsAbs(path) && path != "~" && !strings.HasPrefix(path, "~/") {
			path = filepath.Join(base, path)
		}
		result = append(result, path)
	}
	return result
}
