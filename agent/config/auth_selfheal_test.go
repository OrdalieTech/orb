package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	aiauth "github.com/OrdalieTech/orb/ai/auth"
)

// Upstream parseStorageData treats empty content as an empty store
// (auth-storage.ts "if (!content) return {}"), so a 0-byte auth.json — e.g. a
// crash between file-create and the "{}" seed write — self-heals on the next
// credential write instead of failing every Modify/Delete with EOF.
func TestEmptyAuthFileSelfHeals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := NewAuthStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	key := "sk-test"
	saved, err := storage.Modify(context.Background(), "anthropic", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return &aiauth.Credential{Type: aiauth.CredentialAPIKey, Key: &key}, nil
	})
	if err != nil {
		t.Fatalf("Modify on 0-byte auth.json failed: %v", err)
	}
	if saved == nil || saved.Key == nil || *saved.Key != "sk-test" {
		t.Fatalf("saved credential = %#v", saved)
	}
	credential := ReadStoredCredential("anthropic", path)
	if credential == nil || credential.Key == nil || *credential.Key != "sk-test" {
		t.Fatalf("re-read credential = %#v", credential)
	}
	if err := storage.Delete(context.Background(), "anthropic"); err != nil {
		t.Fatalf("Delete after self-heal failed: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "{}" {
		t.Fatalf("auth.json after delete = %q, %v", contents, err)
	}
}

func TestDeleteOnEmptyAuthFileSelfHeals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := NewAuthStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Delete(context.Background(), "anthropic"); err != nil {
		t.Fatalf("Delete on 0-byte auth.json failed: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "{}" {
		t.Fatalf("auth.json after delete = %q, %v", contents, err)
	}
}
