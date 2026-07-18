package tui

import (
	"fmt"
	"strings"
	"sync"
)

const (
	defaultPrimaryColumnWidth = 32
	primaryColumnGap          = 2
	minDescriptionWidth       = 10
)

func normalizeToSingleLine(text string) string {
	var result strings.Builder
	pending := false
	for _, r := range text {
		if r == '\r' || r == '\n' {
			pending = true
			continue
		}
		if pending {
			result.WriteByte(' ')
			pending = false
		}
		result.WriteRune(r)
	}
	return trimWhitespace(result.String())
}

type SelectItem struct {
	Value       string
	Label       string
	Description string
}

type SelectListTheme struct {
	SelectedPrefix StyleFunc
	SelectedText   StyleFunc
	Description    StyleFunc
	ScrollInfo     StyleFunc
	NoMatch        StyleFunc
}

type SelectListTruncatePrimaryContext struct {
	Text        string
	MaxWidth    int
	ColumnWidth int
	Item        SelectItem
	IsSelected  bool
}

// SelectListLayoutOptions tunes the primary column; zero values mean unset.
type SelectListLayoutOptions struct {
	MinPrimaryColumnWidth int
	MaxPrimaryColumnWidth int
	TruncatePrimary       func(SelectListTruncatePrimaryContext) string
}

// SelectList renders a scrolling picker with optional description column.
type SelectList struct {
	mu            sync.Mutex
	items         []SelectItem
	filteredItems []SelectItem
	selectedIndex int
	maxVisible    int
	theme         SelectListTheme
	layout        SelectListLayoutOptions
	pending       []func()

	OnSelect          func(SelectItem)
	OnCancel          func()
	OnSelectionChange func(SelectItem)
}

func NewSelectList(items []SelectItem, maxVisible int, theme SelectListTheme, layout SelectListLayoutOptions) *SelectList {
	return &SelectList{items: items, filteredItems: items, maxVisible: maxVisible, theme: theme, layout: layout}
}

// SetFilter keeps items whose value starts with filter (case-insensitive) and
// resets the selection.
func (list *SelectList) SetFilter(filter string) {
	list.mu.Lock()
	defer list.mu.Unlock()
	lowered := strings.ToLower(filter)
	filtered := make([]SelectItem, 0, len(list.items))
	for _, item := range list.items {
		if strings.HasPrefix(strings.ToLower(item.Value), lowered) {
			filtered = append(filtered, item)
		}
	}
	list.filteredItems = filtered
	list.selectedIndex = 0
}

func (list *SelectList) SetSelectedIndex(index int) {
	list.mu.Lock()
	defer list.mu.Unlock()
	list.selectedIndex = max(0, min(index, len(list.filteredItems)-1))
}

func (list *SelectList) Invalidate() {}

func (list *SelectList) Render(width int) []string {
	list.mu.Lock()
	defer list.mu.Unlock()
	if len(list.filteredItems) == 0 {
		return []string{list.style(list.theme.NoMatch, "  No matching commands")}
	}

	primaryColumnWidth := list.primaryColumnWidth()
	startIndex := max(0, min(list.selectedIndex-list.maxVisible/2, len(list.filteredItems)-list.maxVisible))
	endIndex := min(startIndex+list.maxVisible, len(list.filteredItems))

	lines := make([]string, 0, endIndex-startIndex+1)
	for index := startIndex; index < endIndex; index++ {
		item := list.filteredItems[index]
		isSelected := index == list.selectedIndex
		description := ""
		if item.Description != "" {
			description = normalizeToSingleLine(item.Description)
		}
		lines = append(lines, list.renderItem(item, isSelected, width, description, primaryColumnWidth))
	}

	if startIndex > 0 || endIndex < len(list.filteredItems) {
		scrollText := fmt.Sprintf("  (%d/%d)", list.selectedIndex+1, len(list.filteredItems))
		lines = append(lines, list.style(list.theme.ScrollInfo, TruncateToWidth(scrollText, width-2, "", false)))
	}
	return lines
}

func (list *SelectList) HandleInput(event KeyEvent) {
	list.mu.Lock()
	data := event.Raw
	kb := GetKeybindings()
	switch {
	case kb.Matches(data, "tui.select.up"):
		if list.selectedIndex == 0 {
			list.selectedIndex = len(list.filteredItems) - 1
		} else {
			list.selectedIndex--
		}
		list.notifySelectionChange()
	case kb.Matches(data, "tui.select.down"):
		if list.selectedIndex == len(list.filteredItems)-1 {
			list.selectedIndex = 0
		} else {
			list.selectedIndex++
		}
		list.notifySelectionChange()
	case kb.Matches(data, "tui.select.confirm"):
		if item, ok := list.selectedItem(); ok && list.OnSelect != nil {
			callback := list.OnSelect
			list.pending = append(list.pending, func() { callback(item) })
		}
	case kb.Matches(data, "tui.select.cancel"):
		if list.OnCancel != nil {
			list.pending = append(list.pending, list.OnCancel)
		}
	}
	pending := list.pending
	list.pending = nil
	list.mu.Unlock()
	for _, callback := range pending {
		callback()
	}
}

func (list *SelectList) renderItem(item SelectItem, isSelected bool, width int, description string, primaryColumnWidth int) string {
	prefix := "  "
	if isSelected {
		prefix = "→ "
	}
	prefixWidth := VisibleWidth(prefix)

	if description != "" && width > 40 {
		effectivePrimaryColumnWidth := max(1, min(primaryColumnWidth, width-prefixWidth-4))
		maxPrimaryWidth := max(1, effectivePrimaryColumnWidth-primaryColumnGap)
		truncatedValue := list.truncatePrimary(item, isSelected, maxPrimaryWidth, effectivePrimaryColumnWidth)
		truncatedValueWidth := VisibleWidth(truncatedValue)
		spacing := strings.Repeat(" ", max(1, effectivePrimaryColumnWidth-truncatedValueWidth))
		descriptionStart := prefixWidth + truncatedValueWidth + len(spacing)
		remainingWidth := width - descriptionStart - 2

		if remainingWidth > minDescriptionWidth {
			truncatedDescription := TruncateToWidth(description, remainingWidth, "", false)
			if isSelected {
				return list.style(list.theme.SelectedText, prefix+truncatedValue+spacing+truncatedDescription)
			}
			return prefix + truncatedValue + list.style(list.theme.Description, spacing+truncatedDescription)
		}
	}

	maxWidth := width - prefixWidth - 2
	truncatedValue := list.truncatePrimary(item, isSelected, maxWidth, maxWidth)
	if isSelected {
		return list.style(list.theme.SelectedText, prefix+truncatedValue)
	}
	return prefix + truncatedValue
}

func (list *SelectList) primaryColumnWidth() int {
	minWidth, maxWidth := list.primaryColumnBounds()
	widest := 0
	for _, item := range list.filteredItems {
		widest = max(widest, VisibleWidth(list.displayValue(item))+primaryColumnGap)
	}
	return max(minWidth, min(widest, maxWidth))
}

func (list *SelectList) primaryColumnBounds() (int, int) {
	rawMin, rawMax := list.layout.MinPrimaryColumnWidth, list.layout.MaxPrimaryColumnWidth
	if rawMin == 0 {
		rawMin = rawMax
	}
	if rawMax == 0 {
		rawMax = list.layout.MinPrimaryColumnWidth
	}
	if rawMin == 0 {
		rawMin = defaultPrimaryColumnWidth
	}
	if rawMax == 0 {
		rawMax = defaultPrimaryColumnWidth
	}
	return max(1, min(rawMin, rawMax)), max(1, max(rawMin, rawMax))
}

func (list *SelectList) truncatePrimary(item SelectItem, isSelected bool, maxWidth, columnWidth int) string {
	displayValue := list.displayValue(item)
	truncatedValue := ""
	if list.layout.TruncatePrimary != nil {
		truncatedValue = list.layout.TruncatePrimary(SelectListTruncatePrimaryContext{
			Text:        displayValue,
			MaxWidth:    maxWidth,
			ColumnWidth: columnWidth,
			Item:        item,
			IsSelected:  isSelected,
		})
	} else {
		truncatedValue = TruncateToWidth(displayValue, maxWidth, "", false)
	}
	return TruncateToWidth(truncatedValue, maxWidth, "", false)
}

func (list *SelectList) displayValue(item SelectItem) string {
	if item.Label != "" {
		return item.Label
	}
	return item.Value
}

func (list *SelectList) notifySelectionChange() {
	if item, ok := list.selectedItem(); ok && list.OnSelectionChange != nil {
		callback := list.OnSelectionChange
		list.pending = append(list.pending, func() { callback(item) })
	}
}

func (list *SelectList) selectedItem() (SelectItem, bool) {
	if list.selectedIndex < 0 || list.selectedIndex >= len(list.filteredItems) {
		return SelectItem{}, false
	}
	return list.filteredItems[list.selectedIndex], true
}

// GetSelectedItem returns the highlighted item, or false when none.
func (list *SelectList) GetSelectedItem() (SelectItem, bool) {
	list.mu.Lock()
	defer list.mu.Unlock()
	return list.selectedItem()
}

func (list *SelectList) style(style StyleFunc, text string) string {
	if style == nil {
		return text
	}
	return style(text)
}
