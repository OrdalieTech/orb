package session

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	EnvAgentDir   = "PI_CODING_AGENT_DIR"
	EnvSessionDir = "PI_CODING_AGENT_SESSION_DIR"
)

func normalizePath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	if strings.HasPrefix(path, "file://") {
		if parsed, err := url.Parse(path); err == nil && (parsed.Host == "" || parsed.Host == "localhost") {
			if decoded, err := url.PathUnescape(parsed.EscapedPath()); err == nil {
				return filepath.FromSlash(decoded)
			}
		}
	}
	return path
}

func resolvePath(path string, bases ...string) (string, error) {
	path = normalizePath(path)
	if !filepath.IsAbs(path) && len(bases) > 0 {
		path = filepath.Join(normalizePath(bases[0]), path)
	}
	return filepath.Abs(path)
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

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
