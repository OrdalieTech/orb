package rpc

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
)

type ExtensionUIRequest struct {
	Type            string    `json:"type"`
	ID              string    `json:"id"`
	Method          string    `json:"method"`
	Title           string    `json:"title,omitempty"`
	Options         []string  `json:"options,omitempty"`
	Timeout         *int64    `json:"timeout,omitempty"`
	Message         string    `json:"message,omitempty"`
	Placeholder     *string   `json:"placeholder,omitempty"`
	Prefill         *string   `json:"prefill,omitempty"`
	NotifyType      string    `json:"notifyType,omitempty"`
	StatusKey       string    `json:"statusKey,omitempty"`
	StatusText      *string   `json:"statusText,omitempty"`
	WidgetKey       string    `json:"widgetKey,omitempty"`
	WidgetLines     *[]string `json:"widgetLines,omitempty"`
	WidgetPlacement string    `json:"widgetPlacement,omitempty"`
	Text            string    `json:"text,omitempty"`
}

func (request ExtensionUIRequest) MarshalJSON() ([]byte, error) { //nolint:gocyclo // Each protocol variant has a distinct ordered shape.
	prefix := struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Method string `json:"method"`
	}{request.Type, request.ID, request.Method}
	switch request.Method {
	case "select":
		return ai.Marshal(struct {
			Type    string   `json:"type"`
			ID      string   `json:"id"`
			Method  string   `json:"method"`
			Title   string   `json:"title"`
			Options []string `json:"options"`
			Timeout *int64   `json:"timeout,omitempty"`
		}{prefix.Type, prefix.ID, prefix.Method, request.Title, request.Options, request.Timeout})
	case "confirm":
		return ai.Marshal(struct {
			Type    string `json:"type"`
			ID      string `json:"id"`
			Method  string `json:"method"`
			Title   string `json:"title"`
			Message string `json:"message"`
			Timeout *int64 `json:"timeout,omitempty"`
		}{prefix.Type, prefix.ID, prefix.Method, request.Title, request.Message, request.Timeout})
	case "input":
		return ai.Marshal(struct {
			Type        string  `json:"type"`
			ID          string  `json:"id"`
			Method      string  `json:"method"`
			Title       string  `json:"title"`
			Placeholder *string `json:"placeholder,omitempty"`
			Timeout     *int64  `json:"timeout,omitempty"`
		}{prefix.Type, prefix.ID, prefix.Method, request.Title, request.Placeholder, request.Timeout})
	case "editor":
		return ai.Marshal(struct {
			Type    string  `json:"type"`
			ID      string  `json:"id"`
			Method  string  `json:"method"`
			Title   string  `json:"title"`
			Prefill *string `json:"prefill,omitempty"`
		}{prefix.Type, prefix.ID, prefix.Method, request.Title, request.Prefill})
	case "notify":
		return ai.Marshal(struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Method     string `json:"method"`
			Message    string `json:"message"`
			NotifyType string `json:"notifyType,omitempty"`
		}{prefix.Type, prefix.ID, prefix.Method, request.Message, request.NotifyType})
	case "setStatus":
		return ai.Marshal(struct {
			Type       string  `json:"type"`
			ID         string  `json:"id"`
			Method     string  `json:"method"`
			StatusKey  string  `json:"statusKey"`
			StatusText *string `json:"statusText,omitempty"`
		}{prefix.Type, prefix.ID, prefix.Method, request.StatusKey, request.StatusText})
	case "setWidget":
		return ai.Marshal(struct {
			Type            string    `json:"type"`
			ID              string    `json:"id"`
			Method          string    `json:"method"`
			WidgetKey       string    `json:"widgetKey"`
			WidgetLines     *[]string `json:"widgetLines,omitempty"`
			WidgetPlacement string    `json:"widgetPlacement,omitempty"`
		}{prefix.Type, prefix.ID, prefix.Method, request.WidgetKey, request.WidgetLines, request.WidgetPlacement})
	case "setTitle":
		return ai.Marshal(struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			Method string `json:"method"`
			Title  string `json:"title"`
		}{prefix.Type, prefix.ID, prefix.Method, request.Title})
	case "set_editor_text":
		return ai.Marshal(struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			Method string `json:"method"`
			Text   string `json:"text"`
		}{prefix.Type, prefix.ID, prefix.Method, request.Text})
	default:
		return nil, fmt.Errorf("rpc extension UI: unknown method %q", request.Method)
	}
}

type uiDialogResult struct {
	response ExtensionUIResponse
	ok       bool
}

// ExtensionUI implements the RPC dialog sub-protocol. Phase-5 bindings use
// this same object; RPC mode already exposes it so native extensions can bind
// without another transport implementation.
type ExtensionUI struct {
	mu      sync.Mutex
	pending map[string]chan uiDialogResult
	output  func(any) error
	closed  bool
}

func newExtensionUI(output func(any) error) *ExtensionUI {
	return &ExtensionUI{pending: make(map[string]chan uiDialogResult), output: output}
}

func (ui *ExtensionUI) Select(ctx context.Context, title string, options []string, timeoutMS *int64) (*string, error) {
	request := ExtensionUIRequest{Method: "select", Title: title, Options: append(make([]string, 0, len(options)), options...)}
	response, ok, err := ui.dialog(ctx, timeoutMS, request)
	if err != nil || !ok || response.Cancelled || response.Value == nil {
		return nil, err
	}
	value := *response.Value
	return &value, nil
}

func (ui *ExtensionUI) Confirm(ctx context.Context, title, message string, timeoutMS *int64) (bool, error) {
	response, ok, err := ui.dialog(ctx, timeoutMS, ExtensionUIRequest{Method: "confirm", Title: title, Message: message})
	if err != nil || !ok || response.Cancelled || response.Confirmed == nil {
		return false, err
	}
	return *response.Confirmed, nil
}

func (ui *ExtensionUI) Input(ctx context.Context, title string, placeholder *string, timeoutMS *int64) (*string, error) {
	response, ok, err := ui.dialog(ctx, timeoutMS, ExtensionUIRequest{Method: "input", Title: title, Placeholder: placeholder})
	if err != nil || !ok || response.Cancelled || response.Value == nil {
		return nil, err
	}
	value := *response.Value
	return &value, nil
}

func (ui *ExtensionUI) Editor(ctx context.Context, title string, prefill *string) (*string, error) {
	response, ok, err := ui.dialog(ctx, nil, ExtensionUIRequest{Method: "editor", Title: title, Prefill: prefill})
	if err != nil || !ok || response.Cancelled || response.Value == nil {
		return nil, err
	}
	value := *response.Value
	return &value, nil
}

func (ui *ExtensionUI) Notify(message, notifyType string) error {
	return ui.fire(ExtensionUIRequest{Method: "notify", Message: message, NotifyType: notifyType})
}

func (ui *ExtensionUI) SetStatus(key string, text *string) error {
	return ui.fire(ExtensionUIRequest{Method: "setStatus", StatusKey: key, StatusText: text})
}

func (ui *ExtensionUI) SetWidget(key string, lines []string, placement string) error {
	var copied *[]string
	if lines != nil {
		value := append(make([]string, 0, len(lines)), lines...)
		copied = &value
	}
	return ui.fire(ExtensionUIRequest{Method: "setWidget", WidgetKey: key, WidgetLines: copied, WidgetPlacement: placement})
}

func (ui *ExtensionUI) SetTitle(title string) error {
	return ui.fire(ExtensionUIRequest{Method: "setTitle", Title: title})
}

func (ui *ExtensionUI) SetEditorText(text string) error {
	return ui.fire(ExtensionUIRequest{Method: "set_editor_text", Text: text})
}

func (ui *ExtensionUI) dialog(
	ctx context.Context,
	timeoutMS *int64,
	request ExtensionUIRequest,
) (ExtensionUIResponse, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return ExtensionUIResponse{}, false, nil
	}
	id, err := newRequestID()
	if err != nil {
		return ExtensionUIResponse{}, false, err
	}
	request.Type, request.ID = "extension_ui_request", id
	if timeoutMS != nil {
		value := *timeoutMS
		request.Timeout = &value
	}
	result := make(chan uiDialogResult, 1)
	ui.mu.Lock()
	if ui.closed {
		ui.mu.Unlock()
		return ExtensionUIResponse{}, false, nil
	}
	ui.pending[id] = result
	ui.mu.Unlock()
	if err := ui.output(request); err != nil {
		ui.remove(id)
		return ExtensionUIResponse{}, false, err
	}

	var timer <-chan time.Time
	if timeoutMS != nil && *timeoutMS != 0 {
		clock := time.NewTimer(time.Duration(*timeoutMS) * time.Millisecond)
		defer clock.Stop()
		timer = clock.C
	}
	select {
	case resolved := <-result:
		return resolved.response, resolved.ok, nil
	case <-ctx.Done():
		ui.remove(id)
		return ExtensionUIResponse{}, false, nil
	case <-timer:
		ui.remove(id)
		return ExtensionUIResponse{}, false, nil
	}
}

func (ui *ExtensionUI) fire(request ExtensionUIRequest) error {
	id, err := newRequestID()
	if err != nil {
		return err
	}
	request.Type, request.ID = "extension_ui_request", id
	return ui.output(request)
}

func (ui *ExtensionUI) HandleResponse(response ExtensionUIResponse) {
	ui.mu.Lock()
	waiter := ui.pending[response.ID]
	delete(ui.pending, response.ID)
	ui.mu.Unlock()
	if waiter != nil {
		waiter <- uiDialogResult{response: response, ok: true}
	}
}

func (ui *ExtensionUI) remove(id string) {
	ui.mu.Lock()
	delete(ui.pending, id)
	ui.mu.Unlock()
}

func (ui *ExtensionUI) close() {
	ui.mu.Lock()
	ui.closed = true
	pending := ui.pending
	ui.pending = make(map[string]chan uiDialogResult)
	ui.mu.Unlock()
	for _, waiter := range pending {
		waiter <- uiDialogResult{}
	}
}

// extensionUIAdapter exposes the RPC dialog sub-protocol through the
// extensions.UI seam, mirroring upstream createExtensionUIContext
// (rpc-mode.ts:135-305): dialogs and fire-and-forget requests go over the
// wire, TUI-only surfaces keep NoopUI behavior.
type extensionUIAdapter struct {
	extensions.NoopUI
	ui *ExtensionUI
}

func newExtensionUIAdapter(ui *ExtensionUI) extensions.UI {
	return extensionUIAdapter{ui: ui}
}

func dialogContext(ctx context.Context, options *extensions.DialogOptions) (context.Context, *int64) {
	if options == nil {
		return ctx, nil
	}
	if options.Signal != nil {
		ctx = options.Signal
	}
	return ctx, options.Timeout
}

func (adapter extensionUIAdapter) Select(ctx context.Context, title string, options []string, dialogOptions *extensions.DialogOptions) (string, bool, error) {
	ctx, timeout := dialogContext(ctx, dialogOptions)
	value, err := adapter.ui.Select(ctx, title, options, timeout)
	if err != nil || value == nil {
		return "", false, err
	}
	return *value, true, nil
}

func (adapter extensionUIAdapter) Confirm(ctx context.Context, title, message string, dialogOptions *extensions.DialogOptions) (bool, error) {
	ctx, timeout := dialogContext(ctx, dialogOptions)
	return adapter.ui.Confirm(ctx, title, message, timeout)
}

func (adapter extensionUIAdapter) Input(ctx context.Context, title string, placeholder *string, dialogOptions *extensions.DialogOptions) (string, bool, error) {
	ctx, timeout := dialogContext(ctx, dialogOptions)
	value, err := adapter.ui.Input(ctx, title, placeholder, timeout)
	if err != nil || value == nil {
		return "", false, err
	}
	return *value, true, nil
}

func (adapter extensionUIAdapter) Editor(ctx context.Context, title string, prefill *string) (string, bool, error) {
	value, err := adapter.ui.Editor(ctx, title, prefill)
	if err != nil || value == nil {
		return "", false, err
	}
	return *value, true, nil
}

func (adapter extensionUIAdapter) Notify(message string, notifyType extensions.NotificationType) {
	_ = adapter.ui.Notify(message, string(notifyType))
}

func (adapter extensionUIAdapter) SetStatus(key string, text *string) {
	_ = adapter.ui.SetStatus(key, text)
}

func (adapter extensionUIAdapter) SetWidget(key string, widget *extensions.Widget, options *extensions.WidgetOptions) {
	// Upstream RPC mode forwards only line content; component factories need a TUI.
	if widget != nil && widget.Factory != nil {
		return
	}
	var lines []string
	if widget != nil {
		lines = widget.Lines
	}
	placement := ""
	if options != nil {
		placement = string(options.Placement)
	}
	_ = adapter.ui.SetWidget(key, lines, placement)
}

func (adapter extensionUIAdapter) SetTitle(title string) {
	_ = adapter.ui.SetTitle(title)
}

func (adapter extensionUIAdapter) PasteToEditor(text string) {
	// Upstream RPC mode falls back to setEditorText for paste requests.
	adapter.SetEditorText(text)
}

func (adapter extensionUIAdapter) SetEditorText(text string) {
	_ = adapter.ui.SetEditorText(text)
}

var _ extensions.UI = extensionUIAdapter{}

func newRequestID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:]), nil
}
