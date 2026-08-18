package config

import (
	"path/filepath"
	"testing"
)

func TestMCPServerSettersRoundTrip(t *testing.T) {
	root := t.TempDir()
	manager, err := NewSettingsManager(root, WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager.SetMCPServer("local", map[string]any{"command": "server", "args": []any{"--fast"}})
	manager.SetMCPServerEnabled("local", false)
	servers := manager.GetMCPServers()
	if servers["local"]["command"] != "server" || servers["local"]["enabled"] != false {
		t.Fatalf("servers = %#v", servers)
	}
	manager.SetMCPServerEnabled("local", true)
	if enabled := manager.GetMCPServers()["local"]["enabled"]; enabled != true {
		t.Fatalf("enabled = %#v", enabled)
	}
	manager.RemoveMCPServer("local")
	if remaining := manager.GetMCPServers(); len(remaining) != 0 {
		t.Fatalf("remaining = %#v", remaining)
	}
	if _, exists := manager.GetSettings()["mcpServers"]; exists {
		t.Fatal("an emptied mcpServers object was left behind")
	}
	if errors := manager.DrainErrors(); len(errors) != 0 {
		t.Fatalf("settings errors: %v", errors)
	}
}
