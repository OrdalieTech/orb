package host

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/tui"
)

type wireCustomOptions struct {
	Overlay        bool                `json:"overlay,omitempty"`
	OverlayOptions *wireOverlayOptions `json:"overlayOptions,omitempty"`
	OnHandle       bool                `json:"onHandle,omitempty"`
}

type wireOverlayOptions struct {
	Width        any                      `json:"width,omitempty"`
	MinWidth     int                      `json:"minWidth,omitempty"`
	MaxHeight    any                      `json:"maxHeight,omitempty"`
	Anchor       extensions.OverlayAnchor `json:"anchor,omitempty"`
	OffsetX      int                      `json:"offsetX,omitempty"`
	OffsetY      int                      `json:"offsetY,omitempty"`
	Row          any                      `json:"row,omitempty"`
	Column       any                      `json:"col,omitempty"`
	Margin       any                      `json:"margin,omitempty"`
	Visible      *bool                    `json:"visible,omitempty"`
	NonCapturing bool                     `json:"nonCapturing,omitempty"`
}

type wireComponentEvent struct {
	ExtensionID     string                    `json:"extensionId,omitempty"`
	ContextID       string                    `json:"contextId,omitempty"`
	ComponentHandle string                    `json:"componentHandle"`
	FactoryHandle   string                    `json:"factoryHandle,omitempty"`
	Event           string                    `json:"event"`
	Kind            string                    `json:"kind,omitempty"`
	Width           int                       `json:"width,omitempty"`
	Height          int                       `json:"height,omitempty"`
	Data            string                    `json:"data,omitempty"`
	Focused         bool                      `json:"focused,omitempty"`
	Text            *string                   `json:"text,omitempty"`
	Theme           *wireTheme                `json:"theme,omitempty"`
	Keybindings     *wireKeybindings          `json:"keybindings,omitempty"`
	Footer          *wireFooterData           `json:"footerData,omitempty"`
	Provider        *wireAutocompleteProvider `json:"provider,omitempty"`
}

type wireComponentResponse struct {
	Text            *string                         `json:"text,omitempty"`
	TerminalResult  *extensions.TerminalInputResult `json:"terminalResult,omitempty"`
	HandlesInput    bool                            `json:"handlesInput,omitempty"`
	TracksFocus     bool                            `json:"tracksFocus,omitempty"`
	WantsKeyRelease bool                            `json:"wantsKeyRelease,omitempty"`
}

type wireComponentRender struct {
	ComponentHandle string   `json:"componentHandle"`
	Lines           []string `json:"lines"`
	Width           int      `json:"width"`
	Text            *string  `json:"text,omitempty"`
}

type wireComponent struct {
	generation *generation
	handle     string
	kind       string

	mu             sync.Mutex
	host           extensions.UIHost
	lines          []string
	text           string
	lastWidth      int
	renderedWidth  int
	requestPending bool
	disposed       bool
	done           extensions.CustomDone
	doneOnce       sync.Once
}

func (component *wireComponent) Render(width int) []string {
	component.mu.Lock()
	lines := append([]string(nil), component.lines...)
	shouldRequest := !component.disposed && !component.requestPending && component.renderedWidth != width
	if shouldRequest {
		component.requestPending = true
		component.lastWidth = width
	}
	component.mu.Unlock()
	if shouldRequest {
		_ = component.generation.sendEvent("ui_component_event", wireComponentEvent{
			ComponentHandle: component.handle,
			Event:           "render",
			Width:           width,
		})
	}
	return lines
}

func (component *wireComponent) handleInput(data string) {
	response := component.call(wireComponentEvent{ComponentHandle: component.handle, Event: "input", Data: data})
	if response.Text != nil {
		component.mu.Lock()
		component.text = *response.Text
		component.mu.Unlock()
	}
}

type wireInputComponent struct {
	*wireComponent
	wantsKeyRelease bool
}

func (component *wireInputComponent) HandleInput(event tui.KeyEvent) {
	component.handleInput(event.Raw)
}

func (component *wireInputComponent) WantsKeyRelease() bool {
	return component.wantsKeyRelease
}

type wireFocusableComponent struct{ *wireInputComponent }

func (component *wireFocusableComponent) SetFocused(focused bool) {
	component.call(wireComponentEvent{ComponentHandle: component.handle, Event: "focus", Focused: focused})
}

type wireEditorComponent struct{ *wireComponent }

func (component *wireEditorComponent) HandleInput(data string) {
	component.handleInput(data)
}

func (component *wireComponent) GetText() string {
	component.mu.Lock()
	defer component.mu.Unlock()
	return component.text
}

func (component *wireComponent) SetText(text string) {
	component.mu.Lock()
	component.text = text
	component.mu.Unlock()
	component.call(wireComponentEvent{ComponentHandle: component.handle, Event: "set_text", Text: &text})
}

func (component *wireComponent) SetAutocompleteProvider(provider extensions.AutocompleteProvider) {
	if provider == nil {
		return
	}
	component.call(wireComponentEvent{
		ComponentHandle: component.handle,
		Event:           "set_autocomplete_provider",
		Provider:        snapshotAutocompleteProvider(provider),
	})
}

func (component *wireComponent) Dispose() {
	component.mu.Lock()
	if component.disposed {
		component.mu.Unlock()
		return
	}
	component.disposed = true
	component.mu.Unlock()
	_ = component.generation.sendEvent("ui_component_event", wireComponentEvent{
		ComponentHandle: component.handle,
		Event:           "dispose",
	})
	if component.generation.ui != nil {
		component.generation.ui.componentMu.Lock()
		delete(component.generation.ui.components, component.handle)
		delete(component.generation.ui.overlays, component.handle)
		component.generation.ui.componentMu.Unlock()
	}
}

func (component *wireComponent) call(event wireComponentEvent) wireComponentResponse {
	ctx, cancel := component.generation.manager.timeoutContext(context.Background())
	defer cancel()
	raw, err := component.generation.request(ctx, "ui_component_event", event, nil)
	if err != nil {
		return wireComponentResponse{}
	}
	var response wireComponentResponse
	_ = json.Unmarshal(raw, &response)
	return response
}

func (generation *generation) sendEvent(method string, params any) error {
	value, err := eventFrame(method, params)
	if err != nil {
		return err
	}
	return generation.codec.write(value)
}

func (ui *uiGeneration) mountComponent(
	ctx context.Context,
	handle, factoryHandle, kind string,
	host extensions.UIHost,
	theme extensions.Theme,
	keybindings extensions.Keybindings,
	footer extensions.FooterDataProvider,
	done extensions.CustomDone,
) (extensions.Component, error) {
	if handle == "" {
		handle = fmt.Sprintf("component-%d", ui.nextID.Add(1))
	}
	component := &wireComponent{generation: ui.generation, handle: handle, kind: kind, host: host, done: done}
	ui.componentMu.Lock()
	ui.components[handle] = component
	ui.componentMu.Unlock()
	event := wireComponentEvent{
		ComponentHandle: handle,
		FactoryHandle:   factoryHandle,
		Event:           "mount",
		Kind:            kind,
		Theme:           snapshotTheme(theme),
		Keybindings:     snapshotKeybindings(keybindings),
		Footer:          snapshotFooterData(footer),
	}
	if host != nil {
		event.Width = host.Width()
		event.Height = host.Height()
	}
	requestContext, cancel := ui.generation.manager.timeoutContext(ctx)
	defer cancel()
	raw, err := ui.generation.request(requestContext, "ui_component_event", event, nil)
	if err != nil {
		ui.componentMu.Lock()
		delete(ui.components, handle)
		ui.componentMu.Unlock()
		return nil, err
	}
	var response wireComponentResponse
	if json.Unmarshal(raw, &response) == nil && response.Text != nil {
		component.text = *response.Text
	}
	switch kind {
	case "editor":
		return &wireEditorComponent{wireComponent: component}, nil
	case "custom":
		if response.TracksFocus {
			return &wireFocusableComponent{wireInputComponent: &wireInputComponent{
				wireComponent: component, wantsKeyRelease: response.WantsKeyRelease,
			}}, nil
		}
		if response.HandlesInput {
			return &wireInputComponent{wireComponent: component, wantsKeyRelease: response.WantsKeyRelease}, nil
		}
	}
	return component, nil
}

func (ui *uiGeneration) routeRender(raw json.RawMessage) {
	var render wireComponentRender
	if json.Unmarshal(raw, &render) != nil || render.ComponentHandle == "" {
		return
	}
	ui.componentMu.RLock()
	component := ui.components[render.ComponentHandle]
	ui.componentMu.RUnlock()
	if component == nil {
		return
	}
	component.mu.Lock()
	component.lines = append([]string(nil), render.Lines...)
	component.renderedWidth = render.Width
	component.requestPending = false
	if render.Text != nil {
		component.text = *render.Text
	}
	host := component.host
	component.mu.Unlock()
	if host != nil {
		host.Invalidate()
	}
}

func (ui *uiGeneration) completeCustom(handle string, raw json.RawMessage) {
	ui.componentMu.RLock()
	component := ui.components[handle]
	ui.componentMu.RUnlock()
	if component == nil || component.done == nil {
		return
	}
	var value any
	if len(raw) != 0 {
		_ = json.Unmarshal(raw, &value)
	}
	component.doneOnce.Do(func() { component.done(value) })
}

func (ui *uiGeneration) invalidateComponent(handle string) {
	ui.componentMu.RLock()
	component := ui.components[handle]
	ui.componentMu.RUnlock()
	if component == nil {
		return
	}
	component.mu.Lock()
	host := component.host
	component.mu.Unlock()
	if host != nil {
		host.Invalidate()
	}
}

func (manager *Manager) handleUICustom(
	ctx context.Context,
	generation *generation,
	uiContext extensions.Context,
	request wireUIRequest,
) (any, *protocolError) {
	options := decodeWireCustomOptions(request.CustomOptions)
	if options != nil && request.CustomOptions.OnHandle {
		options.OnHandle = func(handle extensions.OverlayHandle) {
			generation.ui.componentMu.Lock()
			generation.ui.overlays[request.ComponentHandle] = handle
			generation.ui.componentMu.Unlock()
			_ = generation.sendEvent("ui_component_event", wireComponentEvent{
				ComponentHandle: request.ComponentHandle,
				Event:           "overlay_handle",
			})
		}
	}
	factory := func(host extensions.UIHost, theme extensions.Theme, keybindings extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		return generation.ui.mountComponent(
			ctx, request.ComponentHandle, request.FactoryHandle, "custom", host, theme, keybindings, nil, done,
		)
	}
	value, resolved, err := uiContext.UI().Custom(ctx, factory, options)
	if err != nil {
		return nil, uiRequestError(err)
	}
	if !resolved {
		return wireUIDialogResult{Cancelled: true}, nil
	}
	return wireUIDialogResult{Value: value}, nil
}

func decodeWireCustomOptions(value *wireCustomOptions) *extensions.CustomOptions {
	if value == nil {
		return nil
	}
	options := &extensions.CustomOptions{Overlay: value.Overlay}
	if value.OverlayOptions != nil {
		wire := value.OverlayOptions
		decoded := &extensions.OverlayOptions{
			Width: wire.Width, MinWidth: wire.MinWidth, MaxHeight: wire.MaxHeight,
			Anchor: wire.Anchor, OffsetX: wire.OffsetX, OffsetY: wire.OffsetY,
			Row: wire.Row, Column: wire.Column, Margin: wire.Margin, NonCapturing: wire.NonCapturing,
		}
		if wire.Visible != nil {
			visible := *wire.Visible
			decoded.Visible = func(int, int) bool { return visible }
		}
		options.StaticOverlayOptions = decoded
	}
	return options
}

func (manager *Manager) setUIWidget(generation *generation, ui extensions.UI, request wireUIRequest) {
	options := &extensions.WidgetOptions{Placement: request.WidgetPlacement}
	if request.WidgetPlacement == "" {
		options = nil
	}
	var widget *extensions.Widget
	switch {
	case request.FactoryHandle != "":
		widget = &extensions.Widget{Factory: func(host extensions.UIHost, theme extensions.Theme) extensions.Component {
			component, _ := generation.ui.mountComponent(
				context.Background(), "", request.FactoryHandle, "widget", host, theme, nil, nil, nil,
			)
			return component
		}}
	case request.WidgetLines != nil:
		widget = &extensions.Widget{Lines: append([]string(nil), (*request.WidgetLines)...)}
	}
	ui.SetWidget(request.WidgetKey, widget, options)
}

func (manager *Manager) setUIFooter(generation *generation, ui extensions.UI, request wireUIRequest) {
	if request.FactoryHandle == "" {
		ui.SetFooter(nil)
		return
	}
	ui.SetFooter(func(host extensions.UIHost, theme extensions.Theme, footer extensions.FooterDataProvider) extensions.Component {
		component, _ := generation.ui.mountComponent(
			context.Background(), "", request.FactoryHandle, "footer", host, theme, nil, footer, nil,
		)
		return component
	})
}

func (manager *Manager) setUIHeader(generation *generation, ui extensions.UI, request wireUIRequest) {
	if request.FactoryHandle == "" {
		ui.SetHeader(nil)
		return
	}
	ui.SetHeader(func(host extensions.UIHost, theme extensions.Theme) extensions.Component {
		component, _ := generation.ui.mountComponent(
			context.Background(), "", request.FactoryHandle, "header", host, theme, nil, nil, nil,
		)
		return component
	})
}

func (manager *Manager) setUIEditorComponent(generation *generation, ui extensions.UI, request wireUIRequest) {
	if request.FactoryHandle == "" {
		ui.SetEditorComponent(nil)
		return
	}
	ui.SetEditorComponent(func(host extensions.UIHost, theme extensions.Theme, keybindings extensions.Keybindings) extensions.EditorComponent {
		component, _ := generation.ui.mountComponent(
			context.Background(), "", request.FactoryHandle, "editor", host, theme, keybindings, nil, nil,
		)
		editor, _ := component.(extensions.EditorComponent)
		return editor
	})
}

func (manager *Manager) addUITerminalHandler(generation *generation, ui extensions.UI, request wireUIRequest) {
	if request.HandlerHandle == "" {
		return
	}
	unsubscribe := ui.OnTerminalInput(func(data string) *extensions.TerminalInputResult {
		ctx, cancel := generation.manager.timeoutContext(context.Background())
		defer cancel()
		raw, err := generation.request(ctx, "ui_component_event", wireComponentEvent{
			ComponentHandle: request.HandlerHandle,
			Event:           "terminal_input",
			Data:            data,
		}, nil)
		if err != nil {
			return nil
		}
		var response wireComponentResponse
		if json.Unmarshal(raw, &response) != nil {
			return nil
		}
		return response.TerminalResult
	})
	generation.ui.componentMu.Lock()
	generation.ui.terminal[request.HandlerHandle] = unsubscribe
	generation.ui.componentMu.Unlock()
}

func (ui *uiGeneration) removeTerminalHandler(handle string) {
	ui.componentMu.Lock()
	unsubscribe := ui.terminal[handle]
	delete(ui.terminal, handle)
	ui.componentMu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
}

func (ui *uiGeneration) overlayAction(request wireUIRequest) {
	ui.componentMu.RLock()
	handle := ui.overlays[request.ComponentHandle]
	ui.componentMu.RUnlock()
	if handle == nil {
		return
	}
	switch request.Action {
	case "hide":
		handle.Hide()
	case "setHidden":
		handle.SetHidden(request.Visible)
	case "focus":
		handle.Focus()
	case "unfocus":
		handle.Unfocus()
	}
}
