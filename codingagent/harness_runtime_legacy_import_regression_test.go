package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

func TestRepoBoundHarnessRuntimeMigratesLegacyResumeAndImport(t *testing.T) {
	operations := []struct {
		name string
		run  func(context.Context, *AgentSessionRuntime, string) (extensions.SessionReplacementResult, error)
	}{
		{
			name: "resume",
			run: func(ctx context.Context, runtime *AgentSessionRuntime, path string) (extensions.SessionReplacementResult, error) {
				return runtime.SwitchSession(ctx, path, nil)
			},
		},
		{
			name: "import",
			run: func(ctx context.Context, runtime *AgentSessionRuntime, path string) (extensions.SessionReplacementResult, error) {
				return runtime.ImportFromJSONL(ctx, path, "")
			},
		},
	}

	for _, version := range []int{1, 2} {
		for _, operation := range operations {
			t.Run(fmt.Sprintf("v%d/%s", version, operation.name), func(t *testing.T) {
				ctx := context.Background()
				cwd := t.TempDir()
				runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
				sourcePath := filepath.Join(t.TempDir(), fmt.Sprintf("legacy-v%d.jsonl", version))
				if err := os.WriteFile(sourcePath, legacyRuntimeSessionJSONL(cwd, version), 0o600); err != nil {
					t.Fatal(err)
				}

				result, err := operation.run(ctx, runtime, sourcePath)
				if err != nil || result.Cancelled {
					t.Fatalf("v%d %s = %#v, %v", version, operation.name, result, err)
				}
				assertLegacyRuntimeMigrated(t, runtime, version)
			})
		}
	}
}

func TestRepoBoundHarnessRuntimeInPlaceImportDoesNotRewriteSource(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
	sourcePath := filepath.Join(runtime.Session().Manager().GetSessionDir(), "already-local.jsonl")
	content := stableRuntimeSessionJSONL(cwd, "already-local")
	if err := os.WriteFile(sourcePath, content, 0o700); err != nil {
		t.Fatal(err)
	}
	wantModTime := time.Unix(946684800, 0)
	if err := os.Chtimes(sourcePath, wantModTime, wantModTime); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.ImportFromJSONL(ctx, sourcePath, "")
	if err != nil || result.Cancelled {
		t.Fatalf("in-place import = %#v, %v", result, err)
	}
	if got := runtime.Session().Manager().GetSessionFile(); got != sourcePath {
		t.Fatalf("imported session file = %q, want %q", got, sourcePath)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(wantModTime) {
		t.Fatalf("in-place import rewrote source: mtime = %s, want %s", info.ModTime(), wantModTime)
	}
	gotContent, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotContent, content) {
		t.Fatalf("in-place import changed source bytes:\ngot  %s\nwant %s", gotContent, content)
	}
}

func TestRepoBoundHarnessRuntimeImportPreservesCopiedFileMode(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
	sourcePath := filepath.Join(t.TempDir(), "executable-session.jsonl")
	if err := os.WriteFile(sourcePath, stableRuntimeSessionJSONL(cwd, "copy-mode"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourcePath, 0o500); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.ImportFromJSONL(ctx, sourcePath, "")
	if err != nil || result.Cancelled {
		t.Fatalf("copied import = %#v, %v", result, err)
	}
	destination := runtime.Session().Manager().GetSessionFile()
	if destination == sourcePath {
		t.Fatalf("test import did not copy outside source path %q", sourcePath)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o500 {
		t.Fatalf("copied import mode = %#o, want source mode %#o", got, os.FileMode(0o500))
	}
}

func legacyRuntimeSessionJSONL(cwd string, version int) []byte {
	header := fmt.Sprintf(`{"type":"session","id":"legacy-v%d","timestamp":"2026-07-18T00:00:00.000Z","cwd":%q}`, version, cwd)
	message := `{"type":"message","timestamp":"2026-07-18T00:00:01.000Z","message":{"content":"legacy","role":"hookMessage"}}`
	if version == 2 {
		header = fmt.Sprintf(`{"type":"session","version":2,"id":"legacy-v2","timestamp":"2026-07-18T00:00:00.000Z","cwd":%q}`, cwd)
		message = `{"type":"message","id":"legacy-message","parentId":null,"timestamp":"2026-07-18T00:00:01.000Z","message":{"content":"legacy","role":"hookMessage"}}`
	}
	return []byte(header + "\n" + message + "\n")
}

func stableRuntimeSessionJSONL(cwd, id string) []byte {
	return []byte(fmt.Sprintf(
		"{\"type\":\"session\",\"version\":3,\"id\":%q,\"timestamp\":\"2026-07-18T00:00:00.000Z\",\"cwd\":%q}\n"+
			"{\"type\":\"message\",\"id\":\"user\",\"parentId\":null,\"timestamp\":\"2026-07-18T00:00:01.000Z\",\"message\":{\"role\":\"user\",\"content\":\"keep\"}}\n"+
			"{\"type\":\"thinking_level_change\",\"id\":\"thinking\",\"parentId\":\"user\",\"timestamp\":\"2026-07-18T00:00:02.000Z\",\"thinkingLevel\":\"off\"}\n",
		id, cwd,
	))
}

func assertLegacyRuntimeMigrated(t *testing.T, runtime *AgentSessionRuntime, sourceVersion int) {
	t.Helper()
	manager := runtime.Session().Manager()
	header := manager.GetHeader()
	if header == nil || header.Version == nil || *header.Version != 3 {
		t.Fatalf("migrated v%d header = %#v, want version 3", sourceVersion, header)
	}
	var migratedMessageID string
	for _, entry := range manager.GetEntries() {
		if entry.Type != "message" {
			continue
		}
		var message struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(entry.Message, &message); err != nil {
			t.Fatal(err)
		}
		if message.Role == "custom" {
			migratedMessageID = entry.ID
			break
		}
	}
	if migratedMessageID == "" {
		t.Fatalf("v%d hookMessage role was not migrated to custom: %#v", sourceVersion, manager.GetEntries())
	}
	if sourceVersion == 2 && migratedMessageID != "legacy-message" {
		t.Fatalf("v2 migrated message id = %q, want preserved id", migratedMessageID)
	}

	stored, err := os.ReadFile(manager.GetSessionFile())
	if err != nil {
		t.Fatal(err)
	}
	firstLine, _, ok := bytes.Cut(stored, []byte{'\n'})
	if !ok {
		t.Fatalf("migrated session has no header line: %q", stored)
	}
	var storedHeader struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(firstLine, &storedHeader); err != nil {
		t.Fatal(err)
	}
	if storedHeader.Version != 3 {
		t.Fatalf("persisted migrated v%d header version = %d, want 3", sourceVersion, storedHeader.Version)
	}
}
