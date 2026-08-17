package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/models/internal/cataloggen"
	"github.com/OrdalieTech/orb/internal/filelock"
	"github.com/OrdalieTech/orb/internal/jsonwire"
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

type orderedStore struct {
	order   []string
	entries map[string]storedProvider
}

func (store orderedStore) MarshalJSON() ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, providerID := range store.order {
		if index > 0 {
			output.WriteByte(',')
		}
		key, err := jsonwire.Marshal(providerID)
		if err != nil {
			return nil, err
		}
		value, err := jsonwire.Marshal(store.entries[providerID])
		if err != nil {
			return nil, err
		}
		output.Write(key)
		output.WriteByte(':')
		output.Write(value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

// LoadStore restores the provider-scoped models-store.json overlay. Entries
// for providers bundled in the builtin catalog lose to a newer builtin: the
// overlay only applies when its lastModified is newer than the bundled catalog
// build time (upstream remote-catalog-provider.ts remoteModels).
func LoadStore(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
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
	URL       string
	StorePath string
	Client    *http.Client
	Now       func() time.Time
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
	if options.StorePath != "" {
		var err error
		current, err = LoadStore(options.StorePath)
		if err != nil {
			return nil, err
		}
		if !options.Force && storeFreshAt(options.StorePath, now()) {
			return current, nil
		}
		validator, hasStoredCatalog = storeValidator(options.StorePath)
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
		if err := stampStoreResponse(options.StorePath, checkedAt, false); err != nil {
			return nil, err
		}
		return current, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		unavailable := response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusNotImplemented
		if options.StorePath != "" {
			if err := stampStoreResponse(options.StorePath, checkedAt, unavailable); err != nil {
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
	if options.StorePath != "" {
		lastModified := int64(0)
		if parsed, parseErr := http.ParseTime(response.Header.Get("Last-Modified")); parseErr == nil {
			lastModified = parsed.UnixMilli()
		}
		if err := writeStoreResponse(options.StorePath, catalog, checkedAt, &lastModified, response.Header.Get("ETag")); err != nil {
			return nil, err
		}
		return LoadStore(options.StorePath)
	}
	return catalog, nil
}

func storeValidator(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return "", false
	}
	stored, err := decodeOrderedStore(data)
	if err != nil {
		return "", false
	}
	builtin := builtinProviderIDs()
	hasStoredCatalog := false
	for _, providerID := range stored.order {
		entry := stored.entries[providerID]
		if !builtin[providerID] {
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
func storeFreshAt(path string, now time.Time) bool {
	data, err := os.ReadFile(path)
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

func writeStore(path string, catalog *Catalog, checkedAt int64, lastModified *int64) (err error) {
	return writeStoreResponse(path, catalog, checkedAt, lastModified, "")
}

func writeStoreResponse(path string, catalog *Catalog, checkedAt int64, lastModified *int64, etag string) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	release, lockErr := filelock.Acquire(path)
	if lockErr != nil {
		return lockErr
	}
	defer func() { err = errors.Join(err, release()) }()
	stored := orderedStore{entries: make(map[string]storedProvider)}
	data, readErr := os.ReadFile(path)
	if readErr == nil && len(data) > 0 {
		stored, err = decodeOrderedStore(data)
		if err != nil {
			return fmt.Errorf("decode model store: %w", err)
		}
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	providerIDs := make([]string, 0, len(catalog.providers))
	for providerID := range catalog.providers {
		providerIDs = append(providerIDs, providerID)
	}
	slices.Sort(providerIDs)
	for _, providerID := range providerIDs {
		if _, exists := stored.entries[providerID]; !exists {
			stored.order = append(stored.order, providerID)
		}
		stored.entries[providerID] = storedProvider{
			Models: catalog.Models(providerID), CheckedAt: checkedAt, LastModified: cloneTimestamp(lastModified), ETag: etag,
		}
	}
	return writeOrderedStore(path, stored)
}

func stampStoreResponse(path string, checkedAt int64, unavailable bool) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	release, lockErr := filelock.Acquire(path)
	if lockErr != nil {
		return lockErr
	}
	defer func() { err = errors.Join(err, release()) }()
	stored := orderedStore{entries: make(map[string]storedProvider)}
	data, readErr := os.ReadFile(path)
	if readErr == nil && len(data) > 0 {
		stored, err = decodeOrderedStore(data)
		if err != nil {
			return fmt.Errorf("decode model store: %w", err)
		}
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	providerIDs := make([]string, 0, len(builtinProviderIDs()))
	for providerID := range builtinProviderIDs() {
		providerIDs = append(providerIDs, providerID)
	}
	slices.Sort(providerIDs)
	for _, providerID := range providerIDs {
		entry, exists := stored.entries[providerID]
		if !exists {
			stored.order = append(stored.order, providerID)
			entry.Models = []ai.Model{}
		}
		entry.CheckedAt = checkedAt
		if unavailable {
			entry.LastModified = timestamp(0)
			entry.ETag = ""
		}
		stored.entries[providerID] = entry
	}
	return writeOrderedStore(path, stored)
}

func timestamp(value int64) *int64 { return &value }

func cloneTimestamp(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// writeOrderedStore persists with JSON.stringify(current, null, 2) byte parity
// (upstream models-store.ts): jsonwire leaves <, >, and & literal where
// json.MarshalIndent would HTML-escape them.
func writeOrderedStore(path string, stored orderedStore) error {
	data, err := jsonwire.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".models-store-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func decodeOrderedStore(data []byte) (orderedStore, error) {
	store := orderedStore{entries: make(map[string]storedProvider)}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return store, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return store, errors.New("model store must be an object")
	}
	for decoder.More() {
		providerID, err := decoder.Token()
		if err != nil {
			return store, err
		}
		var entry storedProvider
		if err := decoder.Decode(&entry); err != nil {
			return store, err
		}
		id := providerID.(string)
		if _, exists := store.entries[id]; !exists {
			store.order = append(store.order, id)
		}
		store.entries[id] = entry
	}
	if _, err := decoder.Token(); err != nil {
		return store, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing model store content")
		}
		return store, err
	}
	return store, nil
}
