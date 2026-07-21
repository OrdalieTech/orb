package modes

import (
	"context"
	"strconv"
	"sync"

	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/tui"

	theme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
)

// Selector modes mirroring upstream oauth-selector.ts mode: "login" | "logout".
const (
	oauthSelectorLogin  = "login"
	oauthSelectorLogout = "logout"
)

const authSelectorMaxVisible = 8

// formatAuthSelectorProviderType mirrors oauth-selector.ts
// formatAuthSelectorProviderType.
func formatAuthSelectorProviderType(authType aiauth.AuthType) string {
	if authType == aiauth.AuthTypeOAuth {
		return "subscription"
	}
	return "API key"
}

// OAuthSelectorComponent is the searchable auth-provider selector behind
// /login and /logout, a port of upstream components/oauth-selector.ts: a
// fuzzy-search input over name+id+authType+methodName, an 8-row visible
// window with a scroll counter, and per-row status indicators.
type OAuthSelectorComponent struct {
	container          *tui.Container
	searchInput        *tui.Input
	listContainer      *tui.Container
	allProviders       []InteractiveAuthProvider
	filteredProviders  []InteractiveAuthProvider
	selectedIndex      int
	mode               string
	onSelect           func(InteractiveAuthProvider)
	onCancel           func()
	showAuthTypeLabels bool
}

// NewOAuthSelectorComponent builds the selector; initialSearchInput pre-fills
// the search field (upstream constructor's initialSearchInput) so /login with
// an unmatched fuzzy ref opens the list already filtered.
func NewOAuthSelectorComponent(
	selectorMode string,
	providers []InteractiveAuthProvider,
	onSelect func(InteractiveAuthProvider),
	onCancel func(),
	initialSearchInput string,
) *OAuthSelectorComponent {
	component := &OAuthSelectorComponent{
		container:         &tui.Container{},
		searchInput:       tui.NewInput(),
		listContainer:     &tui.Container{},
		allProviders:      append([]InteractiveAuthProvider(nil), providers...),
		filteredProviders: providers,
		mode:              selectorMode,
		onSelect:          onSelect,
		onCancel:          onCancel,
	}
	types := make(map[aiauth.AuthType]struct{}, 2)
	for _, provider := range providers {
		types[provider.AuthType] = struct{}{}
	}
	component.showAuthTypeLabels = len(types) > 1

	component.container.AddChild(extensionDialogBorder())
	component.container.AddChild(tui.NewSpacer(1))
	title := "Select provider to configure:"
	if selectorMode == oauthSelectorLogout {
		title = "Select provider to logout:"
	}
	component.container.AddChild(tui.NewTruncatedText(theme.FG("accent", theme.Bold(title)), 1, 0))
	component.container.AddChild(tui.NewSpacer(1))

	if initialSearchInput != "" {
		// Insert through HandleInput so the cursor lands after the pre-filled
		// text like upstream Input.setValue.
		component.searchInput.HandleInput(tui.KeyEvent{Raw: initialSearchInput})
	}
	component.searchInput.OnSubmit = func(string) { component.confirmSelection() }
	component.container.AddChild(component.searchInput)
	component.container.AddChild(tui.NewSpacer(1))

	component.container.AddChild(component.listContainer)
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(extensionDialogBorder())

	component.filterProviders(initialSearchInput)
	return component
}

func (component *OAuthSelectorComponent) filterProviders(query string) {
	if query != "" {
		component.filteredProviders = tui.FuzzyFilter(component.allProviders, query, func(provider InteractiveAuthProvider) string {
			return provider.Name + " " + provider.ID + " " + string(provider.AuthType) + " " + provider.MethodName
		})
	} else {
		component.filteredProviders = component.allProviders
	}
	component.selectedIndex = max(0, min(component.selectedIndex, max(0, len(component.filteredProviders)-1)))
	component.updateList()
}

func (component *OAuthSelectorComponent) updateList() {
	component.listContainer.Clear()

	startIndex := max(0, min(
		component.selectedIndex-authSelectorMaxVisible/2,
		len(component.filteredProviders)-authSelectorMaxVisible,
	))
	endIndex := min(startIndex+authSelectorMaxVisible, len(component.filteredProviders))

	for index := startIndex; index < endIndex; index++ {
		provider := component.filteredProviders[index]
		statusIndicator := formatAuthStatusIndicator(provider)
		authTypeLabel := ""
		if component.showAuthTypeLabels {
			authTypeLabel = theme.FG("muted", " ["+formatAuthSelectorProviderType(provider.AuthType)+"]")
		}
		var line string
		if index == component.selectedIndex {
			line = theme.FG("accent", "→ ") + theme.FG("accent", provider.Name) + authTypeLabel + statusIndicator
		} else {
			line = "  " + theme.FG("text", provider.Name) + authTypeLabel + statusIndicator
		}
		component.listContainer.AddChild(tui.NewTruncatedText(line, 1, 0))
	}

	if startIndex > 0 || endIndex < len(component.filteredProviders) {
		scrollInfo := theme.FG("muted", "  ("+strconv.Itoa(component.selectedIndex+1)+"/"+strconv.Itoa(len(component.filteredProviders))+")")
		component.listContainer.AddChild(tui.NewTruncatedText(scrollInfo, 1, 0))
	}

	if len(component.filteredProviders) == 0 {
		message := "No matching providers"
		if len(component.allProviders) == 0 {
			if component.mode == oauthSelectorLogin {
				message = "No providers available"
			} else {
				message = "No providers logged in. Use /login first."
			}
		}
		component.listContainer.AddChild(tui.NewTruncatedText(theme.FG("muted", "  "+message), 1, 0))
	}
}

// formatAuthStatusIndicator ports oauth-selector.ts formatStatusIndicator: raw
// runtime sources render as-is, all-caps names get an "env:" prefix, and the
// OAuth/stored-credential sources collapse to "configured".
func formatAuthStatusIndicator(provider InteractiveAuthProvider) string {
	if provider.Status == nil {
		return theme.FG("muted", " • unconfigured")
	}
	if provider.Status.Type != provider.AuthType {
		label := "API key configured"
		if provider.Status.Type == aiauth.AuthTypeOAuth {
			label = "subscription configured"
		}
		return theme.FG("muted", " • ") + theme.FG("warning", label)
	}
	source := provider.Status.Source
	if source == "" || source == "OAuth" || source == "stored credential" {
		return theme.FG("success", " ✓ configured")
	}
	if isAuthEnvironmentSource(source) {
		source = "env: " + source
	}
	return theme.FG("success", " ✓ "+source)
}

func (component *OAuthSelectorComponent) confirmSelection() {
	if component.selectedIndex >= 0 && component.selectedIndex < len(component.filteredProviders) && component.onSelect != nil {
		component.onSelect(component.filteredProviders[component.selectedIndex])
	}
}

func (component *OAuthSelectorComponent) HandleInput(event tui.KeyEvent) {
	bindings := tui.GetKeybindings()
	switch {
	case bindings.Matches(event.Raw, "tui.select.up"):
		if len(component.filteredProviders) == 0 {
			return
		}
		component.selectedIndex = max(0, component.selectedIndex-1)
		component.updateList()
	case bindings.Matches(event.Raw, "tui.select.down"):
		if len(component.filteredProviders) == 0 {
			return
		}
		component.selectedIndex = min(len(component.filteredProviders)-1, component.selectedIndex+1)
		component.updateList()
	case bindings.Matches(event.Raw, "tui.select.confirm") || event.Raw == "\n":
		component.confirmSelection()
	case bindings.Matches(event.Raw, "tui.select.cancel"):
		if component.onCancel != nil {
			component.onCancel()
		}
	default:
		component.searchInput.HandleInput(event)
		component.filterProviders(component.searchInput.GetValue())
	}
}

func (component *OAuthSelectorComponent) SetFocused(focused bool) {
	component.searchInput.SetFocused(focused)
}

func (component *OAuthSelectorComponent) Invalidate() { component.container.Invalidate() }
func (component *OAuthSelectorComponent) Render(width int) []string {
	return component.container.Render(width)
}

// selectAuthProviderSearchable presents the searchable selector in place of
// the editor and blocks until a choice, cancel, or context cancellation, the
// Go seam for upstream showSelector(OAuthSelectorComponent).
func (mode *InteractiveMode) selectAuthProviderSearchable(
	ctx context.Context,
	selectorMode string,
	providers []InteractiveAuthProvider,
	initialSearchInput string,
) (InteractiveAuthProvider, bool) {
	type authSelection struct {
		provider InteractiveAuthProvider
		ok       bool
	}
	result := make(chan authSelection, 1)
	resolve := func(selection authSelection) {
		select {
		case result <- selection:
		default:
		}
	}
	component := NewOAuthSelectorComponent(selectorMode, providers,
		func(provider InteractiveAuthProvider) { resolve(authSelection{provider: provider, ok: true}) },
		func() { resolve(authSelection{}) },
		initialSearchInput,
	)

	mode.editorContainer.Clear()
	mode.editorContainer.AddChild(component)
	mode.ui.SetFocus(component)
	mode.ui.RequestRender()

	defer func() {
		mode.editorContainer.Clear()
		mode.restoreEditorComponent()
		mode.ui.SetFocus(mode.activeEditorFocus())
		mode.ui.RequestRender()
	}()

	select {
	case selection := <-result:
		return selection.provider, selection.ok
	case <-ctx.Done():
		return InteractiveAuthProvider{}, false
	}
}

// ambientAuthDialogComponent is the titled information dialog for providers
// whose authentication is configured outside the agent (upstream
// showAmbientAuthDialog: LoginDialogComponent with "NAME setup" title and
// showInfo(..., showCloseHint)).
type ambientAuthDialogComponent struct {
	container *tui.Container
	onClose   func()
}

func newAmbientAuthDialogComponent(title, message string, onClose func()) *ambientAuthDialogComponent {
	component := &ambientAuthDialogComponent{container: &tui.Container{}, onClose: onClose}
	component.container.AddChild(extensionDialogBorder())
	component.container.AddChild(tui.NewText(theme.FG("accent", theme.Bold(title)), 1, 0, nil))
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(tui.NewText(theme.FG("text", message), 1, 0, nil))
	component.container.AddChild(tui.NewSpacer(1))
	component.container.AddChild(tui.NewText("("+KeyHint("tui.select.cancel", "to close")+")", 1, 0, nil))
	component.container.AddChild(extensionDialogBorder())
	return component
}

func (component *ambientAuthDialogComponent) HandleInput(event tui.KeyEvent) {
	if tui.GetKeybindings().Matches(event.Raw, "tui.select.cancel") {
		if component.onClose != nil {
			component.onClose()
		}
	}
}

func (component *ambientAuthDialogComponent) Invalidate() { component.container.Invalidate() }
func (component *ambientAuthDialogComponent) Render(width int) []string {
	return component.container.Render(width)
}

// showAmbientAuthDialog presents the ambient-provider information dialog and
// blocks until closed (upstream interactive-mode.ts:5086-5107).
func (mode *InteractiveMode) showAmbientAuthDialog(ctx context.Context, provider InteractiveAuthProvider) {
	method := provider.MethodName
	if method == "" {
		method = "Authentication"
	}
	closed := make(chan struct{})
	var once sync.Once
	dialog := newAmbientAuthDialogComponent(
		provider.Name+" setup",
		method+" is configured outside pigo.",
		func() { once.Do(func() { close(closed) }) },
	)

	mode.editorContainer.Clear()
	mode.editorContainer.AddChild(dialog)
	mode.ui.SetFocus(dialog)
	mode.ui.RequestRender()

	defer func() {
		mode.editorContainer.Clear()
		mode.restoreEditorComponent()
		mode.ui.SetFocus(mode.activeEditorFocus())
		mode.ui.RequestRender()
	}()

	select {
	case <-closed:
	case <-ctx.Done():
	}
}
