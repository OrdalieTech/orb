package worker

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestRefreshModelsPopulatesLiveModelsAndCodex(t *testing.T) {
	ctx := t.Context()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models.json":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", `"v-test-gpt62"`)
			_, _ = w.Write([]byte(`{
				"openai": {
					"models": {
						"gpt-6.2-sol": {
							"id": "gpt-6.2-sol",
							"type": "chat",
							"name": "GPT-6.2 Sol",
							"api": {"responses": {"base_url": "https://api.openai.com/v1"}},
							"modalities": {"input": ["text", "image/png"], "output": ["text"]},
							"capabilities": {"tools": {"supported": true}, "reasoning": {"supported": true}},
							"cost": {"text": {"input": {"price": 2.5}, "output": {"price": 10}}},
							"limit": {"context": 400000, "output": 128000}
						}
					}
				},
				"openai-codex": {
					"models": {
						"gpt-5.4": {
							"id": "gpt-5.4",
							"type": "chat",
							"name": "GPT-5.4",
							"modalities": {"input": ["text", "image"], "output": ["text"]},
							"capabilities": {"tools": {"supported": true}, "reasoning": {"supported": true}},
							"limit": {"context": 272000, "output": 128000}
						}
					}
				}
			}`))
		case "/codex/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"models": [
					{"slug": "gpt-6.1-sol", "display_name": "GPT-6.1 Sol", "visibility": "list", "context_window": 400000, "input_modalities": ["text", "image"], "supported_reasoning_levels": ["low", "high"]},
					{"slug": "gpt-6.2-sol", "display_name": "GPT-6.2 Sol", "visibility": "list", "context_window": 400000, "input_modalities": ["text", "image"], "supported_reasoning_levels": ["low", "medium", "high", "xhigh"]}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	kv := newMemKV()
	instance, err := Open(ctx, Options{
		KV: kv,
		Env: func(name string) (string, bool) {
			if name == "OPENAI_API_KEY" {
				return "sk-test-openai", true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Dispose()

	// Seed openai-codex OAuth token in auth.json (as Mitosis sync does).
	if err := instance.Host.Document("auth.json").Update(ctx, func([]byte) ([]byte, error) {
		return []byte(`{"openai-codex":{"type":"oauth","access":"tok-codex","refresh":"ref","expires":9999999999999}}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	if instance.HasModelStore(ctx) {
		t.Fatal("expected empty models-store.json before refresh")
	}
	beforeRev := instance.Session().ModelRevision()
	if err := instance.refreshModels(ctx, true, server.URL+"/models.json", server.URL+"/codex/models"); err != nil {
		t.Fatal(err)
	}
	if !instance.HasModelStore(ctx) {
		t.Fatal("expected models-store.json to be populated after refresh")
	}
	if afterRev := instance.Session().ModelRevision(); afterRev <= beforeRev {
		t.Fatalf("model revision did not advance: before=%d after=%d", beforeRev, afterRev)
	}
	got, err := instance.DefaultModels()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got.Models, "openai/gpt-6.2-sol") {
		t.Fatalf("missing openai/gpt-6.2-sol in DefaultModels: %v", got.Models)
	}
	if !slices.Contains(got.Models, "openai-codex/gpt-6.2-sol") {
		t.Fatalf("missing openai-codex/gpt-6.2-sol in DefaultModels: %v", got.Models)
	}
}
