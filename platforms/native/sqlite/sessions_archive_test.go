package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine/harness"
)

func createTestSession(t *testing.T, r *Sessions, id, cwd, name, userMsg string) *harness.Session {
	t.Helper()
	ctx := context.Background()
	s, err := r.Create(ctx, harness.SessionCreateOptions{ID: id, CWD: cwd})
	if err != nil {
		t.Fatalf("create session %s: %v", id, err)
	}
	m, err := session.FromHarnessStorage(s.Storage(), session.WithHarnessRepo(r))
	if err != nil {
		t.Fatalf("from harness storage %s: %v", id, err)
	}
	if name != "" {
		if _, err := m.AppendSessionInfo(name); err != nil {
			t.Fatalf("append session info %s: %v", id, err)
		}
	}
	if userMsg != "" {
		if _, err := m.AppendMessage(map[string]any{"role": "user", "content": userMsg}); err != nil {
			t.Fatalf("append message %s: %v", id, err)
		}
	}
	return s
}

func TestSchemaV5MigrationDefaults(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "v5.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 6 {
		t.Fatalf("expected user_version=6, got %d", version)
	}

	r := db.Sessions("default")
	createTestSession(t, r, "sess-1", "/tmp/work", "first", "hello world")

	var archived, localOnly int
	if err := db.QueryRowContext(ctx, "SELECT archived, local_only FROM sessions WHERE id='sess-1'").Scan(&archived, &localOnly); err != nil {
		t.Fatal(err)
	}
	if archived != 0 || localOnly != 0 {
		t.Fatalf("expected archived=0 local_only=0, got archived=%d local_only=%d", archived, localOnly)
	}

	// Simulate downgrade to v4 and reopen to verify idempotent migration to v5.
	if _, err := db.ExecContext(ctx, "ALTER TABLE sessions DROP COLUMN local_only; PRAGMA user_version=4;"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 6 {
		t.Fatalf("expected migrated user_version=6, got %d", version)
	}
	if err := db.QueryRowContext(ctx, "SELECT archived, local_only FROM sessions WHERE id='sess-1'").Scan(&archived, &localOnly); err != nil {
		t.Fatal(err)
	}
	if archived != 0 || localOnly != 0 {
		t.Fatalf("expected migrated archived=0 local_only=0, got archived=%d local_only=%d", archived, localOnly)
	}
}

func TestArchiveSessionAndListAndLatest(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state", "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	r := db.Sessions("default")
	createTestSession(t, r, "s1", "/tmp/a", "alpha", "first message")
	time.Sleep(5 * time.Millisecond)
	createTestSession(t, r, "s2", "/tmp/b", "beta", "second message")

	// Missing ID returns sql.ErrNoRows.
	if err := r.ArchiveSession(ctx, "does-not-exist", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}

	// Latest initially returns s2.
	latestID, _, err := r.Latest(ctx)
	if err != nil || latestID != "s2" {
		t.Fatalf("expected latest=s2, got %q err=%v", latestID, err)
	}

	// Archive s2.
	if err := r.ArchiveSession(ctx, "s2", true); err != nil {
		t.Fatal(err)
	}

	// Latest now skips archived s2 and returns s1.
	latestID, _, err = r.Latest(ctx)
	if err != nil || latestID != "s1" {
		t.Fatalf("expected latest=s1 after archiving s2, got %q err=%v", latestID, err)
	}

	// List skips archived s2.
	list, err := r.List(ctx, harness.SessionListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "s1" {
		t.Fatalf("expected List to return only [s1], got %+v", list)
	}

	// Unarchive s2.
	if err := r.ArchiveSession(ctx, "s2", false); err != nil {
		t.Fatal(err)
	}
	latestID, _, err = r.Latest(ctx)
	if err != nil || latestID != "s2" {
		t.Fatalf("expected latest=s2 after unarchiving, got %q err=%v", latestID, err)
	}
}

func TestCatalogArchiveAndLocalOnlyFiltering(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state", "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	r := db.Sessions("default")
	createTestSession(t, r, "s1", "/tmp/p", "active-public", "msg1")
	createTestSession(t, r, "s2", "/tmp/p", "active-local", "msg2")
	createTestSession(t, r, "s3", "/tmp/p", "archived-public", "msg3")
	createTestSession(t, r, "s4", "/tmp/p", "archived-local", "msg4")

	if err := r.SetSessionLocalOnly(ctx, "missing", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
	if err := r.SetSessionLocalOnly(ctx, "s2", true); err != nil {
		t.Fatal(err)
	}
	if err := r.ArchiveSession(ctx, "s3", true); err != nil {
		t.Fatal(err)
	}
	if err := r.ArchiveSession(ctx, "s4", true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSessionLocalOnly(ctx, "s4", true); err != nil {
		t.Fatal(err)
	}

	// Default (Archived: false, IncludeAll: false, ExcludeLocalOnly: false) -> s1, s2.
	page, err := r.Catalog(ctx, CatalogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 2 {
		t.Fatalf("expected 2 active sessions, got %d", len(page.Sessions))
	}

	// Archived: true -> s3, s4.
	page, err = r.Catalog(ctx, CatalogQuery{Archived: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 2 {
		t.Fatalf("expected 2 archived sessions, got %d", len(page.Sessions))
	}
	for _, e := range page.Sessions {
		if !e.Archived {
			t.Fatalf("expected entry %s Archived=true", e.ID)
		}
	}

	// IncludeAll: true -> all 4.
	page, err = r.Catalog(ctx, CatalogQuery{IncludeAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 4 {
		t.Fatalf("expected 4 sessions with IncludeAll, got %d", len(page.Sessions))
	}

	// ExcludeLocalOnly: true -> s1 only (active + not local_only).
	page, err = r.Catalog(ctx, CatalogQuery{ExcludeLocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].ID != "s1" || page.Sessions[0].LocalOnly {
		t.Fatalf("expected only [s1], got %+v", page.Sessions)
	}

	// IncludeAll + ExcludeLocalOnly -> s1, s3.
	page, err = r.Catalog(ctx, CatalogQuery{IncludeAll: true, ExcludeLocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 2 {
		t.Fatalf("expected 2 sessions with IncludeAll+ExcludeLocalOnly, got %d", len(page.Sessions))
	}

	// Cursor pagination with IncludeAll and cursor validation mismatch.
	p1, err := r.Catalog(ctx, CatalogQuery{IncludeAll: true, Limit: 2})
	if err != nil || p1.Next == "" || len(p1.Sessions) != 2 {
		t.Fatalf("expected first page of 2 with cursor, got len=%d next=%q err=%v", len(p1.Sessions), p1.Next, err)
	}
	// Using IncludeAll cursor on a query without IncludeAll must fail.
	if _, err := r.Catalog(ctx, CatalogQuery{Limit: 2, Cursor: p1.Next}); err == nil {
		t.Fatal("expected cursor mismatch error when IncludeAll differs")
	}
	p2, err := r.Catalog(ctx, CatalogQuery{IncludeAll: true, Limit: 2, Cursor: p1.Next})
	if err != nil || len(p2.Sessions) != 2 {
		t.Fatalf("expected second page of 2, got len=%d err=%v", len(p2.Sessions), err)
	}
}

func TestMatchSessionsAndArchiveMatching(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state", "match.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	r := db.Sessions("default")
	createTestSession(t, r, "abc-111", "/tmp/scratch", "Deploy Script", "fix prod")
	createTestSession(t, r, "abc-222", "/tmp/scratch/sub", "Cleanup", "remove temp files")
	createTestSession(t, r, "xyz-999", "/tmp/scratch2", "Other", "unrelated work")

	// Set explicit modified timestamps for deterministic Before testing.
	tOld := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	tMid := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	tNew := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for id, ts := range map[string]time.Time{"abc-111": tOld, "abc-222": tMid, "xyz-999": tNew} {
		if _, err := db.ExecContext(ctx, "UPDATE sessions SET modified=? WHERE id=?", ts.Format(time.RFC3339Nano), id); err != nil {
			t.Fatal(err)
		}
	}

	// Exact ID and unambiguous prefix.
	got, err := r.MatchSessions(ctx, SessionFilter{IDs: []string{"abc-111", "xyz"}})
	if err != nil || len(got) != 2 {
		t.Fatalf("expected 2 matches for exact+prefix, got %d err=%v", len(got), err)
	}

	// Ambiguous prefix returns error.
	if _, err := r.MatchSessions(ctx, SessionFilter{IDs: []string{"abc"}}); err == nil {
		t.Fatal("expected error for ambiguous prefix 'abc'")
	}

	// CWD matches exact and subdir, NOT /tmp/scratch2.
	got, err = r.MatchSessions(ctx, SessionFilter{CWD: "/tmp/scratch"})
	if err != nil || len(got) != 2 {
		t.Fatalf("expected 2 matches for /tmp/scratch, got %+v err=%v", got, err)
	}

	// Case-insensitive substring match against name/preview/cwd/id.
	got, err = r.MatchSessions(ctx, SessionFilter{Match: "DEPLOY"})
	if err != nil || len(got) != 1 || got[0].ID != "abc-111" {
		t.Fatalf("expected [abc-111] for match DEPLOY, got %+v err=%v", got, err)
	}
	got, err = r.MatchSessions(ctx, SessionFilter{Match: "TEMP FILES"})
	if err != nil || len(got) != 1 || got[0].ID != "abc-222" {
		t.Fatalf("expected [abc-222] for preview match, got %+v err=%v", got, err)
	}

	// Before timestamp filter.
	got, err = r.MatchSessions(ctx, SessionFilter{Before: time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil || len(got) != 2 {
		t.Fatalf("expected 2 matches before 2025-07-01, got %+v err=%v", got, err)
	}

	// ArchiveMatching archives matched sessions and returns them with Archived=true.
	archived, err := r.ArchiveMatching(ctx, SessionFilter{CWD: "/tmp/scratch"}, true)
	if err != nil || len(archived) != 2 {
		t.Fatalf("expected 2 archived sessions, got %+v err=%v", archived, err)
	}
	for _, e := range archived {
		if !e.Archived {
			t.Fatalf("expected returned entry %s to have Archived=true", e.ID)
		}
	}

	// Idempotency: re-running with Archived=false filter matches 0.
	activeOnly := false
	archivedAgain, err := r.ArchiveMatching(ctx, SessionFilter{CWD: "/tmp/scratch", Archived: &activeOnly}, true)
	if err != nil || len(archivedAgain) != 0 {
		t.Fatalf("expected 0 active sessions left in /tmp/scratch, got %+v err=%v", archivedAgain, err)
	}

	// Unarchive via ArchiveMatching with Limit=1.
	archivedOnly := true
	unarchived, err := r.ArchiveMatching(ctx, SessionFilter{CWD: "/tmp/scratch", Archived: &archivedOnly, Limit: 1}, false)
	if err != nil || len(unarchived) != 1 || unarchived[0].Archived {
		t.Fatalf("expected 1 unarchived session, got %+v err=%v", unarchived, err)
	}
}
