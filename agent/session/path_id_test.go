package session

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultSessionDirEncodingAndCreation(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), "agent")
	cwd := filepath.Join(t.TempDir(), `one:two\three`)
	wantName := "--" + strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(strings.TrimPrefix(cwd, "/")) + "--"
	path, err := DefaultSessionDirPath(cwd, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(agentDir, "sessions", wantName) {
		t.Fatalf("default path = %q", path)
	}
	created, err := DefaultSessionDir(cwd, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if created != path {
		t.Fatalf("created path = %q, want %q", created, path)
	}
}
