package modes

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/codingagent"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/codingagent/modes/theme"
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
	listContainer  *tui.Container
	scopeText      *tui.Text
	allModels      []modelSelectorItem
	scopedModels   []modelSelectorItem
	activeModels   []modelSelectorItem
	filteredModels []modelSelectorItem
	selectedIndex  int
	currentModel   *ai.Model
	scope          modelSelectorScope
	onSelect       func(ai.Model)
	onCancel       func()

	rowsMu       sync.Mutex
	rowOffsets   []int
	visibleStart int
	visibleCount int
}

func NewModelSelectorComponent(
	currentModel *ai.Model,
	models []ai.Model,
	scoped []codingagent.ScopedModel,
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
	component.container.AddChild(component.listContainer)
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(extensionDialogBorder())
	component.filterModels(component.searchInput.GetValue())
	return component
}

func (component *ModelSelectorComponent) loadModels(models []ai.Model, scoped []codingagent.ScopedModel) {
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

func (component *ModelSelectorComponent) updateList() {
	component.listContainer.Clear()
	start := max(0, min(
		component.selectedIndex-modelSelectorMaxVisible/2,
		len(component.filteredModels)-modelSelectorMaxVisible,
	))
	end := min(start+modelSelectorMaxVisible, len(component.filteredModels))
	component.rowsMu.Lock()
	component.visibleStart, component.visibleCount = start, end-start
	component.rowsMu.Unlock()
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
			theme.FG(modelStyle, prefix+item.model.ID)+" "+theme.FG("muted", "["+string(item.model.Provider)+"]")+current,
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

// Render inlines the container walk so it can record where each visible row
// landed for mouse hit-testing; the emitted lines are the container's own.
func (component *ModelSelectorComponent) Render(width int) []string {
	component.rowsMu.Lock()
	count := component.visibleCount
	component.rowsMu.Unlock()
	lines, rows := make([]string, 0), make([]int, 0, count+1)
	for _, child := range component.container.Children() {
		if child != component.listContainer {
			lines = append(lines, child.Render(width)...)
			continue
		}
		for index, row := range component.listContainer.Children() {
			if index <= count {
				rows = append(rows, len(lines))
			}
			lines = append(lines, row.Render(width)...)
		}
	}
	if len(rows) == count {
		rows = append(rows, len(lines))
	}
	component.rowsMu.Lock()
	component.rowOffsets = rows
	component.rowsMu.Unlock()
	return lines
}

// WantsMouseMotion turns on hover reports while the selector holds focus.
func (component *ModelSelectorComponent) WantsMouseMotion() bool { return true }

// HandleMouse selects the hovered or clicked row and confirms on a double
// click; the wheel moves the selection one row at a time.
func (component *ModelSelectorComponent) HandleMouse(event tui.MouseEvent) bool {
	if len(component.filteredModels) == 0 {
		return false
	}
	switch {
	case event.Type == tui.MouseWheelUp || event.Type == tui.MouseWheelDown:
		delta := -1
		if event.Type == tui.MouseWheelDown {
			delta = 1
		}
		component.selectedIndex = max(0, min(component.selectedIndex+delta, len(component.filteredModels)-1))
		component.updateList()
		return true
	case event.Type == tui.MouseMove:
		// Hover moves the highlight only while the list cannot scroll: a
		// recentring window would shift rows under the cursor and feed back.
		if len(component.filteredModels) > modelSelectorMaxVisible {
			return false
		}
		index, ok := component.rowAt(event.Row)
		if ok && index != component.selectedIndex {
			component.selectedIndex = index
			component.updateList()
		}
		return ok
	case event.Type == tui.MousePress && event.Button == 0:
		index, ok := component.rowAt(event.Row)
		if !ok {
			return false
		}
		// The first press of a double click already selected this cell.
		// Re-resolving would confirm whatever the recentred list moved under it.
		if event.Clicks >= 2 {
			component.confirmSelection()
			return true
		}
		component.selectedIndex = index
		component.updateList()
		return true
	}
	return false
}

// rowAt maps a component-local row to the filtered-model index it renders.
func (component *ModelSelectorComponent) rowAt(row int) (int, bool) {
	component.rowsMu.Lock()
	rows, start := component.rowOffsets, component.visibleStart
	component.rowsMu.Unlock()
	for index := range max(0, len(rows)-1) {
		if row >= rows[index] && row < rows[index+1] {
			return start + index, true
		}
	}
	return 0, false
}

func (mode *InteractiveMode) selectModelSearchable(
	ctx context.Context,
	current *ai.Model,
	models []ai.Model,
	scoped []codingagent.ScopedModel,
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
