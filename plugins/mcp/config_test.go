package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMergesProjectOverGlobalOnlyWhenTrusted(t *testing.T) {
	agentDir, cwd := t.TempDir(), t.TempDir()
	writeConfig(t, GlobalPath(agentDir), `{"mcpServers":{
		"docs":{"url":"https://example.com/mcp","description":"Docs"},
		"files":{"command":"files","enabled":false},
		"bad name":{"command":"x"},
		"broken":{"url":"ftp://example.com"}
	}}`)
	writeConfig(t, ProjectPath(cwd), `{"mcpServers":{"docs":{"command":"local-docs"},"keyed":{"url":"https://example.com","auth":{"provider":"anthropic"}}}}`)

	entries, problems := Load(agentDir, cwd, false)
	if len(entries) != 2 || entries[0].Name != "docs" || entries[0].Config.URL == "" || entries[1].Config.IsEnabled() {
		t.Fatalf("untrusted entries = %#v", entries)
	}
	if len(problems) != 2 || !strings.Contains(problems[0], `invalid server name "bad name"`) || !strings.Contains(problems[1], "http or https") {
		t.Fatalf("problems = %q", problems)
	}

	entries, problems = Load(agentDir, cwd, true)
	if entries[0].Name != "docs" || entries[0].Config.Command != "local-docs" || entries[0].Scope != "project" {
		t.Fatalf("project entry did not replace the global one: %#v", entries[0])
	}
	if len(problems) != 3 || !strings.Contains(problems[2], "auth is only allowed in the global mcp.json") {
		t.Fatalf("problems = %q", problems)
	}
}

func TestLoadRejectsServersSharingANamespace(t *testing.T) {
	agentDir := t.TempDir()
	writeConfig(t, GlobalPath(agentDir), `{"mcpServers":{"my-server":{"command":"a"},"my_server":{"command":"b"}}}`)
	entries, problems := Load(agentDir, t.TempDir(), false)
	if len(entries) != 1 || len(problems) != 1 || !strings.Contains(problems[0], `"my_server" conflicts with "my-server"`) {
		t.Fatalf("entries = %#v, problems = %q", entries, problems)
	}
}

func TestAddAndRemoveServerKeepOtherContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	writeConfig(t, path, "{\n    \"other\": true,\n    \"mcpServers\": {\"a\": {\"command\": \"a\", \"custom\": 1}}\n}\n")
	if replaced, err := AddServer(path, "b", ServerConfig{URL: "https://example.com"}); err != nil || replaced {
		t.Fatalf("add: %v, replaced %v", err, replaced)
	}
	if replaced, err := AddServer(path, "b", ServerConfig{URL: "https://example.org"}); err != nil || !replaced {
		t.Fatalf("replace: %v, replaced %v", err, replaced)
	}
	if removed, err := RemoveServer(path, "missing"); err != nil || removed {
		t.Fatalf("remove missing: %v, %v", err, removed)
	}
	data, _ := os.ReadFile(path)
	want := "{\n    \"other\": true,\n    \"mcpServers\": {\n        \"a\": {\n            \"command\": \"a\",\n            \"custom\": 1\n        },\n        \"b\": {\n            \"url\": \"https://example.org\"\n        }\n    }\n}\n"
	if string(data) != want {
		t.Fatalf("file =\n%s", data)
	}
	if removed, err := RemoveServer(path, "b"); err != nil || !removed {
		t.Fatalf("remove: %v, %v", err, removed)
	}
}

func TestProjectConfigDoesNotReadOrWritePi(t *testing.T) {
	agentDir, cwd := t.TempDir(), t.TempDir()
	piPath := filepath.Join(cwd, ".pi", "mcp.json")
	sentinel := `{"mcpServers":{"pi-only":{"command":"pi-only"}}}`
	writeConfig(t, piPath, sentinel)
	entries, problems := Load(agentDir, cwd, true)
	if len(entries) != 0 || len(problems) != 0 {
		t.Fatalf("Pi MCP config discovered: %v, %v", entries, problems)
	}
	if _, err := AddServer(ProjectPath(cwd), "orb-only", ServerConfig{Command: "orb-only"}); err != nil {
		t.Fatal(err)
	}
	if got, want := ProjectPath(cwd), filepath.Join(cwd, ".orb", "mcp.json"); got != want {
		t.Fatalf("MCP path = %q, want %q", got, want)
	}
	if got, err := os.ReadFile(piPath); err != nil || string(got) != sentinel {
		t.Fatalf("Pi MCP config changed: %q, %v", got, err)
	}
	entries, problems = Load(agentDir, cwd, true)
	if len(entries) != 1 || entries[0].Name != "orb-only" || len(problems) != 0 {
		t.Fatalf("Orb MCP config = %v, %v", entries, problems)
	}
}
