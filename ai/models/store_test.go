package models

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
)

func TestWriteStoreMatchesUpstreamFixture(t *testing.T) {
	expected, err := os.ReadFile("../../conformance/fixtures/WP250/models-store.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]storedProvider
	if err := json.Unmarshal(expected, &fixture); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "models-store.json")
	for _, providerID := range []string{"z-preserved", "a-refreshed"} {
		modelsByID := map[string]map[string]ai.Model{providerID: {}}
		for _, model := range fixture[providerID].Models {
			modelsByID[providerID][model.ID] = model
		}
		if err := writeStore(path, &Catalog{providers: modelsByID}, fixture[providerID].CheckedAt); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("models-store.json differs from upstream\n--- got ---\n%s\n--- want ---\n%s", actual, expected)
	}
}

func TestRefreshPersistsAndReloadsCatalog(t *testing.T) {
	source := []byte(`{"anthropic":{"models":{"fixture":{"name":"Fixture","tool_call":true,"modalities":{"input":["text"]},"limit":{"context":4096,"output":512},"cost":{"input":1,"output":2,"cache_read":0.1,"cache_write":1}}}}}`)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		_, _ = response.Write(source)
	}))
	defer server.Close()

	storePath := filepath.Join(t.TempDir(), "nested", "models-store.json")
	wantTime := time.UnixMilli(123456789)
	catalog, err := Refresh(context.Background(), RefreshOptions{URL: server.URL, StorePath: storePath, Now: func() time.Time { return wantTime }})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.Find("anthropic", "fixture"); !ok {
		t.Fatal("refreshed catalog missing fixture model")
	}
	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %o, want 600", info.Mode().Perm())
	}
	loaded, err := LoadStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	model, ok := loaded.Find("anthropic", "fixture")
	if !ok || model.ContextWindow != 4096 {
		t.Fatalf("bad reloaded model: %#v, %v", model, ok)
	}
}

func TestRefreshPreservesUnrelatedProviderCatalogs(t *testing.T) {
	directory := t.TempDir()
	storePath := filepath.Join(directory, "models-store.json")
	oldCatalog := &Catalog{providers: map[string]map[string]ai.Model{
		"anthropic": {
			"stale": {ID: "stale", Provider: "anthropic"},
		},
		"extension": {
			"preserved": {ID: "preserved", Provider: "extension"},
		},
	}}
	if err := writeStore(storePath, oldCatalog, 100); err != nil {
		t.Fatal(err)
	}

	source := []byte(`{"anthropic":{"models":{"fresh":{"name":"Fresh","tool_call":true,"modalities":{"input":["text"]},"limit":{"context":4096,"output":512},"cost":{"input":1,"output":2}}}}}`)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(source)
	}))
	defer server.Close()
	if _, err := Refresh(context.Background(), RefreshOptions{
		URL: server.URL, StorePath: storePath, Now: func() time.Time { return time.UnixMilli(200) },
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Find("extension", "preserved"); !ok {
		t.Fatal("refresh removed an unrelated provider catalog")
	}
	if _, ok := loaded.Find("anthropic", "fresh"); !ok {
		t.Fatal("refresh did not publish the fetched provider catalog")
	}
	if _, ok := loaded.Find("anthropic", "stale"); ok {
		t.Fatal("refresh retained a stale model from a refreshed provider")
	}
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]storedProvider
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["extension"].CheckedAt != 100 || stored["anthropic"].CheckedAt != 200 {
		t.Fatalf("checkedAt values = extension %d, anthropic %d", stored["extension"].CheckedAt, stored["anthropic"].CheckedAt)
	}
}

func TestRefreshHTTPErrorDoesNotReplaceStore(t *testing.T) {
	directory := t.TempDir()
	storePath := filepath.Join(directory, "models-store.json")
	original := []byte("original\n")
	if err := os.WriteFile(storePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := Refresh(context.Background(), RefreshOptions{URL: server.URL, StorePath: storePath}); err == nil {
		t.Fatal("Refresh succeeded on HTTP 503")
	}
	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("failed refresh changed store: %q", after)
	}
}

func TestWriteStoreRejectsTrailingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models-store.json")
	original := []byte("{} trailing")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{providers: map[string]map[string]ai.Model{"new": {"m": {ID: "m", Provider: "new"}}}}
	if err := writeStore(path, catalog, 1); err == nil {
		t.Fatal("writeStore accepted trailing content")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("failed write changed store: %q", after)
	}
}
