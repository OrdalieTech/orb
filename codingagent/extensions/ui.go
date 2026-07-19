package extensions

import (
	"context"

	"github.com/OrdalieTech/pi-go/agent"
)

type DialogOptions struct {
	Signal  context.Context
	Timeout *int64
}

type WidgetPlacement string

const (
	WidgetAboveEditor WidgetPlacement = "aboveEditor"
	WidgetBelowEditor WidgetPlacement = "belowEditor"
)

type WidgetOptions struct{ Placement WidgetPlacement }

type NotificationType string

const (
	NotifyInfo    NotificationType = "info"
	NotifyWarning NotificationType = "warning"
	NotifyError   NotificationType = "error"
)

type WorkingIndicatorOptions struct {
	Frames     []string
	IntervalMS int64
}

type TerminalInputResult struct {
	Consume bool
	Data    string
}

type TerminalInputHandler func(string) *TerminalInputResult

type Component interface{ Render(int) []string }

type UIHost interface {
	Width() int
	Height() int
	Invalidate()
}

type Keybindings interface {
	Matches(input, binding string) bool
	Keys(binding string) []string
	Definition(binding string) KeybindingDefinition
	Conflicts() []KeybindingConflict
	UserBindings() map[string][]string
	ResolvedBindings() map[string][]string
}

type KeybindingDefinition struct {
	DefaultKeys []string
	Description string
}

type KeybindingConflict struct {
	Key      string
	Bindings []string
}

type Theme interface {
	FG(color, text string) string
	BG(color, text string) string
	Bold(string) string
	Italic(string) string
	Underline(string) string
	Inverse(string) string
	Strikethrough(string) string
	FGANSI(string) string
	BGANSI(string) string
	ColorMode() string
	ThinkingBorderColor(agent.ThinkingLevel) func(string) string
	BashModeBorderColor() func(string) string
}

type ThemeInfo struct {
	Name string
	Path *string
}

type AutocompleteRequest struct {
	Lines      []string
	CursorLine int
	CursorCol  int
	Signal     context.Context
	Force      bool
}

type AutocompleteResult struct {
	Prefix string
	Items  []AutocompleteItem
}

type AutocompleteProvider interface {
	TriggerCharacters() []string
	GetSuggestions(context.Context, AutocompleteRequest) (*AutocompleteResult, error)
	ApplyCompletion(AutocompleteRequest, AutocompleteItem, string) ([]string, int, int)
	ShouldTriggerFileCompletion(AutocompleteRequest) bool
}

type AutocompleteProviderFactory func(AutocompleteProvider) AutocompleteProvider

type EditorComponent interface {
	Component
	HandleInput(string)
}

// AutocompleteEditorComponent is the optional editor capability used when an
// extension replaces the active editor while autocomplete is configured.
type AutocompleteEditorComponent interface {
	EditorComponent
	SetAutocompleteProvider(AutocompleteProvider)
}

type EditorFactory func(UIHost, Theme, Keybindings) EditorComponent

type DisposableComponent interface {
	Component
	Dispose()
}

type ComponentFactory func(UIHost, Theme) Component

type FooterDataProvider interface {
	GitBranch() string
	Statuses() map[string]string
}

type FooterFactory func(UIHost, Theme, FooterDataProvider) Component

type HeaderFactory func(UIHost, Theme) Component

type OverlayAnchor string

const (
	OverlayTopLeft     OverlayAnchor = "top-left"
	OverlayTop         OverlayAnchor = "top"
	OverlayTopRight    OverlayAnchor = "top-right"
	OverlayLeft        OverlayAnchor = "left"
	OverlayCenter      OverlayAnchor = "center"
	OverlayRight       OverlayAnchor = "right"
	OverlayBottomLeft  OverlayAnchor = "bottom-left"
	OverlayBottom      OverlayAnchor = "bottom"
	OverlayBottomRight OverlayAnchor = "bottom-right"
)

type OverlayOptions struct {
	Width        any
	MinWidth     int
	MaxHeight    any
	Anchor       OverlayAnchor
	OffsetX      int
	OffsetY      int
	Row          any
	Column       any
	Margin       any
	Visible      func(width, height int) bool
	NonCapturing bool
}

type OverlayHandle interface {
	Hide()
	SetHidden(bool)
	IsHidden() bool
	Focus()
	Unfocus(Component)
	IsFocused() bool
}

type CustomOptions struct {
	Overlay               bool
	StaticOverlayOptions  *OverlayOptions
	DynamicOverlayOptions func() OverlayOptions
	OnHandle              func(OverlayHandle)
}

type CustomDone func(any)

type CustomFactory func(UIHost, Theme, Keybindings, CustomDone) (Component, error)

type Widget struct {
	Lines   []string
	Factory ComponentFactory
}

type ThemeSetResult struct {
	Success bool
	Error   string
}

type TrustUI interface {
	Select(context.Context, string, []string, *DialogOptions) (string, bool, error)
	Confirm(context.Context, string, string, *DialogOptions) (bool, error)
	Input(context.Context, string, *string, *DialogOptions) (string, bool, error)
	Notify(string, NotificationType)
}

type UI interface {
	TrustUI
	OnTerminalInput(TerminalInputHandler) func()
	SetStatus(string, *string)
	SetWorkingMessage(*string)
	SetWorkingVisible(bool)
	SetWorkingIndicator(*WorkingIndicatorOptions)
	SetHiddenThinkingLabel(*string)
	SetWidget(string, *Widget, *WidgetOptions)
	SetFooter(FooterFactory)
	SetHeader(HeaderFactory)
	SetTitle(string)
	Custom(context.Context, CustomFactory, *CustomOptions) (any, bool, error)
	PasteToEditor(string)
	SetEditorText(string)
	GetEditorText() string
	Editor(context.Context, string, *string) (string, bool, error)
	AddAutocompleteProvider(AutocompleteProviderFactory)
	SetEditorComponent(EditorFactory)
	GetEditorComponent() EditorFactory
	Theme() Theme
	GetAllThemes() []ThemeInfo
	GetTheme(string) Theme
	SetTheme(any) ThemeSetResult
	GetToolsExpanded() bool
	SetToolsExpanded(bool)
}

type NoopUI struct{}

func NewNoopUI() UI { return NoopUI{} }

func (NoopUI) Select(context.Context, string, []string, *DialogOptions) (string, bool, error) {
	return "", false, nil
}

func (NoopUI) Confirm(context.Context, string, string, *DialogOptions) (bool, error) {
	return false, nil
}

func (NoopUI) Input(context.Context, string, *string, *DialogOptions) (string, bool, error) {
	return "", false, nil
}

func (NoopUI) Notify(string, NotificationType) {}

func (NoopUI) OnTerminalInput(TerminalInputHandler) func() { return func() {} }

func (NoopUI) SetStatus(string, *string) {}

func (NoopUI) SetWorkingMessage(*string) {}

func (NoopUI) SetWorkingVisible(bool) {}

func (NoopUI) SetWorkingIndicator(*WorkingIndicatorOptions) {}

func (NoopUI) SetHiddenThinkingLabel(*string) {}

func (NoopUI) SetWidget(string, *Widget, *WidgetOptions) {}

func (NoopUI) SetFooter(FooterFactory) {}

func (NoopUI) SetHeader(HeaderFactory) {}

func (NoopUI) SetTitle(string) {}

func (NoopUI) Custom(context.Context, CustomFactory, *CustomOptions) (any, bool, error) {
	return nil, false, nil
}

func (NoopUI) PasteToEditor(string) {}

func (NoopUI) SetEditorText(string) {}

func (NoopUI) GetEditorText() string { return "" }

func (NoopUI) Editor(context.Context, string, *string) (string, bool, error) {
	return "", false, nil
}

func (NoopUI) AddAutocompleteProvider(AutocompleteProviderFactory) {}

func (NoopUI) SetEditorComponent(EditorFactory) {}

func (NoopUI) GetEditorComponent() EditorFactory { return nil }

func (NoopUI) Theme() Theme { return plainTheme{} }

func (NoopUI) GetAllThemes() []ThemeInfo { return []ThemeInfo{} }

func (NoopUI) GetTheme(string) Theme { return nil }

func (NoopUI) SetTheme(any) ThemeSetResult {
	return ThemeSetResult{Error: ErrUIUnavailable.Error()}
}

func (NoopUI) GetToolsExpanded() bool { return false }

func (NoopUI) SetToolsExpanded(bool) {}

type plainTheme struct{}

func (plainTheme) FG(_ string, text string) string { return text }

func (plainTheme) BG(_ string, text string) string { return text }

func (plainTheme) Bold(text string) string { return text }

func (plainTheme) Italic(text string) string { return text }

func (plainTheme) Underline(text string) string { return text }

func (plainTheme) Inverse(text string) string { return text }

func (plainTheme) Strikethrough(text string) string { return text }

func (plainTheme) FGANSI(string) string { return "" }

func (plainTheme) BGANSI(string) string { return "" }

func (plainTheme) ColorMode() string { return "" }

func (plainTheme) ThinkingBorderColor(agent.ThinkingLevel) func(string) string {
	return func(text string) string { return text }
}

func (plainTheme) BashModeBorderColor() func(string) string {
	return func(text string) string { return text }
}
