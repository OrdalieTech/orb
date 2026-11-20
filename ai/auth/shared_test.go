package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

func TestMergeSharedRefusesWhatCouldRunOrBreak(t *testing.T) {
	ctx := context.Background()
	doc := &document.Memory{}
	_ = document.Replace(ctx, doc, []byte(`{"anthropic":{"type":"oauth","refresh":"r1","access":"a1","expires":1000}}`))
	store := NewDocumentStore(doc)
	incoming := map[string]json.RawMessage{
		"evil":      json.RawMessage(`{"type":"api_key","key":"!touch /tmp/x"}`),
		"local":     json.RawMessage(`{"type":"oauth","claudeConfigDir":"/Users/u/.orb/claude/a"}`),
		"bad":       json.RawMessage(`"x"`),
		"anthropic": json.RawMessage(`{"type":"api_key","key":"sk-new"}`),
	}
	changed, err := store.MergeShared(ctx, []map[string]json.RawMessage{incoming}, false, nil)
	raw, _ := doc.Read(ctx)
	if err != nil || len(changed) != 1 || strings.Contains(string(raw), "evil") || strings.Contains(string(raw), "local") || strings.Contains(string(raw), "bad") || !strings.Contains(string(raw), "sk-new") {
		t.Fatalf("changed %v, %v: %s", changed, err, raw)
	}
	if !strings.HasPrefix(string(raw), "{\n  \"anthropic\": {") {
		t.Fatalf("not auth.json's own format: %s", raw)
	}
}

func TestShareableExcludesLocalConfigDir(t *testing.T) {
	ctx := context.Background()
	doc := &document.Memory{}
	_ = document.Replace(ctx, doc, []byte(`{"claude-sessions":{"type":"oauth","claudeConfigDir":"/Users/u/.orb/a"},"openai":{"type":"api_key","key":"sk-1"}}`))
	store := NewDocumentStore(doc)
	shared, err := store.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := shared["claude-sessions"]; ok {
		t.Fatalf("shared local claudeConfigDir credential: %v", shared)
	}
	if _, ok := shared["openai"]; !ok {
		t.Fatalf("missing shareable openai credential: %v", shared)
	}
	// A remote removal never drops a local-only credential.
	changed, err := store.MergeShared(ctx, nil, false, []string{"claude-sessions"})
	if err != nil || len(changed) != 0 {
		t.Fatalf("removed local credential: changed=%v err=%v", changed, err)
	}
}

type failWriteDoc struct{}

func (failWriteDoc) Read(context.Context) ([]byte, error) { return nil, nil }
func (failWriteDoc) Update(context.Context, func([]byte) ([]byte, error)) error {
	return errors.New("disk full")
}

func TestDeleteRecordsDigestOnlyAndPropagatesRemovedWriteError(t *testing.T) {
	ctx := context.Background()
	authDoc, remDoc := &document.Memory{}, &document.Memory{}
	store := NewDocumentStore(authDoc).WithRemoved(remDoc)
	if _, err := store.Modify(ctx, "openai", func(*Credential) (*Credential, error) {
		return APIKeyCredential("sk-super-secret-123"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Modify(ctx, "anthropic", func(*Credential) (*Credential, error) {
		return OAuthCredential("refresh-secret-xyz", "access-secret-xyz", 9999), nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "openai"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "anthropic"); err != nil {
		t.Fatal(err)
	}
	rawRem, _ := remDoc.Read(ctx)
	for _, secret := range []string{"sk-super-secret-123", "refresh-secret-xyz", "access-secret-xyz", "api_key", "oauth"} {
		if strings.Contains(string(rawRem), secret) {
			t.Fatalf("auth-removed.json retained %q: %s", secret, rawRem)
		}
	}
	// Even if auth.json is completely empty after logging out of the last provider, Removed still
	// reports the digests so the member hands in the logouts.
	_ = document.Replace(ctx, authDoc, nil)
	got, err := store.Removed(ctx)
	if err != nil || got["openai"] != jsonwire.Digest(before["openai"]) || got["anthropic"] != jsonwire.Digest(before["anthropic"]) {
		t.Fatalf("Removed = %v, %v", got, err)
	}

	// Failing to persist the tombstone surfaces an error from Delete rather than swallowing it.
	failing := NewDocumentStore(&document.Memory{}).WithRemoved(failWriteDoc{})
	if _, err := failing.Modify(ctx, "openai", func(*Credential) (*Credential, error) {
		return APIKeyCredential("sk-1"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := failing.Delete(ctx, "openai"); err == nil {
		t.Fatal("Delete swallowed tombstone persistence error")
	}
}
