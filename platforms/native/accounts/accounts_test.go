package accounts

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/ai/auth"
)

func TestAccountsPreserveDefaultAndPinRefresh(t *testing.T) {
	ctx := t.Context()
	base := auth.NewMemoryStore(map[string]*auth.Credential{"codex": auth.OAuthCredential("default", "original", 0)})
	path := filepath.Join(t.TempDir(), "accounts.json")
	store := NewStore(path, base)
	first, err := store.Add(ctx, "codex", "Work", auth.OAuthCredential("first", "one", 0))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := store.ForProvider(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Add(ctx, "codex", "Personal", auth.OAuthCredential("second", "two", 0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = bound.Modify(ctx, "codex", func(current *auth.Credential) (*auth.Credential, error) {
		if current.Access != "one" {
			t.Fatal("refresh changed accounts")
		}
		current.Access = "refreshed"
		return current, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := store.Read(ctx, "codex")
	if got.Access != "two" {
		t.Fatal("refresh overwrote the selected account")
	}
	original, _ := base.Read(ctx, "codex")
	if original.Access != "original" {
		t.Fatal("changed the compatibility credential")
	}
	if err := store.Select(ctx, "codex", first.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Read(ctx, "codex")
	if got.Access != "refreshed" {
		t.Fatal("refresh was not saved to its account")
	}
	if err := store.Remove(ctx, "codex", first.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Read(ctx, "codex")
	if got.Access != "original" {
		t.Fatal("removal did not restore the default")
	}
	rows, err := store.Accounts(ctx)
	if err != nil || len(rows) != 2 || rows[1].ID != second.ID {
		t.Fatalf("accounts=%v error=%v", rows, err)
	}
	info, err := os.Stat(path)
	want := os.FileMode(0600)
	if runtime.GOOS == "windows" {
		// Windows exposes only the read-only attribute through mode bits.
		want = 0666
	}
	if err != nil || info.Mode().Perm() != want {
		t.Fatal("credentials are not private")
	}
}

func TestAccountsConcurrentWritersAndCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	base := auth.NewMemoryStore(nil)
	var workers sync.WaitGroup
	for i := range 12 {
		workers.Go(func() {
			_, err := NewStore(path, base).Add(context.Background(), "provider", string(rune('a'+i)), auth.APIKeyCredential(string(rune('a'+i))))
			if err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	store := NewStore(path, base)
	rows, err := store.Accounts(t.Context())
	if err != nil || len(rows) != 12 {
		t.Fatalf("lost writes: count=%d error=%v", len(rows), err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(t.Context(), "provider", "new", auth.APIKeyCredential("new")); err == nil {
		t.Fatal("accepted corrupt state")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{broken" {
		t.Fatal("overwrote corrupt state")
	}
}
