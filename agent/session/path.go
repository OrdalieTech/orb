package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/OrdalieTech/orb/internal/nodepath"
)

const (
	EnvAgentDir   = "PI_CODING_AGENT_DIR"
	EnvSessionDir = "PI_CODING_AGENT_SESSION_DIR"
)

func normalizePath(path string) string {
	path = nodepath.NormalizeShellPath(path)
	if path == "~" || strings.HasPrefix(path, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`)) {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	if strings.HasPrefix(path, "file://") {
		if converted, err := nodepath.FileURLToPath(path); err == nil {
			return converted
		}
	}
	return path
}

func resolvePath(path string) (string, error) {
	return filepath.Abs(normalizePath(path))
}

func defaultAgentDir() (string, error) {
	if configured := os.Getenv(EnvAgentDir); configured != "" {
		return resolvePath(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

// DefaultSessionDirPath computes the cwd-specific directory without creating
// it. The replacement rules intentionally mirror the cross-platform regular
// expressions in upstream.
func DefaultSessionDirPath(cwd, agentDir string) (string, error) {
	resolvedCWD, err := resolvePath(cwd)
	if err != nil {
		return "", err
	}
	if agentDir == "" {
		agentDir, err = defaultAgentDir()
		if err != nil {
			return "", err
		}
	}
	resolvedAgentDir, err := resolvePath(agentDir)
	if err != nil {
		return "", err
	}
	encoded := resolvedCWD
	if strings.HasPrefix(encoded, "/") || strings.HasPrefix(encoded, "\\") {
		encoded = encoded[1:]
	}
	encoded = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(encoded)
	return filepath.Join(resolvedAgentDir, "sessions", "--"+encoded+"--"), nil
}

// DefaultSessionDir computes and creates the cwd-specific directory.
func DefaultSessionDir(cwd, agentDir string) (string, error) {
	dir, err := DefaultSessionDirPath(cwd, agentDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
