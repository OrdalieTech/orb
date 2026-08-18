package modes

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
)

const modelSelectorMaxVisible = 10

type modelSelectorScope string

const (
	modelScopeAll    modelSelectorScope = "all"
	modelScopeScoped modelSelectorScope = "scoped"
)

type modelSelectorItem struct{ model ai.Model }

// ModelSelectorComponent is the searchable picker used only by /model.
type ModelSelectorComponent struct {
	container      *tui.Container
	searchInput    *tui.Input
	headerText     *tui.Text
	listContainer  *tui.Container
	scopeText      *tui.Text
	allModels      []modelSelectorItem
	scopedModels   []modelSelectorItem
	activeModels   []modelSelectorItem
	filteredModels []modelSelectorItem
	selectedIndex  int
	window         tui.ListWindow
	currentModel   *ai.Model
	scope          modelSelectorScope
	onSelect       func(ai.Model)
	onCancel       func()
	rows           listRowOffsets
}

func NewModelSelectorComponent(
	currentModel *ai.Model,
	models []ai.Model,
	scoped []agent.ScopedModel,
	onSelect func(ai.Model),
	onCancel func(),
	initialSearchInput string,
) *ModelSelectorComponent {
	component := &ModelSelectorComponent{
		container:     &tui.Container{},
		searchInput:   tui.NewInput(),
		listContainer: &tui.Container{},
		currentModel:  currentModel,
		onSelect:      onSelect,
		onCancel:      onCancel,
		scope:         modelScopeAll,
	}
	component.loadModels(models, scoped)
	if len(component.scopedModels) > 0 {
		component.scope = modelScopeScoped
	}
	component.activeModels = component.modelsForScope()
	component.selectCurrentModel()

	component.container.AddChild(extensionDialogBorder())
	component.container.AddChild(tui.NewSpacer(1))
	if len(component.scopedModels) > 0 {
		component.scopeText = tui.NewText(component.scopeLabel(), 0, 0, nil)
		component.container.AddChild(component.scopeText)
		component.container.AddChild(tui.NewText(
			KeyHint("tui.input.tab", "scope")+theme.FG("muted", " (all/scoped)"),
			0, 0, nil,
		))
	} else {
		component.container.AddChild(tui.NewText(
			theme.FG("warning", "Only showing models from configured providers. Use /login to add providers."),
			0, 0, nil,
		))
	}
	component.container.AddChild(tui.NewSpacer(1))
	component.searchInput.OnSubmit = func(string) { component.confirmSelection() }
	if initialSearchInput != "" {
		component.searchInput.HandleInput(tui.KeyEvent{Raw: initialSearchInput})
	}
	component.container.AddChild(component.searchInput)
	component.container.AddChild(tui.NewSpacer(1))
	component.headerText = tui.NewText("", 0, 0, nil)
	component.container.AddChild(component.headerText)
	component.container.AddChild(component.listContainer)
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(extensionDialogBorder())
	component.filterModels(component.searchInput.GetValue())
	return component
}

func (component *ModelSelectorComponent) loadModels(models []ai.Model, scoped []agent.ScopedModel) {
	component.allModels = make([]modelSelectorItem, len(models))
	refreshed := make(map[string]ai.Model, len(models))
	for index, model := range models {
		component.allModels[index] = modelSelectorItem{model: model}
		refreshed[modelSelectorKey(model)] = model
	}
	slices.SortStableFunc(component.allModels, func(left, right modelSelectorItem) int {
		leftCurrent := modelSelectorModelsEqual(component.currentModel, &left.model)
		rightCurrent := modelSelectorModelsEqual(component.currentModel, &right.model)
		if leftCurrent != rightCurrent {
			if leftCurrent {
				return -1
			}
			return 1
		}
		return strings.Compare(string(left.model.Provider), string(right.model.Provider))
	})
	component.scopedModels = make([]modelSelectorItem, len(scoped))
	for index, entry := range scoped {
		model := entry.Model
		if current, ok := refreshed[modelSelectorKey(model)]; ok {
			model = current
		}
		component.scopedModels[index] = modelSelectorItem{model: model}
	}
}

func modelSelectorKey(model ai.Model) string {
	return string(model.Provider) + "\x00" + model.ID
}

func modelSelectorModelsEqual(left, right *ai.Model) bool {
	return left != nil && right != nil && left.Provider == right.Provider && left.ID == right.ID
}

func modelSelectorSearchText(model ai.Model) string {
	provider := string(model.Provider)
	name := ""
	if model.Name != "" {
		name = " " + model.Name
	}
	return provider + " " + provider + "/" + model.ID + " " + provider + " " + model.ID + name
}

func (component *ModelSelectorComponent) modelsForScope() []modelSelectorItem {
	if component.scope == modelScopeScoped {
		return component.scopedModels
	}
	return component.allModels
}

func (component *ModelSelectorComponent) selectCurrentModel() {
	component.selectedIndex = 0
	for index := range component.activeModels {
		if modelSelectorModelsEqual(component.currentModel, &component.activeModels[index].model) {
			component.selectedIndex = index
			return
		}
	}
}

func (component *ModelSelectorComponent) scopeLabel() string {
	all := theme.FG("muted", "all")
	scoped := theme.FG("muted", "scoped")
	if component.scope == modelScopeAll {
		all = theme.FG("accent", "all")
	} else {
		scoped = theme.FG("accent", "scoped")
	}
	return theme.FG("muted", "Scope: ") + all + theme.FG("muted", " | ") + scoped
}

func (component *ModelSelectorComponent) setScope(scope modelSelectorScope) {
	if component.scope == scope {
		return
	}
	component.scope = scope
	component.activeModels = component.modelsForScope()
	component.selectCurrentModel()
	component.filterModels(component.searchInput.GetValue())
	component.scopeText.SetText(component.scopeLabel())
}

func (component *ModelSelectorComponent) filterModels(query string) {
	if query == "" {
		component.filteredModels = component.activeModels
		component.selectedIndex = min(component.selectedIndex, max(0, len(component.filteredModels)-1))
	} else {
		component.filteredModels = tui.FuzzyFilter(component.activeModels, query, func(item modelSelectorItem) string {
			return modelSelectorSearchText(item.model)
		})
		component.selectedIndex = 0
	}
	component.updateList()
}

func modelSelectorTokens(count float64) string {
	trim := func(value float64) string {
		if math.Trunc(value) == value {
			return strconv.FormatFloat(value, 'f', 0, 64)
		}
		return strconv.FormatFloat(value, 'f', 1, 64)
	}
	switch {
	case count <= 0:
		return "—"
	case count >= 1_000_000:
		return trim(count/1_000_000) + "M"
	case count >= 1_000:
		return trim(count/1_000) + "K"
	}
	return strconv.Itoa(int(count))
}

func modelSelectorCost(cost ai.ModelCost) string {
	if cost.Input == 0 && cost.Output == 0 {
		return "—"
	}
	format := func(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }
	return "$" + format(cost.Input) + "/" + format(cost.Output)
}

func modelSelectorFlags(model ai.Model) string {
	flags := make([]string, 0, 2)
	if model.Reasoning {
		flags = append(flags, "think")
	}
	if slices.Contains(model.Input, ai.InputImage) {
		flags = append(flags, "img")
	}
	if len(flags) == 0 {
		return "—"
	}
	return strings.Join(flags, "+")
}

func (component *ModelSelectorComponent) updateList() {
	component.listContainer.Clear()
	// Invisible grid: measure the metadata columns over the whole filtered
	// set so rows stay aligned while scrolling.
	idWidth, providerWidth, contextWidth, costWidth := 0, 0, len("ctx"), 0
	for _, item := range component.filteredModels {
		idWidth = max(idWidth, len(item.model.ID))
		providerWidth = max(providerWidth, len(item.model.Provider)+2)
		contextWidth = max(contextWidth, len(modelSelectorTokens(item.model.ContextWindow)))
		costWidth = max(costWidth, len(modelSelectorCost(item.model.Cost)))
	}
	idWidth = min(idWidth, 44)
	row := func(style func(string) string, prefix, id, provider, contextTokens, cost, flags, current string) string {
		return style(prefix+fmt.Sprintf("%-*s", idWidth, id)) +
			"  " + theme.FG("muted", fmt.Sprintf("%-*s", providerWidth, provider)) +
			"  " + theme.FG("muted", fmt.Sprintf("%*s", contextWidth, contextTokens)) +
			"  " + theme.FG("dim", fmt.Sprintf("%*s", costWidth, cost)) +
			"  " + theme.FG("dim", flags) + current
	}
	start := component.window.Start(component.selectedIndex, len(component.filteredModels), modelSelectorMaxVisible)
	end := min(start+modelSelectorMaxVisible, len(component.filteredModels))
	component.rows.setWindow(start, end-start)
	if component.headerText != nil {
		header := ""
		if len(component.filteredModels) > 0 {
			header = row(func(text string) string { return theme.FG("dim", text) }, "  ", "model", "provider", "ctx", "$/Mtok", " ", "")
		}
		component.headerText.SetText(header)
	}
	for index := start; index < end; index++ {
		item := component.filteredModels[index]
		prefix, modelStyle := "  ", "text"
		if index == component.selectedIndex {
			prefix, modelStyle = "→ ", "accent"
		}
		current := ""
		if modelSelectorModelsEqual(component.currentModel, &item.model) {
			current = theme.FG("success", " ✓")
		}
		component.listContainer.AddChild(tui.NewText(
			row(func(text string) string { return theme.FG(modelStyle, text) },
				prefix, item.model.ID, "["+string(item.model.Provider)+"]",
				modelSelectorTokens(item.model.ContextWindow), modelSelectorCost(item.model.Cost),
				modelSelectorFlags(item.model), current),
			0, 0, nil,
		))
	}
	if start > 0 || end < len(component.filteredModels) {
		component.listContainer.AddChild(tui.NewText(
			theme.FG("muted", "  ("+strconv.Itoa(component.selectedIndex+1)+"/"+strconv.Itoa(len(component.filteredModels))+")"),
			0, 0, nil,
		))
	}
	if len(component.filteredModels) == 0 {
		component.listContainer.AddChild(tui.NewText(theme.FG("muted", "  No matching models"), 0, 0, nil))
		return
	}
	component.listContainer.AddChild(tui.NewSpacer(1))
	component.listContainer.AddChild(tui.NewText(
		theme.FG("muted", fmt.Sprintf("  Model Name: %s", component.filteredModels[component.selectedIndex].model.Name)),
		0, 0, nil,
	))
}

func (component *ModelSelectorComponent) confirmSelection() {
	if component.selectedIndex < len(component.filteredModels) && component.onSelect != nil {
		component.onSelect(component.filteredModels[component.selectedIndex].model)
	}
}

func (component *ModelSelectorComponent) HandleInput(event tui.KeyEvent) {
	// Any keyboard interaction re-anchors the window on the selection; only
	// pointer selection keeps it frozen.
	component.window.Recenter()
	bindings := tui.GetKeybindings()
	switch {
	case bindings.Matches(event.Raw, "tui.input.tab"):
		if len(component.scopedModels) > 0 {
			if component.scope == modelScopeAll {
				component.setScope(modelScopeScoped)
			} else {
				component.setScope(modelScopeAll)
			}
		}
	case bindings.Matches(event.Raw, "tui.select.up"):
		if len(component.filteredModels) == 0 {
			return
		}
		component.selectedIndex = (component.selectedIndex - 1 + len(component.filteredModels)) % len(component.filteredModels)
		component.updateList()
	case bindings.Matches(event.Raw, "tui.select.down"):
		if len(component.filteredModels) == 0 {
			return
		}
		component.selectedIndex = (component.selectedIndex + 1) % len(component.filteredModels)
		component.updateList()
	case bindings.Matches(event.Raw, "tui.select.confirm") || event.Raw == "\n":
		component.confirmSelection()
	case bindings.Matches(event.Raw, "tui.select.cancel"):
		if component.onCancel != nil {
			component.onCancel()
		}
	default:
		component.searchInput.HandleInput(event)
		component.filterModels(component.searchInput.GetValue())
	}
}

func (component *ModelSelectorComponent) SetFocused(focused bool) {
	component.searchInput.SetFocused(focused)
}

func (component *ModelSelectorComponent) Invalidate() { component.container.Invalidate() }

// Render records where each visible row landed for mouse hit-testing; the
// emitted lines are the container's own.
func (component *ModelSelectorComponent) Render(width int) []string {
	return component.rows.renderRecordingRows(component.container, component.listContainer, width)
}

// WantsMouseMotion turns on hover reports while the selector holds focus.
func (component *ModelSelectorComponent) WantsMouseMotion() bool { return true }

// HandleMouse drives the shared list pointer semantic.
func (component *ModelSelectorComponent) HandleMouse(event tui.MouseEvent) bool {
	if len(component.filteredModels) == 0 {
		return false
	}
	return tui.HandleListMouse(component, event)
}

// ListRowAt maps a component-local row to the filtered-model index it renders.
func (component *ModelSelectorComponent) ListRowAt(row int) (int, bool) {
	index, ok := component.rows.rowAt(row)
	if !ok || index >= len(component.filteredModels) {
		return 0, false
	}
	return index, true
}

// ListSelectRow moves the highlight without re-anchoring the window, so
// hover can never shift rows under the cursor.
func (component *ModelSelectorComponent) ListSelectRow(index int) {
	listSelectRow(&component.window, &component.selectedIndex, index, component.updateList)
}

// ListScroll moves the selection one row per tick, recentring like keyboard
// navigation does.
func (component *ModelSelectorComponent) ListScroll(direction int) {
	listScroll(&component.window, &component.selectedIndex, direction, len(component.filteredModels), component.updateList)
}

// ListConfirm confirms the current selection.
func (component *ModelSelectorComponent) ListConfirm() { component.confirmSelection() }

func (mode *InteractiveMode) selectModelSearchable(
	ctx context.Context,
	current *ai.Model,
	models []ai.Model,
	scoped []agent.ScopedModel,
	initialSearch string,
) (ai.Model, bool) {
	type selection struct {
		model ai.Model
		ok    bool
	}
	result := make(chan selection, 1)
	resolve := func(value selection) {
		select {
		case result <- value:
		default:
		}
	}
	component := NewModelSelectorComponent(
		current, models, scoped,
		func(model ai.Model) { resolve(selection{model: model, ok: true}) },
		func() { resolve(selection{}) },
		initialSearch,
	)
	mode.editorContainer.Clear()
	mode.editorContainer.AddChild(component)
	mode.ui.SetFocus(component)
	mode.ui.RequestRender()
	defer func() {
		children := mode.editorContainer.Children()
		if len(children) != 1 || children[0] != component {
			return
		}
		mode.restoreEditorComponent()
		mode.ui.SetFocus(mode.activeEditorFocus())
		mode.ui.RequestRender()
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case selected := <-result:
		return selected.model, selected.ok
	case <-ctx.Done():
		return ai.Model{}, false
	}
}
