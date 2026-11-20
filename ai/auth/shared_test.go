package auth

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/internal/document"
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
