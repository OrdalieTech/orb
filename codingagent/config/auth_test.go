package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	aiauth "github.com/OrdalieTech/pigo/ai/auth"
)

func TestAuthStorageReadsConfigValuesAndPreservesRawFile(t *testing.T) {
	t.Setenv("AUTH_KEY", "environment-key")
	path := filepath.Join(t.TempDir(), "auth.json")
	input := `{"anthropic":{"type":"api_key","key":"$AUTH_KEY","env":{"REGION":"test"}},"github":{"type":"oauth","access":"access","refresh":"refresh","expires":42,"enterpriseUrl":"https://example.test"}}`
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	storage, err := NewAuthStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := storage.Read(context.Background(), "anthropic")
	if err != nil || credential.Key == nil || *credential.Key != "environment-key" || credential.Env["REGION"] != "test" {
		t.Fatalf("credential = %#v, %v", credential, err)
	}
	oauthCredential, err := storage.Read(context.Background(), "github")
	if err != nil || string(oauthCredential.Extra["enterpriseUrl"]) != `"https://example.test"` {
		t.Fatalf("OAuth credential = %#v, %v", oauthCredential, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != input {
		t.Fatalf("read changed auth file: %q, %v", contents, err)
	}
}

func TestAuthStorageModifyMatchesUpstreamFormattingAndPreservesExternalEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"anthropic":{"type":"api_key","key":"old"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	storage, err := NewAuthStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"anthropic":{"type":"api_key","key":"old"},"openai":{"type":"api_key","key":"external"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Modify(context.Background(), "anthropic", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return aiauth.APIKeyCredential("new"), nil
	}); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"anthropic\": {\n    \"type\": \"api_key\",\n    \"key\": \"new\"\n  },\n  \"openai\": {\n    \"type\": \"api_key\",\n    \"key\": \"external\"\n  }\n}"
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != want {
		t.Fatalf("auth.json = %q, want %q (%v)", contents, want, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestAuthStorageSerializesConcurrentWritersAndRejectsMalformedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	first, err := NewAuthStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewAuthStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for provider, storage := range map[string]*AuthStorage{"anthropic": first, "openai": second} {
		provider, storage := provider, storage
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, modifyErr := storage.Modify(context.Background(), provider, func(*aiauth.Credential) (*aiauth.Credential, error) {
				return aiauth.APIKeyCredential(provider + "-key"), nil
			})
			if modifyErr != nil {
				t.Errorf("modify %s: %v", provider, modifyErr)
			}
		}()
	}
	wait.Wait()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(contents, &decoded); err != nil || len(decoded) != 2 {
		t.Fatalf("auth.json = %s, %v", contents, err)
	}

	if err := os.WriteFile(path, []byte("{invalid-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Modify(context.Background(), "google", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return aiauth.APIKeyCredential("key"), nil
	}); err == nil {
		t.Fatal("malformed auth.json was overwritten")
	}
	if contents, _ := os.ReadFile(path); string(contents) != "{invalid-json" {
		t.Fatalf("malformed auth.json changed to %q", contents)
	}
}

func TestAuthStorageDeleteAndListUseDocumentOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"anthropic":{"type":"api_key","key":"a"},"openai":{"type":"api_key","key":"o"},"google":{"type":"api_key","key":"g"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := NewAuthStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Delete(context.Background(), "anthropic"); err != nil {
		t.Fatal(err)
	}
	listed, err := storage.List(context.Background())
	want := []aiauth.CredentialInfo{{ProviderID: "openai", Type: "api_key"}, {ProviderID: "google", Type: "api_key"}}
	if err != nil || !reflect.DeepEqual(listed, want) {
		t.Fatalf("list = %#v, %v", listed, err)
	}
}

func TestAuthStorageUsesProperLockfileDirectoryProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	storage, err := NewAuthStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".lock"
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := storage.Modify(ctx, "anthropic", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return aiauth.APIKeyCredential("blocked"), nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active proper-lockfile directory error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "{}" {
		t.Fatalf("blocked write changed auth.json to %q: %v", contents, err)
	}

	old := time.Now().Add(-authLockStale - time.Second)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Modify(context.Background(), "anthropic", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return aiauth.APIKeyCredential("stored"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock directory remains after release: %v", err)
	}
}

func TestMigrateLegacyOAuth(t *testing.T) {
	agentDir := t.TempDir()
	legacy := `{"anthropic":{"access":"access","refresh":"refresh","expires":42,"scope":"all"}}`
	if err := os.WriteFile(filepath.Join(agentDir, "oauth.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	providers, err := MigrateAuthToAuthJSON(agentDir)
	if err != nil || !reflect.DeepEqual(providers, []string{"anthropic"}) {
		t.Fatalf("migration = %#v, %v", providers, err)
	}
	if _, err := os.Stat(filepath.Join(agentDir, "oauth.json.migrated")); err != nil {
		t.Fatal(err)
	}
	storage, err := NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := storage.Read(context.Background(), "anthropic")
	if err != nil || credential.Type != aiauth.CredentialOAuth || credential.Access != "access" || string(credential.Extra["scope"]) != `"all"` {
		t.Fatalf("migrated credential = %#v, %v", credential, err)
	}
}
