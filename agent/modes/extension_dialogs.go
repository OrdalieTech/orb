package modes

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
)

type extensionDialogOptions struct {
	searchable            bool
	pinnedOptions         int
	ui                    tui.RenderRequester
	timeout               *int64
	onToggleToolsExpanded func()
}

// ExtensionSelectorComponent is the bordered option-list dialog behind
// ctx.ui.select, exported for extension UI reuse as upstream exports it.
type ExtensionSelectorComponent struct {
	container             *tui.Container
	list                  *tui.Container
	title                 *tui.Text
	baseTitle             string
	options               []tui.SelectItem
	allOptions            []tui.SelectItem
	searchInput           *tui.Input
	pinnedOptions         int
	selected              int
	scrollTop             int
	freezeScroll          bool
	ui                    *tui.TUI
	onSelect              func(string)
	onCancel              func()
	onToggleToolsExpanded func()
	countdown             *CountdownTimer
	// rows holds the first row of every option plus a trailing end row;
	// options wrap at narrow widths, so their heights cannot be assumed.
	rows listRowOffsets
}

// NewExtensionSelectorItemsComponent builds the selector dialog from
// pre-labelled select items.
func NewExtensionSelectorItemsComponent(
	title string,
	options []tui.SelectItem,
	onSelect func(string),
	onCancel func(),
	config *extensionDialogOptions,
) *ExtensionSelectorComponent {
	component := &ExtensionSelectorComponent{
		container: &tui.Container{},
		list:      &tui.Container{},
		baseTitle: title,
		options:   append([]tui.SelectItem(nil), options...),
		onSelect:  onSelect,
		onCancel:  onCancel,
	}
	if config != nil {
		component.onToggleToolsExpanded = config.onToggleToolsExpanded
		component.ui, _ = config.ui.(*tui.TUI)
		component.pinnedOptions = config.pinnedOptions
	}
	component.title = tui.NewText(theme.FG("accent", theme.Bold(title)), 1, 0, nil)
	component.container.AddChild(component.title)
	component.container.AddChild(tui.NewSpacer(1))
	if config != nil && config.searchable {
		component.allOptions = component.options
		component.searchInput = tui.NewInput()
		component.container.AddChild(component.searchInput)
		component.container.AddChild(tui.NewSpacer(1))
		component.filterOptions()
	}
	if config != nil && config.timeout != nil && *config.timeout > 0 && config.ui != nil {
		component.countdown = NewCountdownTimer(*config.timeout, config.ui, func(seconds int) {
			component.title.SetText(theme.FG("accent", theme.Bold(fmt.Sprintf("%s (%ds)", component.baseTitle, seconds))))
		}, component.cancel)
	}
	component.container.AddChild(component.list)
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(tui.NewText(
		RawKeyHint("↑↓", "navigate")+"  "+
			KeyHint("tui.select.confirm", "select")+"  "+
			KeyHint("tui.select.cancel", "cancel"),
		1,
		0,
		nil,
	))
	component.updateList()
	return component
}

func (component *ExtensionSelectorComponent) filterOptions() {
	pinned := min(component.pinnedOptions, len(component.allOptions))
	component.options = append([]tui.SelectItem(nil), component.allOptions[:pinned]...)
	component.options = append(component.options, tui.FuzzyFilter(component.allOptions[pinned:], component.searchInput.GetValue(), func(item tui.SelectItem) string { return item.Value })...)
	component.selected = max(0, min(component.selected, len(component.options)-1))
	if len(component.options) == pinned {
		component.selected = 0
	}
	component.updateList()
}

func (component *ExtensionSelectorComponent) updateList() {
	component.list.Clear()
	for index, option := range component.options {
		label := option.Label
		if label == "" {
			label = option.Value
		}
		// The shared selection language: › cursor plus a full-row background.
		value := "  " + theme.FG("text", label)
		background := tui.StyleFunc(nil)
		if index == component.selected {
			value = theme.FG("accent", "› ") + theme.FG("text", label)
			background = func(text string) string { return theme.BG("selectedBg", text) }
		}
		component.list.AddChild(tui.NewText(value, 1, 0, background))
	}
}

func (component *ExtensionSelectorComponent) HandleInput(event tui.KeyEvent) {
	for _, option := range component.options {
		label := strings.TrimSpace(option.Value)
		if component.searchInput == nil && len(event.Raw) == 1 && len(label) > 1 && label[1] == ' ' && strings.EqualFold(label[:1], event.Raw) {
			if component.onSelect != nil {
				component.onSelect(option.Value)
			}
			return
		}
	}
	bindings := tui.GetKeybindings()
	switch {
	case bindings.Matches(event.Raw, "app.tools.expand"):
		if component.onToggleToolsExpanded != nil {
			component.onToggleToolsExpanded()
		}
	case bindings.Matches(event.Raw, "tui.select.up") || component.searchInput == nil && event.Raw == "k":
		component.freezeScroll = false
		component.selected = max(0, component.selected-1)
		component.updateList()
	case bindings.Matches(event.Raw, "tui.select.down") || component.searchInput == nil && event.Raw == "j":
		component.freezeScroll = false
		component.selected = min(len(component.options)-1, component.selected+1)
		component.updateList()
	case bindings.Matches(event.Raw, "tui.select.confirm") || event.Raw == "\n":
		if component.selected >= 0 && component.selected < len(component.options) && component.onSelect != nil {
			component.onSelect(component.options[component.selected].Value)
		}
	case bindings.Matches(event.Raw, "tui.select.cancel"):
		component.cancel()
	default:
		if component.searchInput != nil {
			query := component.searchInput.GetValue()
			component.searchInput.HandleInput(event)
			if query != component.searchInput.GetValue() {
				component.freezeScroll = false
				component.selected, component.scrollTop = component.pinnedOptions, 0
				component.filterOptions()
			}
		}
	}
}

func (component *ExtensionSelectorComponent) cancel() {
	if component.onCancel != nil {
		component.onCancel()
	}
}

func (component *ExtensionSelectorComponent) Dispose() {
	if component.countdown != nil {
		component.countdown.Dispose()
	}
}

func (component *ExtensionSelectorComponent) SetFocused(focused bool) {
	if component.searchInput != nil {
		component.searchInput.SetFocused(focused)
	}
}

func (component *ExtensionSelectorComponent) Invalidate() { component.container.Invalidate() }

// Keep wrapped options inside the modal and use the same offsets for hit-testing.
func (component *ExtensionSelectorComponent) Render(width int) []string {
	var before, options, after []string
	rows := make([]int, 0, len(component.options)+1)
	seenList := false
	for _, child := range component.container.Children() {
		if child == component.list {
			seenList = true
			for _, option := range component.list.Children() {
				rows = append(rows, len(options))
				options = append(options, option.Render(width)...)
			}
			rows = append(rows, len(options))
		} else if seenList {
			after = append(after, child.Render(width)...)
		} else {
			before = append(before, child.Render(width)...)
		}
	}
	limit := 10
	if component.ui != nil {
		// The modal uses 85% of terminal height; its frame adds two rows.
		limit = max(1, component.ui.Terminal().Rows()*85/100-len(before)-len(after)-3)
	}
	component.scrollTop = max(0, min(component.scrollTop, len(options)-limit))
	if !component.freezeScroll && component.selected >= 0 && component.selected+1 < len(rows) {
		start, end := rows[component.selected], rows[component.selected+1]
		if start < component.scrollTop {
			component.scrollTop = start
		} else if end > component.scrollTop+limit {
			component.scrollTop = min(start, end-limit)
		}
	}
	end := min(len(options), component.scrollTop+limit)
	for index := range rows {
		rows[index] = len(before) + max(0, min(rows[index]-component.scrollTop, end-component.scrollTop))
	}
	component.rows.setOffsets(rows)
	lines := append(before, options[component.scrollTop:end]...)
	if component.searchInput != nil {
		if len(lines) < len(before)+limit && len(component.options) == component.pinnedOptions && component.searchInput.GetValue() != "" {
			lines = append(lines, theme.FG("dim", "  No matches"))
		}
		for len(lines) < len(before)+limit {
			lines = append(lines, "")
		}
	}
	if len(options) > limit || component.searchInput != nil {
		lines = append(lines, theme.FG("dim", tui.TruncateToWidth(fmt.Sprintf("  %d/%d", component.selected+1, len(component.options)), width, "", false)))
	}
	return append(lines, after...)
}

// WantsMouseMotion turns on hover reports while the dialog holds focus.
func (component *ExtensionSelectorComponent) WantsMouseMotion() bool { return true }

// HandleMouse drives the shared list pointer semantic; a single click only
// highlights, so a stray click cannot approve a tool call.
func (component *ExtensionSelectorComponent) HandleMouse(event tui.MouseEvent) bool {
	if len(component.options) == 0 {
		return false
	}
	return tui.HandleListMouse(component, event)
}

// ListRowAt maps a rendered row to its option index; options wrap at narrow
// widths, so their heights come from the recorded render.
func (component *ExtensionSelectorComponent) ListRowAt(row int) (int, bool) {
	index, ok := component.rows.rowAt(row)
	if !ok || index >= len(component.options) {
		return 0, false
	}
	return index, true
}

// ListSelectRow keeps the current window when the pointed option is visible.
func (component *ExtensionSelectorComponent) ListSelectRow(index int) {
	component.freezeScroll = true
	if component.selected != index {
		component.selected = index
		component.updateList()
	}
}

// ListScroll moves the selection one row per tick, like keyboard navigation.
func (component *ExtensionSelectorComponent) ListScroll(direction int) {
	component.freezeScroll = false
	component.selected = max(0, min(component.selected+direction, len(component.options)-1))
	component.updateList()
}

// ListConfirm confirms the current selection.
func (component *ExtensionSelectorComponent) ListConfirm() {
	if component.selected >= 0 && component.selected < len(component.options) && component.onSelect != nil {
		component.onSelect(component.options[component.selected].Value)
	}
}

// ExtensionInputComponent is the bordered single-line input dialog behind
// ctx.ui.input, exported for extension UI reuse.
type ExtensionInputComponent struct {
	container *tui.Container
	input     *tui.Input
	title     *tui.Text
	baseTitle string
	onSubmit  func(string)
	onCancel  func()
	countdown *CountdownTimer
}

// NewExtensionInputComponent builds the single-line input dialog.
func NewExtensionInputComponent(
	title string,
	_ string,
	onSubmit func(string),
	onCancel func(),
	config *extensionDialogOptions,
) *ExtensionInputComponent {
	component := &ExtensionInputComponent{
		container: &tui.Container{},
		input:     tui.NewInput(),
		baseTitle: title,
		onSubmit:  onSubmit,
		onCancel:  onCancel,
	}
	component.title = tui.NewText(theme.FG("accent", title), 1, 0, nil)
	component.container.AddChild(component.title)
	component.container.AddChild(tui.NewSpacer(1))
	if config != nil && config.timeout != nil && *config.timeout > 0 && config.ui != nil {
		component.countdown = NewCountdownTimer(*config.timeout, config.ui, func(seconds int) {
			component.title.SetText(theme.FG("accent", fmt.Sprintf("%s (%ds)", component.baseTitle, seconds)))
		}, component.cancel)
	}
	component.container.AddChild(component.input)
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(tui.NewText(
		KeyHint("tui.select.confirm", "submit")+"  "+KeyHint("tui.select.cancel", "cancel"),
		1,
		0,
		nil,
	))
	return component
}

func (component *ExtensionInputComponent) HandleInput(event tui.KeyEvent) {
	bindings := tui.GetKeybindings()
	switch {
	case bindings.Matches(event.Raw, "tui.select.confirm") || event.Raw == "\n":
		if component.onSubmit != nil {
			component.onSubmit(component.input.GetValue())
		}
	case bindings.Matches(event.Raw, "tui.select.cancel"):
		component.cancel()
	default:
		component.input.HandleInput(event)
	}
}

func (component *ExtensionInputComponent) cancel() {
	if component.onCancel != nil {
		component.onCancel()
	}
}

func (component *ExtensionInputComponent) SetFocused(focused bool) {
	component.input.SetFocused(focused)
}
func (component *ExtensionInputComponent) Dispose() {
	if component.countdown != nil {
		component.countdown.Dispose()
	}
}
func (component *ExtensionInputComponent) Invalidate() { component.container.Invalidate() }
func (component *ExtensionInputComponent) Render(width int) []string {
	return component.container.Render(width)
}

// ExtensionEditorComponent is the bordered multi-line editor dialog behind
// ctx.ui.editor, exported for extension UI reuse.
type ExtensionEditorComponent struct {
	container             *tui.Container
	editor                *tui.Editor
	ui                    *tui.TUI
	bindings              *tui.KeybindingsManager
	externalEditorCommand string
	onCancel              func()
}

// NewExtensionEditorComponent builds the multi-line editor dialog.
func NewExtensionEditorComponent(
	uiInstance *tui.TUI,
	bindings *tui.KeybindingsManager,
	title string,
	prefill string,
	onSubmit func(string),
	onCancel func(),
	externalEditorCommand string,
) *ExtensionEditorComponent {
	if bindings == nil {
		bindings = tui.GetKeybindings()
	}
	// The external editor command is always resolved (upstream 75e6123a):
	// explicit command -> $VISUAL -> $EDITOR -> platform default.
	if externalEditorCommand == "" {
		externalEditorCommand = os.Getenv("VISUAL")
	}
	if externalEditorCommand == "" {
		externalEditorCommand = os.Getenv("EDITOR")
	}
	if externalEditorCommand == "" {
		if runtime.GOOS == "windows" {
			externalEditorCommand = "notepad"
		} else {
			externalEditorCommand = "nano"
		}
	}
	component := &ExtensionEditorComponent{
		container:             &tui.Container{},
		ui:                    uiInstance,
		bindings:              bindings,
		externalEditorCommand: externalEditorCommand,
		onCancel:              onCancel,
	}
	component.container.AddChild(tui.NewText(theme.FG("accent", title), 1, 0, nil))
	component.container.AddChild(tui.NewSpacer(1))
	component.editor = tui.NewEditor(uiInstance, theme.EditorTheme())
	if prefill != "" {
		component.editor.SetText(prefill)
	}
	component.editor.OnSubmit = onSubmit
	component.container.AddChild(component.editor)
	component.container.AddChild(tui.NewSpacer(1))
	// The external-editor hint is unconditional now that the command always
	// resolves (upstream 75e6123a).
	hint :=
		KeyHint("tui.select.confirm", "submit") + "  " +
			KeyHint("tui.input.newLine", "newline") + "  " +
			KeyHint("tui.select.cancel", "cancel") + "  " +
			KeyHint("app.editor.external", "external editor")
	component.container.AddChild(tui.NewText(hint, 1, 0, nil))
	return component
}

func (component *ExtensionEditorComponent) HandleInput(event tui.KeyEvent) {
	if tui.GetKeybindings().Matches(event.Raw, "tui.select.cancel") {
		component.cancel()
		return
	}
	if component.bindings.Matches(event.Raw, "app.editor.external") {
		component.handleOpenExternalEditor()
		return
	}
	component.editor.HandleInput(event)
}

func (component *ExtensionEditorComponent) cancel() {
	if component.onCancel != nil {
		component.onCancel()
	}
}

// handleOpenExternalEditor stops the TUI, edits the prompt through the shared
// editInExternalEditor helper, and restarts with a forced re-render
// (extension-editor.ts handleOpenExternalEditor).
func (component *ExtensionEditorComponent) handleOpenExternalEditor() {
	content := component.editor.GetText()
	_ = component.ui.Stop()
	go func() {
		result := editInExternalEditor(component.externalEditorCommand, content)
		if result.complete {
			component.editor.SetText(result.content)
		}
		_ = component.ui.Start()
		// Force full re-render since the external editor uses the alternate screen.
		component.ui.ForceRender()
	}()
}

func (component *ExtensionEditorComponent) SetFocused(focused bool) {
	component.editor.SetFocused(focused)
}
func (component *ExtensionEditorComponent) Invalidate() { component.container.Invalidate() }
func (component *ExtensionEditorComponent) Render(width int) []string {
	return component.container.Render(width)
}

func extensionDialogBorder() *DynamicBorder {
	return NewDynamicBorderWithColor(func(value string) string { return theme.FG("border", value) })
}

// KeyHint renders a dim key label plus muted description for the resolved
// keybinding, mirroring upstream's exported keyHint helper.
func KeyHint(binding, description string) string {
	return theme.FG("dim", KeyText(binding)) + theme.FG("muted", " "+description)
}

// RawKeyHint renders a dim literal key plus muted description without
// keybinding resolution, mirroring upstream's exported rawKeyHint helper.
func RawKeyHint(key, description string) string {
	return theme.FG("dim", formatKeyText(key)) + theme.FG("muted", " "+description)
}
