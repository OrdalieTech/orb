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
	"slices"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/models/internal/cataloggen"
	"github.com/gofrs/flock"
)

const ModelsDevURL = "https://models.dev/api.json"

type storedProvider struct {
	Models    []ai.Model `json:"models"`
	CheckedAt int64      `json:"checkedAt,omitempty"`
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
		key, err := json.Marshal(providerID)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(store.entries[providerID])
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

// LoadStore restores the provider-scoped models-store.json overlay.
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
		providers[providerID] = make(map[string]ai.Model, len(entry.Models))
		for _, model := range entry.Models {
			if model.Provider != ai.ProviderID(providerID) || model.ID == "" {
				continue
			}
			applyCorrection(&model)
			providers[providerID][model.ID] = model
		}
	}
	return &Catalog{providers: providers}, nil
}

type RefreshOptions struct {
	URL       string
	StorePath string
	Client    *http.Client
	Now       func() time.Time
}

// Refresh fetches models.dev and replaces only fetched providers in the persisted dynamic overlay.
func Refresh(ctx context.Context, options RefreshOptions) (*Catalog, error) {
	endpoint := options.URL
	if endpoint == "" {
		endpoint = ModelsDevURL
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
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, fmt.Errorf("models.dev request failed: %s", response.Status)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	providers, err := cataloggen.Generate(data)
	if err != nil {
		return nil, err
	}
	catalog := &Catalog{providers: providers}
	if options.StorePath != "" {
		now := options.Now
		if now == nil {
			now = time.Now
		}
		if err := writeStore(options.StorePath, catalog, now().UnixMilli()); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

func writeStore(path string, catalog *Catalog, checkedAt int64) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Unlock()) }()
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
		stored.entries[providerID] = storedProvider{Models: catalog.Models(providerID), CheckedAt: checkedAt}
	}
	data, err = json.MarshalIndent(stored, "", "  ")
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
