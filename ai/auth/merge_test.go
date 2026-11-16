package auth

import (
	"context"
	"testing"
)

func mustMarshal(t *testing.T, order []string, creds map[string]*Credential) []byte {
	t.Helper()
	data, err := MarshalCredentials(order, creds)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func keyOf(t *testing.T, data []byte, provider string) string {
	t.Helper()
	creds, err := ParseCredentials(data)
	if err != nil {
		t.Fatal(err)
	}
	c := creds[provider]
	if c == nil || c.Key == nil {
		return ""
	}
	return *c.Key
}

func TestMergeCredentialsOverlayWinsAndKeepsLocal(t *testing.T) {
	base := mustMarshal(t, []string{"anthropic", "openai"}, map[string]*Credential{
		"anthropic": APIKeyCredential("local-ant"),
		"openai":    APIKeyCredential("local-openai"),
	})
	overlay := mustMarshal(t, []string{"anthropic", "gemini"}, map[string]*Credential{
		"anthropic": APIKeyCredential("shared-ant"),
		"gemini":    APIKeyCredential("shared-gemini"),
	})

	merged, err := MergeCredentials(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if got := keyOf(t, merged, "anthropic"); got != "shared-ant" {
		t.Fatalf("anthropic should be overwritten by overlay, got %q", got)
	}
	if got := keyOf(t, merged, "openai"); got != "local-openai" {
		t.Fatalf("local-only openai must be preserved, got %q", got)
	}
	if got := keyOf(t, merged, "gemini"); got != "shared-gemini" {
		t.Fatalf("new gemini should be added, got %q", got)
	}
}

func TestMergeCredentialsEmptyInputs(t *testing.T) {
	overlay := mustMarshal(t, []string{"anthropic"}, map[string]*Credential{"anthropic": APIKeyCredential("k")})

	// Empty base: overlay comes through.
	merged, err := MergeCredentials(nil, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if keyOf(t, merged, "anthropic") != "k" {
		t.Fatal("overlay lost against empty base")
	}
	// Empty overlay: base is unchanged (and a no-op never clobbers).
	merged, err = MergeCredentials(overlay, nil)
	if err != nil {
		t.Fatal(err)
	}
	if keyOf(t, merged, "anthropic") != "k" {
		t.Fatal("base lost against empty overlay")
	}
	// Both empty: valid empty document.
	if _, err := MergeCredentials(nil, nil); err != nil {
		t.Fatal(err)
	}
	// "{}" seeds behave like empty.
	if _, err := MergeCredentials([]byte("{}"), []byte("{}")); err != nil {
		t.Fatal(err)
	}
}

func TestMergeCredentialsDeterministicAndStable(t *testing.T) {
	base := mustMarshal(t, []string{"openai", "anthropic"}, map[string]*Credential{
		"openai":    APIKeyCredential("o"),
		"anthropic": APIKeyCredential("a"),
	})
	overlay := mustMarshal(t, []string{"zed", "gemini"}, map[string]*Credential{
		"zed":    APIKeyCredential("z"),
		"gemini": APIKeyCredential("g"),
	})
	first, err := MergeCredentials(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MergeCredentials(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("merge is not deterministic")
	}
}

func TestStoreExportAndMergeIn(t *testing.T) {
	ctx := context.Background()
	// Peer starts with its own local login.
	store := NewMemoryStore(map[string]*Credential{"openai": APIKeyCredential("peer-local")})

	exported, err := store.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if keyOf(t, exported, "openai") != "peer-local" {
		t.Fatal("export lost the local credential")
	}

	// Owner's shared bundle arrives: adds anthropic, and would change openai.
	overlay := mustMarshal(t, []string{"anthropic", "openai"}, map[string]*Credential{
		"anthropic": APIKeyCredential("shared-ant"),
		"openai":    APIKeyCredential("shared-openai"),
	})
	if err := store.MergeIn(ctx, overlay); err != nil {
		t.Fatal(err)
	}

	got, err := store.Read(ctx, "anthropic")
	if err != nil || got == nil || got.Key == nil || *got.Key != "shared-ant" {
		t.Fatalf("anthropic not applied: %+v", got)
	}
	openai, _ := store.Read(ctx, "openai")
	if openai == nil || openai.Key == nil || *openai.Key != "shared-openai" {
		t.Fatal("openai should have been overwritten by the shared value")
	}
}

func TestMergeCredentialsRejectsMalformed(t *testing.T) {
	good := mustMarshal(t, []string{"a"}, map[string]*Credential{"a": APIKeyCredential("k")})
	if _, err := MergeCredentials([]byte("{ not json"), good); err == nil {
		t.Fatal("malformed base accepted")
	}
	if _, err := MergeCredentials(good, []byte("{ not json")); err == nil {
		t.Fatal("malformed overlay accepted")
	}
}
