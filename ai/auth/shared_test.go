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
		"bad":       json.RawMessage(`"x"`),
		"anthropic": json.RawMessage(`{"type":"api_key","key":"sk-new"}`),
	}
	changed, err := store.MergeShared(ctx, []map[string]json.RawMessage{incoming}, false, nil)
	raw, _ := doc.Read(ctx)
	if err != nil || len(changed) != 1 || strings.Contains(string(raw), "evil") || strings.Contains(string(raw), "bad") || !strings.Contains(string(raw), "sk-new") {
		t.Fatalf("changed %v, %v: %s", changed, err, raw)
	}
	if !strings.HasPrefix(string(raw), "{\n  \"anthropic\": {") {
		t.Fatalf("not auth.json's own format: %s", raw)
	}
}
