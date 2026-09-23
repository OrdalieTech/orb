package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestRefreshCodexStoresAccountModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("chatgpt-account-id") != "account" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-6-astra","display_name":"GPT-6-Astra","visibility":"list","context_window":1},
			{"slug":"gpt-6-sol","display_name":"GPT-6-Sol","visibility":"list","context_window":272000,"input_modalities":["text"],"supported_reasoning_levels":[{"effort":"low"}]},
			{"slug":"gpt-new","display_name":"GPT New","visibility":"list"},
			{"slug":"gpt-reserve","visibility":"hide"}]}`))
	}))
	defer server.Close()

	fresh := generatedCatalogLastModified + 1
	sol := ai.Model{ID: "gpt-6-sol", Name: "GPT-6 Sol", Provider: "openai", API: ai.APIOpenAIResponses, MaxTokens: 64000, Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 2}}}
	store, err := json.Marshal(map[string]storedProvider{
		"openai":       {Models: []ai.Model{sol}, CheckedAt: fresh, LastModified: &fresh},
		"openai-codex": {Models: []ai.Model{}, CheckedAt: fresh, LastModified: &fresh, ETag: "etag"},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "models-store.json")
	if err := os.WriteFile(path, store, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RefreshCodex(context.Background(), RefreshOptions{URL: server.URL, StorePath: path, Client: server.Client()}, "token", "account"); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	models := catalog.Models("openai-codex")
	if len(models) != 3 {
		t.Fatalf("codex models = %#v", models)
	}
	builtinAstra, _ := mustBuiltin(t).Find("openai-codex", "gpt-6-astra")
	if astra, _ := catalog.Find("openai-codex", "gpt-6-astra"); astra.ContextWindow != builtinAstra.ContextWindow {
		t.Fatalf("known model lost catalog metadata: %#v", astra)
	}
	got, _ := catalog.Find("openai-codex", "gpt-6-sol")
	if got.Name != "GPT-6 Sol" || got.API != ai.APIOpenAICodexResponses || got.BaseURL != builtinAstra.BaseURL || got.Cost.Input != 2 || got.MaxTokens != 64000 || !got.Reasoning || len(got.Input) != 1 {
		t.Fatalf("derived model = %#v", got)
	}
	if unknown, _ := catalog.Find("openai-codex", "gpt-new"); unknown.Name != "GPT New" || unknown.Cost.Input != 0 {
		t.Fatalf("unpriced model = %#v", unknown)
	}
	data, _ := os.ReadFile(path)
	var stored map[string]storedProvider
	if err := json.Unmarshal(data, &stored); err != nil || stored["openai-codex"].ETag != "etag" || *stored["openai-codex"].LastModified != fresh {
		t.Fatalf("freshness fields changed: %s", data)
	}
}

func mustBuiltin(t *testing.T) *Catalog {
	t.Helper()
	catalog, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
