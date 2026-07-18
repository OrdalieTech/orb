package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoBoundHarnessRuntimeMissingResumePathCreatesFreshSessionAtExactPath(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
	beforeMetadata, ok := runtime.Session().Manager().HarnessMetadata()
	if !ok {
		t.Fatal("initial session is not harness-backed")
	}
	exactPath := filepath.Join(filepath.Dir(runtime.Session().Manager().GetSessionFile()), "missing-explicit.jsonl")

	result, err := runtime.SwitchSession(ctx, exactPath, nil)
	if err != nil || result.Cancelled {
		t.Fatalf("missing-path resume = %#v, %v", result, err)
	}
	manager := runtime.Session().Manager()
	if got := manager.GetSessionFile(); got != exactPath {
		t.Fatalf("fresh session path = %q, want exact requested path %q", got, exactPath)
	}
	afterMetadata, ok := manager.HarnessMetadata()
	if !ok {
		t.Fatal("fresh session is no longer harness-backed")
	}
	if afterMetadata.ID == "" || afterMetadata.ID == beforeMetadata.ID {
		t.Fatalf("fresh session id = %q, previous id %q", afterMetadata.ID, beforeMetadata.ID)
	}
	entries := manager.GetEntries()
	if len(entries) != 2 || entries[0].Type != "model_change" || entries[1].Type != "thinking_level_change" {
		t.Fatalf("fresh runtime bootstrap entries = %#v, want model and thinking changes", entries)
	}
	if _, statErr := os.Stat(exactPath); !os.IsNotExist(statErr) {
		t.Fatalf("missing resume path was materialized before an assistant response: %v", statErr)
	}
	if _, err := manager.AppendMessage(map[string]any{"role": "assistant", "content": "persist now"}); err != nil {
		t.Fatal(err)
	}
	wantJSONL, err := manager.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	storedJSONL, err := os.ReadFile(exactPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedJSONL, wantJSONL) {
		t.Fatalf("first assistant response did not flush the complete resumed session:\ngot  %s\nwant %s", storedJSONL, wantJSONL)
	}
}

func TestRepoBoundHarnessRuntimeEmptyResumePathInitializesExactFile(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
	exactPath := filepath.Join(filepath.Dir(runtime.Session().Manager().GetSessionFile()), "empty-explicit.jsonl")
	if err := os.WriteFile(exactPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.SwitchSession(ctx, exactPath, nil)
	if err != nil || result.Cancelled {
		t.Fatalf("empty-path resume = %#v, %v", result, err)
	}
	manager := runtime.Session().Manager()
	if got := manager.GetSessionFile(); got != exactPath {
		t.Fatalf("initialized session path = %q, want exact requested path %q", got, exactPath)
	}
	contents, err := os.ReadFile(exactPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) == 0 || contents[len(contents)-1] != '\n' || bytes.Count(contents, []byte{'\n'}) != 3 {
		t.Fatalf("initialized session file is not a header plus runtime bootstrap entries: %q", contents)
	}
	var header struct {
		Type      string `json:"type"`
		Version   int    `json:"version"`
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
	}
	lines := bytes.Split(bytes.TrimSuffix(contents, []byte{'\n'}), []byte{'\n'})
	if err := json.Unmarshal(lines[0], &header); err != nil {
		t.Fatalf("initialized session header is invalid JSON: %v", err)
	}
	if header.Type != "session" || header.Version != 3 || header.ID == "" || header.Timestamp == "" {
		t.Fatalf("initialized session header = %#v", header)
	}
	entries := manager.GetEntries()
	if len(entries) != 2 || entries[0].Type != "model_change" || entries[1].Type != "thinking_level_change" {
		t.Fatalf("initialized runtime bootstrap entries = %#v, want model and thinking changes", entries)
	}
}

func TestRepoBoundHarnessRuntimeInvalidResumePathStaysUnmodified(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
	beforeSession := runtime.Session()
	exactPath := filepath.Join(filepath.Dir(beforeSession.Manager().GetSessionFile()), "invalid-explicit.jsonl")
	original := []byte("not a pi session\n")
	if err := os.WriteFile(exactPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.SwitchSession(ctx, exactPath, nil)
	if err == nil || !strings.Contains(err.Error(), "Session file is not a valid pi session: "+exactPath) {
		t.Fatalf("invalid-path resume = %#v, %v", result, err)
	}
	if result.Cancelled {
		t.Fatal("invalid-path resume was reported as cancelled")
	}
	contents, readErr := os.ReadFile(exactPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(contents, original) {
		t.Fatalf("invalid session was modified: got %q, want %q", contents, original)
	}
	if runtime.Session() != beforeSession {
		t.Fatal("invalid session replaced the active runtime")
	}
}
