package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
			{"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","visibility":"list","context_window":500000},
			{"slug":"gpt-7-sol","display_name":"GPT-7-Sol","visibility":"list","context_window":272000,"input_modalities":["text"],"supported_reasoning_levels":[{"effort":"low"}]},
			{"slug":"gpt-new","display_name":"GPT New","visibility":"list"},
			{"slug":"gpt-reserve","visibility":"hide"}]}`))
	}))
	defer server.Close()

	fresh := generatedCatalogLastModified + 1
	sol := ai.Model{ID: "gpt-7-sol", Name: "GPT-7 Sol", Provider: "openai", API: ai.APIOpenAIResponses, MaxTokens: 64000, Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 2}}}
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
	builtinSol, _ := mustBuiltin(t).Find("openai-codex", "gpt-6.1-sol")
	if sol, _ := catalog.Find("openai-codex", "gpt-6.1-sol"); sol.ContextWindow != 500000 || sol.MaxTokens != builtinSol.MaxTokens {
		t.Fatalf("known model = %#v", sol)
	}
	got, _ := catalog.Find("openai-codex", "gpt-7-sol")
	if got.Name != "GPT-7 Sol" || got.API != ai.APIOpenAICodexResponses || got.BaseURL != builtinSol.BaseURL || got.Cost.Input != 2 || got.MaxTokens != 64000 || got.ContextWindow != 272000 || !got.Reasoning || len(got.Input) != 1 {
		t.Fatalf("derived model = %#v", got)
	}
	if unknown, _ := catalog.Find("openai-codex", "gpt-new"); unknown.Name != "GPT New" || unknown.Cost.Input != 0 || unknown.ContextWindow != builtinSol.ContextWindow {
		t.Fatalf("unpriced model = %#v", unknown)
	}
	data, _ := os.ReadFile(path)
	var stored map[string]storedProvider
	if err := json.Unmarshal(data, &stored); err != nil || stored["openai-codex"].ETag != "etag" || *stored["openai-codex"].LastModified != fresh {
		t.Fatalf("freshness fields changed: %s", data)
	}
}

func TestCodexModelsUsesLiveContextWindow(t *testing.T) {
	catalog := mustBuiltin(t)
	before, _ := catalog.Find("openai-codex", "gpt-6.1-sol")
	for _, test := range []struct {
		name string
		live float64
		want float64
	}{
		{"increased", 500000, 500000},
		{"decreased", 128000, 128000},
		{"missing", 0, before.ContextWindow},
		{"invalid", -1, before.ContextWindow},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := codexModels(catalog, []codexLiveModel{{
				Slug: before.ID, Visibility: "list", ContextWindow: test.live,
				DisplayName: "Server name", InputModalities: []string{"text"},
			}})
			want := *before.Clone()
			want.ContextWindow = test.want
			if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
				t.Fatalf("models = %#v, want %#v", got, want)
			}
			after, _ := catalog.Find("openai-codex", before.ID)
			if !reflect.DeepEqual(after, before) {
				t.Fatal("refresh mutated the existing catalog")
			}
		})
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
