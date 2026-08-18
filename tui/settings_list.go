package tui

import (
	"fmt"
	"strings"
	"sync"
)

type SettingItem struct {
	// ID uniquely identifies the setting.
	ID string
	// Label is the display text (left side).
	Label string
	// Description is shown when the item is selected.
	Description string
	// CurrentValue is displayed on the right side.
	CurrentValue string
	// Values, when set, are cycled through by Enter/Space.
	Values []string
	// Submenu, when set, is opened by Enter. It receives the current value
	// and a done callback; done(nil) cancels and done(&value) selects.
	Submenu func(currentValue string, done func(selected *string)) Component
}

// SettingsStyleFunc styles text differently for the selected row.
type SettingsStyleFunc func(text string, selected bool) string

type SettingsListTheme struct {
	Label       SettingsStyleFunc
	Value       SettingsStyleFunc
	Description StyleFunc
	Cursor      string
	Hint        StyleFunc
	SelectedBg  StyleFunc // full-row background for the selection
}

type SettingsListOptions struct {
	EnableSearch bool
	// FixedGeometry keeps the rendered height constant across selection and
	// filtering: the row region pads to its full size, the scroll counter
	// keeps its slot, and the description area always occupies
	// settingsDescriptionLines lines (truncated with an ellipsis). Floating
	// windows need it so they never grow, shrink, or re-center.
	FixedGeometry bool
}

// settingsDescriptionLines is the fixed description area height.
const settingsDescriptionLines = 2

// SettingsList renders label/value rows with cycling values, submenus, and
// optional fuzzy search.
type SettingsList struct {
	mu            sync.Mutex
	items         []*SettingItem
	filteredItems []*SettingItem
	theme         SettingsListTheme
	selectedIndex int
	window        ListWindow
	maxVisible    int
	onChange      func(id, newValue string)
	onCancel      func()
	searchInput   *Input
	searchEnabled bool
	fixedGeometry bool
	pending       []func()

	submenuComponent Component
	submenuItemIndex int
	submenuOpen      bool

	// Where the item rows landed in the last render, for hit-testing.
	rowTop, rowStart, rowCount int
}

func NewSettingsList(items []SettingItem, maxVisible int, theme SettingsListTheme, onChange func(id, newValue string), onCancel func(), options SettingsListOptions) *SettingsList {
	stored := make([]*SettingItem, len(items))
	for index := range items {
		item := items[index]
		stored[index] = &item
	}
	list := &SettingsList{
		items:         stored,
		filteredItems: stored,
		maxVisible:    maxVisible,
		theme:         theme,
		onChange:      onChange,
		onCancel:      onCancel,
		searchEnabled: options.EnableSearch,
		fixedGeometry: options.FixedGeometry,
	}
	if list.searchEnabled {
		list.searchInput = NewInput()
		list.searchInput.Prompt = "⌕ "
	}
	return list
}

// UpdateValue updates an item's current value.
func (list *SettingsList) UpdateValue(id, newValue string) {
	list.mu.Lock()
	defer list.mu.Unlock()
	for _, item := range list.items {
		if item.ID == id {
			item.CurrentValue = newValue
			return
		}
	}
}

func (list *SettingsList) Invalidate() {
	list.mu.Lock()
	submenu := list.submenuComponent
	list.mu.Unlock()
	if submenu != nil {
		invalidate(submenu)
	}
}

func (list *SettingsList) Render(width int) []string {
	list.mu.Lock()
	submenu := list.submenuComponent
	list.mu.Unlock()
	if submenu != nil {
		return submenu.Render(width)
	}
	list.mu.Lock()
	defer list.mu.Unlock()
	return list.renderMainList(width)
}

func (list *SettingsList) renderMainList(width int) []string {
	lines := make([]string, 0, list.maxVisible+6)

	if list.searchEnabled && list.searchInput != nil {
		lines = append(lines, list.searchInput.Render(width)...)
		lines = append(lines, "")
	}

	if len(list.items) == 0 {
		lines = append(lines, list.hint("  No settings available"))
		if list.searchEnabled {
			lines = list.addHintLine(lines, width)
		}
		return lines
	}

	displayItems := list.displayItems()
	if len(displayItems) == 0 {
		lines = append(lines, TruncateToWidth(list.hint("  No matching settings"), width, "...", false))
		if list.fixedGeometry {
			for rendered := 1; rendered < min(list.maxVisible, len(list.items)); rendered++ {
				lines = append(lines, "")
			}
			if len(list.items) > list.maxVisible {
				lines = append(lines, "")
			}
			lines = append(lines, list.fixedDescriptionArea(displayItems, width)...)
		}
		return list.addHintLine(lines, width)
	}

	startIndex := list.window.Start(list.selectedIndex, len(displayItems), list.maxVisible)
	endIndex := min(startIndex+list.maxVisible, len(displayItems))
	list.rowTop, list.rowStart, list.rowCount = len(lines), startIndex, endIndex-startIndex

	maxLabelWidth := 0
	for _, item := range list.items {
		maxLabelWidth = max(maxLabelWidth, VisibleWidth(item.Label))
	}
	maxLabelWidth = min(30, maxLabelWidth)

	for index := startIndex; index < endIndex; index++ {
		item := displayItems[index]
		isSelected := index == list.selectedIndex
		prefix := "  "
		if isSelected {
			prefix = list.theme.Cursor
		}
		prefixWidth := VisibleWidth(prefix)

		labelPadded := item.Label + strings.Repeat(" ", max(0, maxLabelWidth-VisibleWidth(item.Label)))
		labelText := list.styleSelected(list.theme.Label, labelPadded, isSelected)

		separator := "  "
		usedWidth := prefixWidth + maxLabelWidth + VisibleWidth(separator)
		valueMaxWidth := width - usedWidth - 2

		valueText := list.styleSelected(list.theme.Value, TruncateToWidth(item.CurrentValue, valueMaxWidth, "", false), isSelected)
		line := TruncateToWidth(prefix+labelText+separator+valueText, width, "...", false)
		if isSelected && list.theme.SelectedBg != nil {
			line = ApplyBackgroundToLine(TruncateToWidth(line, width, "", true), width, list.theme.SelectedBg)
		}
		lines = append(lines, line)
	}

	if list.fixedGeometry {
		for rendered := endIndex - startIndex; rendered < min(list.maxVisible, len(list.items)); rendered++ {
			lines = append(lines, "")
		}
	}

	if startIndex > 0 || endIndex < len(displayItems) {
		scrollText := fmt.Sprintf("  (%d/%d)", list.selectedIndex+1, len(displayItems))
		lines = append(lines, list.hint(TruncateToWidth(scrollText, width-2, "", false)))
	} else if list.fixedGeometry && len(list.items) > list.maxVisible {
		lines = append(lines, "")
	}

	if list.fixedGeometry {
		lines = append(lines, list.fixedDescriptionArea(displayItems, width)...)
	} else if list.selectedIndex < len(displayItems) {
		if description := displayItems[list.selectedIndex].Description; description != "" {
			lines = append(lines, "")
			for _, line := range WrapTextWithANSI(description, width-4) {
				line = "  " + line
				if list.theme.Description != nil {
					line = list.theme.Description(line)
				}
				lines = append(lines, line)
			}
		}
	}

	return list.addHintLine(lines, width)
}

// fixedDescriptionArea renders the constant-height description block: one
// separator line plus exactly settingsDescriptionLines lines, blank when the
// selection has no description and ellipsized when it wraps longer.
func (list *SettingsList) fixedDescriptionArea(displayItems []*SettingItem, width int) []string {
	description := ""
	if list.selectedIndex >= 0 && list.selectedIndex < len(displayItems) {
		description = displayItems[list.selectedIndex].Description
	}
	var wrapped []string
	if description != "" {
		wrapped = WrapTextWithANSI(description, width-4)
	}
	area := make([]string, 0, settingsDescriptionLines+1)
	area = append(area, "")
	for index := range settingsDescriptionLines {
		line := ""
		if index < len(wrapped) {
			line = wrapped[index]
			if index == settingsDescriptionLines-1 && len(wrapped) > settingsDescriptionLines {
				line = TruncateToWidth(line, max(1, width-6), "", false) + "…"
			}
			line = "  " + line
			if list.theme.Description != nil {
				line = list.theme.Description(line)
			}
		}
		area = append(area, line)
	}
	return area
}

func (list *SettingsList) HandleInput(event KeyEvent) {
	// An open submenu receives all input; delegation happens outside the
	// lock so the submenu's done callback can re-enter this list.
	list.mu.Lock()
	submenu := list.submenuComponent
	list.mu.Unlock()
	if submenu != nil {
		if handler, ok := submenu.(InputHandler); ok {
			handler.HandleInput(event)
		}
		return
	}

	list.mu.Lock()
	list.handleData(event.Raw)
	pending := list.pending
	list.pending = nil
	list.mu.Unlock()
	for _, callback := range pending {
		callback()
	}
}

// HandleMouse drives the shared list pointer semantic: hover moves the
// highlight in place, wheel scrolls, a double click cycles the value like
// Enter/Space. Hover became safe when FixedGeometry pinned the description
// area: rows can no longer shift under a stationary cursor. An open submenu
// keeps the mouse, as it keeps keyboard input.
func (list *SettingsList) HandleMouse(event MouseEvent) bool {
	list.mu.Lock()
	if list.submenuComponent != nil {
		submenu, ok := list.submenuComponent.(MouseHandler)
		list.mu.Unlock()
		return ok && submenu.HandleMouse(event)
	}
	empty := len(list.displayItems()) == 0
	list.mu.Unlock()
	if empty {
		return false
	}
	return HandleListMouse(list, event)
}

// WantsMouseMotion turns on hover reports while the list holds focus.
func (list *SettingsList) WantsMouseMotion() bool { return true }

// ListRowAt maps a rendered row to its display-item index.
func (list *SettingsList) ListRowAt(row int) (int, bool) {
	list.mu.Lock()
	defer list.mu.Unlock()
	return ListRowIndex(row, list.rowTop, list.rowStart, list.rowCount, len(list.displayItems()))
}

// ListSelectRow moves the highlight without re-anchoring the window.
func (list *SettingsList) ListSelectRow(index int) {
	list.mu.Lock()
	list.window.Freeze()
	list.selectedIndex = index
	list.mu.Unlock()
}

// ListScroll moves the selection one row per tick, recentring like keyboard
// navigation does.
func (list *SettingsList) ListScroll(direction int) {
	list.mu.Lock()
	list.window.Recenter()
	list.selectedIndex = max(0, min(list.selectedIndex+direction, len(list.displayItems())-1))
	list.mu.Unlock()
}

// ListConfirm activates the selected item, matching Enter/Space.
func (list *SettingsList) ListConfirm() {
	list.mu.Lock()
	list.activateItem()
	pending := list.pending
	list.pending = nil
	list.mu.Unlock()
	for _, callback := range pending {
		callback()
	}
}

func (list *SettingsList) handleData(data string) {
	kb := GetKeybindings()
	displayItems := list.displayItems()
	switch {
	case kb.Matches(data, "tui.select.up"):
		if len(displayItems) == 0 {
			return
		}
		list.window.Recenter()
		if list.selectedIndex == 0 {
			list.selectedIndex = len(displayItems) - 1
		} else {
			list.selectedIndex--
		}
	case kb.Matches(data, "tui.select.down"):
		if len(displayItems) == 0 {
			return
		}
		list.window.Recenter()
		if list.selectedIndex == len(displayItems)-1 {
			list.selectedIndex = 0
		} else {
			list.selectedIndex++
		}
	case kb.Matches(data, "tui.select.confirm") || data == " ":
		list.activateItem()
	case kb.Matches(data, "tui.select.cancel"):
		if list.onCancel != nil {
			list.pending = append(list.pending, list.onCancel)
		}
	default:
		if list.searchEnabled && list.searchInput != nil {
			sanitized := strings.ReplaceAll(data, " ", "")
			if sanitized == "" {
				return
			}
			list.searchInput.HandleInput(keyEventFor(sanitized))
			list.applyFilter(list.searchInput.GetValue())
		}
	}
}

func (list *SettingsList) activateItem() {
	displayItems := list.displayItems()
	if list.selectedIndex >= len(displayItems) {
		return
	}
	item := displayItems[list.selectedIndex]

	if item.Submenu != nil {
		selectedIndex := list.selectedIndex
		currentValue := item.CurrentValue
		list.pending = append(list.pending, func() {
			list.openSubmenu(item, selectedIndex, currentValue)
		})
		return
	}
	if len(item.Values) > 0 {
		currentIndex := -1
		for index, value := range item.Values {
			if value == item.CurrentValue {
				currentIndex = index
				break
			}
		}
		newValue := item.Values[(currentIndex+1)%len(item.Values)]
		item.CurrentValue = newValue
		if list.onChange != nil {
			id := item.ID
			callback := list.onChange
			list.pending = append(list.pending, func() { callback(id, newValue) })
		}
	}
}

func (list *SettingsList) openSubmenu(item *SettingItem, selectedIndex int, currentValue string) {
	list.mu.Lock()
	list.submenuItemIndex = selectedIndex
	list.submenuOpen = true
	list.mu.Unlock()

	done := func(selected *string) {
		var fire func()
		list.mu.Lock()
		if selected != nil {
			item.CurrentValue = *selected
			if list.onChange != nil {
				id, value, callback := item.ID, *selected, list.onChange
				fire = func() { callback(id, value) }
			}
		}
		list.closeSubmenu()
		list.mu.Unlock()
		if fire != nil {
			fire()
		}
	}

	component := item.Submenu(currentValue, done)
	list.mu.Lock()
	if list.submenuOpen && list.submenuItemIndex == selectedIndex {
		list.submenuComponent = component
	}
	list.mu.Unlock()
}

func (list *SettingsList) closeSubmenu() {
	list.submenuComponent = nil
	if list.submenuOpen {
		list.selectedIndex = list.submenuItemIndex
		list.window.Recenter()
		list.submenuOpen = false
	}
}

func (list *SettingsList) applyFilter(query string) {
	list.filteredItems = FuzzyFilter(list.items, query, func(item *SettingItem) string { return item.Label })
	list.selectedIndex = 0
	list.window.Recenter()
}

func (list *SettingsList) displayItems() []*SettingItem {
	if list.searchEnabled {
		return list.filteredItems
	}
	return list.items
}

func (list *SettingsList) addHintLine(lines []string, width int) []string {
	lines = append(lines, "")
	hint := "  Enter/Space to change · Esc to cancel"
	if list.searchEnabled {
		hint = "  Type to search · Enter/Space to change · Esc to cancel"
	}
	return append(lines, TruncateToWidth(list.hint(hint), width, "...", false))
}

func (list *SettingsList) hint(text string) string {
	if list.theme.Hint == nil {
		return text
	}
	return list.theme.Hint(text)
}

func (list *SettingsList) styleSelected(style SettingsStyleFunc, text string, selected bool) string {
	if style == nil {
		return text
	}
	return style(text, selected)
}

func keyEventFor(raw string) KeyEvent {
	return KeyEvent{Raw: raw, Key: ParseKey(raw), Type: KeyEventTypeOf(raw)}
}
