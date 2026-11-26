package worker

import (
	"testing"

	"github.com/OrdalieTech/orb/platforms/memory"
)

func TestStoreDocumentUpdateNoOpDoesNotFireOnWrite(t *testing.T) {
	ctx := t.Context()
	documents, err := OpenFileSystem(ctx, newMemKV(), documentsNamespace, memory.Options{})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(documents, nil)
	writes := 0
	store.OnWrite = func(string) { writes++ }

	doc := store.Document("/agent/settings.json")

	// No-op update on a missing document (current == nil, next == nil) must not fire OnWrite.
	if err := doc.Update(ctx, func(data []byte) ([]byte, error) {
		if data != nil {
			t.Fatalf("expected nil for missing document, got %q", data)
		}
		return data, nil
	}); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("no-op Update on missing document fired OnWrite %d times", writes)
	}

	// Creating the document must fire OnWrite once.
	if err := doc.Update(ctx, func([]byte) ([]byte, error) {
		return []byte(`{"defaultModel":"x"}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("create Update fired OnWrite %d times, want 1", writes)
	}

	// No-op update on an existing document must not fire OnWrite.
	if err := doc.Update(ctx, func(data []byte) ([]byte, error) {
		return data, nil
	}); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("no-op Update on existing document fired OnWrite %d times, want 1", writes)
	}

	// Deleting an existing document must fire OnWrite once.
	if err := doc.Update(ctx, func([]byte) ([]byte, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatalf("delete Update fired OnWrite %d times, want 2", writes)
	}
}
