package modes

import (
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/tui"
)

func TestModelSelectorSearchAndKeyboardParity(t *testing.T) {
	alpha1 := ai.Model{Provider: "anthropic", ID: "alpha-1", Name: "Alpha One"}
	alpha2 := ai.Model{Provider: "anthropic", ID: "alpha-2", Name: "Alpha Two"}
	alpha3 := ai.Model{Provider: "anthropic", ID: "alpha-3", Name: "Alpha Three"}
	direct := ai.Model{Provider: "openai", ID: "gpt-5", Name: "GPT-5", BaseURL: "direct"}
	proxy := ai.Model{Provider: "openrouter", ID: "openai/gpt-5", Name: "Proxy GPT-5", BaseURL: "proxy"}
	models := []ai.Model{proxy, alpha1, alpha2, alpha3, direct}
	scoped := []agent.ScopedModel{{Model: alpha2}, {Model: alpha3}, {Model: alpha1}}

	var selected ai.Model
	cancelled := false
	selector := NewModelSelectorComponent(&alpha1, models, scoped, func(model ai.Model) {
		selected = model
	}, func() {
		cancelled = true
	}, "alpha")

	if got := selector.searchInput.GetValue(); got != "alpha" {
		t.Fatalf("initial query = %q, want alpha", got)
	}
	if selector.scope != modelScopeScoped || selector.selectedIndex != 0 ||
		selector.filteredModels[0].model.ID != "alpha-2" {
		t.Fatalf("initial scoped selection = scope %q index %d models %#v", selector.scope, selector.selectedIndex, selector.filteredModels)
	}

	selector.HandleInput(tui.KeyEvent{Raw: "\x1b[B"})
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b[B"})
	selector.HandleInput(tui.KeyEvent{Raw: "\x15"})
	if selector.searchInput.GetValue() != "" || selector.selectedIndex != 2 ||
		selector.filteredModels[selector.selectedIndex].model.ID != "alpha-1" {
		t.Fatalf("cleared query did not preserve clamped selection: index %d models %#v", selector.selectedIndex, selector.filteredModels)
	}

	selector.HandleInput(tui.KeyEvent{Raw: "\t"})
	if selector.scope != modelScopeAll {
		t.Fatalf("tab scope = %q, want all", selector.scope)
	}
	for range 3 {
		selector.HandleInput(tui.KeyEvent{Raw: "\x1b[B"})
	}
	selector.HandleInput(tui.KeyEvent{Raw: "openai/gpt-5"})
	if selector.selectedIndex != 0 || len(selector.filteredModels) != 2 ||
		selector.filteredModels[0].model.BaseURL != "direct" {
		t.Fatalf("provider-prefixed search = index %d models %#v", selector.selectedIndex, selector.filteredModels)
	}

	selector.HandleInput(tui.KeyEvent{Raw: "\x1b[A"})
	if selector.selectedIndex != 1 {
		t.Fatalf("up wrap index = %d, want 1", selector.selectedIndex)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b[B"})
	if selector.selectedIndex != 0 {
		t.Fatalf("down wrap index = %d, want 0", selector.selectedIndex)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\r"})
	if selected.BaseURL != "direct" || selected.Provider != direct.Provider || selected.ID != direct.ID {
		t.Fatalf("selected model = %#v, want actual direct model", selected)
	}

	cancelSelector := NewModelSelectorComponent(&alpha1, models, scoped, nil, func() {
		cancelled = true
	}, "")
	cancelSelector.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	if !cancelled {
		t.Fatal("escape did not cancel")
	}
}
