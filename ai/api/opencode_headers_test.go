package api

import (
	"net/http"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestOpenCodeSessionHeader(t *testing.T) {
	sessionID := "conversation-1"
	model := &ai.Model{Provider: "opencode"}
	for _, test := range []struct {
		name    string
		headers ai.ProviderHeaders
		want    string
	}{
		{name: "generated", want: sessionID},
		{name: "caller override", headers: ai.ProviderHeaders{"X-OpenCode-Session": openCodeStringPointer("caller")}, want: "caller"},
		{name: "caller deletion", headers: ai.ProviderHeaders{"X-OpenCode-Session": nil}},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			for name, value := range test.headers {
				if value != nil {
					headers.Set(name, *value)
				}
			}
			addOpenCodeSessionHeader(headers, model, &ai.StreamOptions{
				SessionID: &sessionID, CacheRetention: openCodeCacheRetentionPointer(ai.CacheRetentionNone), Headers: test.headers,
			})
			if got := headers.Get(openCodeSessionHeader); got != test.want {
				t.Fatalf("header = %q, want %q", got, test.want)
			}
		})
	}
}

func openCodeStringPointer(value string) *string { return &value }

func openCodeCacheRetentionPointer(value ai.CacheRetention) *ai.CacheRetention { return &value }
