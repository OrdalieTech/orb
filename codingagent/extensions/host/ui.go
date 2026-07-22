package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

type UIDialogCancellationReason string

const UIDialogCancellationHostRestarted UIDialogCancellationReason = "host_restarted"

type UIDialogCancellationError struct {
	Reason UIDialogCancellationReason
}

func (err *UIDialogCancellationError) Error() string {
	return "extension host UI dialog cancelled: " + string(err.Reason)
}

var ErrUIDialogHostRestarted = &UIDialogCancellationError{Reason: UIDialogCancellationHostRestarted}

type uiGeneration struct {
	generation *generation
	context    context.Context
	cancel     context.CancelCauseFunc

	contextMu sync.RWMutex
	contexts  map[string]extensions.Context
	dialogs   map[string]context.CancelCauseFunc
	nextID    atomic.Uint64

	componentMu sync.RWMutex
	components  map[string]*wireComponent
	overlays    map[string]extensions.OverlayHandle
	terminal    map[string]func()
}

func newUIGeneration(value *generation) *uiGeneration {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &uiGeneration{
		generation: value,
		context:    ctx,
		cancel:     cancel,
		contexts:   make(map[string]extensions.Context),
		dialogs:    make(map[string]context.CancelCauseFunc),
		components: make(map[string]*wireComponent),
		overlays:   make(map[string]extensions.OverlayHandle),
		terminal:   make(map[string]func()),
	}
}

func (ui *uiGeneration) close() {
	if ui == nil {
		return
	}
	ui.cancel(ErrUIDialogHostRestarted)
	ui.componentMu.Lock()
	unsubscribers := ui.terminal
	ui.terminal = make(map[string]func())
	ui.components = make(map[string]*wireComponent)
	ui.overlays = make(map[string]extensions.OverlayHandle)
	ui.componentMu.Unlock()
	for _, unsubscribe := range unsubscribers {
		unsubscribe()
	}
}

type boundUIContext struct {
	manager    *Manager
	generation *generation
	id         string
	wire       wireContext
}

func (manager *Manager) bindUIContext(value extensions.Context) (*boundUIContext, error) {
	manager.mu.Lock()
	generation := manager.current
	restarting := manager.restarting
	manager.mu.Unlock()
	if generation == nil {
		if restarting {
			return nil, ErrRestarting
		}
		return nil, ErrNotRunning
	}
	if !generation.ready.Load() || generation.ui == nil {
		return nil, ErrRestarting
	}
	id := fmt.Sprintf("ui-%d", generation.ui.nextID.Add(1))
	generation.ui.contextMu.Lock()
	generation.ui.contexts[id] = value
	generation.ui.contextMu.Unlock()
	wire := newWireContext(value)
	wire.UIContextID = id
	wire.UI = snapshotUI(value)
	return &boundUIContext{manager: manager, generation: generation, id: id, wire: wire}, nil
}

func (bound *boundUIContext) close() {
	if bound == nil || bound.generation.ui == nil {
		return
	}
	bound.generation.ui.contextMu.Lock()
	delete(bound.generation.ui.contexts, bound.id)
	bound.generation.ui.contextMu.Unlock()
}

func (bound *boundUIContext) request(
	ctx context.Context,
	method string,
	params any,
	update func(json.RawMessage),
) (json.RawMessage, error) {
	requestContext, cancel := bound.manager.timeoutContext(ctx)
	defer cancel()
	return bound.generation.request(requestContext, method, params, update)
}

type wireUIRequest struct {
	ExtensionID string `json:"extensionId"`
	ContextID   string `json:"contextId"`
	Method      string `json:"method"`

	Title       string   `json:"title,omitempty"`
	Options     []string `json:"options,omitempty"`
	Timeout     *int64   `json:"timeout,omitempty"`
	Message     string   `json:"message,omitempty"`
	Placeholder *string  `json:"placeholder,omitempty"`
	Prefill     *string  `json:"prefill,omitempty"`
	NotifyType  string   `json:"notifyType,omitempty"`

	StatusKey  string  `json:"statusKey,omitempty"`
	StatusText *string `json:"statusText,omitempty"`
	Text       *string `json:"text,omitempty"`
	Visible    bool    `json:"visible,omitempty"`
	Expanded   bool    `json:"expanded,omitempty"`

	WorkingIndicator *extensions.WorkingIndicatorOptions `json:"workingIndicator,omitempty"`
	WidgetKey        string                              `json:"widgetKey,omitempty"`
	WidgetLines      *[]string                           `json:"widgetLines,omitempty"`
	WidgetPlacement  extensions.WidgetPlacement          `json:"widgetPlacement,omitempty"`
	FactoryHandle    string                              `json:"factoryHandle,omitempty"`
	ComponentHandle  string                              `json:"componentHandle,omitempty"`
	HandlerHandle    string                              `json:"handlerHandle,omitempty"`
	ThemeName        string                              `json:"themeName,omitempty"`
	Action           string                              `json:"action,omitempty"`
	RequestID        string                              `json:"requestId,omitempty"`
	Value            json.RawMessage                     `json:"value,omitempty"`
	CustomOptions    *wireCustomOptions                  `json:"customOptions,omitempty"`
}

type wireUIDialogResult struct {
	Value     any   `json:"value,omitempty"`
	Confirmed *bool `json:"confirmed,omitempty"`
	Cancelled bool  `json:"cancelled,omitempty"`
}

func (manager *Manager) handleUIRequest(generation *generation, frameValue frame) (any, *protocolError) {
	var request wireUIRequest
	if err := json.Unmarshal(frameValue.Params, &request); err != nil {
		return nil, uiProtocolError("invalid_ui_request", err)
	}
	uiContext := generation.ui.contextValue(request.ContextID)
	if uiContext == nil {
		return nil, uiProtocolError("stale_ui_context", errors.New("UI context is no longer active"))
	}
	ctx, cancel := generation.ui.requestContext(uiContext)
	defer cancel(nil)
	if request.Method == "select" || request.Method == "confirm" || request.Method == "input" {
		generation.ui.registerDialog(frameValue.ID, cancel)
		defer generation.ui.unregisterDialog(frameValue.ID)
	}
	userInterface := uiContext.UI()
	dialogOptions := &extensions.DialogOptions{Signal: ctx, Timeout: request.Timeout}

	switch request.Method {
	case "select":
		value, selected, err := userInterface.Select(ctx, request.Title, append([]string(nil), request.Options...), dialogOptions)
		if err != nil {
			return nil, uiRequestError(err)
		}
		if !selected {
			return wireUIDialogResult{Cancelled: true}, nil
		}
		return wireUIDialogResult{Value: value}, nil
	case "confirm":
		confirmed, err := userInterface.Confirm(ctx, request.Title, request.Message, dialogOptions)
		if err != nil {
			return nil, uiRequestError(err)
		}
		return wireUIDialogResult{Confirmed: &confirmed}, nil
	case "input":
		value, entered, err := userInterface.Input(ctx, request.Title, request.Placeholder, dialogOptions)
		if err != nil {
			return nil, uiRequestError(err)
		}
		if !entered {
			return wireUIDialogResult{Cancelled: true}, nil
		}
		return wireUIDialogResult{Value: value}, nil
	case "editor":
		value, edited, err := userInterface.Editor(ctx, request.Title, request.Prefill)
		if err != nil {
			return nil, uiRequestError(err)
		}
		if !edited {
			return wireUIDialogResult{Cancelled: true}, nil
		}
		return wireUIDialogResult{Value: value}, nil
	case "custom":
		return manager.handleUICustom(ctx, generation, uiContext, request)
	default:
		return nil, uiProtocolError("unknown_ui_method", fmt.Errorf("unknown correlated UI method %q", request.Method))
	}
}

func (manager *Manager) handleUIEvent(generation *generation, raw json.RawMessage) {
	var request wireUIRequest
	if json.Unmarshal(raw, &request) != nil || generation.ui == nil {
		return
	}
	uiContext := generation.ui.contextValue(request.ContextID)
	if uiContext == nil && request.Method != "custom_done" && request.Method != "component_request_render" && request.Method != "overlay_action" {
		return
	}
	var userInterface extensions.UI
	if uiContext != nil {
		userInterface = uiContext.UI()
	}
	switch request.Method {
	case "notify":
		kind := extensions.NotificationType(request.NotifyType)
		if kind == "" {
			kind = extensions.NotifyInfo
		}
		userInterface.Notify(request.Message, kind)
	case "setStatus":
		userInterface.SetStatus(request.StatusKey, request.StatusText)
	case "setWorkingMessage":
		userInterface.SetWorkingMessage(request.Text)
	case "setWorkingVisible":
		userInterface.SetWorkingVisible(request.Visible)
	case "setWorkingIndicator":
		userInterface.SetWorkingIndicator(request.WorkingIndicator)
	case "setHiddenThinkingLabel":
		userInterface.SetHiddenThinkingLabel(request.Text)
	case "setWidget":
		manager.setUIWidget(generation, userInterface, request)
	case "setFooter":
		manager.setUIFooter(generation, userInterface, request)
	case "setHeader":
		manager.setUIHeader(generation, userInterface, request)
	case "setTitle":
		userInterface.SetTitle(request.Title)
	case "pasteToEditor":
		if request.Text != nil {
			userInterface.PasteToEditor(*request.Text)
		}
	case "setEditorText":
		if request.Text != nil {
			userInterface.SetEditorText(*request.Text)
		}
	case "setEditorComponent":
		manager.setUIEditorComponent(generation, userInterface, request)
	case "addAutocompleteProvider":
		manager.addUIAutocompleteProvider(generation, userInterface, request)
	case "setTheme":
		userInterface.SetTheme(request.ThemeName)
	case "setToolsExpanded":
		userInterface.SetToolsExpanded(request.Expanded)
	case "onTerminalInput":
		manager.addUITerminalHandler(generation, userInterface, request)
	case "unsubscribeTerminalInput":
		generation.ui.removeTerminalHandler(request.HandlerHandle)
	case "custom_done":
		generation.ui.completeCustom(request.ComponentHandle, request.Value)
	case "component_request_render":
		generation.ui.invalidateComponent(request.ComponentHandle)
	case "overlay_action":
		generation.ui.overlayAction(request)
	case "cancelDialog":
		generation.ui.cancelDialog(request.RequestID)
	}
}

func (ui *uiGeneration) contextValue(id string) extensions.Context {
	if ui == nil || id == "" {
		return nil
	}
	ui.contextMu.RLock()
	defer ui.contextMu.RUnlock()
	return ui.contexts[id]
}

func (ui *uiGeneration) requestContext(value extensions.Context) (context.Context, context.CancelCauseFunc) {
	parent := context.Background()
	if value != nil && value.Signal() != nil {
		parent = value.Signal()
	}
	ctx, cancel := context.WithCancelCause(parent)
	stop := context.AfterFunc(ui.context, func() { cancel(context.Cause(ui.context)) })
	return ctx, func(err error) {
		stop()
		cancel(err)
	}
}

func (ui *uiGeneration) cancelDialog(id string) {
	ui.contextMu.Lock()
	cancel, registered := ui.dialogs[id]
	if !registered {
		ui.dialogs[id] = nil
	}
	ui.contextMu.Unlock()
	if cancel != nil {
		cancel(context.Canceled)
	}
}

func (ui *uiGeneration) registerDialog(id string, cancel context.CancelCauseFunc) {
	ui.contextMu.Lock()
	_, cancelled := ui.dialogs[id]
	ui.dialogs[id] = cancel
	ui.contextMu.Unlock()
	if cancelled {
		cancel(context.Canceled)
	}
}

func (ui *uiGeneration) unregisterDialog(id string) {
	ui.contextMu.Lock()
	delete(ui.dialogs, id)
	ui.contextMu.Unlock()
}

func uiRequestError(err error) *protocolError {
	var cancellation *UIDialogCancellationError
	if errors.As(err, &cancellation) || errors.Is(err, ErrUIDialogHostRestarted) {
		return &protocolError{Code: "ui_cancelled", Message: err.Error()}
	}
	if errors.Is(err, context.Canceled) {
		return &protocolError{Code: "ui_cancelled", Message: err.Error()}
	}
	return uiProtocolError("ui_error", err)
}

func uiProtocolError(code string, err error) *protocolError {
	return &protocolError{Code: code, Message: err.Error()}
}
