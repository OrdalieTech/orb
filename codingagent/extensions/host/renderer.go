package host

import (
	"context"
	"encoding/json"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/codingagent/extensions"
)

const (
	rendererMessage = "message"
	rendererEntry   = "entry"
)

type wireRendererRegistration struct {
	Kind       string
	CustomType string
}

type wireRendererComponent struct {
	generation *generation
	handle     string
}

func (manager *Manager) messageRenderer(extensionID, customType string) extensions.MessageRenderer {
	return func(message extensions.CustomMessage, options extensions.MessageRenderOptions, theme extensions.Theme) extensions.Component {
		// Only message renderers receive outputPad (518855dd); entry
		// renderer options stay {expanded}.
		outputPad := options.OutputPad
		return manager.createRendererComponent(extensionID, rendererMessage, customType, message, options.Expanded, &outputPad, theme)
	}
}

func (manager *Manager) entryRenderer(extensionID, customType string) extensions.EntryRenderer {
	return func(entry any, options extensions.EntryRenderOptions, theme extensions.Theme) extensions.Component {
		return manager.createRendererComponent(extensionID, rendererEntry, customType, entry, options.Expanded, nil, theme)
	}
}

func (manager *Manager) createRendererComponent(extensionID, kind, customType string, value any, expanded bool, outputPad *int, theme extensions.Theme) extensions.Component {
	manager.mu.Lock()
	generation := manager.current
	manager.mu.Unlock()
	if generation == nil || !generation.ready.Load() {
		return nil
	}
	ctx, cancel := callbackContext(context.Background())
	defer cancel()
	raw, err := generation.request(ctx, "create_registered_renderer_component", struct {
		ExtensionID string     `json:"extensionId"`
		Kind        string     `json:"kind"`
		CustomType  string     `json:"customType"`
		Value       any        `json:"value"`
		Expanded    bool       `json:"expanded"`
		OutputPad   *int       `json:"outputPad,omitempty"`
		Theme       *wireTheme `json:"theme,omitempty"`
	}{extensionID, kind, customType, value, expanded, outputPad, snapshotTheme(theme)}, nil)
	if err != nil {
		manager.report(extensions.Diagnostic{Type: "error", Message: err.Error(), Path: extensionID})
		return nil
	}
	var response struct {
		Present bool   `json:"present"`
		Handle  string `json:"handle"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.Present || response.Handle == "" {
		return nil
	}
	return &wireRendererComponent{generation: generation, handle: response.Handle}
}

// toolPromptGuidelines re-reads a registered tool's current promptGuidelines
// from the host process (lazy `get promptGuidelines()` upstream contract).
func (manager *Manager) toolPromptGuidelines(ctx context.Context, extensionID, toolName string) ([]string, error) {
	manager.mu.Lock()
	generation := manager.current
	manager.mu.Unlock()
	if generation == nil || !generation.ready.Load() {
		return nil, ErrNotRunning
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Bounded: this runs on the prompt path, so a wedged host process must
	// degrade to the registration snapshot instead of stalling the turn.
	callbackCtx, cancel := context.WithTimeout(ctx, manager.options.RequestTimeout)
	defer cancel()
	raw, err := generation.request(callbackCtx, "get_tool_prompt_guidelines", struct {
		ExtensionID string `json:"extensionId"`
		ToolName    string `json:"toolName"`
	}{extensionID, toolName}, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Guidelines []string `json:"guidelines"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	return response.Guidelines, nil
}

// wireToolRenderContext is the serializable subset of ToolRenderContext that
// crosses to the host process; live members (Invalidate, LastComponent,
// State) stay Go-side and the JS context carries inert stand-ins.
type wireToolRenderContext struct {
	ToolCallID       string `json:"toolCallId,omitempty"`
	CWD              string `json:"cwd,omitempty"`
	ExecutionStarted bool   `json:"executionStarted,omitempty"`
	ArgsComplete     bool   `json:"argsComplete,omitempty"`
	IsPartial        bool   `json:"isPartial,omitempty"`
	Expanded         bool   `json:"expanded,omitempty"`
	ShowImages       bool   `json:"showImages,omitempty"`
	IsError          bool   `json:"isError,omitempty"`
	Args             any    `json:"args,omitempty"`
}

func newWireToolRenderContext(context extensions.ToolRenderContext) wireToolRenderContext {
	return wireToolRenderContext{
		ToolCallID:       context.ToolCallID,
		CWD:              context.CWD,
		ExecutionStarted: context.ExecutionStarted,
		ArgsComplete:     context.ArgsComplete,
		IsPartial:        context.IsPartial,
		Expanded:         context.Expanded,
		ShowImages:       context.ShowImages,
		IsError:          context.IsError,
		Args:             context.Args,
	}
}

// toolRenderCall bridges a registered tool's live renderCall function: the
// component is created host-side and rendered/disposed through the existing
// renderer-component RPC.
func (manager *Manager) toolRenderCall(extensionID, toolName string) func(any, extensions.Theme, extensions.ToolRenderContext) extensions.Component {
	return func(args any, theme extensions.Theme, renderContext extensions.ToolRenderContext) extensions.Component {
		return manager.createToolRenderComponent(extensionID, toolName, "call", args, nil, theme, renderContext)
	}
}

// toolRenderResult bridges a registered tool's live renderResult function.
func (manager *Manager) toolRenderResult(extensionID, toolName string) func(agent.AgentToolResult, extensions.ToolRenderResultOptions, extensions.Theme, extensions.ToolRenderContext) extensions.Component {
	return func(result agent.AgentToolResult, options extensions.ToolRenderResultOptions, theme extensions.Theme, renderContext extensions.ToolRenderContext) extensions.Component {
		return manager.createToolRenderComponent(extensionID, toolName, "result", result, &options, theme, renderContext)
	}
}

func (manager *Manager) createToolRenderComponent(
	extensionID, toolName, kind string,
	value any,
	options *extensions.ToolRenderResultOptions,
	theme extensions.Theme,
	renderContext extensions.ToolRenderContext,
) extensions.Component {
	manager.mu.Lock()
	generation := manager.current
	manager.mu.Unlock()
	if generation == nil || !generation.ready.Load() {
		return nil
	}
	ctx, cancel := callbackContext(context.Background())
	defer cancel()
	request := struct {
		ExtensionID string                `json:"extensionId"`
		ToolName    string                `json:"toolName"`
		Kind        string                `json:"kind"`
		Value       any                   `json:"value"`
		Expanded    bool                  `json:"expanded"`
		IsPartial   bool                  `json:"isPartial"`
		Theme       *wireTheme            `json:"theme,omitempty"`
		Context     wireToolRenderContext `json:"context"`
	}{
		ExtensionID: extensionID, ToolName: toolName, Kind: kind, Value: value,
		Theme: snapshotTheme(theme), Context: newWireToolRenderContext(renderContext),
	}
	if options != nil {
		request.Expanded = options.Expanded
		request.IsPartial = options.IsPartial
	}
	raw, err := generation.request(ctx, "create_tool_render_component", request, nil)
	if err != nil {
		manager.report(extensions.Diagnostic{Type: "error", Message: err.Error(), Path: extensionID})
		return nil
	}
	var response struct {
		Present bool   `json:"present"`
		Handle  string `json:"handle"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.Present || response.Handle == "" {
		return nil
	}
	return &wireRendererComponent{generation: generation, handle: response.Handle}
}

func (component *wireRendererComponent) Render(width int) []string {
	ctx, cancel := callbackContext(context.Background())
	defer cancel()
	raw, err := component.generation.request(ctx, "render_registered_renderer_component", struct {
		Handle string `json:"handle"`
		Width  int    `json:"width"`
	}{component.handle, width}, nil)
	if err != nil {
		return nil
	}
	var response struct {
		Lines []string `json:"lines"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil
	}
	return response.Lines
}

func (component *wireRendererComponent) Dispose() {
	ctx, cancel := callbackContext(context.Background())
	defer cancel()
	_, _ = component.generation.request(ctx, "dispose_registered_renderer_component", struct {
		Handle string `json:"handle"`
	}{component.handle}, nil)
}
