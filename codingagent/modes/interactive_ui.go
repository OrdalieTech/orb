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

	mu                        sync.Mutex
	widgets                   map[string]widgetEntry
	widgetComps               map[string]tui.Component
	editorFactory             extensions.EditorFactory
	acProviders               []extensions.AutocompleteProviderFactory
	terminalInputListeners    map[uint64]func()
	nextTerminalInputListener uint64
	workingMessage            *string
	workingVisible            bool
	workingIndicator          *extensions.WorkingIndicatorOptions
	customFooter              extensions.Component
	customHeader              extensions.Component
	widgetOrder               []string
	builtInFooter             []tui.Component
	builtInHeader             []tui.Component
	activeSelector            *ExtensionSelectorComponent
	activeInput               *ExtensionInputComponent
	activeEditorDialog        *ExtensionEditorComponent
}

type widgetEntry struct {
	placement extensions.WidgetPlacement
}

func NewInteractiveUI(mode *InteractiveMode) *InteractiveUI {
	ui := &InteractiveUI{
		mode:                   mode,
		widgets:                make(map[string]widgetEntry),
		widgetComps:            make(map[string]tui.Component),
		terminalInputListeners: make(map[uint64]func()),
		workingVisible:         true,
	}
	// The JS bridge's CustomEditor base constructs the current mode's real
	// built-in editor (upstream custom-editor.ts extends the tui Editor).
	extensions.RegisterCustomEditorBase(func(extensions.UIHost, extensions.Theme, extensions.Keybindings) extensions.EditorComponent {
		return bridgeEditorBase{NewCustomEditor(mode.ui, theme.EditorTheme(), mode.keybindings)}
	})
	if mode.footer != nil {
		ui.builtInFooter = mode.footer.Children()
	}
	if mode.header != nil {
		ui.builtInHeader = mode.header.Children()
	}
	if mode.widgetAbove != nil && len(mode.widgetAbove.Children()) == 0 {
		mode.widgetAbove.AddChild(tui.NewSpacer(1))
	}
	return ui
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
	if opts != nil && opts.Signal != nil {
		select {
		case <-opts.Signal.Done():
			return "", false, nil
		default:
		}
	}
	result := make(chan selectResult, 1)
	resolve := func(value selectResult) {
		select {
		case result <- value:
		default:
		}
	}
	dialog := NewExtensionSelectorItemsComponent(title, items,
		func(value string) { resolve(selectResult{value: value}) },
		func() { resolve(selectResult{cancelled: true}) },
		&extensionDialogOptions{ui: ui.mode.ui, timeout: dialogTimeout(opts), onToggleToolsExpanded: func() {
			ui.SetToolsExpanded(!ui.GetToolsExpanded())
		}},
	)
	ui.mu.Lock()
	previous := ui.activeSelector
	ui.activeSelector = dialog
	ui.mu.Unlock()
	if previous != nil {
		previous.cancel()
	}

	ui.mode.editorContainer.Clear()
	ui.mode.editorContainer.AddChild(dialog)
	ui.mode.ui.SetFocus(dialog)
	ui.mode.ui.RequestRender()

	defer func() {
		ui.mu.Lock()
		if ui.activeSelector == dialog {
			ui.activeSelector = nil
		}
		ui.mu.Unlock()
		dialog.Dispose()
		ui.mode.editorContainer.Clear()
		ui.mode.restoreEditorComponent()
		ui.mode.ui.SetFocus(ui.mode.activeEditorFocus())
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
		return "", false, nil
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
	if opts != nil && opts.Signal != nil {
		select {
		case <-opts.Signal.Done():
			return "", false, nil
		default:
		}
	}
	result := make(chan inputDialogResult, 1)
	resolve := func(value inputDialogResult) {
		select {
		case result <- value:
		default:
		}
	}
	placeholderValue := ""
	if placeholder != nil {
		placeholderValue = *placeholder
	}
	dialog := NewExtensionInputComponent(title, placeholderValue,
		func(value string) { resolve(inputDialogResult{value: value}) },
		func() { resolve(inputDialogResult{cancelled: true}) },
		&extensionDialogOptions{ui: ui.mode.ui, timeout: dialogTimeout(opts)},
	)
	ui.mu.Lock()
	previous := ui.activeInput
	ui.activeInput = dialog
	ui.mu.Unlock()
	if previous != nil {
		previous.cancel()
	}

	ui.mode.editorContainer.Clear()
	ui.mode.editorContainer.AddChild(dialog)
	ui.mode.ui.SetFocus(dialog)
	ui.mode.ui.RequestRender()

	defer func() {
		ui.mu.Lock()
		if ui.activeInput == dialog {
			ui.activeInput = nil
		}
		ui.mu.Unlock()
		dialog.Dispose()
		ui.mode.editorContainer.Clear()
		ui.mode.restoreEditorComponent()
		ui.mode.ui.SetFocus(ui.mode.activeEditorFocus())
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
		return "", false, nil
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
	switch notifyType {
	case extensions.NotifyWarning:
		ui.mode.chat.AddChild(tui.NewSpacer(1))
		ui.mode.chat.AddChild(tui.NewText(theme.FG("warning", "Warning: "+message), 1, 0, nil))
	case extensions.NotifyError:
		ui.mode.chat.AddChild(tui.NewSpacer(1))
		ui.mode.chat.AddChild(tui.NewText(theme.FG("error", "Error: "+message), 1, 0, nil))
	default:
		ui.mode.showStatusMessage(message)
		return
	}
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) OnTerminalInput(handler extensions.TerminalInputHandler) func() {
	if handler == nil {
		return func() {}
	}
	unsubscribe := ui.mode.ui.AddInputListener(func(data string) tui.InputListenerResult {
		result := handler(data)
		if result == nil {
			return tui.InputListenerResult{}
		}
		return tui.InputListenerResult{Consume: result.Consume, Data: result.Data}
	})
	ui.mu.Lock()
	ui.nextTerminalInputListener++
	id := ui.nextTerminalInputListener
	ui.terminalInputListeners[id] = unsubscribe
	ui.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			unsubscribe()
			ui.mu.Lock()
			delete(ui.terminalInputListeners, id)
			ui.mu.Unlock()
		})
	}
}

func (ui *InteractiveUI) clearTerminalInputListeners() {
	ui.mu.Lock()
	listeners := make([]func(), 0, len(ui.terminalInputListeners))
	for id, unsubscribe := range ui.terminalInputListeners {
		listeners = append(listeners, unsubscribe)
		delete(ui.terminalInputListeners, id)
	}
	ui.mu.Unlock()
	for _, unsubscribe := range listeners {
		unsubscribe()
	}
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
	ui.mu.Lock()
	ui.workingMessage = cloneStringPointer(msg)
	ui.mu.Unlock()
	ui.mutateWorkingIndicator(func(indicator *StatusIndicator) {
		indicator.SetMessage(workingMessage(msg))
	})
}

func (ui *InteractiveUI) SetWorkingVisible(visible bool) {
	ui.mu.Lock()
	ui.workingVisible = visible
	ui.mu.Unlock()
	if !visible {
		ui.mode.clearStatusIndicatorKind(StatusWorking)
		return
	}
	ui.showWorkingIndicator()
}

func (ui *InteractiveUI) SetWorkingIndicator(opts *extensions.WorkingIndicatorOptions) {
	copy := cloneWorkingIndicatorOptions(opts)
	ui.mu.Lock()
	ui.workingIndicator = copy
	ui.mu.Unlock()
	ui.mutateWorkingIndicator(func(indicator *StatusIndicator) {
		indicator.SetIndicator(loaderIndicatorOptions(copy))
	})
	ui.mode.ui.RequestRender()
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneWorkingIndicatorOptions(value *extensions.WorkingIndicatorOptions) *extensions.WorkingIndicatorOptions {
	if value == nil {
		return nil
	}
	copy := *value
	if value.Frames != nil {
		copy.Frames = append([]string{}, value.Frames...)
	}
	return &copy
}

func workingMessage(value *string) string {
	if value == nil {
		return "Working..."
	}
	return *value
}

func loaderIndicatorOptions(value *extensions.WorkingIndicatorOptions) *tui.LoaderIndicatorOptions {
	if value == nil {
		return nil
	}
	frames := []string(nil)
	if value.Frames != nil {
		frames = append([]string{}, value.Frames...)
	}
	interval := time.Duration(0)
	if value.IntervalMS > 0 {
		interval = time.Duration(value.IntervalMS) * time.Millisecond
	}
	return &tui.LoaderIndicatorOptions{Frames: frames, Interval: interval}
}

// Upstream mutates the working indicator single-threaded; lookup and mutation
// must stay atomic here or a concurrent Dispose can be resurrected by
// Loader.SetIndicator's unconditional ticker restart.
func (ui *InteractiveUI) mutateWorkingIndicator(mutate func(indicator *StatusIndicator)) {
	ui.mode.mu.Lock()
	defer ui.mode.mu.Unlock()
	indicator, ok := ui.mode.statusIndicator.(*StatusIndicator)
	if !ok || indicator.Kind != StatusWorking {
		return
	}
	mutate(indicator)
}

func (ui *InteractiveUI) showWorkingIndicator() {
	ui.mu.Lock()
	visible := ui.workingVisible
	message := cloneStringPointer(ui.workingMessage)
	options := cloneWorkingIndicatorOptions(ui.workingIndicator)
	ui.mu.Unlock()
	if !visible {
		ui.mode.clearStatusIndicatorKind("")
		return
	}
	ui.mode.mu.Lock()
	streaming := ui.mode.streaming
	current, isWorking := ui.mode.statusIndicator.(*StatusIndicator)
	isWorking = isWorking && current.Kind == StatusWorking
	ui.mode.mu.Unlock()
	if streaming && !isWorking {
		ui.mode.setStatus(NewWorkingStatusIndicator(ui.mode.ui, workingMessage(message), options))
	}
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) SetHiddenThinkingLabel(label *string) {
	resolved := "Thinking..."
	if label != nil {
		resolved = *label
	}
	ui.mode.mu.Lock()
	ui.mode.thinkingLabel = resolved
	component := ui.mode.currentStreaming
	ui.mode.mu.Unlock()
	if ui.mode.chat != nil {
		for _, child := range ui.mode.chat.Children() {
			if assistant, ok := child.(*AssistantMessageComponent); ok {
				assistant.SetHiddenThinkingLabel(resolved)
			}
		}
	}
	if component != nil {
		component.SetHiddenThinkingLabel(resolved)
	}
	ui.mode.ui.RequestRender()
}

// ─── Widgets ─────────────────────────────────────────────

func (ui *InteractiveUI) SetWidget(key string, widget *extensions.Widget, opts *extensions.WidgetOptions) {
	placement := extensions.WidgetAboveEditor
	if opts != nil && opts.Placement != "" {
		placement = opts.Placement
	}
	ui.mu.Lock()
	existing := ui.widgetComps[key]
	delete(ui.widgets, key)
	delete(ui.widgetComps, key)
	ui.widgetOrder = removeWidgetOrderKey(ui.widgetOrder, key)
	ui.mu.Unlock()
	disposeExtensionComponent(existing)
	if widget == nil {
		ui.renderWidgets()
		return
	}

	var component extensions.Component
	if widget.Factory != nil {
		component = widget.Factory(ui.mode, ui.Theme())
	} else {
		container := &tui.Container{}
		limit := min(len(widget.Lines), 10)
		for _, line := range widget.Lines[:limit] {
			container.AddChild(tui.NewText(line, 1, 0, nil))
		}
		if len(widget.Lines) > 10 {
			container.AddChild(tui.NewText(theme.FG("muted", "... (widget truncated)"), 1, 0, nil))
		}
		component = container
	}
	if component != nil {
		ui.mu.Lock()
		ui.widgets[key] = widgetEntry{placement: placement}
		ui.widgetComps[key] = component
		ui.widgetOrder = append(ui.widgetOrder, key)
		ui.mu.Unlock()
	}
	ui.renderWidgets()
}

func removeWidgetOrderKey(order []string, key string) []string {
	for index, candidate := range order {
		if candidate == key {
			return append(order[:index], order[index+1:]...)
		}
	}
	return order
}

func (ui *InteractiveUI) renderWidgets() {
	ui.mu.Lock()
	type placedComponent struct {
		placement extensions.WidgetPlacement
		component extensions.Component
	}
	components := make([]placedComponent, 0, len(ui.widgetOrder))
	for _, key := range ui.widgetOrder {
		entry, exists := ui.widgets[key]
		component := ui.widgetComps[key]
		if exists && component != nil {
			components = append(components, placedComponent{placement: entry.placement, component: component})
		}
	}
	if ui.mode.widgetAbove != nil {
		ui.mode.widgetAbove.Clear()
		hasAbove := false
		for _, entry := range components {
			if entry.placement != extensions.WidgetBelowEditor {
				hasAbove = true
				break
			}
		}
		ui.mode.widgetAbove.AddChild(tui.NewSpacer(1))
		if hasAbove {
			for _, entry := range components {
				if entry.placement != extensions.WidgetBelowEditor {
					ui.mode.widgetAbove.AddChild(entry.component)
				}
			}
		}
	}
	if ui.mode.widgetBelow != nil {
		ui.mode.widgetBelow.Clear()
		for _, entry := range components {
			if entry.placement == extensions.WidgetBelowEditor {
				ui.mode.widgetBelow.AddChild(entry.component)
			}
		}
	}
	ui.mu.Unlock()
	if ui.mode.ui != nil {
		ui.mode.ui.RequestRender()
	}
}

func (ui *InteractiveUI) clearWidgets() {
	ui.mu.Lock()
	components := make([]extensions.Component, 0, len(ui.widgetComps))
	for _, placement := range []extensions.WidgetPlacement{extensions.WidgetAboveEditor, extensions.WidgetBelowEditor} {
		for _, key := range ui.widgetOrder {
			if ui.widgets[key].placement == placement {
				components = append(components, ui.widgetComps[key])
			}
		}
	}
	ui.widgets = make(map[string]widgetEntry)
	ui.widgetComps = make(map[string]tui.Component)
	ui.widgetOrder = nil
	ui.mu.Unlock()
	for _, component := range components {
		disposeExtensionComponent(component)
	}
	ui.renderWidgets()
}

func (ui *InteractiveUI) resetExtensionUI() {
	ui.mu.Lock()
	selector := ui.activeSelector
	input := ui.activeInput
	editorDialog := ui.activeEditorDialog
	ui.activeSelector = nil
	ui.activeInput = nil
	ui.activeEditorDialog = nil
	ui.mu.Unlock()
	if selector != nil {
		selector.cancel()
	}
	if input != nil {
		input.cancel()
	}
	if editorDialog != nil {
		editorDialog.cancel()
	}
	if ui.mode.ui != nil {
		ui.mode.ui.HideOverlay()
	}
	ui.clearTerminalInputListeners()
	ui.SetFooter(nil)
	ui.SetHeader(nil)
	ui.clearWidgets()
	ui.mode.mu.Lock()
	clear(ui.mode.footerStatuses)
	ui.mode.thinkingLabel = "Thinking..."
	ui.mode.mu.Unlock()
	if ui.mode.footer != nil {
		ui.mode.footer.Invalidate()
	}
	ui.mu.Lock()
	ui.acProviders = nil
	ui.editorFactory = nil
	ui.workingMessage = nil
	ui.workingVisible = true
	ui.workingIndicator = nil
	ui.mu.Unlock()
	if ui.mode.editor != nil {
		ui.mode.editor.OnExtensionShortcut = nil
		if ui.mode.editorContainer != nil && ui.mode.ui != nil {
			ui.mode.installEditorFactory(nil)
		}
	}
	if ui.mode.session != nil {
		ui.mode.setupAutocomplete()
	}
	if ui.mode.ui != nil {
		ui.mode.updateTerminalTitle()
	}
	ui.mutateWorkingIndicator(func(indicator *StatusIndicator) {
		indicator.SetIndicator(nil)
		indicator.SetMessage(fmt.Sprintf("Working... (%s to interrupt)", KeyText("app.interrupt")))
	})
	ui.SetHiddenThinkingLabel(nil)
}

// ─── Layout ──────────────────────────────────────────────

func (ui *InteractiveUI) SetFooter(factory extensions.FooterFactory) {
	ui.mu.Lock()
	previous := ui.customFooter
	ui.customFooter = nil
	ui.mu.Unlock()
	disposeExtensionComponent(previous)
	if ui.mode.footer == nil {
		return
	}
	ui.mode.footer.Clear()
	if factory == nil {
		if len(ui.builtInFooter) > 0 {
			for _, component := range ui.builtInFooter {
				ui.mode.footer.AddChild(component)
			}
		} else if ui.mode.session != nil {
			ui.mode.footer.AddChild(NewFooterComponent(ui.mode.session, ui.mode))
		}
	} else if component := factory(ui.mode, ui.Theme(), ui.mode); component != nil {
		ui.mu.Lock()
		ui.customFooter = component
		ui.mu.Unlock()
		ui.mode.footer.AddChild(component)
	}
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) SetHeader(factory extensions.HeaderFactory) {
	ui.mu.Lock()
	previous := ui.customHeader
	ui.customHeader = nil
	ui.mu.Unlock()
	disposeExtensionComponent(previous)
	if ui.mode.header == nil {
		return
	}
	ui.mode.header.Clear()
	if factory != nil {
		if component := factory(ui.mode, ui.Theme()); component != nil {
			ui.mode.mu.Lock()
			expanded := ui.mode.toolsExpanded
			ui.mode.mu.Unlock()
			setExpandedComponent(component, expanded)
			ui.mu.Lock()
			ui.customHeader = component
			ui.mu.Unlock()
			ui.mode.header.AddChild(component)
		}
	} else if len(ui.builtInHeader) > 0 {
		ui.mode.mu.Lock()
		expanded := ui.mode.toolsExpanded
		ui.mode.mu.Unlock()
		for _, component := range ui.builtInHeader {
			setExpandedComponent(component, expanded)
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
	savedText := ui.mode.activeEditorText(false)
	overlay := opts != nil && opts.Overlay
	result := make(chan any, 1)
	var transactionMu sync.Mutex
	closed := false
	var component extensions.Component
	var tuiOverlay tui.OverlayHandle
	restoreEditor := func() {
		ui.mode.editorContainer.Clear()
		ui.mode.restoreEditorComponent()
		ui.mode.setActiveEditorText(savedText)
		ui.mode.ui.SetFocus(ui.mode.activeEditorFocus())
		ui.mode.ui.RequestRender()
	}
	done := func(value any) {
		transactionMu.Lock()
		if closed {
			transactionMu.Unlock()
			return
		}
		closed = true
		installedComponent := component
		installedOverlay := tuiOverlay
		transactionMu.Unlock()
		if overlay {
			if installedOverlay != nil {
				installedOverlay.Hide()
			} else {
				ui.mode.ui.HideOverlay()
			}
		} else {
			restoreEditor()
		}
		disposeExtensionComponent(installedComponent)
		result <- value
	}
	created, err := factory(ui.mode, ui.Theme(), extensionKeybindings{ui.mode.keybindings}, done)
	transactionMu.Lock()
	if closed {
		transactionMu.Unlock()
		return <-result, true, nil
	}
	if err != nil {
		closed = true
		transactionMu.Unlock()
		if !overlay {
			restoreEditor()
		}
		return nil, false, err
	}
	if created == nil {
		closed = true
		transactionMu.Unlock()
		return nil, false, nil
	}
	component = created
	var overlayHandle *interactiveOverlayHandle
	if overlay {
		resolved := resolveCustomOverlayOptions(opts, component)
		if resolved == nil {
			tuiOverlay = ui.mode.ui.ShowOverlay(component)
		} else {
			tuiOverlay = ui.mode.ui.ShowOverlay(component, toTUIOverlayOptions(*resolved))
		}
		overlayHandle = &interactiveOverlayHandle{overlay: tuiOverlay}
	} else {
		ui.mode.editorContainer.Clear()
		ui.mode.editorContainer.AddChild(component)
		focusExtensionComponent(ui.mode, component)
	}
	transactionMu.Unlock()
	if overlay && opts.OnHandle != nil {
		opts.OnHandle(overlayHandle)
	}
	ui.mode.ui.RequestRender()
	select {
	case value := <-result:
		return value, true, nil
	case <-ctx.Done():
		transactionMu.Lock()
		if !closed {
			closed = true
			installedComponent := component
			installedOverlay := tuiOverlay
			transactionMu.Unlock()
			if overlay {
				if installedOverlay != nil {
					installedOverlay.Hide()
				}
			} else {
				restoreEditor()
			}
			disposeExtensionComponent(installedComponent)
		} else {
			transactionMu.Unlock()
		}
		return nil, false, ctx.Err()
	}
}

func disposeExtensionComponent(component extensions.Component) {
	disposable, ok := component.(extensions.DisposableComponent)
	if !ok {
		return
	}
	defer func() { _ = recover() }()
	disposable.Dispose()
}

func focusExtensionComponent(mode *InteractiveMode, component extensions.Component) {
	if editor, ok := component.(extensions.EditorComponent); ok {
		mode.ui.SetFocus(extensionEditorAdapter{EditorComponent: editor})
	} else {
		mode.ui.SetFocus(component)
	}
}

func resolveCustomOverlayOptions(opts *extensions.CustomOptions, component extensions.Component) *extensions.OverlayOptions {
	if opts != nil {
		if opts.DynamicOverlayOptions != nil {
			resolved := opts.DynamicOverlayOptions()
			return &resolved
		}
		if opts.StaticOverlayOptions != nil {
			resolved := *opts.StaticOverlayOptions
			return &resolved
		}
	}
	if sized, ok := component.(interface{ Width() int }); ok && sized.Width() > 0 {
		return &extensions.OverlayOptions{Width: sized.Width()}
	}
	return nil
}

func toTUIOverlayOptions(value extensions.OverlayOptions) tui.OverlayOptions {
	return tui.OverlayOptions{
		Width:        overlaySizeValue(value.Width),
		MinWidth:     value.MinWidth,
		MaxHeight:    overlaySizeValue(value.MaxHeight),
		Anchor:       overlayAnchor(value.Anchor),
		OffsetX:      value.OffsetX,
		OffsetY:      value.OffsetY,
		Row:          overlaySizeValue(value.Row),
		Col:          overlaySizeValue(value.Column),
		Margin:       overlayMargin(value.Margin),
		Visible:      value.Visible,
		NonCapturing: value.NonCapturing,
	}
}

func overlaySizeValue(value any) tui.SizeValue {
	switch typed := value.(type) {
	case int:
		return tui.AbsoluteSize(typed)
	case int64:
		return tui.AbsoluteSize(int(typed))
	case float64:
		return tui.AbsoluteSize(int(typed))
	case string:
		if strings.HasSuffix(typed, "%") {
			var percent float64
			if _, err := fmt.Sscanf(strings.TrimSuffix(typed, "%"), "%f", &percent); err == nil {
				return tui.PercentSize(percent)
			}
		}
	}
	return tui.SizeValue{}
}

func overlayAnchor(value extensions.OverlayAnchor) tui.OverlayAnchor {
	switch value {
	case extensions.OverlayTop:
		return tui.OverlayTopCenter
	case extensions.OverlayLeft:
		return tui.OverlayLeftCenter
	case extensions.OverlayRight:
		return tui.OverlayRightCenter
	case extensions.OverlayBottom:
		return tui.OverlayBottomCenter
	default:
		return tui.OverlayAnchor(value)
	}
}

func overlayMargin(value any) *tui.OverlayMargin {
	switch typed := value.(type) {
	case int:
		return tui.UniformOverlayMargin(typed)
	case int64:
		return tui.UniformOverlayMargin(int(typed))
	case float64:
		return tui.UniformOverlayMargin(int(typed))
	case map[string]int:
		return &tui.OverlayMargin{Top: typed["top"], Right: typed["right"], Bottom: typed["bottom"], Left: typed["left"]}
	case *tui.OverlayMargin:
		if typed == nil {
			return nil
		}
		copy := *typed
		return &copy
	case tui.OverlayMargin:
		copy := typed
		return &copy
	}
	return nil
}

type interactiveOverlayHandle struct {
	overlay tui.OverlayHandle
}

func (handle *interactiveOverlayHandle) Hide()                 { handle.overlay.Hide() }
func (handle *interactiveOverlayHandle) SetHidden(hidden bool) { handle.overlay.SetHidden(hidden) }
func (handle *interactiveOverlayHandle) IsHidden() bool        { return handle.overlay.IsHidden() }
func (handle *interactiveOverlayHandle) Focus()                { handle.overlay.Focus() }
func (handle *interactiveOverlayHandle) Unfocus(options ...extensions.OverlayUnfocusOptions) {
	if len(options) == 0 {
		handle.overlay.Unfocus()
		return
	}
	target := tui.Component(options[0].Target)
	if editor, ok := options[0].Target.(extensions.EditorComponent); ok {
		target = extensionEditorAdapter{EditorComponent: editor}
	}
	handle.overlay.Unfocus(tui.OverlayUnfocusOptions{Target: target})
}
func (handle *interactiveOverlayHandle) IsFocused() bool { return handle.overlay.IsFocused() }

// ─── Editor ──────────────────────────────────────────────

func (ui *InteractiveUI) PasteToEditor(text string) {
	ui.mode.sendActiveEditorInput("\x1b[200~" + text + "\x1b[201~")
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) SetEditorText(text string) {
	ui.mode.setActiveEditorText(text)
	ui.mode.ui.RequestRender()
}

func (ui *InteractiveUI) GetEditorText() string {
	return ui.mode.activeEditorText(true)
}

func (ui *InteractiveUI) Editor(ctx context.Context, title string, prefill *string) (string, bool, error) {
	result := make(chan inputDialogResult, 1)
	resolve := func(value inputDialogResult) {
		select {
		case result <- value:
		default:
		}
	}
	prefillValue := ""
	if prefill != nil {
		prefillValue = *prefill
	}
	externalEditorCommand := ""
	if ui.mode.session != nil {
		externalEditorCommand = ui.mode.session.InteractiveModeSettings().ExternalEditor
	}
	editor := NewExtensionEditorComponent(ui.mode.ui, ui.mode.keybindings, title, prefillValue,
		func(value string) { resolve(inputDialogResult{value: value}) },
		func() { resolve(inputDialogResult{cancelled: true}) },
		externalEditorCommand,
	)
	ui.mu.Lock()
	previous := ui.activeEditorDialog
	ui.activeEditorDialog = editor
	ui.mu.Unlock()
	if previous != nil {
		previous.cancel()
	}
	ui.mode.editorContainer.Clear()
	ui.mode.editorContainer.AddChild(editor)
	ui.mode.ui.SetFocus(editor)
	ui.mode.ui.RequestRender()
	defer func() {
		ui.mu.Lock()
		if ui.activeEditorDialog == editor {
			ui.activeEditorDialog = nil
		}
		ui.mu.Unlock()
		ui.mode.editorContainer.Clear()
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

func dialogTimeout(opts *extensions.DialogOptions) *int64 {
	if opts == nil {
		return nil
	}
	return opts.Timeout
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
	switch typed := value.(type) {
	case themeAdapter:
		if ui.mode.themeController == nil {
			return extensions.ThemeSetResult{Error: "theme controller is not initialized"}
		}
		if err := ui.mode.themeController.SetInstance(typed.value); err != nil {
			return extensions.ThemeSetResult{Error: err.Error()}
		}
		ui.mode.applyTheme()
		return extensions.ThemeSetResult{Success: true}
	case string:
		if err := theme.SetTheme(typed); err != nil {
			return extensions.ThemeSetResult{Error: err.Error()}
		}
		if err := ui.mode.session.SetTheme(typed); err != nil {
			return extensions.ThemeSetResult{Error: err.Error()}
		}
		ui.mode.applyTheme()
		return extensions.ThemeSetResult{Success: true}
	default:
		return extensions.ThemeSetResult{Error: "theme must be a string name or theme instance"}
	}
}

type extensionEditorAdapter struct{ extensions.EditorComponent }

func (adapter extensionEditorAdapter) HandleInput(event tui.KeyEvent) {
	adapter.EditorComponent.HandleInput(event.Raw)
}
func (adapter extensionEditorAdapter) SetFocused(focused bool) {
	if component, ok := adapter.EditorComponent.(interface{ SetFocused(bool) }); ok {
		component.SetFocused(focused)
	}
}

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
	ui.mode.mu.Unlock()
	for _, container := range []*tui.Container{ui.mode.header, ui.mode.chat} {
		if container == nil {
			continue
		}
		for _, component := range container.Children() {
			setExpandedComponent(component, expanded)
		}
	}
	ui.mode.ui.RequestRender()
}

func setExpandedComponent(component tui.Component, expanded bool) {
	if expandable, ok := component.(interface{ SetExpanded(bool) }); ok {
		expandable.SetExpanded(expanded)
	}
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

// selectListTheme mirrors upstream getSelectListTheme's color mapping.
func selectListTheme() tui.SelectListTheme {
	return tui.SelectListTheme{
		SelectedPrefix: func(s string) string { return theme.FG("accent", s) },
		SelectedText:   func(s string) string { return theme.FG("accent", s) },
		Description:    func(s string) string { return theme.FG("muted", s) },
		ScrollInfo:     func(s string) string { return theme.FG("muted", s) },
		NoMatch:        func(s string) string { return theme.FG("muted", s) },
	}
}
