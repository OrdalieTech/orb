package codingagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

type f12ExportJSONLFixture struct {
	Schema       int    `json:"schema"`
	NowUnixMilli int64  `json:"nowUnixMilli"`
	Input        string `json:"input"`
	Expected     string `json:"expected"`
}

func TestF12JSONLExportMatchesUpstreamBytes(t *testing.T) {
	fixtureBytes, err := os.ReadFile(filepath.Join("..", "conformance", "fixtures", "F12-export-jsonl", "case.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture f12ExportJSONLFixture
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Schema != 1 {
		t.Fatalf("fixture schema = %d, want 1", fixture.Schema)
	}

	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.jsonl")
	if err := os.WriteFile(sourcePath, []byte(fixture.Input), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.Open(sourcePath, filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: agent.NewAgent(nil), SessionManager: manager, Settings: settings,
		Clock: func() int64 { return fixture.NowUnixMilli },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)

	outputPath := filepath.Join(root, "export.jsonl")
	if _, err := runtime.ExportJSONL(outputPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != fixture.Expected {
		t.Fatalf("JSONL export differs from upstream\nwant: %q\n got: %q", fixture.Expected, got)
	}
}
