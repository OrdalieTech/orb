package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindMostRecentSessionValidatesHeaderAndFiltersCWD(t *testing.T) {
	dir := t.TempDir()
	cwdA := filepath.Join(dir, "project-a")
	cwdB := filepath.Join(dir, "project-b")
	for _, cwd := range []string{cwdA, cwdB} {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	oldA := writeRecentFixture(t, dir, "old-a.jsonl", "a-old", cwdA, base)
	newA := writeRecentFixture(t, dir, "new-a.jsonl", "a-new", cwdA, base.Add(time.Minute))
	newB := writeRecentFixture(t, dir, "new-b.jsonl", "b-new", cwdB, base.Add(2*time.Minute))
	_ = oldA
	if err := os.WriteFile(filepath.Join(dir, "invalid.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := FindMostRecentSession(dir, ""); got != newB {
		t.Fatalf("unfiltered recent = %q, want %q", got, newB)
	}
	if got := FindMostRecentSession(dir, cwdA); got != newA {
		t.Fatalf("cwd-filtered recent = %q, want %q", got, newA)
	}
	if got := FindMostRecentSession(filepath.Join(dir, "missing"), cwdA); got != "" {
		t.Fatalf("missing directory recent = %q", got)
	}
}

func TestContinueRecentUsesCustomFilterAndPreservesRequestedCWD(t *testing.T) {
	dir := t.TempDir()
	cwdA := filepath.Join(dir, "project-a")
	cwdB := filepath.Join(dir, "project-b")
	for _, cwd := range []string{cwdA, cwdB} {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	want := writeRecentFixture(t, dir, "a.jsonl", "a", cwdA, base)
	writeRecentFixture(t, dir, "b.jsonl", "b", cwdB, base.Add(time.Minute))
	manager, err := ContinueRecent(cwdA, dir)
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetSessionFile() != want || manager.GetCWD() != cwdA {
		t.Fatalf("continued path=%q cwd=%q, want path=%q cwd=%q", manager.GetSessionFile(), manager.GetCWD(), want, cwdA)
	}

	agentDir := filepath.Join(dir, "agent")
	defaultDir, err := DefaultSessionDir(cwdA, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	writeRecentFixture(t, defaultDir, "moved.jsonl", "moved", cwdB, base.Add(2*time.Minute))
	defaultManager, err := ContinueRecent(cwdA, "", WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if defaultManager.GetSessionID() != "moved" {
		t.Fatalf("default directory unexpectedly filtered cwd: id=%q", defaultManager.GetSessionID())
	}
	if defaultManager.GetCWD() != cwdA {
		t.Fatalf("continue changed requested cwd to header cwd: %q", defaultManager.GetCWD())
	}
}

func TestReadSessionHeaderOnlyUsesFirst512Bytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oversized.jsonl")
	header := fmt.Sprintf(`{"type":"session","id":"s","padding":"%s"}`, strings.Repeat("x", 600))
	if err := os.WriteFile(path, []byte(header+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := FindMostRecentSession(dir, ""); got != "" {
		t.Fatalf("truncated 512-byte header was accepted: %q", got)
	}
}

func writeRecentFixture(t *testing.T, dir, name, id, cwd string, modified time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	content := fmt.Sprintf("{\"type\":\"session\",\"version\":3,\"id\":%q,\"timestamp\":\"2025-01-01T00:00:00.000Z\",\"cwd\":%q}\n", id, cwd)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	return path
}
