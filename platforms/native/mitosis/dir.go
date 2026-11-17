package mitosis

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/OrdalieTech/orb/agent/config"
)

// ValidName reports whether s can name a Mitosis profile or peer alias.
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

// Dir is a Mitosis profile's directory: under ORB_MITOSIS_HOME, else beside an
// explicit agent dir, else ~/.orb/mitosis.
func Dir(profile string) (string, error) {
	if !ValidName(profile) {
		return "", errors.New("invalid mitosis profile")
	}
	if root := os.Getenv("ORB_MITOSIS_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("ORB_MITOSIS_HOME must be absolute")
		}
		return filepath.Join(root, profile), nil
	}
	if os.Getenv(config.EnvAgentDir) != "" {
		agentDir, err := config.GetAgentDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(agentDir, "mitosis", profile), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".orb", "mitosis", profile), nil
}
