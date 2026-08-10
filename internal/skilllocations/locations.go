package skilllocations

import (
	"os"
	"path/filepath"
	"strings"
)

var projectDirs = [...]string{".claude", ".codex", ".opencode", ".gemini", ".cursor", ".github"}

// Project returns external Agent Skills roots below one project directory.
func Project(base string) []string {
	roots := make([]string, len(projectDirs))
	for i, dir := range projectDirs {
		roots[i] = filepath.Join(base, dir, "skills")
	}
	return roots
}

// User returns external Agent Skills roots for one user.
func User(home string) []string {
	clean := func(path string) string {
		path = strings.TrimSpace(path)
		if path == "" {
			return ""
		}
		if path == "~" {
			path = home
		} else if strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, path[2:])
		}
		if path != "" && !filepath.IsAbs(path) {
			abs, err := filepath.Abs(path)
			if err != nil {
				return ""
			}
			path = abs
		}
		return filepath.Clean(path)
	}
	root := func(env, fallback string) string {
		if path := os.Getenv(env); path != "" {
			return clean(path)
		}
		return clean(fallback)
	}
	homePath := func(name string) string {
		if home == "" {
			return ""
		}
		return filepath.Join(home, name)
	}
	roots, seen := []string{}, map[string]bool{}
	add := func(base string, suffix ...string) {
		if base == "" {
			return
		}
		path := clean(filepath.Join(append([]string{base}, suffix...)...))
		if !seen[path] {
			seen[path] = true
			roots = append(roots, path)
		}
	}

	add(root("CLAUDE_CONFIG_DIR", homePath(".claude")), "skills")
	add(root("CODEX_HOME", homePath(".codex")), "skills")
	add(root("OPENCODE_CONFIG_DIR", ""), "skills")
	add(root("XDG_CONFIG_HOME", homePath(".config")), "opencode", "skills")
	add(root("GEMINI_CLI_HOME", home), ".gemini", "skills")
	add(home, ".cursor", "skills")
	add(root("COPILOT_HOME", homePath(".copilot")), "skills")
	for path := range strings.SplitSeq(os.Getenv("COPILOT_SKILLS_DIRS"), ",") {
		add(path)
	}
	return roots
}
