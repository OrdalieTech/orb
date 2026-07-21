package modes

import (
	"context"
	"errors"
	"testing"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

// Sweep regression (modes-ext): headless ctx.ui must mirror upstream
// noOpUIContext (runner.ts:232-263) exactly: custom/select/input/editor
// resolve undefined (Go: zero value with ok=false and NO error), confirm
// resolves false, getEditorText returns "", getEditorComponent and getTheme
// return nothing, setTheme reports {success:false, error:"UI not available"},
// getAllThemes returns [], getToolsExpanded returns false. A returned error
// would surface to extensions as a thrown error - print-mode tools would then
// produce an isError tool result where upstream sees a plain resolution.
func TestNoopUIMirrorsUpstreamNoOpUIContext(t *testing.T) {
	ui := extensions.NewNoopUI()
	ctx := context.Background()

	if value, ok, err := ui.Select(ctx, "title", []string{"a", "b"}, nil); value != "" || ok || err != nil {
		t.Fatalf("Select = (%q, %v, %v), want undefined resolution (\"\", false, nil)", value, ok, err)
	}
	if confirmed, err := ui.Confirm(ctx, "title", "message", nil); confirmed || err != nil {
		t.Fatalf("Confirm = (%v, %v), want (false, nil)", confirmed, err)
	}
	if value, ok, err := ui.Input(ctx, "title", nil, nil); value != "" || ok || err != nil {
		t.Fatalf("Input = (%q, %v, %v), want undefined resolution (\"\", false, nil)", value, ok, err)
	}
	if value, ok, err := ui.Editor(ctx, "title", nil); value != "" || ok || err != nil {
		t.Fatalf("Editor = (%q, %v, %v), want undefined resolution (\"\", false, nil)", value, ok, err)
	}
	factoryRan := false
	value, ok, err := ui.Custom(ctx, func(extensions.UIHost, extensions.Theme, extensions.Keybindings, extensions.CustomDone) (extensions.Component, error) {
		factoryRan = true
		return nil, errors.New("factory must not run")
	}, nil)
	if value != nil || ok || err != nil {
		t.Fatalf("Custom = (%v, %v, %v), want undefined resolution (nil, false, nil)", value, ok, err)
	}
	if factoryRan {
		t.Fatal("Custom ran the component factory, upstream noOpUIContext never invokes it")
	}
	if text := ui.GetEditorText(); text != "" {
		t.Fatalf("GetEditorText = %q, want \"\"", text)
	}
	if factory := ui.GetEditorComponent(); factory != nil {
		t.Fatalf("GetEditorComponent = %v, want nil", factory)
	}
	if themeValue := ui.GetTheme("dark"); themeValue != nil {
		t.Fatalf("GetTheme = %v, want nil", themeValue)
	}
	if result := ui.SetTheme("dark"); result.Success || result.Error != "UI not available" {
		t.Fatalf("SetTheme = %+v, want {Success:false Error:%q}", result, "UI not available")
	}
	if themes := ui.GetAllThemes(); themes == nil || len(themes) != 0 {
		t.Fatalf("GetAllThemes = %v, want an empty slice", themes)
	}
	if expanded := ui.GetToolsExpanded(); expanded {
		t.Fatal("GetToolsExpanded = true, want false")
	}
	if unsubscribe := ui.OnTerminalInput(func(string) *extensions.TerminalInputResult { return nil }); unsubscribe == nil {
		t.Fatal("OnTerminalInput returned nil unsubscribe, want a no-op func")
	}
}
