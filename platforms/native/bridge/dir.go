package bridge

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/OrdalieTech/orb/agent/config"
)

// ValidName reports whether s can name a Bridge profile or peer alias.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > 48 {
		return false
	}
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// Dir is a Bridge profile's directory: under ORB_BRIDGE_HOME, else beside an
// explicit agent dir, else ~/.orb/bridge.
func Dir(profile string) (string, error) {
	if !ValidName(profile) {
		return "", errors.New("invalid bridge profile")
	}
	if root := os.Getenv("ORB_BRIDGE_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("ORB_BRIDGE_HOME must be absolute")
		}
		return filepath.Join(root, profile), nil
	}
	if os.Getenv(config.EnvAgentDir) != "" {
		agentDir, err := config.GetAgentDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(agentDir, "bridge", profile), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".orb", "bridge", profile), nil
}
