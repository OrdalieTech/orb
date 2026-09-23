package models

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/OrdalieTech/orb/ai"
)

// The ChatGPT backend hides models whose minimum Codex client version exceeds
// client_version and rejects requests without it.
// ponytail: a far-future version lists everything; a model needing new Codex
// wire features still needs an adapter update before it works.
const codexModelsURL = "https://chatgpt.com/backend-api/codex/models?client_version=999.0.0"

type codexLiveModel struct {
	Slug            string   `json:"slug"`
	DisplayName     string   `json:"display_name"`
	Visibility      string   `json:"visibility"`
	ContextWindow   float64  `json:"context_window"`
	InputModalities []string `json:"input_modalities"`
	ReasoningLevels []any    `json:"supported_reasoning_levels"`
}

// RefreshCodex replaces the stored openai-codex models with the list the
// ChatGPT account offers. Known models keep their catalog metadata; new ones
// inherit it from the highest-priority known model, taking name, cost and
// output limit from the same-id OpenAI API model when models.dev has one.
// The entry's freshness fields stay untouched, so the models.dev gate is
// unaffected; without an entry (no models.dev refresh yet) nothing is written.
func RefreshCodex(ctx context.Context, options RefreshOptions, token, accountID string) error {
	if options.Client == nil {
		return errors.New("codex models refresh needs an HTTP client")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, cmp.Or(options.URL, codexModelsURL), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("chatgpt-account-id", accountID)
	request.Header.Set("originator", "pi")
	if options.UserAgent != "" {
		request.Header.Set("User-Agent", options.UserAgent)
	}
	response, err := options.Client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("codex models request failed: %s", response.Status)
	}
	var body struct {
		Models []codexLiveModel `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&body); err != nil {
		return fmt.Errorf("decode codex models: %w", err)
	}
	builtin, err := Builtin()
	if err != nil {
		return err
	}
	stored, err := loadStore(options.StorePath, options.StoreDocument)
	if err != nil {
		return err
	}
	known := builtin.Merge(stored)
	models := codexModels(known, body.Models)
	if len(models) == 0 {
		return nil
	}
	var documents []StoreDocument
	if options.StoreDocument != nil {
		documents = append(documents, options.StoreDocument)
	}
	return updateStore(options.StorePath, documents, func(stored *orderedStore) {
		if entry, ok := stored.entries["openai-codex"]; ok {
			entry.Models = models
			stored.entries["openai-codex"] = entry
		}
	})
}

func codexModels(known *Catalog, live []codexLiveModel) []ai.Model {
	var template ai.Model
	for _, item := range live {
		if model, ok := known.Find("openai-codex", item.Slug); ok && item.Visibility == "list" {
			template = model
			break
		}
	}
	var models []ai.Model
	for _, item := range live {
		if item.Visibility != "list" || item.Slug == "" {
			continue
		}
		model, ok := known.Find("openai-codex", item.Slug)
		if !ok {
			if template.ID == "" {
				continue
			}
			model = cloneModel(template)
			model.ID, model.Name, model.Cost = item.Slug, cmp.Or(item.DisplayName, item.Slug), ai.ModelCost{}
			if api, ok := known.Find("openai", item.Slug); ok {
				model.Name, model.Cost, model.MaxTokens = api.Name, api.Cost, api.MaxTokens
			}
			model.ContextWindow = cmp.Or(item.ContextWindow, model.ContextWindow)
			if len(item.InputModalities) > 0 {
				model.Input = ai.InputModalities{}
				for _, modality := range item.InputModalities {
					model.Input = append(model.Input, ai.InputModality(modality))
				}
			}
			model.Reasoning = len(item.ReasoningLevels) > 0
		}
		models = append(models, model)
	}
	return models
}
