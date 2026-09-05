package search_test

import (
	"context"
	"encoding/json"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/engine/search"
)

func message(text string) json.RawMessage {
	encoded, err := json.Marshal(map[string]any{
		"role":    "user",
		"content": []any{map[string]any{"type": "text", "text": text}},
	})
	if err != nil {
		panic(err)
	}
	return encoded
}

func appendMessage(t *testing.T, storage harness.SessionV4Storage, id, text string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"type": "message", "id": id, "message": json.RawMessage(message(text))})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := storage.AppendEntry(payload, "main")
	if err != nil {
		t.Fatal(err)
	}
	return entry.ID
}

func sessions(all ...search.Session) iter.Seq2[search.Session, error] {
	return func(yield func(search.Session, error) bool) {
		for _, session := range all {
			if !yield(session, nil) {
				return
			}
		}
	}
}

func collect(t *testing.T, hits iter.Seq2[search.Hit, error]) []search.Hit {
	t.Helper()
	collected := []search.Hit{}
	for hit, err := range hits {
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		collected = append(collected, hit)
	}
	return collected
}

func memorySession(t *testing.T, id string) search.Session {
	t.Helper()
	storage := harness.NewInMemorySessionV4Storage(harness.SessionV4Metadata{ID: id})
	return search.Session{ID: id, Readable: storage}
}

func TestSearchScansProjectedSources(t *testing.T) {
	ctx := context.Background()
	root, other := memorySession(t, "root"), memorySession(t, "other")
	appendMessage(t, root.Readable.(harness.SessionV4Storage), "e1", "fix auth flow")
	appendMessage(t, other.Readable.(harness.SessionV4Storage), "e2", "auth in another workspace")

	hits := collect(t, search.Search(ctx, sessions(root, other), "auth", search.Options{}))
	if len(hits) != 2 || hits[0].SessionID != "root" || hits[1].SessionID != "other" {
		t.Fatalf("hits = %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, "fix auth flow") {
		t.Fatalf("snippet = %q", hits[0].Snippet)
	}
	if hits := collect(t, search.Search(ctx, sessions(root, other), "missing", search.Options{})); len(hits) != 0 {
		t.Fatalf("unmatched query hits = %+v", hits)
	}
	if hits := collect(t, search.Search(ctx, sessions(root), "   ", search.Options{})); len(hits) != 0 {
		t.Fatalf("blank query hits = %+v", hits)
	}
}

func TestSearchIncludesLabelsAndHonorsBounds(t *testing.T) {
	ctx := context.Background()
	session := memorySession(t, "session")
	storage := session.Readable.(*harness.InMemorySessionV4Storage)
	messageID := appendMessage(t, storage, "e1", "plain body")
	label := "important label"
	if err := storage.SetLabel(messageID, &label); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.AppendEntry(json.RawMessage(
		`{"type":"custom","id":"e2","customType":"note","data":{"text":"auth custom"}}`), "main"); err != nil {
		t.Fatal(err)
	}
	appendMessage(t, storage, "e3", "auth message")

	hits := collect(t, search.Search(ctx, sessions(session), "important", search.Options{}))
	if len(hits) != 1 || hits[0].EntryID != messageID {
		t.Fatalf("label hits = %+v", hits)
	}

	hits = collect(t, search.Search(ctx, sessions(session), "auth", search.Options{EntryTypes: []string{"message"}}))
	if len(hits) != 1 || hits[0].EntryID != "e3" {
		t.Fatalf("typed hits = %+v", hits)
	}
	// Two entry types keeps the storage query untyped and filters in the scan.
	hits = collect(t, search.Search(ctx, sessions(session), "auth", search.Options{EntryTypes: []string{"message", "custom"}}))
	if len(hits) != 2 {
		t.Fatalf("multi-typed hits = %+v", hits)
	}
	// PageSize 1 forces the cursor loop to page.
	hits = collect(t, search.Search(ctx, sessions(session), "auth", search.Options{PageSize: 1}))
	if len(hits) != 2 {
		t.Fatalf("paged hits = %+v", hits)
	}
	if hits = collect(t, search.Search(ctx, sessions(session), "auth", search.Options{Limit: 1})); len(hits) != 1 {
		t.Fatalf("limited hits = %+v", hits)
	}
}

func TestSearchReportsCancellationAndDuplicateSessions(t *testing.T) {
	session := memorySession(t, "session")
	appendMessage(t, session.Readable.(harness.SessionV4Storage), "e1", "auth message")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range search.Search(cancelled, sessions(session), "auth", search.Options{}) {
		if err == nil {
			t.Fatal("cancelled search yielded a hit")
		}
	}

	failed := false
	for _, err := range search.Search(context.Background(), sessions(session, session), "auth", search.Options{}) {
		if err != nil {
			failed = true
			if !strings.Contains(err.Error(), "duplicate session id") {
				t.Fatalf("duplicate error = %v", err)
			}
		}
	}
	if !failed {
		t.Fatal("duplicate session id was accepted")
	}
}

func TestSearchScansJSONLSessionsFromDisk(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	env := &harness.NodeExecutionEnv{CWD: root}
	t.Cleanup(func() { _ = env.Cleanup() })
	repo := harness.NewJSONLSessionV4Repo(env, root)

	create := func(id, cwd, text string) {
		t.Helper()
		storage, err := repo.Create(ctx, harness.JSONLSessionV4CreateOptions{
			SessionV4CreateOptions: harness.SessionV4CreateOptions{ID: &id}, CWD: cwd,
		})
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(map[string]any{
			"kind": "entry", "entry": map[string]any{
				"type": "message", "id": id + "-entry", "parentId": nil,
				"message": message(text),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := storage.Commit(ctx, []json.RawMessage{payload}); err != nil {
			t.Fatal(err)
		}
		if err := storage.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	create("jsonl", filepath.Join(root, "workspace"), "jsonl backed auth entry")
	create("other", filepath.Join(root, "other"), "jsonl backed auth entry in another cwd")

	listed := func(yield func(search.Session, error) bool) {
		metadata, err := harness.ListJSONLSessionV4Metadata(ctx, env, root, nil)
		if err != nil {
			yield(search.Session{}, err)
			return
		}
		for _, entry := range metadata {
			storage, err := harness.OpenJSONLSessionV4Storage(ctx, env, root, entry)
			if !yield(search.Session{ID: entry.ID, Readable: storage}, err) {
				return
			}
		}
	}

	found := map[string]string{}
	for hit, err := range search.Search(ctx, listed, "auth", search.Options{}) {
		if err != nil {
			t.Fatal(err)
		}
		found[hit.SessionID] = hit.EntryID
	}
	if len(found) != 2 || found["jsonl"] != "jsonl-entry" || found["other"] != "other-entry" {
		t.Fatalf("disk hits = %+v", found)
	}
}
