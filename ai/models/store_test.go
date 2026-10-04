package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
)

type catalogRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip catalogRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func storeTimestamp(value int64) *int64 { return &value }

func TestRefreshPersistsAndReloadsCatalog(t *testing.T) {
	source := []byte(`{"anthropic":{"models":{"fixture":{"name":"Fixture","tool_call":true,"modalities":{"input":["text"]},"limit":{"context":4096,"output":512},"cost":{"input":1,"output":2,"cache_read":0.1,"cache_write":1}}}}}`)
	wantTime := time.UnixMilli(generatedCatalogLastModified + 123456789).Truncate(time.Second)
	client := &http.Client{Transport: catalogRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Last-Modified": []string{wantTime.UTC().Format(http.TimeFormat)}},
			Body:       io.NopCloser(bytes.NewReader(source)),
		}, nil
	})}

	storePath := filepath.Join(t.TempDir(), "nested", "models-store.json")
	catalog, err := Refresh(context.Background(), RefreshOptions{
		URL: "https://catalog.test", StorePath: storePath, Client: client,
		Now: func() time.Time { return wantTime },
	})
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
	want := os.FileMode(0o600)
	if runtime.GOOS == "windows" {
		// Windows exposes only the read-only attribute through mode bits, as Node's fs.stat does.
		want = 0o666
	}
	if info.Mode().Perm() != want {
		t.Fatalf("store mode = %o, want %o", info.Mode().Perm(), want)
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

func TestRefreshRevalidatesStoredCatalogWithETag(t *testing.T) {
	source := []byte(`{"anthropic":{"models":{"fixture":{"name":"Fixture","tool_call":true,"modalities":{"input":["text"]},"limit":{"context":4096,"output":512},"cost":{"input":1,"output":2}}}}}`)
	base := time.UnixMilli(generatedCatalogLastModified + time.Hour.Milliseconds()).Truncate(time.Second)
	now := base
	requests := 0
	client := &http.Client{Transport: catalogRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			if got := request.Header.Get("If-None-Match"); got != "" {
				t.Fatalf("first If-None-Match = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK, Status: "200 OK",
				Header: http.Header{"Last-Modified": []string{base.Format(http.TimeFormat)}, "Etag": []string{`"catalog-1"`}},
				Body:   io.NopCloser(bytes.NewReader(source)),
			}, nil
		}
		if got := request.Header.Get("If-None-Match"); got != `"catalog-1"` {
			t.Fatalf("revalidation If-None-Match = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusNotModified, Status: "304 Not Modified",
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")),
		}, nil
	})}
	storePath := filepath.Join(t.TempDir(), "models-store.json")
	options := RefreshOptions{
		URL: "https://catalog.test", StorePath: storePath, Client: client, Force: true,
		Now: func() time.Time { return now },
	}
	if _, err := Refresh(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	var before map[string]storedProvider
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &before); err != nil {
		t.Fatal(err)
	}

	now = base.Add(time.Minute)
	catalog, err := Refresh(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.Find("anthropic", "fixture"); !ok {
		t.Fatal("304 dropped the stored overlay")
	}
	var after map[string]storedProvider
	data, err = os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || after["anthropic"].CheckedAt != now.UnixMilli() ||
		after["anthropic"].ETag != `"catalog-1"` ||
		!reflect.DeepEqual(after["anthropic"].Models, before["anthropic"].Models) ||
		!reflect.DeepEqual(after["anthropic"].LastModified, before["anthropic"].LastModified) {
		t.Fatalf("304 store = %+v, before = %+v, requests = %d", after["anthropic"], before["anthropic"], requests)
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
	if err := writeStoreResponse(storePath, oldCatalog, 100, storeTimestamp(0), ""); err != nil {
		t.Fatal(err)
	}

	refreshedAt := generatedCatalogLastModified + 2000
	source := []byte(`{"anthropic":{"models":{"fresh":{"name":"Fresh","tool_call":true,"modalities":{"input":["text"]},"limit":{"context":4096,"output":512},"cost":{"input":1,"output":2}}}}}`)
	client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Last-Modified": []string{time.UnixMilli(refreshedAt).UTC().Format(http.TimeFormat)}},
			Body:       io.NopCloser(bytes.NewReader(source)),
		}, nil
	})}
	if _, err := Refresh(context.Background(), RefreshOptions{
		URL: "https://catalog.test", StorePath: storePath, Client: client,
		Now: func() time.Time { return time.UnixMilli(refreshedAt) },
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
	client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Status:     "503 Service Unavailable",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("unavailable\n")),
		}, nil
	})}
	if _, err := Refresh(context.Background(), RefreshOptions{
		URL: "https://catalog.test", StorePath: storePath, Client: client,
	}); err == nil {
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
		"xai": {
			Models:       []ai.Model{{ID: "grok-4.6", Provider: "xai"}},
			CheckedAt:    generatedCatalogLastModified + 1,
			LastModified: storeTimestamp(0),
			ETag:         `"catalog"`,
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
	if _, ok := loaded.Find("xai", "grok-4.6"); !ok {
		t.Fatal("models.dev overlay with an ETag and no Last-Modified was dropped")
	}
	if _, ok := loaded.Find("extension", "extension-model"); !ok {
		t.Fatal("overlay for a non-bundled provider was dropped")
	}
}

func TestCATm1ConcurrentForcedRefreshesShareInflightResult(t *testing.T) {
	source := []byte(`{"anthropic":{"models":{"fixture":{"name":"Fixture","tool_call":true,"modalities":{"input":["text"]},"limit":{"context":4096,"output":512},"cost":{"input":1,"output":2}}}}}`)
	for _, test := range []struct {
		name      string
		storePath func(*testing.T) string
	}{
		{name: "pathless", storePath: func(*testing.T) string { return "" }},
		{name: "stored", storePath: func(t *testing.T) string { return filepath.Join(t.TempDir(), "models-store.json") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			started := make(chan struct{})
			release := make(chan struct{})
			lastModified := time.UnixMilli(generatedCatalogLastModified).Add(time.Hour).UTC()
			client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
				if requests.Add(1) == 1 {
					close(started)
				}
				<-release
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     http.Header{"Last-Modified": []string{lastModified.Format(http.TimeFormat)}},
					Body:       io.NopCloser(bytes.NewReader(source)),
				}, nil
			})}
			options := RefreshOptions{
				URL: "https://catalog.test/" + test.name, StorePath: test.storePath(t), Client: client,
				Now: func() time.Time { return lastModified }, Force: true,
			}
			type result struct {
				catalog *Catalog
				err     error
			}
			first := make(chan result, 1)
			go func() {
				catalog, err := Refresh(context.Background(), options)
				first <- result{catalog: catalog, err: err}
			}()
			<-started
			time.AfterFunc(100*time.Millisecond, func() { close(release) })
			secondCatalog, secondErr := Refresh(context.Background(), options)
			firstResult := <-first
			if firstResult.err != nil || secondErr != nil {
				t.Fatalf("shared refresh errors = first %v, second %v", firstResult.err, secondErr)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("concurrent forced refreshes made %d requests, want one", got)
			}
			if firstResult.catalog != secondCatalog {
				t.Fatal("concurrent refresh callers did not receive the shared result")
			}
			if _, ok := secondCatalog.Find("anthropic", "fixture"); !ok {
				t.Fatal("shared refresh result is missing the fetched model")
			}
		})
	}
}

func TestSharedRefreshJoinerHonorsItsOwnCancellation(t *testing.T) {
	source := []byte(`{"anthropic":{"models":{"fixture":{"name":"Fixture","tool_call":true,"modalities":{"input":["text"]},"limit":{"context":4096,"output":512},"cost":{"input":1,"output":2}}}}}`)
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	lastModified := time.UnixMilli(generatedCatalogLastModified).Add(time.Hour).UTC()
	client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			close(started)
		}
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Last-Modified": []string{lastModified.Format(http.TimeFormat)}},
			Body:       io.NopCloser(bytes.NewReader(source)),
		}, nil
	})}
	options := RefreshOptions{
		URL: "https://catalog.test/joiner-cancel", Client: client,
		Now: func() time.Time { return lastModified }, Force: true,
	}
	leaderResult := make(chan error, 1)
	go func() {
		_, err := Refresh(context.Background(), options)
		leaderResult <- err
	}()
	<-started
	joinerCtx, cancelJoiner := context.WithCancel(context.Background())
	cancelJoiner()
	if _, err := Refresh(joinerCtx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled joiner error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-leaderResult; err != nil {
		t.Fatalf("leader refresh after joiner cancellation: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("joiner cancellation disturbed the shared fetch: %d requests, want one", got)
	}
}

func TestCATm1CancellationAfterResponseDoesNotMutateStore(t *testing.T) {
	source := []byte(`{"anthropic":{"models":{"replacement":{"name":"Replacement","tool_call":true,"modalities":{"input":["text"]},"limit":{"context":4096,"output":512},"cost":{"input":1,"output":2}}}}}`)
	for _, test := range []struct {
		name     string
		response func(context.CancelFunc) *http.Response
	}{
		{
			name: "after headers",
			response: func(cancel context.CancelFunc) *http.Response {
				cancel()
				return &http.Response{
					StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader("unavailable")),
				}
			},
		},
		{
			name: "after parsing",
			response: func(cancel context.CancelFunc) *http.Response {
				return &http.Response{
					StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
					Body: &cancelAfterReadBody{data: source, cancel: cancel},
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "models-store.json")
			lastModified := generatedCatalogLastModified + time.Hour.Milliseconds()
			cached := &Catalog{providers: map[string]map[string]ai.Model{
				"anthropic": {"cached": {ID: "cached", Provider: "anthropic"}},
			}}
			if err := writeStoreResponse(storePath, cached, lastModified, storeTimestamp(lastModified), ""); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &http.Client{Transport: catalogRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return test.response(cancel), nil
			})}
			catalog, err := Refresh(ctx, RefreshOptions{
				URL: "https://catalog.test/cancel/" + test.name, StorePath: storePath, Client: client, Force: true,
			})
			if err != nil {
				t.Fatalf("refresh returned an error after response cancellation: %v", err)
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("context error = %v, want canceled", ctx.Err())
			}
			if _, ok := catalog.Find("anthropic", "cached"); !ok {
				t.Fatal("canceled refresh did not retain the previously loaded overlay")
			}
			if _, ok := catalog.Find("anthropic", "replacement"); ok {
				t.Fatal("canceled refresh published the parsed replacement")
			}
			after, err := os.ReadFile(storePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("canceled refresh changed the store\n--- before ---\n%s\n--- after ---\n%s", before, after)
			}
		})
	}
}

type cancelAfterReadBody struct {
	data   []byte
	cancel context.CancelFunc
	read   bool
}

func (body *cancelAfterReadBody) Read(buffer []byte) (int, error) {
	if body.read {
		return 0, io.EOF
	}
	body.read = true
	count := copy(buffer, body.data)
	body.cancel()
	return count, nil
}

func (*cancelAfterReadBody) Close() error { return nil }
