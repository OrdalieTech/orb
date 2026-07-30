package modes

import (
	"slices"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/codingagent"
)

// Upstream a3ee1d28: configured-but-missing model ids surface as unavailable
// entries instead of being hidden from /models.
func TestScopedModelsSelectorStateExposesUnavailableConfiguredModels(t *testing.T) {
	models := []ai.Model{
		{Provider: "anthropic", ID: "available", Name: "Available"},
		{Provider: "anthropic", ID: "second", Name: "Second"},
	}

	selected, unavailable := scopedModelsSelectorState(models, []string{"anthropic/available", "anthropic/unavailable-model"}, nil)
	if !selected["anthropic/available"] || selected["anthropic/second"] || !selected["anthropic/unavailable-model"] {
		t.Fatalf("selected = %#v", selected)
	}
	if !slices.Equal(unavailable, []string{"anthropic/unavailable-model"}) {
		t.Fatalf("unavailable = %#v", unavailable)
	}

	// Session scope wins over the configured matches, but configured no-match
	// patterns still surface as unavailable entries.
	selected, unavailable = scopedModelsSelectorState(
		models,
		[]string{"anthropic/second", "anthropic/unavailable-model"},
		[]codingagent.ScopedModel{{Model: models[0]}},
	)
	if !selected["anthropic/available"] || selected["anthropic/second"] || !selected["anthropic/unavailable-model"] {
		t.Fatalf("session-scope selected = %#v", selected)
	}
	if !slices.Equal(unavailable, []string{"anthropic/unavailable-model"}) {
		t.Fatalf("session-scope unavailable = %#v", unavailable)
	}

	// No filter: everything is enabled and nothing is unavailable.
	selected, unavailable = scopedModelsSelectorState(models, nil, nil)
	if len(unavailable) != 0 || !selected["anthropic/available"] || !selected["anthropic/second"] {
		t.Fatalf("unfiltered selected = %#v, unavailable = %#v", selected, unavailable)
	}

	// Patterns that match nothing still open the selector with only
	// unavailable entries (upstream regression test for issue #6949).
	selected, unavailable = scopedModelsSelectorState(nil, []string{"anthropic/gone-one", "anthropic/gone-two"}, nil)
	if !selected["anthropic/gone-one"] || !selected["anthropic/gone-two"] {
		t.Fatalf("all-unavailable selected = %#v", selected)
	}
	if !slices.Equal(unavailable, []string{"anthropic/gone-one", "anthropic/gone-two"}) {
		t.Fatalf("all-unavailable unavailable = %#v", unavailable)
	}
}

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
