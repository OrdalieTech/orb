package tui

// Compatibility overlay surface exercised only by tests; production overlays
// go through showOverlay/OverlayHandle directly.

// Overlay is the compatibility handle used by the Go extension UI adapter.
type Overlay struct{ handle OverlayHandle }

func (overlay *Overlay) Hide()                 { overlay.handle.SetHidden(true) }
func (overlay *Overlay) SetHidden(hidden bool) { overlay.handle.SetHidden(hidden) }
func (overlay *Overlay) IsHidden() bool        { return overlay.handle.IsHidden() }
func (overlay *Overlay) Remove()               { overlay.handle.Hide() }
func (overlay *Overlay) Focus()                { overlay.handle.Focus() }
func (overlay *Overlay) Unfocus(options ...OverlayUnfocusOptions) {
	overlay.handle.Unfocus(options...)
}
func (overlay *Overlay) IsFocused() bool { return overlay.handle.IsFocused() }

// AddOverlay adapts the Go extension UI's dynamic layout callback onto the
// upstream overlay stack without changing its existing lifecycle contract.
func (ui *TUI) AddOverlay(component Component, layout func(width, height int) OverlayLayout) *Overlay {
	resolve := func(width, height int) *OverlayOptions {
		resolved := OverlayLayout{Anchor: string(OverlayCenter)}
		if layout != nil {
			resolved = layout(width, height)
		}
		options := &OverlayOptions{
			MinWidth:     resolved.MinWidth,
			Anchor:       OverlayAnchor(resolved.Anchor),
			OffsetX:      resolved.OffsetX,
			OffsetY:      resolved.OffsetY,
			Margin:       resolved.Margin,
			Visible:      resolved.Visible,
			NonCapturing: resolved.NonCapturing,
		}
		if resolved.Width != 0 {
			options.Width = AbsoluteSize(resolved.Width)
		}
		if resolved.MaxHeight != 0 {
			options.MaxHeight = AbsoluteSize(resolved.MaxHeight)
		}
		if resolved.Row != nil {
			options.Row = AbsoluteSize(*resolved.Row)
		}
		if resolved.Column != nil {
			options.Col = AbsoluteSize(*resolved.Column)
		}
		return options
	}
	return &Overlay{handle: ui.showOverlay(component, nil, resolve, false)}
}

func (ui *TUI) renderWithOverlays(width, height int) []string {
	return ui.compositeOverlays(append([]string(nil), ui.Render(width)...), width, height)
}
