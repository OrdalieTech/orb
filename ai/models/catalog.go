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
			result = append(result, cloneModel(catalog.providers[providerID][id]))
		}
	}
	return result
}

// Find returns a detached model value.
func (catalog *Catalog) Find(provider, id string) (ai.Model, bool) {
	model, ok := catalog.providers[provider][id]
	return cloneModel(model), ok
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
			merged[providerID][id] = cloneModel(model)
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
	result := make([]ai.Model, 0)
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
			result = append(result, cloneModel(model))
		}
	}
	return result
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
			result[providerID][id] = cloneModel(model)
		}
	}
	return result
}

func cloneModel(model ai.Model) ai.Model {
	model.Input = append(ai.InputModalities(nil), model.Input...)
	if model.Cost.Tiers != nil {
		tiers := append([]ai.ModelCostTier(nil), (*model.Cost.Tiers)...)
		model.Cost.Tiers = &tiers
	}
	if model.ThinkingLevelMap != nil {
		mapping := make(map[ai.ModelThinkingLevel]*string, len(*model.ThinkingLevelMap))
		for level, value := range *model.ThinkingLevelMap {
			if value == nil {
				mapping[level] = nil
				continue
			}
			copy := *value
			mapping[level] = &copy
		}
		model.ThinkingLevelMap = &mapping
	}
	if model.Headers != nil {
		headers := make(map[string]string, len(*model.Headers))
		for name, value := range *model.Headers {
			headers[name] = value
		}
		model.Headers = &headers
	}
	model.Compat = append(json.RawMessage(nil), model.Compat...)
	return model
}
