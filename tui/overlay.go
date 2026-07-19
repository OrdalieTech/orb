package tui

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// OverlayAnchor positions an overlay within the terminal's available area.
type OverlayAnchor string

const (
	OverlayCenter       OverlayAnchor = "center"
	OverlayTopLeft      OverlayAnchor = "top-left"
	OverlayTopRight     OverlayAnchor = "top-right"
	OverlayBottomLeft   OverlayAnchor = "bottom-left"
	OverlayBottomRight  OverlayAnchor = "bottom-right"
	OverlayTopCenter    OverlayAnchor = "top-center"
	OverlayBottomCenter OverlayAnchor = "bottom-center"
	OverlayLeftCenter   OverlayAnchor = "left-center"
	OverlayRightCenter  OverlayAnchor = "right-center"
)

// OverlayMargin reserves terminal cells around an overlay. Negative values
// are accepted here and clamped to zero during layout, as upstream does.
type OverlayMargin struct {
	Top    int
	Right  int
	Bottom int
	Left   int
}

// UniformOverlayMargin returns the number form of upstream's margin option.
func UniformOverlayMargin(value int) *OverlayMargin {
	return &OverlayMargin{Top: value, Right: value, Bottom: value, Left: value}
}

type sizeValueKind uint8

const (
	sizeValueUnset sizeValueKind = iota
	sizeValueAbsolute
	sizeValuePercent
)

// SizeValue is either an absolute terminal-cell value or a percentage.
// The zero value means that the corresponding overlay option is unset.
type SizeValue struct {
	kind  sizeValueKind
	value float64
}

// AbsoluteSize creates an absolute overlay size or position.
func AbsoluteSize(value int) SizeValue {
	return SizeValue{kind: sizeValueAbsolute, value: float64(value)}
}

// PercentSize creates a percentage overlay size or position, where 50 means 50%.
func PercentSize(value float64) SizeValue {
	return SizeValue{kind: sizeValuePercent, value: value}
}

// OverlayOptions controls overlay sizing, placement, visibility, and focus capture.
type OverlayOptions struct {
	Width        SizeValue
	MinWidth     int
	MaxHeight    SizeValue
	Anchor       OverlayAnchor
	OffsetX      int
	OffsetY      int
	Row          SizeValue
	Col          SizeValue
	Margin       *OverlayMargin
	Visible      func(termWidth, termHeight int) bool
	NonCapturing bool
}

// OverlayLayout preserves the existing Go extension adapter while the public
// TUI surface follows upstream's OverlayOptions. The callback is evaluated on
// every render so dynamic extension layouts remain responsive.
type OverlayLayout struct {
	Width, MinWidth, MaxHeight int
	Anchor                     string
	OffsetX, OffsetY           int
	Row, Column                *int
	Margin                     *OverlayMargin
	Visible                    func(width, height int) bool
	NonCapturing               bool
}

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

// OverlayUnfocusOptions selects an explicit focus target, including nil.
type OverlayUnfocusOptions struct {
	Target Component
}

// OverlayHandle controls one overlay after it has been shown.
type OverlayHandle interface {
	Hide()
	SetHidden(bool)
	IsHidden() bool
	Focus()
	Unfocus(...OverlayUnfocusOptions)
	IsFocused() bool
}

type overlayStackEntry struct {
	component      Component
	options        *OverlayOptions
	resolveOptions func(width, height int) *OverlayOptions
	preFocus       Component
	hidden         bool
	focusOrder     uint64
}

type overlayFocusRestoreStatus uint8

const (
	overlayFocusRestoreInactive overlayFocusRestoreStatus = iota
	overlayFocusRestoreEligible
	overlayFocusRestoreBlocked
)

type overlayFocusResumeKind uint8

const (
	overlayFocusResumeOverlay overlayFocusResumeKind = iota
	overlayFocusResumeTarget
)

type overlayBlockedFocusResume struct {
	kind   overlayFocusResumeKind
	target Component
}

type overlayFocusRestoreState struct {
	status    overlayFocusRestoreStatus
	overlay   *overlayStackEntry
	blockedBy Component
	resume    overlayBlockedFocusResume
}

type overlayFocusRestorePolicy uint8

const (
	overlayFocusRestoreClear overlayFocusRestorePolicy = iota
	overlayFocusRestorePreserve
)

type overlayHandle struct {
	ui    *TUI
	entry *overlayStackEntry
}

func (ui *TUI) SetFocus(component Component) {
	ui.focusMu.Lock()
	ui.setFocusLocked(component, overlayFocusRestoreClear)
	ui.focusMu.Unlock()
}

func (ui *TUI) setFocusLocked(component Component, policy overlayFocusRestorePolicy) {
	previousFocus := ui.focused
	nextFocus := component
	previousFocusedOverlay := ui.visibleOverlayForComponentLocked(previousFocus)
	nextFocusIsOverlay := ui.overlayForComponentLocked(nextFocus) != nil
	restoreState := ui.visibleOverlayFocusRestoreLocked()
	if nextFocus != nil && !nextFocusIsOverlay {
		if restoreState.status == overlayFocusRestoreBlocked && restoreState.blockedBy == previousFocus {
			if restoreState.resume.kind == overlayFocusResumeTarget || !ui.isComponentMountedLocked(restoreState.blockedBy) {
				nextFocus = ui.resolveBlockedOverlayFocusResumeLocked(restoreState)
			} else {
				ui.overlayFocusRestore = overlayFocusRestoreState{
					status:    overlayFocusRestoreBlocked,
					overlay:   restoreState.overlay,
					blockedBy: nextFocus,
					resume:    restoreState.resume,
				}
			}
		} else if previousFocusedOverlay != nil &&
			restoreState.status != overlayFocusRestoreInactive &&
			restoreState.overlay == previousFocusedOverlay &&
			!ui.isOverlayFocusAncestorLocked(previousFocusedOverlay, nextFocus) {
			ui.overlayFocusRestore = overlayFocusRestoreState{
				status:    overlayFocusRestoreBlocked,
				overlay:   previousFocusedOverlay,
				blockedBy: nextFocus,
				resume:    overlayBlockedFocusResume{kind: overlayFocusResumeOverlay},
			}
		}
	} else if nextFocus == nil {
		if restoreState.status == overlayFocusRestoreBlocked && restoreState.blockedBy == previousFocus {
			nextFocus = ui.resolveBlockedOverlayFocusResumeLocked(restoreState)
		} else if policy == overlayFocusRestoreClear {
			ui.clearOverlayFocusRestoreLocked()
		}
	}

	if previous, ok := ui.focused.(Focusable); ok {
		previous.SetFocused(false)
	}
	ui.focused = nextFocus
	if next, ok := nextFocus.(Focusable); ok {
		next.SetFocused(true)
	}
	if focusedOverlay := ui.visibleOverlayForComponentLocked(nextFocus); focusedOverlay != nil {
		ui.overlayFocusRestore = overlayFocusRestoreState{status: overlayFocusRestoreEligible, overlay: focusedOverlay}
	}
}

func (ui *TUI) clearOverlayFocusRestoreLocked() {
	ui.overlayFocusRestore = overlayFocusRestoreState{status: overlayFocusRestoreInactive}
}

func (ui *TUI) clearOverlayFocusRestoreForLocked(overlay *overlayStackEntry) {
	if ui.overlayFocusRestore.status != overlayFocusRestoreInactive && ui.overlayFocusRestore.overlay == overlay {
		ui.clearOverlayFocusRestoreLocked()
	}
}

func (ui *TUI) resolveBlockedOverlayFocusResumeLocked(state overlayFocusRestoreState) Component {
	if state.resume.kind == overlayFocusResumeOverlay {
		return state.overlay.component
	}
	ui.clearOverlayFocusRestoreLocked()
	return state.resume.target
}

func (ui *TUI) visibleOverlayFocusRestoreLocked() overlayFocusRestoreState {
	state := ui.overlayFocusRestore
	if state.status == overlayFocusRestoreInactive {
		return state
	}
	if !ui.hasOverlayEntryLocked(state.overlay) || !ui.isOverlayVisibleLocked(state.overlay) {
		return overlayFocusRestoreState{status: overlayFocusRestoreInactive}
	}
	return state
}

func (ui *TUI) isOverlayFocusAncestorLocked(entry *overlayStackEntry, component Component) bool {
	visited := make(map[Component]struct{})
	current := entry.preFocus
	for current != nil {
		if _, exists := visited[current]; exists {
			break
		}
		visited[current] = struct{}{}
		if current == component {
			return true
		}
		parent := ui.overlayForComponentLocked(current)
		if parent == nil {
			break
		}
		current = parent.preFocus
	}
	return false
}

func (ui *TUI) retargetOverlayPreFocusLocked(removed *overlayStackEntry) {
	for _, overlay := range ui.overlayStack {
		if overlay != removed && overlay.preFocus == removed.component {
			overlay.preFocus = removed.preFocus
		}
	}
}

func (ui *TUI) isComponentMountedLocked(component Component) bool {
	for _, child := range ui.childrenSnapshot() {
		if containsComponent(child, component) {
			return true
		}
	}
	return false
}

func containsComponent(root, target Component) bool {
	if root == target {
		return true
	}
	container, ok := root.(*Container)
	if !ok {
		return false
	}
	for _, child := range container.childrenSnapshot() {
		if containsComponent(child, target) {
			return true
		}
	}
	return false
}

// ShowOverlay adds an overlay and returns a permanent control handle. Omitting
// options applies upstream's centered, width-at-most-80 defaults.
func (ui *TUI) ShowOverlay(component Component, options ...OverlayOptions) OverlayHandle {
	var optionCopy *OverlayOptions
	if len(options) > 0 {
		copy := options[0]
		optionCopy = &copy
	}
	return ui.showOverlay(component, optionCopy, nil, true)
}

func (ui *TUI) showOverlay(component Component, options *OverlayOptions, resolveOptions func(width, height int) *OverlayOptions, autoFocus bool) *overlayHandle {
	ui.focusMu.Lock()
	ui.focusOrderCounter++
	entry := &overlayStackEntry{
		component:      component,
		options:        options,
		resolveOptions: resolveOptions,
		preFocus:       ui.focused,
		focusOrder:     ui.focusOrderCounter,
	}
	ui.overlayStack = append(ui.overlayStack, entry)
	resolved := ui.overlayOptionsLocked(entry)
	if autoFocus && (resolved == nil || !resolved.NonCapturing) && ui.isOverlayVisibleWithOptionsLocked(entry, resolved) {
		ui.setFocusLocked(component, overlayFocusRestoreClear)
	}
	ui.focusMu.Unlock()
	ui.terminal.HideCursor()
	ui.RequestRender()
	return &overlayHandle{ui: ui, entry: entry}
}

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

func (handle *overlayHandle) Hide() {
	ui, entry := handle.ui, handle.entry
	ui.focusMu.Lock()
	index := ui.overlayIndexLocked(entry)
	if index < 0 {
		ui.focusMu.Unlock()
		return
	}
	ui.clearOverlayFocusRestoreForLocked(entry)
	ui.retargetOverlayPreFocusLocked(entry)
	ui.overlayStack = append(ui.overlayStack[:index], ui.overlayStack[index+1:]...)
	if ui.focused == entry.component {
		top := ui.topmostVisibleOverlayLocked()
		if top != nil {
			ui.setFocusLocked(top.component, overlayFocusRestoreClear)
		} else {
			ui.setFocusLocked(entry.preFocus, overlayFocusRestoreClear)
		}
	}
	empty := len(ui.overlayStack) == 0
	ui.focusMu.Unlock()
	if empty {
		ui.terminal.HideCursor()
	}
	ui.RequestRender()
}

func (handle *overlayHandle) SetHidden(hidden bool) {
	ui, entry := handle.ui, handle.entry
	ui.focusMu.Lock()
	if entry.hidden == hidden {
		ui.focusMu.Unlock()
		return
	}
	entry.hidden = hidden
	if hidden {
		ui.clearOverlayFocusRestoreForLocked(entry)
		if ui.focused == entry.component {
			top := ui.topmostVisibleOverlayLocked()
			if top != nil {
				ui.setFocusLocked(top.component, overlayFocusRestoreClear)
			} else {
				ui.setFocusLocked(entry.preFocus, overlayFocusRestoreClear)
			}
		}
	} else if options := ui.overlayOptionsLocked(entry); (options == nil || !options.NonCapturing) && ui.isOverlayVisibleWithOptionsLocked(entry, options) {
		ui.focusOrderCounter++
		entry.focusOrder = ui.focusOrderCounter
		ui.setFocusLocked(entry.component, overlayFocusRestoreClear)
	}
	ui.focusMu.Unlock()
	ui.RequestRender()
}

func (handle *overlayHandle) IsHidden() bool {
	handle.ui.focusMu.RLock()
	defer handle.ui.focusMu.RUnlock()
	return handle.entry.hidden
}

func (handle *overlayHandle) Focus() {
	ui, entry := handle.ui, handle.entry
	ui.focusMu.Lock()
	if !ui.hasOverlayEntryLocked(entry) || !ui.isOverlayVisibleLocked(entry) {
		ui.focusMu.Unlock()
		return
	}
	ui.focusOrderCounter++
	entry.focusOrder = ui.focusOrderCounter
	ui.setFocusLocked(entry.component, overlayFocusRestoreClear)
	ui.focusMu.Unlock()
	ui.RequestRender()
}

func (handle *overlayHandle) Unfocus(options ...OverlayUnfocusOptions) {
	ui, entry := handle.ui, handle.entry
	hasOptions := len(options) > 0
	var target Component
	if hasOptions {
		target = options[0].Target
	}
	ui.focusMu.Lock()
	isFocused := ui.focused == entry.component
	restoreState := ui.overlayFocusRestore
	hasPendingRestore := restoreState.status != overlayFocusRestoreInactive && restoreState.overlay == entry
	if !isFocused && !hasPendingRestore {
		ui.focusMu.Unlock()
		return
	}
	if restoreState.status == overlayFocusRestoreBlocked && restoreState.overlay == entry && ui.focused == restoreState.blockedBy {
		if hasOptions {
			ui.overlayFocusRestore = overlayFocusRestoreState{
				status:    overlayFocusRestoreBlocked,
				overlay:   entry,
				blockedBy: restoreState.blockedBy,
				resume:    overlayBlockedFocusResume{kind: overlayFocusResumeTarget, target: target},
			}
		} else {
			ui.clearOverlayFocusRestoreLocked()
		}
		ui.focusMu.Unlock()
		ui.RequestRender()
		return
	}
	ui.clearOverlayFocusRestoreForLocked(entry)
	if isFocused || hasOptions {
		top := ui.topmostVisibleOverlayLocked()
		fallback := entry.preFocus
		if top != nil && top != entry {
			fallback = top.component
		}
		if hasOptions {
			ui.setFocusLocked(target, overlayFocusRestoreClear)
		} else {
			ui.setFocusLocked(fallback, overlayFocusRestoreClear)
		}
	}
	ui.focusMu.Unlock()
	ui.RequestRender()
}

func (handle *overlayHandle) IsFocused() bool {
	handle.ui.focusMu.RLock()
	defer handle.ui.focusMu.RUnlock()
	return handle.ui.focused == handle.entry.component
}

// HideOverlay removes the most recently created overlay, independent of focus order.
func (ui *TUI) HideOverlay() {
	ui.focusMu.Lock()
	if len(ui.overlayStack) == 0 {
		ui.focusMu.Unlock()
		return
	}
	overlay := ui.overlayStack[len(ui.overlayStack)-1]
	ui.clearOverlayFocusRestoreForLocked(overlay)
	ui.retargetOverlayPreFocusLocked(overlay)
	ui.overlayStack = ui.overlayStack[:len(ui.overlayStack)-1]
	if ui.focused == overlay.component {
		top := ui.topmostVisibleOverlayLocked()
		if top != nil {
			ui.setFocusLocked(top.component, overlayFocusRestoreClear)
		} else {
			ui.setFocusLocked(overlay.preFocus, overlayFocusRestoreClear)
		}
	}
	empty := len(ui.overlayStack) == 0
	ui.focusMu.Unlock()
	if empty {
		ui.terminal.HideCursor()
	}
	ui.RequestRender()
}

// HasOverlay reports whether at least one overlay is currently visible.
func (ui *TUI) HasOverlay() bool {
	ui.focusMu.RLock()
	defer ui.focusMu.RUnlock()
	for _, overlay := range ui.overlayStack {
		if ui.isOverlayVisibleLocked(overlay) {
			return true
		}
	}
	return false
}

func (ui *TUI) overlayIndexLocked(entry *overlayStackEntry) int {
	for index, overlay := range ui.overlayStack {
		if overlay == entry {
			return index
		}
	}
	return -1
}

func (ui *TUI) hasOverlayEntryLocked(entry *overlayStackEntry) bool {
	return ui.overlayIndexLocked(entry) >= 0
}

func (ui *TUI) overlayForComponentLocked(component Component) *overlayStackEntry {
	if component == nil {
		return nil
	}
	for _, overlay := range ui.overlayStack {
		if overlay.component == component {
			return overlay
		}
	}
	return nil
}

func (ui *TUI) visibleOverlayForComponentLocked(component Component) *overlayStackEntry {
	overlay := ui.overlayForComponentLocked(component)
	if overlay != nil && ui.isOverlayVisibleLocked(overlay) {
		return overlay
	}
	return nil
}

func (ui *TUI) isOverlayVisibleLocked(entry *overlayStackEntry) bool {
	return ui.isOverlayVisibleWithOptionsLocked(entry, ui.overlayOptionsLocked(entry))
}

func (ui *TUI) isOverlayVisibleWithOptionsLocked(entry *overlayStackEntry, options *OverlayOptions) bool {
	if entry.hidden {
		return false
	}
	if options != nil && options.Visible != nil {
		return options.Visible(ui.terminal.Columns(), ui.terminal.Rows())
	}
	return true
}

func (ui *TUI) overlayOptionsLocked(entry *overlayStackEntry) *OverlayOptions {
	if entry.resolveOptions != nil {
		return entry.resolveOptions(ui.terminal.Columns(), ui.terminal.Rows())
	}
	return entry.options
}

func (ui *TUI) topmostVisibleOverlayLocked() *overlayStackEntry {
	var topmost *overlayStackEntry
	for _, overlay := range ui.overlayStack {
		options := ui.overlayOptionsLocked(overlay)
		if options != nil && options.NonCapturing || !ui.isOverlayVisibleWithOptionsLocked(overlay, options) {
			continue
		}
		if topmost == nil || overlay.focusOrder > topmost.focusOrder {
			topmost = overlay
		}
	}
	return topmost
}

func (ui *TUI) overlayCount() int {
	ui.focusMu.RLock()
	defer ui.focusMu.RUnlock()
	return len(ui.overlayStack)
}

type overlayLayout struct {
	width, row, col int
	maxHeight       int
	hasMaxHeight    bool
}

func parseSizeValue(value SizeValue, referenceSize int) (int, bool) {
	switch value.kind {
	case sizeValueAbsolute:
		return int(value.value), true
	case sizeValuePercent:
		if value.value < 0 || math.IsNaN(value.value) || math.IsInf(value.value, 0) {
			return 0, false
		}
		return int(math.Floor(float64(referenceSize) * value.value / 100)), true
	default:
		return 0, false
	}
}

func resolveAnchorRow(anchor OverlayAnchor, height, availableHeight, marginTop int) int {
	switch anchor {
	case OverlayTopLeft, OverlayTopCenter, OverlayTopRight:
		return marginTop
	case OverlayBottomLeft, OverlayBottomCenter, OverlayBottomRight:
		return marginTop + availableHeight - height
	default:
		return marginTop + int(math.Floor(float64(availableHeight-height)/2))
	}
}

func resolveAnchorCol(anchor OverlayAnchor, width, availableWidth, marginLeft int) int {
	switch anchor {
	case OverlayTopLeft, OverlayLeftCenter, OverlayBottomLeft:
		return marginLeft
	case OverlayTopRight, OverlayRightCenter, OverlayBottomRight:
		return marginLeft + availableWidth - width
	default:
		return marginLeft + int(math.Floor(float64(availableWidth-width)/2))
	}
}

func resolveOverlayLayout(options *OverlayOptions, overlayHeight, termWidth, termHeight int) overlayLayout {
	var opt OverlayOptions
	if options != nil {
		opt = *options
	}
	margin := OverlayMargin{}
	if opt.Margin != nil {
		margin = *opt.Margin
	}
	marginTop := max(0, margin.Top)
	marginRight := max(0, margin.Right)
	marginBottom := max(0, margin.Bottom)
	marginLeft := max(0, margin.Left)
	availableWidth := max(1, termWidth-marginLeft-marginRight)
	availableHeight := max(1, termHeight-marginTop-marginBottom)

	width, hasWidth := parseSizeValue(opt.Width, termWidth)
	if !hasWidth {
		width = min(80, availableWidth)
	}
	if opt.MinWidth != 0 {
		width = max(width, opt.MinWidth)
	}
	width = max(1, min(width, availableWidth))

	maxHeight, hasMaxHeight := parseSizeValue(opt.MaxHeight, termHeight)
	if hasMaxHeight {
		maxHeight = max(1, min(maxHeight, availableHeight))
	}
	effectiveHeight := overlayHeight
	if hasMaxHeight {
		effectiveHeight = min(effectiveHeight, maxHeight)
	}

	anchor := opt.Anchor
	if anchor == "" {
		anchor = OverlayCenter
	}
	row, hasRow := parseSizeValue(opt.Row, termHeight)
	if opt.Row.kind == sizeValuePercent {
		if hasRow {
			maxRow := max(0, availableHeight-effectiveHeight)
			row = marginTop + int(math.Floor(float64(maxRow)*opt.Row.value/100))
		} else {
			row = resolveAnchorRow(OverlayCenter, effectiveHeight, availableHeight, marginTop)
		}
	} else if !hasRow {
		row = resolveAnchorRow(anchor, effectiveHeight, availableHeight, marginTop)
	}
	col, hasCol := parseSizeValue(opt.Col, termWidth)
	if opt.Col.kind == sizeValuePercent {
		if hasCol {
			maxCol := max(0, availableWidth-width)
			col = marginLeft + int(math.Floor(float64(maxCol)*opt.Col.value/100))
		} else {
			col = resolveAnchorCol(OverlayCenter, width, availableWidth, marginLeft)
		}
	} else if !hasCol {
		col = resolveAnchorCol(anchor, width, availableWidth, marginLeft)
	}
	row += opt.OffsetY
	col += opt.OffsetX
	row = max(marginTop, min(row, termHeight-marginBottom-effectiveHeight))
	col = max(marginLeft, min(col, termWidth-marginRight-width))
	return overlayLayout{width: width, row: row, col: col, maxHeight: maxHeight, hasMaxHeight: hasMaxHeight}
}

type overlayRenderEntry struct {
	component  Component
	options    *OverlayOptions
	focusOrder uint64
}

func (ui *TUI) compositeOverlays(lines []string, termWidth, termHeight int) []string {
	ui.focusMu.RLock()
	entries := make([]overlayRenderEntry, 0, len(ui.overlayStack))
	for _, entry := range ui.overlayStack {
		var options *OverlayOptions
		if resolved := ui.overlayOptionsLocked(entry); resolved != nil {
			copy := *resolved
			options = &copy
		}
		if !ui.isOverlayVisibleWithOptionsLocked(entry, options) {
			continue
		}
		entries = append(entries, overlayRenderEntry{component: entry.component, options: options, focusOrder: entry.focusOrder})
	}
	ui.focusMu.RUnlock()
	sort.SliceStable(entries, func(left, right int) bool { return entries[left].focusOrder < entries[right].focusOrder })
	type renderedOverlay struct {
		lines         []string
		row, col, wid int
	}
	result := append([]string(nil), lines...)
	rendered := make([]renderedOverlay, 0, len(entries))
	minimumLines := len(result)
	for _, entry := range entries {
		initial := resolveOverlayLayout(entry.options, 0, termWidth, termHeight)
		overlayLines := entry.component.Render(initial.width)
		if initial.hasMaxHeight && len(overlayLines) > initial.maxHeight {
			overlayLines = overlayLines[:initial.maxHeight]
		}
		layout := resolveOverlayLayout(entry.options, len(overlayLines), termWidth, termHeight)
		rendered = append(rendered, renderedOverlay{lines: overlayLines, row: layout.row, col: layout.col, wid: layout.width})
		minimumLines = max(minimumLines, layout.row+len(overlayLines))
	}
	workingHeight := max(len(result), termHeight, minimumLines)
	for len(result) < workingHeight {
		result = append(result, "")
	}
	viewportStart := max(0, workingHeight-termHeight)
	for _, overlay := range rendered {
		for lineIndex, line := range overlay.lines {
			index := viewportStart + overlay.row + lineIndex
			if index < 0 || index >= len(result) {
				continue
			}
			if VisibleWidth(line) > overlay.wid {
				line = SliceByColumn(line, 0, overlay.wid, true)
			}
			result[index] = compositeLineAt(result[index], line, overlay.col, overlay.wid, termWidth)
		}
	}
	return result
}

func (ui *TUI) renderWithOverlays(width, height int) []string {
	return ui.compositeOverlays(append([]string(nil), ui.Render(width)...), width, height)
}

// LineSegments is the before/after result used by overlay composition.
type LineSegments struct {
	Before      string
	BeforeWidth int
	After       string
	AfterWidth  int
}

// ExtractSegments extracts styled visible columns on each side of an overlay.
func ExtractSegments(line string, beforeEnd, afterStart, afterLength int, strictAfter bool) LineSegments {
	var result LineSegments
	currentColumn := 0
	pendingBefore := ""
	afterStarted := false
	afterEnd := afterStart + afterLength
	tracker := &ansiTracker{}
	for position := 0; position < len(line); {
		if code, next, ok := extractANSI(line, position); ok {
			tracker.process(code)
			if currentColumn < beforeEnd {
				pendingBefore += code
			} else if currentColumn >= afterStart && currentColumn < afterEnd && afterStarted {
				result.After += code
			}
			position = next
			continue
		}
		textEnd := position
		for textEnd < len(line) {
			if _, _, ok := extractANSI(line, textEnd); ok {
				break
			}
			_, size := utf8.DecodeRuneInString(line[textEnd:])
			textEnd += size
		}
		forEachGrapheme(line[position:textEnd], func(grapheme string) bool {
			width := graphemeWidth(grapheme)
			if currentColumn < beforeEnd && currentColumn+width <= beforeEnd {
				if pendingBefore != "" {
					result.Before += pendingBefore
					pendingBefore = ""
				}
				result.Before += grapheme
				result.BeforeWidth += width
			} else if currentColumn >= afterStart && currentColumn < afterEnd {
				fits := !strictAfter || currentColumn+width <= afterEnd
				if fits {
					if !afterStarted {
						result.After += tracker.active()
						afterStarted = true
					}
					result.After += grapheme
					result.AfterWidth += width
				}
			}
			currentColumn += width
			if afterLength <= 0 {
				return currentColumn < beforeEnd
			}
			return currentColumn < afterEnd
		})
		position = textEnd
		if afterLength <= 0 && currentColumn >= beforeEnd || afterLength > 0 && currentColumn >= afterEnd {
			break
		}
	}
	return result
}

func compositeLineAt(baseLine, overlayLine string, startColumn, overlayWidth, totalWidth int) string {
	if IsImageLine(baseLine) {
		return baseLine
	}
	afterStart := startColumn + overlayWidth
	base := ExtractSegments(baseLine, startColumn, afterStart, totalWidth-afterStart, true)
	overlay, overlayVisibleWidth := sliceWithWidth(overlayLine, 0, overlayWidth, true)
	beforePadding := max(0, startColumn-base.BeforeWidth)
	overlayPadding := max(0, overlayWidth-overlayVisibleWidth)
	actualBeforeWidth := max(startColumn, base.BeforeWidth)
	actualOverlayWidth := max(overlayWidth, overlayVisibleWidth)
	afterTarget := max(0, totalWidth-actualBeforeWidth-actualOverlayWidth)
	afterPadding := max(0, afterTarget-base.AfterWidth)
	result := base.Before + strings.Repeat(" ", beforePadding) + segmentReset +
		overlay + strings.Repeat(" ", overlayPadding) + segmentReset +
		base.After + strings.Repeat(" ", afterPadding)
	if VisibleWidth(result) <= totalWidth {
		return result
	}
	return SliceByColumn(result, 0, totalWidth, true)
}
