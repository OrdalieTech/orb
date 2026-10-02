package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPCLIRoundTrip(t *testing.T) {
	env := setupPackageCLI(t)
	code, stdout, stderr := runPackageCLI(t, []string{"mcp", "add", "local", "--env", "TOKEN=x", "--", "orb-missing-mcp-server", "--fast"})
	if code != 0 || !strings.Contains(stdout, `Added global MCP server "local"`) {
		t.Fatalf("add local: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, _, stderr = runPackageCLI(t, []string{"mcp", "add", "remote", "--url", "https://example.com/mcp", "--bearer-token-env-var", "REMOTE_TOKEN", "--exposure", "direct", "--description", "Remote docs"})
	if code != 0 {
		t.Fatalf("add remote: code=%d stderr=%q", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(env.agentDir, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	local, remote := file.MCPServers["local"], file.MCPServers["remote"]
	if local["command"] != "orb-missing-mcp-server" || local["env"].(map[string]any)["TOKEN"] != "x" ||
		remote["headers"].(map[string]any)["Authorization"] != "Bearer ${REMOTE_TOKEN}" || remote["exposure"] != "direct" || remote["description"] != "Remote docs" {
		t.Fatalf("mcp.json = %s", data)
	}
	if err := os.WriteFile(filepath.Join(env.agentDir, "mcp.json"), []byte(`{"mcpServers":{"local":{"command":"orb-missing-mcp-server","timeout":2},"remote":{"url":"https://example.com/mcp","enabled":false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = runPackageCLI(t, []string{"mcp", "list"})
	if code != 1 || !strings.Contains(stdout, "local: failed (codemode, global)\n  orb-missing-mcp-server") || !strings.Contains(stdout, "remote: disabled (codemode, global)") {
		t.Fatalf("list: code=%d stdout=%q", code, stdout)
	}
	code, stdout, _ = runPackageCLI(t, []string{"mcp", "list", "--json"})
	if code != 1 || !strings.Contains(stdout, `"state": "disabled"`) || !strings.Contains(stdout, `"errors": []`) {
		t.Fatalf("list --json: code=%d stdout=%q", code, stdout)
	}
	code, _, stderr = runPackageCLI(t, []string{"mcp", "add", "bad", "--url", "https://example.com", "--", "command"})
	if code != 1 || !strings.Contains(stderr, "Usage: orb mcp add") {
		t.Fatalf("invalid add: code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = runPackageCLI(t, []string{"mcp", "add", "bad", "--env", "A=b", "--url", "https://example.com"})
	if code != 1 || !strings.Contains(stderr, "--env only applies to stdio servers") {
		t.Fatalf("misplaced option: code=%d stderr=%q", code, stderr)
	}
	code, stdout, _ = runPackageCLI(t, []string{"mcp", "remove", "local"})
	if code != 0 || !strings.Contains(stdout, `Removed global MCP server "local"`) {
		t.Fatalf("remove: code=%d stdout=%q", code, stdout)
	}
	code, _, stderr = runPackageCLI(t, []string{"mcp", "remove", "ghost"})
	if code != 1 || !strings.Contains(stderr, `No global MCP server named "ghost"`) {
		t.Fatalf("remove unknown: code=%d stderr=%q", code, stderr)
	}
}
