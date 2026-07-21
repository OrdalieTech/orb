package harness_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	harness "github.com/OrdalieTech/pigo/agent/harness"
)

func TestInMemorySessionRepoPreservesMapInsertionOrder(t *testing.T) {
	ctx := context.Background()
	repo := harness.NewInMemorySessionRepo()
	first, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "second"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "first"}); err != nil {
		t.Fatal(err)
	}
	if got := listedSessionIDs(t, repo, ctx); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("overwrite order = %v", got)
	}
	if err := repo.Delete(ctx, first.Metadata()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "first"}); err != nil {
		t.Fatal(err)
	}
	if got := listedSessionIDs(t, repo, ctx); !reflect.DeepEqual(got, []string{"second", "first"}) {
		t.Fatalf("delete/reinsert order = %v, want [second first]", got)
	}
}

func TestInMemorySessionRepoForkSelectionsMatchPhysicalLog(t *testing.T) {
	ctx := context.Background()
	repo := harness.NewInMemorySessionRepo()
	source, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	userOne, err := source.AppendMessage(map[string]any{"role": "user", "content": "one"})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := source.AppendMessage(map[string]any{"role": "assistant", "content": "two"})
	if err != nil {
		t.Fatal(err)
	}
	userTwo, err := source.AppendMessage(map[string]any{"role": "user", "content": "three"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.MoveTo(stringPointer(userOne), nil); err != nil {
		t.Fatal(err)
	}
	physical := entryIDs(source.Entries())
	if len(physical) != 4 || source.Entries()[3].Type != "leaf" {
		t.Fatalf("source physical log = %#v", source.Entries())
	}

	before, err := repo.Fork(ctx, source.Metadata(), harness.SessionForkOptions{
		SessionCreateOptions: harness.SessionCreateOptions{ID: "before"}, EntryID: userTwo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entryIDs(before.Entries()), []string{userOne, assistant}; !reflect.DeepEqual(got, want) {
		t.Fatalf("before fork = %v, want %v", got, want)
	}

	at, err := repo.Fork(ctx, source.Metadata(), harness.SessionForkOptions{
		SessionCreateOptions: harness.SessionCreateOptions{ID: "at"}, EntryID: userTwo, Position: harness.ForkAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entryIDs(at.Entries()), []string{userOne, assistant, userTwo}; !reflect.DeepEqual(got, want) {
		t.Fatalf("at fork = %v, want %v", got, want)
	}

	full, err := repo.Fork(ctx, source.Metadata(), harness.SessionForkOptions{
		SessionCreateOptions: harness.SessionCreateOptions{ID: "full"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := entryIDs(full.Entries()); !reflect.DeepEqual(got, physical) {
		t.Fatalf("full fork = %v, want physical log %v", got, physical)
	}
}

func TestJSONLSessionRepoEncodingListingAndMetadataInheritance(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	env := harness.NodeExecutionEnv{CWD: root}
	defer func() { _ = env.Cleanup() }()
	repo := harness.NewJSONLSessionRepo(env, filepath.Join(root, "sessions"))
	metadataRaw := json.RawMessage(`{"profile":"reviewer","nested":{"z":1, "a":2}}`)
	source, err := repo.Create(ctx, harness.SessionCreateOptions{
		ID: "source", CWD: `/tmp/a:b\c`, Metadata: metadataRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceMetadata := source.Metadata()
	if !strings.Contains(sourceMetadata.Path, "--tmp-a-b-c--") {
		t.Fatalf("encoded cwd path = %q", sourceMetadata.Path)
	}
	invalidPath := filepath.Join(filepath.Dir(sourceMetadata.Path), "invalid.jsonl")
	if err := os.WriteFile(invalidPath, []byte("not a session header\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err := repo.List(ctx, harness.SessionListOptions{CWD: sourceMetadata.CWD})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "source" {
		t.Fatalf("listed sessions = %#v", listed)
	}
	if string(listed[0].Metadata) != `{"profile":"reviewer","nested":{"z":1,"a":2}}` {
		t.Fatalf("listed metadata = %s, want JSON.stringify-normalized bytes", listed[0].Metadata)
	}

	fork, err := repo.Fork(ctx, sourceMetadata, harness.SessionForkOptions{
		SessionCreateOptions: harness.SessionCreateOptions{ID: "fork", CWD: "/tmp/target"},
	})
	if err != nil {
		t.Fatal(err)
	}
	forkMetadata := fork.Metadata()
	if forkMetadata.ParentSessionPath == nil || *forkMetadata.ParentSessionPath != sourceMetadata.Path {
		t.Fatalf("fork parent = %v, want %q", forkMetadata.ParentSessionPath, sourceMetadata.Path)
	}
	if string(forkMetadata.Metadata) != `{"profile":"reviewer","nested":{"z":1,"a":2}}` {
		t.Fatalf("fork metadata = %s, want JSON.stringify-normalized bytes", forkMetadata.Metadata)
	}
}

func listedSessionIDs(t *testing.T, repo *harness.InMemorySessionRepo, ctx context.Context) []string {
	t.Helper()
	metadata, err := repo.List(ctx, harness.SessionListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(metadata))
	for index := range metadata {
		ids[index] = metadata[index].ID
	}
	return ids
}

func entryIDs(entries []harness.SessionTreeEntry) []string {
	ids := make([]string, len(entries))
	for index := range entries {
		ids[index] = entries[index].ID
	}
	return ids
}
