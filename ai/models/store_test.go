package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

type catalogRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip catalogRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func storeTimestamp(value int64) *int64 { return &value }

func requireStoreTimestamp(t *testing.T, value *int64, want int64) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("lastModified = %v, want present value %d", value, want)
	}
}

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
		if err := writeStore(path, &Catalog{providers: modelsByID}, fixture[providerID].CheckedAt, fixture[providerID].LastModified); err != nil {
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
	wantTime := time.UnixMilli(generatedCatalogLastModified + 123456789).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		response.Header().Set("Last-Modified", wantTime.UTC().Format(http.TimeFormat))
		_, _ = response.Write(source)
	}))
	defer server.Close()

	storePath := filepath.Join(t.TempDir(), "nested", "models-store.json")
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
	if err := writeStore(storePath, oldCatalog, 100, storeTimestamp(0)); err != nil {
		t.Fatal(err)
	}

	refreshedAt := generatedCatalogLastModified + 2000
	source := []byte(`{"anthropic":{"models":{"fresh":{"name":"Fresh","tool_call":true,"modalities":{"input":["text"]},"limit":{"context":4096,"output":512},"cost":{"input":1,"output":2}}}}}`)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Last-Modified", time.UnixMilli(refreshedAt).UTC().Format(http.TimeFormat))
		_, _ = response.Write(source)
	}))
	defer server.Close()
	if _, err := Refresh(context.Background(), RefreshOptions{
		URL: server.URL, StorePath: storePath, Now: func() time.Time { return time.UnixMilli(refreshedAt) },
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
	if stored["extension"].CheckedAt != 100 || stored["anthropic"].CheckedAt != refreshedAt {
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

// SYNC-4: a newer bundled catalog beats a stale cached overlay; the overlay
// only wins when its lastModified postdates the bundled catalog build.
func TestSYNC4NewerBuiltinBeatsStaleStoreOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models-store.json")
	store := map[string]storedProvider{
		"anthropic": {
			Models:       []ai.Model{{ID: "stale-model", Provider: "anthropic"}},
			CheckedAt:    generatedCatalogLastModified - 1,
			LastModified: storeTimestamp(generatedCatalogLastModified - 1),
		},
		"openai": {
			Models:       []ai.Model{{ID: "fresh-model", Provider: "openai"}},
			CheckedAt:    generatedCatalogLastModified + 1,
			LastModified: storeTimestamp(generatedCatalogLastModified + 1),
		},
		"mistral": {
			// Legacy entry written before lastModified existed.
			Models:    []ai.Model{{ID: "legacy-model", Provider: "mistral"}},
			CheckedAt: generatedCatalogLastModified + 1,
		},
		"extension": {
			Models: []ai.Model{{ID: "extension-model", Provider: "extension"}},
		},
	}
	data, err := json.Marshal(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Find("anthropic", "stale-model"); ok {
		t.Fatal("stale overlay for a bundled provider beat the newer builtin catalog")
	}
	if _, ok := loaded.Find("mistral", "legacy-model"); ok {
		t.Fatal("legacy overlay without lastModified beat the builtin catalog")
	}
	if _, ok := loaded.Find("openai", "fresh-model"); !ok {
		t.Fatal("overlay newer than the builtin catalog was dropped")
	}
	if _, ok := loaded.Find("extension", "extension-model"); !ok {
		t.Fatal("overlay for a non-bundled provider was dropped")
	}
}

// CAT-m1: startup refreshes are gated at 4h via checkedAt and send the pi
// User-Agent; Force bypasses the gate.
func TestCATm1RefreshGatesOnCheckedAtAndSendsUserAgent(t *testing.T) {
	requests := 0
	base := time.UnixMilli(generatedCatalogLastModified + 1000)
	source := []byte(`{"anthropic":{"models":{"fixture":{"name":"Fixture","tool_call":true,"modalities":{"input":["text"]},"limit":{"context":4096,"output":512},"cost":{"input":1,"output":2}}}}}`)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if got := request.Header.Get("User-Agent"); got != PiUserAgent("1.2.3") {
			t.Errorf("User-Agent = %q", got)
		}
		response.Header().Set("Last-Modified", base.UTC().Format(http.TimeFormat))
		_, _ = response.Write(source)
	}))
	defer server.Close()

	storePath := filepath.Join(t.TempDir(), "models-store.json")
	options := RefreshOptions{
		URL: server.URL, StorePath: storePath, UserAgent: PiUserAgent("1.2.3"),
		Now: func() time.Time { return base },
	}
	if _, err := Refresh(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests after first refresh = %d", requests)
	}

	options.Now = func() time.Time { return base.Add(remoteCatalogRefreshInterval - time.Minute) }
	catalog, err := Refresh(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("refresh within the gate interval hit the network (%d requests)", requests)
	}
	if _, ok := catalog.Find("anthropic", "fixture"); !ok {
		t.Fatal("gated refresh did not reload the stored overlay")
	}

	forced := options
	forced.Force = true
	if _, err := Refresh(context.Background(), forced); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("forced refresh did not hit the network (%d requests)", requests)
	}

	options.Now = func() time.Time { return base.Add(2 * remoteCatalogRefreshInterval) }
	if _, err := Refresh(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatalf("refresh after the gate interval skipped the network (%d requests)", requests)
	}
}

// CAT-m1: v0.81 only applies the four-hour gate when both checkedAt and
// lastModified are present. A legacy entry with checkedAt alone must refresh.
func TestCATm1RefreshGateRequiresCheckedAtAndLastModified(t *testing.T) {
	base := time.UnixMilli(generatedCatalogLastModified + 5000)
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	if err := writeStore(storePath, &Catalog{providers: map[string]map[string]ai.Model{
		"anthropic": {"preserved": {ID: "preserved", Provider: "anthropic"}},
	}}, base.UnixMilli(), nil); err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("unavailable")),
		}, nil
	})}

	catalog, err := Refresh(context.Background(), RefreshOptions{
		URL: "https://catalog.test", StorePath: storePath, Client: client,
		Now: func() time.Time { return base.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("fresh checkedAt without lastModified made %d requests, want one", requests)
	}
	if _, ok := catalog.Find("anthropic", "preserved"); ok {
		t.Fatal("404 lastModified=0 left a stale bundled-provider overlay active")
	}
	if _, err := Refresh(context.Background(), RefreshOptions{
		URL: "https://catalog.test", StorePath: storePath, Client: client,
		Now: func() time.Time { return base.Add(time.Minute) },
	}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("404 response with lastModified=0 did not gate the immediate retry: %d requests", requests)
	}
}

// CAT-m1: upstream treats an unavailable provider endpoint as a completed
// check. It preserves the cached catalog and suppresses another request for
// four hours.
func TestCATm1UnavailableResponsesStampCheckedAtAndGate(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusNotImplemented} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			base := time.UnixMilli(generatedCatalogLastModified + remoteCatalogRefreshInterval.Milliseconds() + 10_000)
			oldCheckedAt := base.Add(-remoteCatalogRefreshInterval - time.Minute).UnixMilli()
			oldLastModified := base.Add(-time.Hour).UnixMilli()
			storePath := filepath.Join(t.TempDir(), "models-store.json")
			if err := writeStore(storePath, &Catalog{providers: map[string]map[string]ai.Model{
				"anthropic": {"stale": {ID: "stale", Provider: "anthropic"}},
				"extension": {"preserved": {ID: "preserved", Provider: "extension"}},
			}}, oldCheckedAt, storeTimestamp(oldLastModified)); err != nil {
				t.Fatal(err)
			}
			requests := 0
			client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{
					StatusCode: status,
					Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("unavailable")),
				}, nil
			})}
			options := RefreshOptions{
				URL: "https://catalog.test", StorePath: storePath, Client: client,
				Now: func() time.Time { return base },
			}

			catalog, err := Refresh(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := catalog.Find("extension", "preserved"); !ok {
				t.Fatal("unavailable response replaced the stored catalog")
			}
			if _, ok := catalog.Find("anthropic", "stale"); ok {
				t.Fatal("unavailable response kept a bundled-provider overlay with lastModified=0")
			}
			data, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]storedProvider
			if err := json.Unmarshal(data, &stored); err != nil {
				t.Fatal(err)
			}
			entry := stored["anthropic"]
			if entry.CheckedAt != base.UnixMilli() {
				t.Fatalf("checkedAt = %d, want %d", entry.CheckedAt, base.UnixMilli())
			}
			requireStoreTimestamp(t, entry.LastModified, 0)
			if extension := stored["extension"]; extension.CheckedAt != oldCheckedAt {
				t.Fatalf("unrelated extension checkedAt = %d, want preserved %d", extension.CheckedAt, oldCheckedAt)
			}

			options.Now = func() time.Time { return base.Add(time.Minute) }
			if _, err := Refresh(context.Background(), options); err != nil {
				t.Fatal(err)
			}
			if requests != 1 {
				t.Fatalf("request count = %d, want one request followed by a gated refresh", requests)
			}
		})
	}
}

// CAT-m1: a first 404/501 still persists a completed check. The Go global
// models.dev adaptation must create real built-in provider entries and never
// expose a metadata sentinel as a provider.
func TestCATm1UnavailableResponseCreatesFreshStoreWithoutFakeProvider(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusNotImplemented} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			base := time.UnixMilli(generatedCatalogLastModified + 50_000)
			storePath := filepath.Join(t.TempDir(), "models-store.json")
			requests := 0
			client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{
					StatusCode: status,
					Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("unavailable")),
				}, nil
			})}
			options := RefreshOptions{
				URL: "https://catalog.test", StorePath: storePath, Client: client,
				Now: func() time.Time { return base },
			}

			catalog, err := Refresh(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if len(catalog.providers) != 0 {
				t.Fatalf("unavailable empty-store refresh exposed providers: %#v", catalog.providers)
			}
			data, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]storedProvider
			if err := json.Unmarshal(data, &stored); err != nil {
				t.Fatal(err)
			}
			entry, ok := stored["anthropic"]
			if !ok || entry.CheckedAt != base.UnixMilli() {
				t.Fatalf("created anthropic metadata entry = %#v", entry)
			}
			requireStoreTimestamp(t, entry.LastModified, 0)

			options.Now = func() time.Time { return base.Add(time.Minute) }
			if _, err := Refresh(context.Background(), options); err != nil {
				t.Fatal(err)
			}
			if requests != 1 {
				t.Fatalf("unavailable missing-store response did not gate retry: %d requests", requests)
			}
		})
	}
}

// CAT-m1: an HTTP error still completed the remote check, so the error is
// surfaced once while the preserved catalog gates an immediate retry.
func TestCATm1OtherHTTPErrorStampsCheckedAtAndGates(t *testing.T) {
	base := time.UnixMilli(generatedCatalogLastModified + remoteCatalogRefreshInterval.Milliseconds() + 20_000)
	oldCheckedAt := base.Add(-remoteCatalogRefreshInterval - time.Minute).UnixMilli()
	oldLastModified := base.Add(-time.Hour).UnixMilli()
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	if err := writeStore(storePath, &Catalog{providers: map[string]map[string]ai.Model{
		"anthropic": {"preserved": {ID: "preserved", Provider: "anthropic"}},
		"extension": {"extension": {ID: "extension", Provider: "extension"}},
	}}, oldCheckedAt, storeTimestamp(oldLastModified)); err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("unavailable")),
		}, nil
	})}
	options := RefreshOptions{
		URL: "https://catalog.test", StorePath: storePath, Client: client,
		Now: func() time.Time { return base },
	}

	if _, err := Refresh(context.Background(), options); err == nil {
		t.Fatal("Refresh succeeded on HTTP 503")
	}
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]storedProvider
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	entry := stored["anthropic"]
	if entry.CheckedAt != base.UnixMilli() {
		t.Fatalf("checkedAt = %d, want %d", entry.CheckedAt, base.UnixMilli())
	}
	if entry.LastModified == nil || *entry.LastModified != oldLastModified || len(entry.Models) != 1 || entry.Models[0].ID != "preserved" {
		t.Fatalf("failed response changed stored catalog: %+v", entry)
	}

	options.Now = func() time.Time { return base.Add(time.Minute) }
	catalog, err := Refresh(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.Find("anthropic", "preserved"); !ok {
		t.Fatal("gated retry did not return the preserved catalog")
	}
	if extension := stored["extension"]; extension.CheckedAt != oldCheckedAt {
		t.Fatalf("failed response stamped unrelated extension entry: %+v", extension)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want one failed response followed by a gated refresh", requests)
	}
}

// CAT-m1: an error response with no prior lastModified writes checkedAt but
// remains ungated, matching the spread of {models: []} at v0.81 lines 96-98.
func TestCATm1OtherHTTPErrorWithoutStoreRetries(t *testing.T) {
	base := time.UnixMilli(generatedCatalogLastModified + 60_000)
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	requests := 0
	client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("unavailable")),
		}, nil
	})}
	options := RefreshOptions{
		URL: "https://catalog.test", StorePath: storePath, Client: client,
		Now: func() time.Time { return base },
	}
	for range 2 {
		if _, err := Refresh(context.Background(), options); err == nil {
			t.Fatal("Refresh succeeded on HTTP 503")
		}
	}
	if requests != 2 {
		t.Fatalf("503 without lastModified was gated: %d requests", requests)
	}
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]storedProvider
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	entry, ok := stored["anthropic"]
	if !ok || entry.CheckedAt != base.UnixMilli() || entry.LastModified != nil {
		t.Fatalf("503 missing-store entry = %#v", entry)
	}
}

// CAT-m1: without an HTTP response there was no completed check to gate, so a
// transport failure leaves the store untouched and the next call retries.
func TestCATm1TransportFailureDoesNotStampOrGate(t *testing.T) {
	base := time.UnixMilli(generatedCatalogLastModified + remoteCatalogRefreshInterval.Milliseconds() + 30_000)
	oldCheckedAt := base.Add(-remoteCatalogRefreshInterval - time.Minute).UnixMilli()
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	if err := writeStore(storePath, &Catalog{providers: map[string]map[string]ai.Model{
		"extension": {"preserved": {ID: "preserved", Provider: "extension"}},
	}}, oldCheckedAt, storeTimestamp(base.Add(-time.Hour).UnixMilli())); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("offline")
	})}
	options := RefreshOptions{
		URL: "https://catalog.test", StorePath: storePath, Client: client,
		Now: func() time.Time { return base },
	}

	for range 2 {
		if _, err := Refresh(context.Background(), options); err == nil {
			t.Fatal("Refresh succeeded despite a transport failure")
		}
	}
	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("transport failure changed store\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want transport failure to remain ungated", requests)
	}
}

func TestWriteStoreRejectsTrailingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models-store.json")
	original := []byte("{} trailing")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{providers: map[string]map[string]ai.Model{"new": {"m": {ID: "m", Provider: "new"}}}}
	if err := writeStore(path, catalog, 1, storeTimestamp(1)); err == nil {
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
