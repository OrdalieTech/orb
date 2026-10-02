package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectMCPConfigRequiresTrust(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ConfigDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ConfigDirName, "mcp.json"), []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasTrustRequiringProjectResources(cwd) {
		t.Fatal("a project mcp.json does not ask for trust")
	}
}
