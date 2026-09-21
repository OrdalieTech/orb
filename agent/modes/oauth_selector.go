package modes

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/accounts"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/tui"
	"github.com/OrdalieTech/orb/usage"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
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
	window             tui.ListWindow
	mode               string
	onSelect           func(InteractiveAuthProvider)
	onCancel           func()
	showAuthTypeLabels bool
	rows               listRowOffsets
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
		searchInput:       newSearchInput(),
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

	startIndex := component.window.Start(component.selectedIndex, len(component.filteredProviders), authSelectorMaxVisible)
	endIndex := min(startIndex+authSelectorMaxVisible, len(component.filteredProviders))
	component.rows.setWindow(startIndex, endIndex-startIndex)

	for index := startIndex; index < endIndex; index++ {
		provider := component.filteredProviders[index]
		statusIndicator := formatAuthStatusIndicator(provider)
		authTypeLabel := ""
		if component.showAuthTypeLabels {
			authTypeLabel = theme.FG("muted", " ["+formatAuthSelectorProviderType(provider.AuthType)+"]")
		}
		var line string
		if index == component.selectedIndex {
			line = theme.FG("accent", "› ") + theme.FG("text", provider.Name) + authTypeLabel + statusIndicator
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
	// Any keyboard interaction re-anchors the window on the selection; only
	// pointer selection keeps it frozen.
	component.window.Recenter()
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

// Render records where each visible row landed for mouse hit-testing; the
// emitted lines are the container's own.
func (component *OAuthSelectorComponent) Render(width int) []string {
	return component.rows.renderRecordingRows(component.container, component.listContainer, width)
}

// WantsMouseMotion turns on hover reports while the selector holds focus.
func (component *OAuthSelectorComponent) WantsMouseMotion() bool { return true }

// HandleMouse drives the shared list pointer semantic.
func (component *OAuthSelectorComponent) HandleMouse(event tui.MouseEvent) bool {
	if len(component.filteredProviders) == 0 {
		return false
	}
	return tui.HandleListMouse(component, event)
}

// ListRowAt maps a component-local row to the filtered-provider index it
// renders.
func (component *OAuthSelectorComponent) ListRowAt(row int) (int, bool) {
	index, ok := component.rows.rowAt(row)
	if !ok || index >= len(component.filteredProviders) {
		return 0, false
	}
	return index, true
}

// ListSelectRow moves the highlight without re-anchoring the window, so
// hover can never shift rows under the cursor.
func (component *OAuthSelectorComponent) ListSelectRow(index int) {
	listSelectRow(&component.window, &component.selectedIndex, index, component.updateList)
}

// ListScroll moves the selection one row per tick, recentring like keyboard
// navigation does.
func (component *OAuthSelectorComponent) ListScroll(direction int) {
	listScroll(&component.window, &component.selectedIndex, direction, len(component.filteredProviders), component.updateList)
}

// ListConfirm confirms the current selection.
func (component *OAuthSelectorComponent) ListConfirm() { component.confirmSelection() }

type authDialogLine struct {
	text  string
	style string
}

// loginAuthDialogComponent keeps OAuth notifications in the editor area for
// the lifetime of a login. It is the waiting-state subset of upstream's
// LoginDialogComponent; prompt/select input continues through InteractiveUI.
type loginAuthDialogComponent struct {
	mu        sync.Mutex
	container *tui.Container
	content   *tui.Container
	title     string
	lines     []authDialogLine
	onCancel  func()
}

func newLoginAuthDialogComponent(title string, onCancel func()) *loginAuthDialogComponent {
	component := &loginAuthDialogComponent{
		container: &tui.Container{}, content: &tui.Container{}, title: title, onCancel: onCancel,
	}
	component.container.AddChild(extensionDialogBorder())
	component.container.AddChild(tui.NewText(theme.FG("accent", theme.Bold(title)), 1, 0, nil))
	component.container.AddChild(component.content)
	component.container.AddChild(extensionDialogBorder())
	return component
}

func (component *loginAuthDialogComponent) replace(lines ...authDialogLine) {
	component.mu.Lock()
	component.lines = append([]authDialogLine(nil), lines...)
	component.rebuildLocked()
	component.mu.Unlock()
}

func (component *loginAuthDialogComponent) append(lines ...authDialogLine) {
	component.mu.Lock()
	component.lines = append(component.lines, lines...)
	component.rebuildLocked()
	component.mu.Unlock()
}

func (component *loginAuthDialogComponent) rebuildLocked() {
	component.content.Clear()
	for _, line := range component.lines {
		if line.style == "spacer" {
			component.content.AddChild(tui.NewSpacer(1))
			continue
		}
		text := line.text
		switch line.style {
		case "accent":
			text = theme.FG("accent", text)
		case "dim":
			text = theme.FG("dim", text)
		case "warning":
			text = theme.FG("warning", text)
		default:
			text = theme.FG("text", text)
		}
		component.content.AddChild(tui.NewText(text, 1, 0, nil))
	}
}

func authDialogHyperlink(url, label string) string {
	return "\x1b]8;;" + url + "\x07" + label + "\x1b]8;;\x07"
}

func (component *loginAuthDialogComponent) showAuth(url, instructions string) {
	clickHint := "Ctrl+click to open"
	if runtime.GOOS == "darwin" {
		clickHint = "Cmd+click to open"
	}
	lines := []authDialogLine{
		{style: "spacer"},
		{text: authDialogHyperlink(url, url), style: "accent"},
		{text: authDialogHyperlink(url, clickHint), style: "dim"},
	}
	if instructions != "" {
		lines = append(lines, authDialogLine{style: "spacer"}, authDialogLine{text: instructions, style: "warning"})
	}
	component.replace(lines...)
}

func (component *loginAuthDialogComponent) showDeviceCode(verificationURI, userCode string) {
	clickHint := "Ctrl+click to open"
	if runtime.GOOS == "darwin" {
		clickHint = "Cmd+click to open"
	}
	component.replace(
		authDialogLine{style: "spacer"},
		authDialogLine{text: authDialogHyperlink(verificationURI, verificationURI), style: "accent"},
		authDialogLine{text: authDialogHyperlink(verificationURI, clickHint), style: "dim"},
		authDialogLine{style: "spacer"},
		authDialogLine{text: "Enter code: " + userCode, style: "warning"},
		authDialogLine{style: "spacer"},
		authDialogLine{text: "Waiting for authentication...", style: "dim"},
		authDialogLine{text: "(" + KeyHint("tui.select.cancel", "to cancel") + ")"},
	)
}

func (component *loginAuthDialogComponent) showInfo(message string, links []aiauth.AuthInfoLink) {
	lines := []authDialogLine{{style: "spacer"}, {text: message}}
	for _, link := range links {
		label := link.URL
		if link.Label != "" {
			label = link.Label + ": " + link.URL
		}
		lines = append(lines, authDialogLine{text: authDialogHyperlink(link.URL, label), style: "accent"})
	}
	component.append(lines...)
}

func (component *loginAuthDialogComponent) showProgress(message string) {
	component.append(authDialogLine{text: message, style: "dim"})
}

func (component *loginAuthDialogComponent) showDetails(lines ...string) {
	entries := []authDialogLine{{style: "spacer"}}
	for _, line := range lines {
		entries = append(entries, authDialogLine{text: line})
	}
	component.replace(entries...)
}

func (component *loginAuthDialogComponent) promptTitle(message string) string {
	component.mu.Lock()
	defer component.mu.Unlock()
	lines := []string{component.title}
	for _, line := range component.lines {
		if line.style == "spacer" || line.text == "" {
			continue
		}
		lines = append(lines, line.text)
	}
	lines = append(lines, "", message)
	return strings.Join(lines, "\n")
}

func (component *loginAuthDialogComponent) HandleInput(event tui.KeyEvent) {
	if tui.GetKeybindings().Matches(event.Raw, "tui.select.cancel") && component.onCancel != nil {
		component.onCancel()
	}
}

func (component *loginAuthDialogComponent) SetFocused(bool) {}
func (component *loginAuthDialogComponent) Invalidate() {
	component.mu.Lock()
	component.container.Invalidate()
	component.mu.Unlock()
}
func (component *loginAuthDialogComponent) Render(width int) []string {
	component.mu.Lock()
	defer component.mu.Unlock()
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

	title := "Login"
	if selectorMode == oauthSelectorLogout {
		title = "Logout"
	}
	handle := mode.ui.ShowOverlay(menuFrame(title, component), configOverlayOptions())
	mode.ui.RequestRender()
	defer func() {
		handle.Hide()
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
		method+" is configured outside orb.",
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

// providerMenu uses the same bounded, keyboard/mouse-aware list as Ctrl+P.
func (mode *InteractiveMode) providerMenu(ctx context.Context, title string, rows []tui.GridRow) (string, bool) {
	result := make(chan string, 1)
	resolve := func(value string) {
		select {
		case result <- value:
		default:
		}
	}
	palette := newCommandPalette(rows, mode.keybindings, mode.Height, resolve, func() { resolve("") })
	frame := menuFrame(title, palette)
	if title == "Providers" {
		frame.Action = "+ Connect provider"
		frame.OnAction = func() { resolve("connect") }
	}
	handle := mode.ui.ShowOverlay(frame, dialogOverlayOptions())
	mode.ui.RequestRender()
	defer func() { handle.Hide(); mode.ui.RequestRender() }()
	select {
	case value := <-result:
		return value, value != ""
	case <-ctx.Done():
		return "", false
	}
}

func providerAccountRows(connected []accounts.Account, enabled bool) []tui.GridRow {
	rows := make([]tui.GridRow, 0, len(connected)+4)
	provider := ""
	for i, account := range connected {
		if provider != account.Provider {
			provider = account.Provider
			rows = append(rows, tui.GridRow{Header: true, Cells: []string{theme.Bold(theme.FG("text", provider))}})
		}
		name := account.Name
		if account.Active {
			name += " ✓"
		}
		kind := "API key"
		if account.Type == aiauth.CredentialOAuth {
			kind = "Subscription"
		}
		rows = append(rows, tui.GridRow{Value: strconv.Itoa(i), Cells: []string{name, theme.FG("muted", kind)}, Search: account.Provider + " " + account.Name + " " + kind, Detail: []string{"Manage " + account.Provider + " · " + account.Name}})
		if i+1 == len(connected) || connected[i+1].Provider != provider {
			rows = append(rows, tui.GridRow{Value: "add:" + provider, Cells: []string{theme.FG("muted", "+ Add account")}, Search: provider + " add account"})
		}
	}
	toggle := "Show usage in footer"
	if enabled {
		toggle = "Hide usage from footer"
	}
	rows = append(rows, tui.GridRow{Value: "usage", Cells: []string{toggle}, Search: "usage quota limits footer"})
	return rows
}

func (mode *InteractiveMode) showProviders(host InteractiveProviderHost) {
	ctx := mode.authenticationContext()
	for ctx.Err() == nil {
		connected, err := host.ProviderAccounts(ctx)
		if err != nil {
			mode.showError(err)
			return
		}
		selected, ok := mode.providerMenu(ctx, "Providers", providerAccountRows(connected, host.UsageEnabled()))
		if !ok {
			return
		}
		if provider, add := strings.CutPrefix(selected, "add:"); add {
			mode.connectProviderAccount(ctx, provider, nil)
			continue
		}
		switch selected {
		case "connect":
			mode.connectProviderAccount(ctx, "", nil)
		case "usage":
			if err := host.SetUsageEnabled(!host.UsageEnabled()); err != nil {
				mode.showError(err)
				continue
			}
			// Loading/unloading the optional module uses the normal assembly lifecycle.
			if err := mode.options.Host.Reload(ctx); err != nil {
				mode.showError(err)
			}
			return
		default:
			index, err := strconv.Atoi(selected)
			if err == nil && index >= 0 && index < len(connected) {
				mode.manageProviderAccount(ctx, host, connected[index])
			}
		}
	}
}

func (mode *InteractiveMode) connectProviderAccount(ctx context.Context, provider string, reconnect *accounts.Account) {
	options, err := mode.options.Host.AuthOptions(ctx)
	if err != nil {
		mode.showError(err)
		return
	}
	candidates := make([]InteractiveAuthProvider, 0, len(options.Login))
	rows := make([]tui.GridRow, 0, len(options.Login))
	for _, candidate := range options.Login {
		if provider != "" && candidate.ID != provider {
			continue
		}
		if reconnect != nil && aiauth.CredentialType(candidate.AuthType) != reconnect.Type {
			continue
		}
		if !candidate.LoginAvailable {
			continue
		}
		kind := "API key"
		if candidate.AuthType == aiauth.AuthTypeOAuth {
			kind = "Subscription"
		}
		rows = append(rows, tui.GridRow{Value: strconv.Itoa(len(candidates)), Cells: []string{candidate.Name, kind}, Search: candidate.Name + " " + candidate.ID + " " + kind})
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		mode.showStatusMessage("This provider is configured outside Orb.")
		return
	}
	index := 0
	if len(candidates) > 1 {
		chosen, ok := mode.providerMenu(ctx, "Connect provider", rows)
		if !ok {
			return
		}
		index, err = strconv.Atoi(chosen)
		if err != nil || index < 0 || index >= len(candidates) {
			return
		}
	}
	candidate := candidates[index]
	candidate.AccountLogin = true
	if reconnect != nil {
		candidate.AccountID = reconnect.ID
		candidate.AccountName = reconnect.Name
	} else {
		name, ok, err := mode.interactiveUI.Input(ctx, "Account name · Personal, Work…", nil, nil)
		if err != nil || !ok {
			return
		}
		candidate.AccountName = strings.TrimSpace(name)
	}
	mode.runLogin(candidate)
}

func (mode *InteractiveMode) manageProviderAccount(ctx context.Context, host InteractiveProviderHost, account accounts.Account) {
	mutable := account.ID != "ambient" && account.ID != "runtime"
	rows := []tui.GridRow{}
	add := func(value, label string) {
		rows = append(rows, tui.GridRow{Value: value, Cells: []string{label}, Search: label})
	}
	if mutable && !account.Active {
		add("select", "Use this account")
	}
	add("add", "Add another account")
	if mutable {
		add("rename", "Rename account")
		add("reconnect", "Reconnect")
		add("remove", "Disconnect")
	}
	if account.Provider == "openai-codex" || account.Provider == "opencode-go" {
		add("usage", "Usage and reset times")
	}
	action, ok := mode.providerMenu(ctx, account.Provider+" · "+account.Name, rows)
	if !ok {
		return
	}
	switch action {
	case "add":
		mode.connectProviderAccount(ctx, account.Provider, nil)
		return
	case "reconnect":
		mode.connectProviderAccount(ctx, account.Provider, &account)
		return
	case "usage":
		mode.showAccountUsage(ctx, host, account)
		return
	case "select":
		mode.switchProviderAccount(ctx, host, account)
		return
	}
	name := ""
	if action == "rename" {
		value, ok, err := mode.interactiveUI.Input(ctx, "Account name", nil, nil)
		if err != nil || !ok {
			return
		}
		name = value
	}
	if action == "remove" {
		confirmed, err := mode.interactiveUI.Confirm(ctx, "Disconnect account", account.Provider+" · "+account.Name, nil)
		if err != nil || !confirmed {
			return
		}
	}
	if err := host.ChangeAccount(ctx, account.Provider, account.ID, action, name); err != nil {
		mode.showError(err)
	}
}

func usageResetTime(window usage.Window) string {
	if window.ResetsAt.IsZero() {
		return "Reset time unavailable"
	}
	return "Resets " + window.ResetsAt.Local().Format("Mon 15:04 · 2 Jan MST")
}

func (mode *InteractiveMode) showAccountUsage(parent context.Context, host InteractiveProviderHost, account accounts.Account) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	closed := make(chan struct{}, 1)
	closeMenu := func() {
		select {
		case closed <- struct{}{}:
		default:
		}
	}
	palette := newCommandPalette([]tui.GridRow{{Cells: []string{"Checking usage…"}}}, mode.keybindings, mode.Height, func(string) { closeMenu() }, closeMenu)
	handle := mode.ui.ShowOverlay(menuFrame("Usage · "+account.Name, palette), dialogOverlayOptions())
	mode.ui.RequestRender()
	defer func() { handle.Hide(); mode.ui.RequestRender() }()
	result := make(chan []tui.GridRow, 1)
	go func() {
		snapshot, err := host.AccountUsage(ctx, account.Provider, account.ID)
		rows := []tui.GridRow{{Value: "close", Cells: []string{"Usage unavailable"}, Detail: []string{"Try again later or reconnect this account."}}}
		if err == nil {
			rows = nil
			for _, window := range snapshot.Windows {
				rows = append(rows, tui.GridRow{Value: "close", Cells: []string{window.Name, fmt.Sprintf("%.0f%% left", window.Remaining)}, Detail: []string{usageResetTime(window)}})
			}
		}
		result <- rows
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-closed:
			return
		case rows := <-result:
			palette.mu.Lock()
			palette.list.SetRows(rows)
			palette.mu.Unlock()
			mode.ui.RequestRender()
		}
	}
}

func (mode *InteractiveMode) showAccountSwitcher(host InteractiveProviderHost) {
	mode.mu.Lock()
	if mode.accountSwitcherOpen {
		mode.mu.Unlock()
		return
	}
	mode.accountSwitcherOpen = true
	mode.mu.Unlock()
	defer func() {
		mode.mu.Lock()
		mode.accountSwitcherOpen = false
		mode.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(mode.authenticationContext())
	defer cancel()
	connected, err := host.ProviderAccounts(ctx)
	if err != nil {
		mode.showError(err)
		return
	}
	if len(connected) == 0 {
		mode.showProviders(host)
		return
	}
	summaries := make([]string, len(connected))
	details := make([][]string, len(connected))
	applyUsage := func(index int, snapshot usage.Snapshot, ok bool) {
		summaries[index] = "Unavailable"
		details[index] = []string{"Usage unavailable · try again later or reconnect this account."}
		if !ok || len(snapshot.Windows) == 0 {
			return
		}
		limited := snapshot.Windows[0]
		for _, window := range snapshot.Windows {
			if window.Remaining < limited.Remaining {
				limited = window
			}
		}
		summaries[index] = fmt.Sprintf("%s %.0f%% left", limited.Name, limited.Remaining)
		details[index] = nil
		for _, window := range snapshot.Windows {
			details[index] = append(details[index], fmt.Sprintf("%s %.0f%% left · %s", window.Name, window.Remaining, usageResetTime(window)))
		}
		if time.Since(snapshot.CheckedAt) > time.Minute {
			summaries[index] += " · stale"
		}
	}
	for i, account := range connected {
		if account.Provider != "openai-codex" && account.Provider != "opencode-go" {
			continue
		}
		summaries[i] = "Checking…"
		if cached, ok := host.CachedAccountUsage(account.Provider, account.ID); ok {
			applyUsage(i, cached, true)
		}
	}
	rows := func() []tui.GridRow {
		result := providerAccountRows(connected, false)
		result = result[:len(result)-1]
		for i := range result {
			index, err := strconv.Atoi(result[i].Value)
			if err == nil && index >= 0 && index < len(summaries) && summaries[index] != "" {
				result[i].Cells[1] = theme.FG("muted", summaries[index])
				result[i].Detail = details[index]
			}
		}
		return append(result, tui.GridRow{Value: "manage", Cells: []string{"Manage providers"}, Search: "manage connect disconnect providers"})
	}
	selected := make(chan string, 1)
	choose := func(value string) {
		select {
		case selected <- value:
		default:
		}
	}
	palette := newCommandPalette(rows(), mode.keybindings, mode.Height, choose, func() { choose("") })
	palette.list.DetailHeight = 3
	if mode.session != nil {
		current := mode.session.State().Model
		if current != nil {
			for index, row := range rows() {
				account, err := strconv.Atoi(row.Value)
				if err == nil && connected[account].Active && connected[account].Provider == string(current.Provider) {
					palette.list.ListSelectRow(index)
					break
				}
			}
		}
	}
	handle := mode.ui.ShowOverlay(menuFrame("Switch account", palette), dialogOverlayOptions())
	mode.ui.RequestRender()
	defer func() { handle.Hide(); mode.ui.RequestRender() }()
	type updated struct {
		index    int
		snapshot usage.Snapshot
		err      error
	}
	updates := make(chan updated, len(connected))
	jobs := make(chan int, len(connected))
	for i, account := range connected {
		if account.Provider == "openai-codex" || account.Provider == "opencode-go" {
			jobs <- i
		}
	}
	close(jobs)
	for range min(4, len(connected)) {
		go func() {
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				account := connected[index]
				snapshot, err := host.AccountUsage(ctx, account.Provider, account.ID)
				updates <- updated{index: index, snapshot: snapshot, err: err}
			}
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case update := <-updates:
			applyUsage(update.index, update.snapshot, update.err == nil)
			palette.mu.Lock()
			palette.list.SetRows(rows())
			palette.mu.Unlock()
			mode.ui.RequestRender()
		case value := <-selected:
			handle.Hide()
			cancel()
			if value == "manage" {
				mode.showProviders(host)
				return
			}
			if provider, add := strings.CutPrefix(value, "add:"); add {
				mode.connectProviderAccount(mode.authenticationContext(), provider, nil)
				return
			}
			index, err := strconv.Atoi(value)
			if err != nil || index < 0 || index >= len(connected) {
				return
			}
			mode.switchProviderAccount(mode.authenticationContext(), host, connected[index])
			return
		}
	}
}

func (mode *InteractiveMode) switchProviderAccount(ctx context.Context, host InteractiveProviderHost, account accounts.Account) {
	if mode.session != nil && mode.session.State().IsStreaming {
		mode.showError(fmt.Errorf("wait for the current response before switching providers"))
		return
	}
	if account.ID != "ambient" && account.ID != "runtime" {
		if err := host.ChangeAccount(ctx, account.Provider, account.ID, "select", ""); err != nil {
			mode.showError(err)
			return
		}
	}
	if mode.session == nil {
		return
	}
	current := mode.session.State().Model
	if current != nil && string(current.Provider) == account.Provider {
		mode.ui.RequestRender()
		return
	}
	available := mode.session.AvailableModels()
	if current != nil {
		for _, model := range available {
			if string(model.Provider) == account.Provider && model.ID == current.ID {
				if err := mode.session.SetModel(ctx, model); err != nil {
					mode.showError(err)
				}
				mode.ui.RequestRender()
				return
			}
		}
	}
	// Provider changes keep the model choice explicit when no identical model exists.
	mode.showModelSelector(account.Provider)
}
