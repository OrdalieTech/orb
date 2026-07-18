package modes

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/tui"

	theme "github.com/OrdalieTech/pi-go/codingagent/modes/theme"
)

// InteractiveUI backs the extensions.UI interface with TUI components.
type InteractiveUI struct {
	mode *InteractiveMode

	mu            sync.Mutex
	widgets       map[string]widgetEntry
	widgetComps   map[string]tui.Component
	editorFactory extensions.EditorFactory
	acProviders   []extensions.AutocompleteProviderFactory
}

type widgetEntry struct {
	placement extensions.WidgetPlacement
}

func NewInteractiveUI(mode *InteractiveMode) *InteractiveUI {
	return &InteractiveUI{
		mode:        mode,
		widgets:     make(map[string]widgetEntry),
		widgetComps: make(map[string]tui.Component),
	}
}

// ─── Dialogs ─────────────────────────────────────────────

func (ui *InteractiveUI) Select(ctx context.Context, title string, options []string, opts *extensions.DialogOptions) (string, bool, error) {
	items := make([]tui.SelectItem, len(options))
	for i, opt := range options {
		items[i] = tui.SelectItem{Value: opt, Label: opt}
	}
	return ui.selectItems(ctx, title, items, opts)
}

func (ui *InteractiveUI) selectItems(ctx context.Context, title string, items []tui.SelectItem, opts *extensions.DialogOptions) (string, bool, error) {
	if len(items) == 0 {
		return "", false, nil
	}
	result := make(chan selectResult, 1)

	sl := tui.NewSelectList(items, 10, selectListTheme(), tui.SelectListLayoutOptions{})
	sl.OnSelect = func(item tui.SelectItem) {
		result <- selectResult{value: item.Value}
	}
	sl.OnCancel = func() {
		result <- selectResult{cancelled: true}
	}

	dialogContainer := &tui.Container{}
	dialogContainer.AddChild(tui.NewText(theme.Bold(title), 1, 0, nil))
	dialogContainer.AddChild(sl)

	var timer *CountdownTimer
	if opts != nil && opts.Timeout != nil {
		countdownText := tui.NewText("", 1, 0, nil)
		dialogContainer.AddChild(countdownText)
		timer = NewCountdownTimer(*opts.Timeout, ui.mode.ui, func(remaining int) {
			countdownText.SetText(theme.FG("dim", fmt.Sprintf("(%ds)", remaining)))
		}, func() {
			result <- selectResult{cancelled: true}
		})
	}

	ui.mode.widgetAbove.AddChild(dialogContainer)
	ui.mode.ui.SetFocus(sl)
	ui.mode.ui.RequestRender()

	defer func() {
		if timer != nil {
			timer.Dispose()
		}
		ui.mode.widgetAbove.RemoveChild(dialogContainer)
		ui.mode.ui.SetFocus(ui.mode.editor)
		ui.mode.ui.RequestRender()
	}()

	signal := context.Background()
	if opts != nil && opts.Signal != nil {
		signal = opts.Signal
	}

	select {
	case r := <-result:
		return r.value, !r.cancelled, nil
	case <-signal.Done():
		return "", false, signal.Err()
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
}

type selectResult struct {
	value     string
	cancelled bool
}

func (ui *InteractiveUI) Confirm(ctx context.Context, title, message string, opts *extensions.DialogOptions) (bool, error) {
	options := []string{"Yes", "No"}
	selected, ok, err := ui.Select(ctx, title+"\n"+message, options, opts)
	if err != nil || !ok {
		return false, err
	}
	return selected == "Yes", nil
}

func (ui *InteractiveUI) Input(ctx context.Context, title string, placeholder *string, opts *extensions.DialogOptions) (string, bool, error) {
	result := make(chan inputDialogResult, 1)

	input := tui.NewInput()
	if placeholder != nil {
		input.SetValue(*placeholder)
	}
	input.OnSubmit = func(value string) {
		result <- inputDialogResult{value: value}
	}
	input.OnEscape = func() {
		result <- inputDialogResult{cancelled: true}
	}

	dialogContainer := &tui.Container{}
	dialogContainer.AddChild(tui.NewText(theme.Bold(title), 1, 0, nil))
	dialogContainer.AddChild(input)

	var timer *CountdownTimer
	if opts != nil && opts.Timeout != nil {
		countdownText := tui.NewText("", 1, 0, nil)
		dialogContainer.AddChild(countdownText)
		timer = NewCountdownTimer(*opts.Timeout, ui.mode.ui, func(remaining int) {
			countdownText.SetText(theme.FG("dim", fmt.Sprintf("(%ds)", remaining)))
		}, func() {
			result <- inputDialogResult{cancelled: true}
		})
	}

	ui.mode.widgetAbove.AddChild(dialogContainer)
	input.SetFocused(true)
	ui.mode.ui.SetFocus(input)
	ui.mode.ui.RequestRender()

	defer func() {
		if timer != nil {
			timer.Dispose()
		}
		ui.mode.widgetAbove.RemoveChild(dialogContainer)
		ui.mode.ui.SetFocus(ui.mode.editor)
		ui.mode.ui.RequestRender()
	}()

	signal := context.Background()
	if opts != nil && opts.Signal != nil {
		signal = opts.Signal
	}

	select {
	case r := <-result:
		return r.value, !r.cancelled, nil
	case <-signal.Done():
		return "", false, signal.Err()
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
}

type inputDialogResult struct {
	value     string
	cancelled bool
}

// ─── Notifications & Status ──────────────────────────────

func (ui *InteractiveUI) Notify(message string, notifyType extensions.NotificationType) {
	color := "dim"
	switch notifyType {
	case extensions.NotifyWarning:
		color = "warning"
	case extensions.NotifyError:
		color = "error"
	}
	ui.mode.chat.AddChild(newStyledText(color, message))
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) OnTerminalInput(handler extensions.TerminalInputHandler) func() {
	if handler == nil {
		return func() {}
	}
	return ui.mode.ui.AddInputListener(func(data string) tui.InputListenerResult {
		result := handler(data)
		if result == nil {
			return tui.InputListenerResult{}
		}
		var transformed *string
		if result.Data != "" {
			copy := result.Data
			transformed = &copy
		}
		return tui.InputListenerResult{Consume: result.Consume, Data: transformed}
	})
}

func (ui *InteractiveUI) SetStatus(key string, text *string) {
	ui.mode.mu.Lock()
	if text == nil {
		delete(ui.mode.footerStatuses, key)
	} else {
		ui.mode.footerStatuses[key] = *text
	}
	ui.mode.mu.Unlock()
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) SetWorkingMessage(msg *string) {
	ui.mode.mu.Lock()
	streaming := ui.mode.streaming
	ui.mode.mu.Unlock()
	if !streaming {
		return
	}
	message := "Working..."
	if msg != nil && *msg != "" {
		message = *msg
	}
	ui.mode.setStatus(NewWorkingStatusIndicator(ui.mode.ui, message))
}

func (ui *InteractiveUI) SetWorkingVisible(visible bool) {
	if !visible {
		ui.mode.setStatus(&IdleStatus{})
		return
	}
	ui.mode.mu.Lock()
	streaming := ui.mode.streaming
	ui.mode.mu.Unlock()
	if streaming {
		ui.mode.setStatus(NewWorkingStatusIndicator(ui.mode.ui, "Working..."))
	}
}

func (ui *InteractiveUI) SetWorkingIndicator(opts *extensions.WorkingIndicatorOptions) {
	if opts == nil || len(opts.Frames) == 0 {
		ui.mode.mu.Lock()
		streaming := ui.mode.streaming
		ui.mode.mu.Unlock()
		if streaming {
			ui.mode.setStatus(NewWorkingStatusIndicator(ui.mode.ui, "Working..."))
		}
		return
	}
	intervalMS := opts.IntervalMS
	if intervalMS <= 0 {
		intervalMS = 100
	}
	ui.mode.setStatus(&StatusIndicator{
		Loader: tui.NewLoader(ui.mode.ui,
			func(s string) string { return theme.FG("accent", s) },
			func(s string) string { return theme.FG("muted", s) },
			opts.Frames[0], &tui.LoaderIndicatorOptions{
				Frames:   opts.Frames,
				Interval: time.Duration(intervalMS) * time.Millisecond,
			},
		),
		Kind: StatusWorking,
	})
}

func (ui *InteractiveUI) SetHiddenThinkingLabel(label *string) {
	ui.mode.mu.Lock()
	if label != nil {
		ui.mode.thinkingLabel = *label
	} else {
		ui.mode.thinkingLabel = ""
	}
	component := ui.mode.currentStreaming
	hidden := ui.mode.thinkingHidden
	currentLabel := ui.mode.thinkingLabel
	ui.mode.mu.Unlock()
	if component != nil {
		component.SetHideThinkingBlock(hidden, currentLabel)
	}
	ui.mode.ui.RequestRender()
}

// ─── Widgets ─────────────────────────────────────────────

func (ui *InteractiveUI) SetWidget(key string, widget *extensions.Widget, opts *extensions.WidgetOptions) {
	ui.mu.Lock()
	defer ui.mu.Unlock()

	placement := extensions.WidgetAboveEditor
	if opts != nil && opts.Placement != "" {
		placement = opts.Placement
	}

	if widget == nil {
		if existing, ok := ui.widgets[key]; ok {
			if comp, hasComp := ui.widgetComps[key]; hasComp {
				ui.containerForPlacement(existing.placement).RemoveChild(comp)
				delete(ui.widgetComps, key)
			}
			delete(ui.widgets, key)
		}
		ui.mode.ui.RequestRender()
		return
	}

	// Remove old if key exists
	if existing, ok := ui.widgets[key]; ok {
		if comp, hasComp := ui.widgetComps[key]; hasComp {
			ui.containerForPlacement(existing.placement).RemoveChild(comp)
			delete(ui.widgetComps, key)
		}
	}

	ui.widgets[key] = widgetEntry{placement: placement}

	if widget.Factory != nil {
		comp := widget.Factory(ui.mode, ui.Theme())
		if comp != nil {
			ui.widgetComps[key] = comp
			ui.containerForPlacement(placement).AddChild(comp)
		}
	} else if len(widget.Lines) > 0 {
		comp := tui.NewText(strings.Join(widget.Lines, "\n"), 1, 0, nil)
		ui.widgetComps[key] = comp
		ui.containerForPlacement(placement).AddChild(comp)
	}
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) containerForPlacement(placement extensions.WidgetPlacement) *tui.Container {
	if placement == extensions.WidgetBelowEditor {
		return ui.mode.widgetBelow
	}
	return ui.mode.widgetAbove
}

// ─── Layout ──────────────────────────────────────────────

func (ui *InteractiveUI) SetFooter(factory extensions.FooterFactory) {
	ui.mode.footer.Clear()
	if factory == nil {
		ui.mode.footer.AddChild(NewFooterComponent(ui.mode.session, ui.mode))
	} else if component := factory(ui.mode, ui.Theme(), ui.mode); component != nil {
		ui.mode.footer.AddChild(component)
	}
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) SetHeader(factory extensions.HeaderFactory) {
	ui.mode.header.Clear()
	if factory != nil {
		if component := factory(ui.mode, ui.Theme()); component != nil {
			ui.mode.header.AddChild(component)
		}
	} else {
		ui.mode.addDefaultHeader()
	}
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) SetTitle(title string) {
	ui.mode.ui.Terminal().SetTitle(title)
}

func (ui *InteractiveUI) Custom(ctx context.Context, factory extensions.CustomFactory, opts *extensions.CustomOptions) (any, bool, error) {
	if factory == nil {
		return nil, false, nil
	}
	result := make(chan any, 1)
	done := func(value any) {
		select {
		case result <- value:
		default:
		}
	}
	component, err := factory(ui.mode, ui.Theme(), extensionKeybindings{ui.mode.keybindings}, done)
	if err != nil {
		return nil, false, err
	}
	if component == nil {
		return nil, false, nil
	}
	overlay := opts != nil && opts.Overlay
	container := ui.mode.editorContainer
	var tuiOverlay *tui.Overlay
	var overlayHandle *interactiveOverlayHandle
	if overlay {
		tuiOverlay = ui.mode.ui.AddOverlay(component, func(width, height int) tui.OverlayLayout { return resolveOverlayLayout(opts, width, height) })
		overlayHandle = &interactiveOverlayHandle{overlay: tuiOverlay, mode: ui.mode, component: component}
		if opts.OnHandle != nil {
			opts.OnHandle(overlayHandle)
		}
		layout := resolveOverlayLayout(opts, ui.mode.Width(), ui.mode.Height())
		if !layoutNonCapturing(opts, layout) {
			overlayHandle.Focus()
		}
	} else {
		container.Clear()
		container.AddChild(component)
		focusExtensionComponent(ui.mode, component)
	}
	ui.mode.ui.RequestRender()
	defer func() {
		if disposable, ok := component.(extensions.DisposableComponent); ok {
			disposable.Dispose()
		}
		if overlay {
			tuiOverlay.Remove()
		} else {
			container.Clear()
			ui.mode.restoreEditorComponent()
		}
		ui.mode.ui.SetFocus(ui.mode.activeEditorFocus())
		ui.mode.ui.RequestRender()
	}()
	select {
	case value := <-result:
		return value, true, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func focusExtensionComponent(mode *InteractiveMode, component extensions.Component) {
	if editor, ok := component.(extensions.EditorComponent); ok {
		mode.ui.SetFocus(extensionEditorAdapter{EditorComponent: editor})
	} else {
		mode.ui.SetFocus(component)
	}
}

func resolveOverlayLayout(opts *extensions.CustomOptions, width, height int) tui.OverlayLayout {
	resolved := extensions.OverlayOptions{Anchor: extensions.OverlayCenter}
	if opts != nil {
		if opts.StaticOverlayOptions != nil {
			resolved = *opts.StaticOverlayOptions
		}
		if opts.DynamicOverlayOptions != nil {
			resolved = opts.DynamicOverlayOptions()
		}
	}
	result := tui.OverlayLayout{Width: overlayDimension(resolved.Width, width), MinWidth: resolved.MinWidth, MaxHeight: overlayDimension(resolved.MaxHeight, height), Anchor: string(resolved.Anchor), OffsetX: resolved.OffsetX, OffsetY: resolved.OffsetY, Visible: resolved.Visible, NonCapturing: resolved.NonCapturing}
	if value, ok := overlayCoordinate(resolved.Row); ok {
		result.Row = &value
	}
	if value, ok := overlayCoordinate(resolved.Column); ok {
		result.Column = &value
	}
	return result
}

func overlayDimension(value any, total int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if strings.HasSuffix(typed, "%") {
			var percent float64
			if _, err := fmt.Sscanf(strings.TrimSuffix(typed, "%"), "%f", &percent); err == nil {
				return int(float64(total) * percent / 100)
			}
		}
	}
	return 0
}

func overlayCoordinate(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	}
	return 0, false
}

func layoutNonCapturing(opts *extensions.CustomOptions, _ tui.OverlayLayout) bool {
	if opts == nil {
		return false
	}
	resolved := opts.StaticOverlayOptions
	if opts.DynamicOverlayOptions != nil {
		value := opts.DynamicOverlayOptions()
		resolved = &value
	}
	return resolved != nil && resolved.NonCapturing
}

type interactiveOverlayHandle struct {
	mu        sync.Mutex
	overlay   *tui.Overlay
	mode      *InteractiveMode
	component extensions.Component
	focused   bool
}

func (handle *interactiveOverlayHandle) Hide()                 { handle.SetHidden(true) }
func (handle *interactiveOverlayHandle) SetHidden(hidden bool) { handle.overlay.SetHidden(hidden) }
func (handle *interactiveOverlayHandle) IsHidden() bool        { return handle.overlay.IsHidden() }
func (handle *interactiveOverlayHandle) Focus() {
	handle.mu.Lock()
	handle.focused = true
	handle.mu.Unlock()
	focusExtensionComponent(handle.mode, handle.component)
}
func (handle *interactiveOverlayHandle) Unfocus(component extensions.Component) {
	handle.mu.Lock()
	handle.focused = false
	handle.mu.Unlock()
	if component != nil {
		focusExtensionComponent(handle.mode, component)
	} else {
		handle.mode.ui.SetFocus(handle.mode.activeEditorFocus())
	}
}
func (handle *interactiveOverlayHandle) IsFocused() bool {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.focused
}

// ─── Editor ──────────────────────────────────────────────

func (ui *InteractiveUI) PasteToEditor(text string) {
	ui.mode.editor.InsertTextAtCursor(text)
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) SetEditorText(text string) {
	ui.mode.editor.SetText(text)
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) GetEditorText() string {
	return ui.mode.editor.GetText()
}

func (ui *InteractiveUI) Editor(ctx context.Context, content string, language *string) (string, bool, error) {
	result := make(chan inputDialogResult, 1)
	editor := NewCustomEditor(ui.mode.ui, theme.EditorTheme(), ui.mode.keybindings)
	editor.SetText(content)
	editor.OnSubmit = func(value string) { result <- inputDialogResult{value: value} }
	editor.OnEscape = func() { result <- inputDialogResult{cancelled: true} }
	ui.mode.editorContainer.Clear()
	if language != nil && *language != "" {
		ui.mode.editorContainer.AddChild(tui.NewText(theme.FG("dim", *language), 1, 0, nil))
	}
	ui.mode.editorContainer.AddChild(editor)
	ui.mode.ui.SetFocus(editor)
	ui.mode.ui.RequestRender()
	defer func() {
		ui.mode.restoreEditorComponent()
		ui.mode.ui.SetFocus(ui.mode.activeEditorFocus())
		ui.mode.ui.RequestRender()
	}()
	select {
	case resolved := <-result:
		return resolved.value, !resolved.cancelled, nil
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
}

func (ui *InteractiveUI) AddAutocompleteProvider(factory extensions.AutocompleteProviderFactory) {
	ui.mu.Lock()
	ui.acProviders = append(ui.acProviders, factory)
	ui.mu.Unlock()
	ui.mode.setupAutocomplete()
}

func (ui *InteractiveUI) SetEditorComponent(factory extensions.EditorFactory) {
	ui.mu.Lock()
	ui.editorFactory = factory
	ui.mu.Unlock()
	ui.mode.installEditorFactory(factory)
}

func (ui *InteractiveUI) GetEditorComponent() extensions.EditorFactory {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.editorFactory
}

// ─── Theme ───────────────────────────────────────────────

func (ui *InteractiveUI) Theme() extensions.Theme {
	return themeAdapter{value: theme.Current()}
}

func (ui *InteractiveUI) GetAllThemes() []extensions.ThemeInfo {
	return theme.GetAllThemes()
}

func (ui *InteractiveUI) GetTheme(name string) extensions.Theme {
	value := theme.GetTheme(name)
	if value == nil {
		return nil
	}
	return themeAdapter{value: value}
}

func (ui *InteractiveUI) SetTheme(value any) extensions.ThemeSetResult {
	name, ok := value.(string)
	if !ok {
		return extensions.ThemeSetResult{Error: "theme must be a string name"}
	}
	if err := theme.SetTheme(name); err != nil {
		return extensions.ThemeSetResult{Error: err.Error()}
	}
	if err := ui.mode.session.SetTheme(name); err != nil {
		return extensions.ThemeSetResult{Error: err.Error()}
	}
	ui.mode.applyTheme()
	return extensions.ThemeSetResult{Success: true}
}

type extensionEditorAdapter struct{ extensions.EditorComponent }

func (adapter extensionEditorAdapter) HandleInput(event tui.KeyEvent) {
	adapter.EditorComponent.HandleInput(event.Raw)
}
func (extensionEditorAdapter) SetFocused(bool) {}

type extensionKeybindings struct{ manager *tui.KeybindingsManager }

func (bindings extensionKeybindings) Matches(input, binding string) bool {
	return bindings.manager.Matches(input, binding)
}
func (bindings extensionKeybindings) Keys(binding string) []string {
	keys := bindings.manager.Keys(binding)
	result := make([]string, len(keys))
	for index := range keys {
		result[index] = string(keys[index])
	}
	return result
}
func (bindings extensionKeybindings) Definition(binding string) extensions.KeybindingDefinition {
	definition, _ := bindings.manager.Definition(binding)
	keys := make([]string, len(definition.DefaultKeys))
	for index := range definition.DefaultKeys {
		keys[index] = string(definition.DefaultKeys[index])
	}
	return extensions.KeybindingDefinition{DefaultKeys: keys, Description: definition.Description}
}
func (bindings extensionKeybindings) Conflicts() []extensions.KeybindingConflict {
	conflicts := bindings.manager.Conflicts()
	result := make([]extensions.KeybindingConflict, len(conflicts))
	for index := range conflicts {
		result[index] = extensions.KeybindingConflict{Key: string(conflicts[index].Key), Bindings: append([]string(nil), conflicts[index].Keybindings...)}
	}
	return result
}
func (bindings extensionKeybindings) UserBindings() map[string][]string {
	return keybindingStrings(bindings.manager.UserBindings())
}
func (bindings extensionKeybindings) ResolvedBindings() map[string][]string {
	return keybindingStrings(bindings.manager.ResolvedBindings())
}

func keybindingStrings(values tui.KeybindingsConfig) map[string][]string {
	result := make(map[string][]string, len(values))
	for name, keys := range values {
		converted := make([]string, len(keys))
		for index := range keys {
			converted[index] = string(keys[index])
		}
		result[name] = converted
	}
	return result
}

type extensionAutocompleteAdapter struct {
	provider extensions.AutocompleteProvider
}

func (adapter extensionAutocompleteAdapter) GetSuggestions(ctx context.Context, lines []string, cursorLine, cursorCol int, force bool) *tui.AutocompleteSuggestions {
	result, err := adapter.provider.GetSuggestions(ctx, extensions.AutocompleteRequest{Lines: lines, CursorLine: cursorLine, CursorCol: cursorCol, Signal: ctx, Force: force})
	if err != nil || result == nil {
		return nil
	}
	items := make([]tui.AutocompleteItem, len(result.Items))
	for index, item := range result.Items {
		items[index] = tui.AutocompleteItem{Value: item.Value, Label: item.Label, Description: item.Description}
	}
	return &tui.AutocompleteSuggestions{Prefix: result.Prefix, Items: items}
}
func (adapter extensionAutocompleteAdapter) ApplyCompletion(lines []string, cursorLine, cursorCol int, item tui.AutocompleteItem, prefix string) tui.CompletionResult {
	request := extensions.AutocompleteRequest{Lines: lines, CursorLine: cursorLine, CursorCol: cursorCol, Signal: context.Background()}
	newLines, line, col := adapter.provider.ApplyCompletion(request, extensions.AutocompleteItem{Value: item.Value, Label: item.Label, Description: item.Description}, prefix)
	return tui.CompletionResult{Lines: newLines, CursorLine: line, CursorCol: col}
}
func (adapter extensionAutocompleteAdapter) TriggerCharacters() []string {
	return adapter.provider.TriggerCharacters()
}
func (adapter extensionAutocompleteAdapter) ShouldTriggerFileCompletion(lines []string, cursorLine, cursorCol int) bool {
	return adapter.provider.ShouldTriggerFileCompletion(extensions.AutocompleteRequest{Lines: lines, CursorLine: cursorLine, CursorCol: cursorCol, Signal: context.Background()})
}

type tuiAutocompleteAdapter struct{ provider tui.AutocompleteProvider }

func (adapter tuiAutocompleteAdapter) TriggerCharacters() []string {
	if value, ok := adapter.provider.(tui.TriggerCharacterProvider); ok {
		return value.TriggerCharacters()
	}
	return nil
}
func (adapter tuiAutocompleteAdapter) GetSuggestions(ctx context.Context, request extensions.AutocompleteRequest) (*extensions.AutocompleteResult, error) {
	result := adapter.provider.GetSuggestions(ctx, request.Lines, request.CursorLine, request.CursorCol, request.Force)
	if result == nil {
		return nil, nil
	}
	items := make([]extensions.AutocompleteItem, len(result.Items))
	for index, item := range result.Items {
		items[index] = extensions.AutocompleteItem{Value: item.Value, Label: item.Label, Description: item.Description}
	}
	return &extensions.AutocompleteResult{Prefix: result.Prefix, Items: items}, nil
}
func (adapter tuiAutocompleteAdapter) ApplyCompletion(request extensions.AutocompleteRequest, item extensions.AutocompleteItem, prefix string) ([]string, int, int) {
	result := adapter.provider.ApplyCompletion(request.Lines, request.CursorLine, request.CursorCol, tui.AutocompleteItem{Value: item.Value, Label: item.Label, Description: item.Description}, prefix)
	return result.Lines, result.CursorLine, result.CursorCol
}
func (adapter tuiAutocompleteAdapter) ShouldTriggerFileCompletion(request extensions.AutocompleteRequest) bool {
	if value, ok := adapter.provider.(tui.FileCompletionGate); ok {
		return value.ShouldTriggerFileCompletion(request.Lines, request.CursorLine, request.CursorCol)
	}
	return false
}

// ─── Tools ───────────────────────────────────────────────

func (ui *InteractiveUI) GetToolsExpanded() bool {
	ui.mode.mu.Lock()
	defer ui.mode.mu.Unlock()
	return ui.mode.toolsExpanded
}

func (ui *InteractiveUI) SetToolsExpanded(expanded bool) {
	ui.mode.mu.Lock()
	ui.mode.toolsExpanded = expanded
	components := make([]*ToolExecutionComponent, 0, len(ui.mode.toolComponents))
	for _, component := range ui.mode.toolComponents {
		components = append(components, component)
	}
	ui.mode.mu.Unlock()
	for _, tc := range components {
		tc.SetExpanded(expanded)
	}
	ui.mode.ui.RequestRender()
}

// Verify interface compliance.
var _ extensions.UI = (*InteractiveUI)(nil)

// ─── Helpers ─────────────────────────────────────────────

func newStyledText(color, text string) *styledTextComponent {
	return &styledTextComponent{color: color, text: text}
}

type styledTextComponent struct {
	color string
	text  string
}

func (c *styledTextComponent) Invalidate() {}
func (c *styledTextComponent) Render(width int) []string {
	return []string{theme.FG(c.color, c.text)}
}

func selectListTheme() tui.SelectListTheme {
	return tui.SelectListTheme{
		SelectedPrefix: func(s string) string { return theme.FG("accent", s) },
		SelectedText:   func(s string) string { return theme.FG("selectedText", s) },
		Description:    func(s string) string { return theme.FG("dim", s) },
		ScrollInfo:     func(s string) string { return theme.FG("dim", s) },
		NoMatch:        func(s string) string { return theme.FG("dim", s) },
	}
}
