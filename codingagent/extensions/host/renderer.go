package host

import (
	"context"
	"encoding/json"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
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
		return manager.createRendererComponent(extensionID, rendererMessage, customType, message, options.Expanded, theme)
	}
}

func (manager *Manager) entryRenderer(extensionID, customType string) extensions.EntryRenderer {
	return func(entry any, options extensions.EntryRenderOptions, theme extensions.Theme) extensions.Component {
		return manager.createRendererComponent(extensionID, rendererEntry, customType, entry, options.Expanded, theme)
	}
}

func (manager *Manager) createRendererComponent(extensionID, kind, customType string, value any, expanded bool, theme extensions.Theme) extensions.Component {
	manager.mu.Lock()
	generation := manager.current
	manager.mu.Unlock()
	if generation == nil || !generation.ready.Load() {
		return nil
	}
	ctx, cancel := manager.timeoutContext(context.Background())
	defer cancel()
	raw, err := generation.request(ctx, "create_registered_renderer_component", struct {
		ExtensionID string     `json:"extensionId"`
		Kind        string     `json:"kind"`
		CustomType  string     `json:"customType"`
		Value       any        `json:"value"`
		Expanded    bool       `json:"expanded"`
		Theme       *wireTheme `json:"theme,omitempty"`
	}{extensionID, kind, customType, value, expanded, snapshotTheme(theme)}, nil)
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
	ctx, cancel := component.generation.manager.timeoutContext(context.Background())
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
	ctx, cancel := component.generation.manager.timeoutContext(context.Background())
	defer cancel()
	_, _ = component.generation.request(ctx, "dispose_registered_renderer_component", struct {
		Handle string `json:"handle"`
	}{component.handle}, nil)
}
