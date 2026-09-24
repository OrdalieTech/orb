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
	roots, seen := []string{}, map[string]bool{}
	add := func(base string, suffix ...string) {
		if base == "" {
			return
		}
		path := cleanPath(home, filepath.Join(append([]string{base}, suffix...)...))
		if !seen[path] {
			seen[path] = true
			roots = append(roots, path)
		}
	}

	add(envDir(home, "CLAUDE_CONFIG_DIR", homePath(home, ".claude")), "skills")
	add(envDir(home, "CODEX_HOME", homePath(home, ".codex")), "skills")
	add(envDir(home, "OPENCODE_CONFIG_DIR", ""), "skills")
	add(envDir(home, "XDG_CONFIG_HOME", homePath(home, ".config")), "opencode", "skills")
	add(envDir(home, "GEMINI_CLI_HOME", home), ".gemini", "skills")
	add(home, ".cursor", "skills")
	add(envDir(home, "COPILOT_HOME", homePath(home, ".copilot")), "skills")
	for path := range strings.SplitSeq(os.Getenv("COPILOT_SKILLS_DIRS"), ",") {
		add(path)
	}
	return roots
}

// Managed returns the entries directly below a User root that the owning tool
// fills for itself; they hold no user skills. Claude Code mirrors claude.ai
// skills into skills/synced once per signed-in account and loads only the
// active account's copy, so scanning the mirror would import every synced
// skill once per account.
func Managed(home, root string) []string {
	if claude := envDir(home, "CLAUDE_CONFIG_DIR", homePath(home, ".claude")); claude != "" && root == filepath.Join(claude, "skills") {
		return []string{"synced"}
	}
	return nil
}

func cleanPath(home, path string) string {
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

func envDir(home, env, fallback string) string {
	if path := os.Getenv(env); path != "" {
		return cleanPath(home, path)
	}
	return cleanPath(home, fallback)
}

func homePath(home, name string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, name)
}
