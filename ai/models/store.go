package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/models/internal/cataloggen"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/internal/filelock"
	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/internal/ptr"
)

const ModelsDevURL = "https://models.dev/api.json"

// remoteCatalogRefreshInterval mirrors upstream
// REMOTE_CATALOG_REFRESH_INTERVAL_MS (remote-catalog-provider.ts).
const remoteCatalogRefreshInterval = 4 * time.Hour

type storedProvider struct {
	Models    []ai.Model `json:"models"`
	CheckedAt int64      `json:"checkedAt,omitempty"`
	// LastModified is the catalog content timestamp (UnixMilli). Overlay
	// entries for bundled providers lose to a newer builtin catalog. A pointer
	// preserves the upstream distinction between an absent field and the
	// explicit zero written for 404/501 or a missing Last-Modified header.
	LastModified *int64 `json:"lastModified,omitempty"`
	ETag         string `json:"etag,omitempty"`
}

// orderedStore keeps upstream's JSON.stringify member order, each value a
// storedProvider; jsonwire leaves <, >, and & literal where encoding/json
// would HTML-escape them.
type orderedStore = jsonwire.OrderedObject

// LoadStore restores the provider-scoped models-store.json overlay. Entries
// for providers bundled in the builtin catalog lose to a newer builtin: the
// overlay only applies when its lastModified is newer than the bundled catalog
// build time (upstream remote-catalog-provider.ts remoteModels).
func LoadStore(path string) (*Catalog, error) { return loadStore(path, nil) }

func LoadStoreDocument(document document.Document) (*Catalog, error) { return loadStore("", document) }

// StoreFile is models-store.json at path, locked like upstream's FileModelsStore.
func StoreFile(path string) document.Document {
	return filelock.File{Path: path, Perm: 0o600, Atomic: true}
}

func readStore(path string, store document.Document) ([]byte, error) {
	if store == nil {
		store = StoreFile(path)
	}
	return store.Read(context.Background())
}

func loadStore(path string, document document.Document) (*Catalog, error) {
	data, err := readStore(path, document)
	if err == nil && len(data) == 0 {
		return &Catalog{providers: make(map[string]map[string]ai.Model)}, nil
	}
	if err != nil {
		return nil, err
	}
	var stored map[string]storedProvider
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("decode model store: %w", err)
	}
	providers := make(map[string]map[string]ai.Model, len(stored))
	for providerID, entry := range stored {
		// ponytail: models.dev omits Last-Modified; its ETag distinguishes a successful catalog response from 404/501.
		if entry.LastModified != nil && *entry.LastModified == 0 && entry.ETag != "" {
			entry.LastModified = &entry.CheckedAt
		}
		if builtinProviderIDs()[providerID] && (entry.LastModified == nil || *entry.LastModified <= generatedCatalogLastModified) {
			continue
		}
		providers[providerID] = make(map[string]ai.Model, len(entry.Models))
		for _, model := range entry.Models {
			if model.Provider != ai.ProviderID(providerID) || model.ID == "" {
				continue
			}
			providers[providerID][model.ID] = model
		}
	}
	return &Catalog{providers: providers}, nil
}

var builtinProviderIDs = sync.OnceValue(func() map[string]bool {
	result := make(map[string]bool)
	catalog, err := Builtin()
	if err != nil {
		return result
	}
	for providerID := range catalog.providers {
		result[providerID] = true
	}
	return result
})

type RefreshOptions struct {
	StoreDocument document.Document
	URL           string
	StorePath     string
	Client        *http.Client
	Now           func() time.Time
	// UserAgent identifies the client on the catalog request (upstream sends a
	// pi User-Agent from remote-catalog-provider.ts).
	UserAgent string
	// Force bypasses the checkedAt refresh gate.
	Force bool
}

// OrbUserAgent formats the catalog-refresh User-Agent, mirroring upstream
// getPiUserAgent (pi-user-agent.ts) with Orb's public identity.
func OrbUserAgent(version string) string {
	return fmt.Sprintf("orb/%s (%s; %s; %s)", version, runtime.GOOS, runtime.Version(), runtime.GOARCH)
}

type refreshCall struct {
	done    chan struct{}
	catalog *Catalog
	err     error
}

type refreshCallGroup struct {
	mu    sync.Mutex
	calls map[string]*refreshCall
}

// do shares one in-flight refresh per key; a joining caller's wait honors its
// own cancellation (upstream model-catalog-refresh.ts raceWithAbortSignal).
// The fetch itself runs on the initiating caller's context: the store's
// cancellation contract (a canceled refresh returns the stored overlay and
// never mutates the store) needs the refresh to observe that cancellation
// synchronously at its commit points, which upstream's detached refcounted
// controller cannot provide — see the 2026-08-17 sync amendments.
func (group *refreshCallGroup) do(ctx context.Context, key string, refresh func() (*Catalog, error)) (*Catalog, error) {
	group.mu.Lock()
	if group.calls == nil {
		group.calls = make(map[string]*refreshCall)
	}
	if call := group.calls[key]; call != nil {
		group.mu.Unlock()
		select {
		case <-call.done:
			return call.catalog, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	group.calls[key] = call
	group.mu.Unlock()

	call.catalog, call.err = refresh()
	group.mu.Lock()
	delete(group.calls, key)
	close(call.done)
	group.mu.Unlock()
	return call.catalog, call.err
}

var catalogRefreshCalls refreshCallGroup

// Refresh fetches models.dev and replaces only fetched providers in the
// persisted dynamic overlay. A completed refresh within the last 4 hours skips
// the network and returns the stored overlay (upstream
// remote-catalog-provider.ts gates on checkedAt plus lastModified presence).
func Refresh(ctx context.Context, options RefreshOptions) (*Catalog, error) {
	endpoint := options.URL
	if endpoint == "" {
		endpoint = ModelsDevURL
	}
	key := "url:" + endpoint
	if options.StorePath != "" {
		key = "store:" + options.StorePath
	}
	if options.StoreDocument != nil {
		key = fmt.Sprintf("document:%p", options.StoreDocument)
	}
	return catalogRefreshCalls.do(ctx, key, func() (*Catalog, error) {
		return refresh(ctx, options, endpoint)
	})
}

func refresh(ctx context.Context, options RefreshOptions, endpoint string) (*Catalog, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	current := &Catalog{providers: make(map[string]map[string]ai.Model)}
	var validator string
	var hasStoredCatalog bool
	if options.StorePath != "" || options.StoreDocument != nil {
		var err error
		current, err = loadStore(options.StorePath, options.StoreDocument)
		if err != nil {
			return nil, err
		}
		if !options.Force && storeFreshAt(options.StorePath, now(), options.StoreDocument) {
			return current, nil
		}
		validator, hasStoredCatalog = storeValidator(options.StorePath, options.StoreDocument)
	}
	client := options.Client
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if options.UserAgent != "" {
		request.Header.Set("User-Agent", options.UserAgent)
	}
	if validator != "" {
		request.Header.Set("If-None-Match", validator)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if ctx.Err() != nil {
		return current, nil
	}
	checkedAt := now().UnixMilli()
	if response.StatusCode == http.StatusNotModified && hasStoredCatalog {
		if err := stampStoreResponse(options.StorePath, checkedAt, false, options.StoreDocument); err != nil {
			return nil, err
		}
		return current, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		unavailable := response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusNotImplemented
		if options.StorePath != "" || options.StoreDocument != nil {
			if err := stampStoreResponse(options.StorePath, checkedAt, unavailable, options.StoreDocument); err != nil {
				return nil, err
			}
		}
		if unavailable {
			return current, nil
		}
		return nil, fmt.Errorf("models.dev request failed: %s", response.Status)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	providers, err := cataloggen.Generate(cataloggen.Sources{ModelsDev: data})
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return current, nil
	}
	catalog := &Catalog{providers: providers}
	if options.StorePath != "" || options.StoreDocument != nil {
		lastModified := int64(0)
		if parsed, parseErr := http.ParseTime(response.Header.Get("Last-Modified")); parseErr == nil {
			lastModified = parsed.UnixMilli()
		}
		if err := writeStoreResponse(options.StorePath, catalog, checkedAt, &lastModified, response.Header.Get("ETag"), options.StoreDocument); err != nil {
			return nil, err
		}
		return loadStore(options.StorePath, options.StoreDocument)
	}
	return catalog, nil
}

func storeValidator(path string, documents ...document.Document) (string, bool) {
	var document document.Document
	if len(documents) > 0 {
		document = documents[0]
	}
	data, err := readStore(path, document)
	if err != nil || len(data) == 0 {
		return "", false
	}
	stored, err := decodeOrderedStore(data)
	if err != nil {
		return "", false
	}
	builtin := builtinProviderIDs()
	hasStoredCatalog := false
	for _, member := range stored {
		entry := member.Value.(storedProvider)
		if !builtin[member.Name] {
			continue
		}
		hasStoredCatalog = true
		if len(entry.Models) > 0 && entry.ETag != "" {
			return entry.ETag, true
		}
	}
	return "", hasStoredCatalog
}

// storeFreshAt reports whether a models.dev refresh with both upstream
// freshness fields completed within the gating interval.
func storeFreshAt(path string, now time.Time, documents ...document.Document) bool {
	var document document.Document
	if len(documents) > 0 {
		document = documents[0]
	}
	data, err := readStore(path, document)
	if err != nil || len(data) == 0 {
		return false
	}
	var stored map[string]storedProvider
	if json.Unmarshal(data, &stored) != nil {
		return false
	}
	var latest int64
	builtin := builtinProviderIDs()
	for providerID, entry := range stored {
		if builtin[providerID] && entry.CheckedAt != 0 && entry.LastModified != nil && entry.CheckedAt > latest {
			latest = entry.CheckedAt
		}
	}
	return latest != 0 && now.UnixMilli()-latest < remoteCatalogRefreshInterval.Milliseconds()
}

func updateStore(path string, documents []document.Document, change func(*orderedStore)) error {
	store := StoreFile(path)
	if len(documents) > 0 && documents[0] != nil {
		store = documents[0]
	}
	return updateOrderedStore(context.Background(), store, change)
}

func updateOrderedStore(ctx context.Context, store document.Document, change func(*orderedStore)) error {
	return store.Update(ctx, func(data []byte) ([]byte, error) {
		stored := orderedStore{}
		var err error
		if len(data) > 0 {
			stored, err = decodeOrderedStore(data)
			if err != nil {
				return nil, fmt.Errorf("decode model store: %w", err)
			}
		}
		change(&stored)
		return jsonwire.MarshalIndent(stored, "", "  ")
	})
}

// StoreEntry is one provider's models-store.json entry.
type StoreEntry = storedProvider

// ReadStoreEntry returns providerID's entry, or nil when it has none.
func ReadStoreEntry(ctx context.Context, store document.Document, providerID string) (*StoreEntry, error) {
	data, err := store.Read(ctx)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	stored, err := decodeOrderedStore(data)
	if entry, ok := stored.Value(providerID); ok && err == nil {
		entry := entry.(storedProvider)
		return &entry, nil
	}
	return nil, err
}

// WriteStoreEntry replaces providerID's entry in place, appending a new one;
// nil deletes it. Other entries keep their order and fields.
func WriteStoreEntry(ctx context.Context, store document.Document, providerID string, entry *StoreEntry) error {
	return updateOrderedStore(ctx, store, func(stored *orderedStore) {
		if entry == nil {
			stored.Delete(providerID)
		} else {
			stored.Set(providerID, *entry)
		}
	})
}

func writeStoreResponse(path string, catalog *Catalog, checkedAt int64, lastModified *int64, etag string, documents ...document.Document) error {
	return updateStore(path, documents, func(stored *orderedStore) {
		providerIDs := make([]string, 0, len(catalog.providers))
		for id := range catalog.providers {
			providerIDs = append(providerIDs, id)
		}
		slices.Sort(providerIDs)
		for _, id := range providerIDs {
			stored.Set(id, storedProvider{Models: catalog.Models(id), CheckedAt: checkedAt, LastModified: ptr.Clone(lastModified), ETag: etag})
		}
	})
}

func stampStoreResponse(path string, checkedAt int64, unavailable bool, documents ...document.Document) error {
	return updateStore(path, documents, func(stored *orderedStore) {
		ids := make([]string, 0, len(builtinProviderIDs()))
		for id := range builtinProviderIDs() {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			value, exists := stored.Value(id)
			entry, _ := value.(storedProvider)
			if !exists {
				entry.Models = []ai.Model{}
			}
			entry.CheckedAt = checkedAt
			if unavailable {
				entry.LastModified = timestamp(0)
				entry.ETag = ""
			}
			stored.Set(id, entry)
		}
	})
}

func timestamp(value int64) *int64 { return &value }

func decodeOrderedStore(data []byte) (orderedStore, error) {
	object, ok := jsonwire.ParseRawObject(data)
	if !ok {
		return nil, errors.New("model store must be a JSON object")
	}
	store := make(orderedStore, 0, len(object))
	for _, member := range object {
		var entry storedProvider
		if err := json.Unmarshal(member.Value, &entry); err != nil {
			return nil, err
		}
		store = append(store, jsonwire.OrderedMember{Name: member.Name, Value: entry})
	}
	return store, nil
}
