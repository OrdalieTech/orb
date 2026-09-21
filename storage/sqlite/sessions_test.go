package sqlite

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine/harness"
)

func TestSessionJournalPersistsAndFencesStaleWriter(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state", "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo := db.Sessions("personal")
	s, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "conversation", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := repo.Open(ctx, s.Metadata())
	if err != nil {
		t.Fatal(err)
	}
	m, err := session.FromHarnessStorage(s.Storage(), session.WithHarnessRepo(repo))
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsPersisted() || m.GetSessionFile() != "" {
		t.Fatal("database session must be durable without a pretend file")
	}
	_, revision := m.AggregateStats()
	_ = m.GetHeader()
	if _, next := m.AggregateStats(); next != revision {
		t.Fatal("reading an unchanged journal advanced its revision")
	}
	id, err := m.AppendMessage(map[string]any{"role": "user", "content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AppendCustomEntry("test", map[string]any{"unknown": true}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.AppendSessionInfo("A saved conversation"); err != nil {
		t.Fatal(err)
	}
	if err = m.Branch(id); err != nil {
		t.Fatal(err)
	}
	if err = stale.Storage().AppendEntry(harness.SessionTreeEntry{ID: "stale", Type: "custom", Timestamp: "2026-09-21T00:00:00.000Z"}); err == nil {
		t.Fatal("stale writer accepted")
	}
	if len(stale.Storage().Entries()) != 0 {
		t.Fatal("failed durable write changed memory")
	}
	want, err := m.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	opened, err := repo.Open(ctx, s.Metadata())
	if err != nil {
		t.Fatal(err)
	}
	got, err := opened.Storage().(harness.ByteSessionStorage).Bytes()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("reopened journal differs: %v", err)
	}
	if leaf, err := opened.Storage().LeafID(); err != nil || leaf == nil || *leaf != id {
		t.Fatalf("lost branch: %v %v", leaf, err)
	}
	listed, err := repo.List(ctx, harness.SessionListOptions{CWD: m.GetCWD()})
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v %v", listed, err)
	}
	if _, err := db.Sessions("other").Open(ctx, s.Metadata()); err == nil {
		t.Fatal("namespace leaked")
	}
	if _, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "conversation", CWD: m.GetCWD()}); err == nil {
		t.Fatal("duplicate session replaced history")
	}
	fork, err := repo.Fork(ctx, s.Metadata(), harness.SessionForkOptions{SessionCreateOptions: harness.SessionCreateOptions{ID: "fork", CWD: m.GetCWD()}, EntryID: id, Position: harness.ForkAt})
	if err != nil || len(fork.Storage().Entries()) != 1 {
		t.Fatalf("fork: %v", err)
	}
	if err := repo.Delete(ctx, fork.Metadata()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Open(ctx, fork.Metadata()); err == nil {
		t.Fatal("deleted session reopened")
	}
}

func BenchmarkSessionAppend10K(b *testing.B) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(b.TempDir(), "state", "orb.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	r := db.Sessions("personal")
	s, err := r.Create(ctx, harness.SessionCreateOptions{ID: "bench", CWD: "/project"})
	if err != nil {
		b.Fatal(err)
	}
	_, err = db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000)
 INSERT INTO entries SELECT 'personal','bench',x,printf('%08d',x),
 json_object('type','custom','id',printf('%08d',x),'parentId',CASE WHEN x=1 THEN NULL ELSE printf('%08d',x-1) END,'timestamp','2026-09-21T00:00:00.000Z','customType','benchmark','data',x) FROM n;
 UPDATE sessions SET revision=10000 WHERE id='bench'`)
	if err != nil {
		b.Fatal(err)
	}
	s, err = r.Open(ctx, s.Metadata())
	if err != nil {
		b.Fatal(err)
	}
	m, err := session.FromHarnessStorage(s.Storage(), session.WithHarnessRepo(r))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := m.AppendCustomEntry("benchmark", true); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCatalogPagesAndImportAreStable(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state", "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	r := db.Sessions("personal")
	for i := 0; i < 7; i++ {
		_, err = r.Import(ctx, []byte(fmt.Sprintf(`{"type":"session","version":3,"id":"s%d","timestamp":"2026-09-21T00:00:00.000Z","cwd":"/project","future":true}`+"\n", i)))
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := r.Catalog(ctx, CatalogQuery{Limit: 3, Cursor: cursor, CWD: "/project"})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range page.Sessions {
			if seen[s.ID] {
				t.Fatal("duplicate page entry")
			}
			seen[s.ID] = true
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(seen) != 7 {
		t.Fatalf("lost sessions: %v", seen)
	}
	page, err := r.Catalog(ctx, CatalogQuery{CWD: "/other"})
	if err != nil || len(page.Sessions) != 0 {
		t.Fatalf("workspace leaked: %v", err)
	}
	if _, err := r.Catalog(ctx, CatalogQuery{Cursor: "invalid"}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	s, err := r.Open(ctx, harness.SessionMetadata{ID: "s0"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := s.Storage().(harness.ByteSessionStorage).Bytes()
	if _, err := r.Import(ctx, data); err != nil {
		t.Fatal("identical import should be retryable", err)
	}
	if _, err := r.Import(ctx, bytes.Replace(data, []byte("future\":true"), []byte("future\":false"), 1)); err == nil {
		t.Fatal("conflicting import accepted")
	}
}

func BenchmarkCatalog100K(b *testing.B) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(b.TempDir(), "state", "orb.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000)
 INSERT INTO sessions(namespace,id,header,cwd,created,modified,name)
 SELECT 'personal',printf('%08d',x),json_object('type','session','version',3,'id',printf('%08d',x),'cwd','/project','timestamp','2026-09-21T00:00:00.000Z'),
 '/project','2026-09-21T00:00:00.000Z','2026-09-21T00:00:00.000Z',CASE WHEN x%1000=0 THEN 'needle' ELSE 'conversation' END FROM n`)
	if err != nil {
		b.Fatal(err)
	}
	r := db.Sessions("personal")
	first, err := r.Catalog(ctx, CatalogQuery{Limit: 128})
	if err != nil {
		b.Fatal(err)
	}
	for name, q := range map[string]CatalogQuery{"page": {Limit: 128}, "next": {Limit: 128, Cursor: first.Next}, "search": {Search: "needle", Limit: 128}, "workspace": {CWD: "/project", Limit: 128}} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := r.Catalog(ctx, q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestImportLegacySessionsAndRejectDamagedTrees(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state", "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo := db.Sessions("legacy")
	for _, version := range []int{1, 2, 3} {
		raw := fmt.Sprintf(`{"type":"session","version":%d,"id":"legacy%d","timestamp":"2026-09-21T00:00:00.000Z","cwd":"/project","unknown":42}`+"\n"+`{"type":"message","id":"entry1","parentId":null,"timestamp":"2026-09-21T00:00:00.000Z","message":{"role":"user","content":"keep me"},"future":true}`+"\n", version, version)
		metadata, err := repo.Import(ctx, []byte(raw))
		if err != nil {
			t.Fatalf("v%d: %v", version, err)
		}
		if _, err = repo.Import(ctx, []byte(raw)); err != nil {
			t.Fatalf("v%d retry: %v", version, err)
		}
		opened, err := repo.Open(ctx, metadata)
		if err != nil {
			t.Fatal(err)
		}
		data, err := opened.Storage().(harness.ByteSessionStorage).Bytes()
		if err != nil || !bytes.Contains(data, []byte(`"unknown":42`)) || !bytes.Contains(data, []byte(`"future":true`)) {
			t.Fatalf("payload lost: %s %v", data, err)
		}
	}
	header := `{"type":"session","version":3,"id":"broken","timestamp":"2026-09-21T00:00:00.000Z","cwd":"/project"}` + "\n"
	for _, entry := range []string{
		`{`,
		`{"type":"custom","id":"child","parentId":"missing","timestamp":"2026-09-21T00:00:00.000Z"}`,
		`{"type":"leaf","id":"leaf","targetId":"missing","timestamp":"2026-09-21T00:00:00.000Z"}`,
		`{"type":"custom","id":"loop","parentId":"loop","timestamp":"2026-09-21T00:00:00.000Z"}`,
	} {
		if _, err := repo.Import(ctx, []byte(header+entry+"\n")); err == nil {
			t.Fatalf("damaged source accepted: %s", entry)
		}
	}
	rows, err := repo.List(ctx, harness.SessionListOptions{})
	if err != nil || len(rows) != 3 {
		t.Fatalf("failed imports left state: %v %v", rows, err)
	}
	if _, err := repo.Import(ctx, []byte(strings.ReplaceAll(header, `"version":3`, `"version":99`))); err == nil {
		t.Fatal("future format accepted")
	}
}
