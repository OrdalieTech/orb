package main

import (
	"strings"
	"testing"
)

func TestMCPCLIRoundTrip(t *testing.T) {
	setupPackageCLI(t)
	code, stdout, stderr := runPackageCLI(t, []string{"mcp", "add", "local", "--env", "TOKEN=x", "--", "server-bin", "--fast"})
	if code != 0 || !strings.Contains(stdout, "Added local") {
		t.Fatalf("add local: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, _, stderr = runPackageCLI(t, []string{"mcp", "add", "remote", "--url", "https://example.com/mcp", "--header", "Authorization=Bearer x", "--disabled"})
	if code != 0 {
		t.Fatalf("add remote: code=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr = runPackageCLI(t, []string{"mcp", "list"})
	if code != 0 || stderr != "" ||
		!strings.Contains(stdout, "local\tstdio\ton\tserver-bin --fast") ||
		!strings.Contains(stdout, "remote\thttp\toff\thttps://example.com/mcp") {
		t.Fatalf("list: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, _ = runPackageCLI(t, []string{"mcp", "get", "local"})
	if code != 0 || !strings.Contains(stdout, `"server-bin"`) || !strings.Contains(stdout, `"TOKEN": "x"`) {
		t.Fatalf("get: code=%d stdout=%q", code, stdout)
	}
	code, _, _ = runPackageCLI(t, []string{"mcp", "enable", "remote"})
	if code != 0 {
		t.Fatal("enable failed")
	}
	code, stdout, _ = runPackageCLI(t, []string{"mcp", "list"})
	if code != 0 || !strings.Contains(stdout, "remote\thttp\ton\t") {
		t.Fatalf("enabled list: %q", stdout)
	}
	// A server with both a command and a URL must be rejected by the same
	// validation the session applies.
	code, _, stderr = runPackageCLI(t, []string{"mcp", "add", "bad", "--url", "https://example.com", "--", "command"})
	if code != 1 || !strings.Contains(stderr, "exactly one of command or url") {
		t.Fatalf("invalid add: code=%d stderr=%q", code, stderr)
	}
	code, stdout, _ = runPackageCLI(t, []string{"mcp", "remove", "local"})
	if code != 0 || !strings.Contains(stdout, "Removed local") {
		t.Fatalf("remove: code=%d stdout=%q", code, stdout)
	}
	code, stdout, _ = runPackageCLI(t, []string{"mcp", "list"})
	if code != 0 || strings.Contains(stdout, "local") {
		t.Fatalf("post-remove list: %q", stdout)
	}
	code, _, stderr = runPackageCLI(t, []string{"mcp", "remove", "ghost"})
	if code != 1 || !strings.Contains(stderr, `Unknown MCP server "ghost"`) {
		t.Fatalf("remove unknown: code=%d stderr=%q", code, stderr)
	}
}
