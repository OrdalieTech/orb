package modes

import (
	"slices"
	"testing"
)

func TestApplyScopedModelSelectionKeepsUnavailablePatterns(t *testing.T) {
	mode := newF12AutocompleteMode(t, true)
	models := mode.session.AvailableModels()
	allIDs := []string{"anthropic/claude-sonnet-4-5", "openai/gpt-5.1", "openrouter/openai/gpt-5"}
	unavailable := []string{"anthropic/gone"}
	selected := map[string]bool{"anthropic/gone": true}
	for _, id := range allIDs {
		selected[id] = true
	}

	// All available models enabled plus an unavailable id: no session scope,
	// but the persisted filter keeps every id (upstream onPersist).
	mode.applyScopedModelSelection(models, unavailable, selected, true)
	if scoped := mode.session.ScopedModels(); len(scoped) != 0 {
		t.Fatalf("scoped models = %#v, want none", scoped)
	}
	if got, want := mode.session.EnabledModels(), append(append([]string(nil), allIDs...), "anthropic/gone"); !slices.Equal(got, want) {
		t.Fatalf("persisted patterns = %#v, want %#v", got, want)
	}

	// A partial available selection plus the unavailable id keeps the partial
	// session scope (upstream: unavailable ids never clear a partial scope).
	selected = map[string]bool{"anthropic/claude-sonnet-4-5": true, "anthropic/gone": true}
	mode.applyScopedModelSelection(models, unavailable, selected, false)
	scoped := mode.session.ScopedModels()
	if len(scoped) != 1 || scoped[0].Model.ID != "claude-sonnet-4-5" {
		t.Fatalf("partial scoped models = %#v", scoped)
	}

	// Removing the unavailable id while every available model is enabled
	// clears the persisted filter entirely.
	selected = map[string]bool{}
	for _, id := range allIDs {
		selected[id] = true
	}
	mode.applyScopedModelSelection(models, unavailable, selected, true)
	if got := mode.session.EnabledModels(); len(got) != 0 {
		t.Fatalf("cleared patterns = %#v, want none", got)
	}
}
