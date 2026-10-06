// Package models contains the generated model catalog and its persisted refresh overlay.
package models

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/OrdalieTech/orb/ai"
)

// Catalog is an immutable-by-convention provider/model lookup.
type Catalog struct {
	providers map[string]map[string]ai.Model
	base      sync.Once
	models    []ai.Model // BaseModels without an overlay
}

// Builtin loads the catalog generated from the committed models.dev snapshot.
// The decoded catalog is cached for the process lifetime: the embedded JSON is
// immutable and every accessor returns detached values, so callers share one
// instance instead of re-decoding ~518KB per startup surface. The JSON ships
// gzip-compressed in generated.go and gunzips here on first use.
func Builtin() (*Catalog, error) { return builtinCatalog() }

var builtinCatalog = sync.OnceValues(func() (*Catalog, error) {
	reader, err := gzip.NewReader(bytes.NewReader(generatedCatalogGzipJSON))
	if err != nil {
		return nil, fmt.Errorf("decompress model catalog: %w", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("decompress model catalog: %w", err)
	}
	if err := reader.Close(); err != nil {
		return nil, fmt.Errorf("decompress model catalog: %w", err)
	}
	return Decode(data)
})

// Decode loads the normalized provider-keyed catalog shape.
func Decode(data []byte) (*Catalog, error) {
	providers := make(map[string]map[string]ai.Model)
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, fmt.Errorf("decode model catalog: %w", err)
	}
	for providerID, entries := range providers {
		for modelID, model := range entries {
			if model.ID == "" {
				model.ID = modelID
			}
			if model.Name == "" {
				model.Name = model.ID
			}
			model.Provider = ai.ProviderID(providerID)
			entries[modelID] = model
		}
	}
	return &Catalog{providers: providers}, nil
}

// Models returns detached model values sorted by provider and model id.
func (catalog *Catalog) Models(provider ...string) []ai.Model {
	if catalog == nil {
		return []ai.Model{}
	}
	providerIDs := make([]string, 0, len(catalog.providers))
	if len(provider) > 0 {
		if len(provider) != 1 {
			panic("models: Models accepts at most one provider")
		}
		providerIDs = append(providerIDs, provider[0])
	} else {
		for providerID := range catalog.providers {
			providerIDs = append(providerIDs, providerID)
		}
		slices.Sort(providerIDs)
	}
	result := make([]ai.Model, 0)
	for _, providerID := range providerIDs {
		ids := make([]string, 0, len(catalog.providers[providerID]))
		for id := range catalog.providers[providerID] {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			model := catalog.providers[providerID][id]
			result = append(result, *model.Clone())
		}
	}
	return result
}

// Find returns a detached model value.
func (catalog *Catalog) Find(provider, id string) (ai.Model, bool) {
	model, ok := catalog.providers[provider][id]
	return *model.Clone(), ok
}

// Merge overlays models by provider and id.
func (catalog *Catalog) Merge(overlay *Catalog) *Catalog {
	if catalog == nil {
		catalog = &Catalog{providers: make(map[string]map[string]ai.Model)}
	}
	if overlay == nil {
		return &Catalog{providers: cloneProviders(catalog.providers)}
	}
	merged := cloneProviders(catalog.providers)
	for providerID, entries := range overlay.providers {
		if merged[providerID] == nil {
			merged[providerID] = make(map[string]ai.Model)
		}
		for id, model := range entries {
			merged[providerID][id] = *model.Clone()
		}
	}
	return &Catalog{providers: merged}
}

// MergedModels returns what Merge(overlay).Models() returns without
// materializing the intermediate merged catalog: each model is cloned exactly
// once into the sorted result.
func (catalog *Catalog) MergedModels(overlay *Catalog) []ai.Model {
	if catalog == nil {
		catalog = &Catalog{}
	}
	providerIDs := make(map[string]struct{}, len(catalog.providers))
	for providerID := range catalog.providers {
		providerIDs[providerID] = struct{}{}
	}
	if overlay != nil {
		for providerID := range overlay.providers {
			providerIDs[providerID] = struct{}{}
		}
	}
	sortedProviders := make([]string, 0, len(providerIDs))
	for providerID := range providerIDs {
		sortedProviders = append(sortedProviders, providerID)
	}
	slices.Sort(sortedProviders)
	count := 0
	for _, providerID := range sortedProviders {
		count += len(catalog.providers[providerID])
		if overlay != nil {
			for id := range overlay.providers[providerID] {
				if _, exists := catalog.providers[providerID][id]; !exists {
					count++
				}
			}
		}
	}
	result := make([]ai.Model, 0, count)
	ids := make([]string, 0)
	for _, providerID := range sortedProviders {
		base, over := catalog.providers[providerID], map[string]ai.Model(nil)
		if overlay != nil {
			over = overlay.providers[providerID]
		}
		ids = ids[:0]
		for id := range base {
			ids = append(ids, id)
		}
		for id := range over {
			if _, exists := base[id]; !exists {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			model, ok := over[id]
			if !ok {
				model = base[id]
			}
			result = append(result, *model.Clone())
		}
	}
	return result
}

// BaseModels is MergedModels for callers that do not modify the models or
// anything they point to: without an overlay, they all share one sorted copy,
// so each model registry need not clone and sort the catalog again.
func (catalog *Catalog) BaseModels(overlay *Catalog) []ai.Model {
	if catalog == nil || overlay != nil && len(overlay.providers) > 0 {
		return catalog.MergedModels(overlay)
	}
	catalog.base.Do(func() { catalog.models = catalog.MergedModels(nil) })
	return catalog.models
}

func (catalog *Catalog) MarshalJSON() ([]byte, error) {
	if catalog == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(catalog.providers)
}

func cloneProviders(source map[string]map[string]ai.Model) map[string]map[string]ai.Model {
	result := make(map[string]map[string]ai.Model, len(source))
	for providerID, entries := range source {
		result[providerID] = make(map[string]ai.Model, len(entries))
		for id, model := range entries {
			result[providerID][id] = *model.Clone()
		}
	}
	return result
}
