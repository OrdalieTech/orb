package modes

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"
)

func renderPlain(t *testing.T, component tui.Component, width int) string {
	t.Helper()
	return selectorANSI.ReplaceAllString(strings.Join(component.Render(width), "\n"), "")
}

func anthropicWarningShown(mode *InteractiveMode) bool {
	mode.mu.Lock()
	defer mode.mu.Unlock()
	return mode.anthropicSubscriptionWarningShown
}

// LOG-m1: selector titles match upstream oauth-selector.ts:72.
func TestLOGm1OAuthSelectorTitles(t *testing.T) {
	providers := []InteractiveAuthProvider{{ID: "groq", Name: "Groq", AuthType: aiauth.AuthTypeAPIKey}}
	login := NewOAuthSelectorComponent(oauthSelectorLogin, providers, nil, nil, "")
	if got := renderPlain(t, login, 60); !strings.Contains(got, "Select provider to configure:") {
		t.Fatalf("login selector missing title:\n%s", got)
	}
	logout := NewOAuthSelectorComponent(oauthSelectorLogout, providers, nil, nil, "")
	if got := renderPlain(t, logout, 60); !strings.Contains(got, "Select provider to logout:") {
		t.Fatalf("logout selector missing title:\n%s", got)
	}
}

// LOG-M2: the /login provider picker is a searchable selector with an 8-row
// visible window, a scroll counter, and fuzzy search over
// name+id+authType+methodName (upstream oauth-selector.ts:102-161).
func TestLOGM2OAuthSelectorFuzzySearchAndWindowing(t *testing.T) {
	providers := make([]InteractiveAuthProvider, 0, 12)
	for index := 1; index <= 12; index++ {
		name := fmt.Sprintf("alpha-%02d", index)
		providers = append(providers, InteractiveAuthProvider{ID: name, Name: name, AuthType: aiauth.AuthTypeAPIKey})
	}
	component := NewOAuthSelectorComponent(oauthSelectorLogin, providers, nil, nil, "")

	rendered := renderPlain(t, component, 60)
	if !strings.Contains(rendered, "→ alpha-01") || !strings.Contains(rendered, "alpha-08") {
		t.Fatalf("initial window rows missing:\n%s", rendered)
	}
	if strings.Contains(rendered, "alpha-09") {
		t.Fatalf("window rendered more than %d rows:\n%s", authSelectorMaxVisible, rendered)
	}
	if !strings.Contains(rendered, "(1/12)") {
		t.Fatalf("scroll counter missing:\n%s", rendered)
	}

	for range 9 {
		component.HandleInput(tui.KeyEvent{Raw: "\x1b[B"})
	}
	rendered = renderPlain(t, component, 60)
	if !strings.Contains(rendered, "→ alpha-10") || !strings.Contains(rendered, "alpha-12") {
		t.Fatalf("window did not follow selection:\n%s", rendered)
	}
	if strings.Contains(rendered, "alpha-01") || !strings.Contains(rendered, "(10/12)") {
		t.Fatalf("scrolled window rows wrong:\n%s", rendered)
	}

	component.HandleInput(tui.KeyEvent{Raw: "12"})
	if len(component.filteredProviders) != 1 || component.filteredProviders[0].ID != "alpha-12" {
		t.Fatalf("fuzzy filter = %#v", component.filteredProviders)
	}
	if rendered = renderPlain(t, component, 60); !strings.Contains(rendered, "→ alpha-12") {
		t.Fatalf("filtered selection not rendered:\n%s", rendered)
	}
}

// LOG-M2: search also matches the auth method name and shows the empty-state
// messages (upstream oauth-selector.ts:107,152-161).
func TestLOGM2OAuthSelectorSearchesMethodNameAndShowsEmptyStates(t *testing.T) {
	providers := []InteractiveAuthProvider{
		{ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth, MethodName: "Claude Pro/Max"},
		{ID: "groq", Name: "Groq", AuthType: aiauth.AuthTypeAPIKey},
	}
	component := NewOAuthSelectorComponent(oauthSelectorLogin, providers, nil, nil, "")
	component.HandleInput(tui.KeyEvent{Raw: "claude"})
	if len(component.filteredProviders) != 1 || component.filteredProviders[0].ID != "anthropic" {
		t.Fatalf("method-name search = %#v", component.filteredProviders)
	}
	component.HandleInput(tui.KeyEvent{Raw: "zzz"})
	if got := renderPlain(t, component, 60); !strings.Contains(got, "No matching providers") {
		t.Fatalf("missing no-match state:\n%s", got)
	}

	loginEmpty := NewOAuthSelectorComponent(oauthSelectorLogin, nil, nil, nil, "")
	if got := renderPlain(t, loginEmpty, 60); !strings.Contains(got, "No providers available") {
		t.Fatalf("missing login empty state:\n%s", got)
	}
	logoutEmpty := NewOAuthSelectorComponent(oauthSelectorLogout, nil, nil, nil, "")
	if got := renderPlain(t, logoutEmpty, 60); !strings.Contains(got, "No providers logged in. Use /login first.") {
		t.Fatalf("missing logout empty state:\n%s", got)
	}
}

// LOG-M2/LOG-m3: rows carry auth-type labels when mixed and status
// indicators in both selector modes, including the logout list.
func TestLOGm3OAuthSelectorShowsStatusOnLogoutList(t *testing.T) {
	providers := []InteractiveAuthProvider{
		{ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth,
			Status: &InteractiveAuthStatus{Type: aiauth.AuthTypeOAuth, Source: "stored credential"}},
		{ID: "groq", Name: "Groq", AuthType: aiauth.AuthTypeAPIKey,
			Status: &InteractiveAuthStatus{Type: aiauth.AuthTypeAPIKey, Source: "stored credential"}},
	}
	component := NewOAuthSelectorComponent(oauthSelectorLogout, providers, nil, nil, "")
	rendered := renderPlain(t, component, 80)
	if !strings.Contains(rendered, "Anthropic [subscription] ✓ configured") {
		t.Fatalf("logout row missing type label/status:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Groq [API key] ✓ configured") {
		t.Fatalf("logout row missing API key status:\n%s", rendered)
	}
}

// LOG-M3: an initial search input pre-filters the list and keeps the query
// editable (upstream oauth-selector.ts:76-79,99).
func TestLOGM3InitialSearchInputPrefiltersSelector(t *testing.T) {
	providers := []InteractiveAuthProvider{
		{ID: "groq", Name: "Groq", AuthType: aiauth.AuthTypeAPIKey},
		{ID: "openai", Name: "OpenAI", AuthType: aiauth.AuthTypeAPIKey},
	}
	component := NewOAuthSelectorComponent(oauthSelectorLogin, providers, nil, nil, "gro")
	if got := component.searchInput.GetValue(); got != "gro" {
		t.Fatalf("initial search value = %q", got)
	}
	if len(component.filteredProviders) != 1 || component.filteredProviders[0].ID != "groq" {
		t.Fatalf("pre-filtered providers = %#v", component.filteredProviders)
	}
	// The cursor sits at the end of the pre-filled query, so typing appends.
	component.HandleInput(tui.KeyEvent{Raw: "q"})
	if got := component.searchInput.GetValue(); got != "groq" {
		t.Fatalf("appended search value = %q", got)
	}
}

type authFlowHost struct {
	InteractiveSessionHost
	options       InteractiveAuthOptions
	loginProvider chan string
	logoutErr     error
}

func (host *authFlowHost) AuthOptions(context.Context) (InteractiveAuthOptions, error) {
	return host.options, nil
}

func (host *authFlowHost) Login(_ context.Context, providerID string, _ aiauth.AuthType, _ aiauth.AuthInteraction) error {
	if host.loginProvider != nil {
		host.loginProvider <- providerID
	}
	return nil
}

func (host *authFlowHost) Logout(context.Context, string) error { return host.logoutErr }

type bedrockPromptHost struct {
	authFlowHost
	promptStarted chan struct{}
}

func (host *bedrockPromptHost) Login(ctx context.Context, _ string, _ aiauth.AuthType, interaction aiauth.AuthInteraction) error {
	close(host.promptStarted)
	_, err := interaction.Prompt(ctx, aiauth.AuthPrompt{Type: aiauth.PromptSecret, Message: "Enter Amazon Bedrock bearer token"})
	return err
}

func newAuthFlowTestMode(host InteractiveSessionHost) *InteractiveMode {
	mode := &InteractiveMode{
		ui:              tui.NewTUI(newFakeTerminal(80, 24)),
		chat:            &tui.Container{},
		editorContainer: &tui.Container{},
		keybindings:     NewAppKeybindings(nil),
		options:         InteractiveModeOptions{Host: host},
	}
	mode.editor = NewCustomEditor(mode.ui, tui.EditorTheme{}, mode.keybindings)
	return mode
}

func newAnthropicWarningTestMode(t *testing.T, key string, host InteractiveSessionHost) *InteractiveMode {
	return newAnthropicWarningTestModeWithResolver(t, host, func(context.Context, ai.ProviderID) (*agent.RequestAuth, error) {
		return &agent.RequestAuth{APIKey: &key}, nil
	})
}

func newAnthropicWarningTestModeWithResolver(
	t *testing.T,
	host InteractiveSessionHost,
	resolver func(context.Context, ai.ProviderID) (*agent.RequestAuth, error),
) *InteractiveMode {
	t.Helper()
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	model := &ai.Model{Provider: "anthropic", ID: "claude-opus-4-8"}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent:          agent.NewAgent(nil, agent.WithInitialState(agent.AgentState{Model: model})),
		SessionManager: manager,
		Settings:       settings,
		GetRequestAuth: resolver,
		AvailableModels: func() []ai.Model {
			return []ai.Model{*model, {Provider: "openai", ID: "gpt-5.5"}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	mode := newAuthFlowTestMode(host)
	mode.session = runtime
	mode.cwd = filepath.Dir(agentDir)
	return mode
}

// LOG-M4: the warning is once per interactive session even when two Go
// callsites complete their auth lookup concurrently (the TS event loop makes
// this serialization implicit upstream).
func TestLOGM4AnthropicWarningIsAtomicOncePerSession(t *testing.T) {
	const workers = 8
	started := make(chan struct{}, workers)
	release := make(chan struct{})
	key := "sk-ant-oat-concurrent"
	mode := newAnthropicWarningTestModeWithResolver(t, &authFlowHost{}, func(context.Context, ai.ProviderID) (*agent.RequestAuth, error) {
		started <- struct{}{}
		<-release
		return &agent.RequestAuth{APIKey: &key}, nil
	})
	model := mode.session.State().Model
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			mode.maybeWarnAboutAnthropicSubscriptionAuth(context.Background(), model)
		}()
	}
	for range workers {
		<-started
	}
	close(release)
	wait.Wait()

	rendered := selectorANSI.ReplaceAllString(strings.Join(mode.chat.Render(160), "\n"), "")
	if count := strings.Count(rendered, "Warning: Anthropic subscription auth is active."); count != 1 {
		t.Fatalf("warning rendered %d times, want once:\n%s", count, rendered)
	}
}

// LOG-M4: warning detection follows the effective runtime credential. A
// runtime API-key override owns Anthropic even when auth.json still contains
// OAuth, so a normal API key must suppress the subscription warning.
func TestLOGM4AnthropicWarningUsesEffectiveRuntimeCredential(t *testing.T) {
	host := &authFlowHost{options: InteractiveAuthOptions{Logout: []InteractiveAuthProvider{{
		ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth,
	}}}}
	mode := newAnthropicWarningTestMode(t, "sk-ant-api-runtime-override", host)
	model := mode.session.State().Model
	if model == nil || model.Provider != "anthropic" {
		t.Fatalf("warning precondition model = %#v", model)
	}

	mode.maybeWarnAboutAnthropicSubscriptionAuth(context.Background(), model)

	if anthropicWarningShown(mode) {
		t.Fatal("stored OAuth overrode the effective runtime API key")
	}
	rendered := selectorANSI.ReplaceAllString(strings.Join(mode.chat.Render(160), "\n"), "")
	if strings.Contains(rendered, anthropicSubscriptionAuthWarning) {
		t.Fatalf("stored OAuth overrode the effective runtime API key:\n%s", rendered)
	}
}

// LOG-M4: upstream checkAuth reads stored OAuth without refreshing it before
// resolving a request key. A failed refresh therefore cannot hide the warning.
func TestLOGM4AnthropicWarningSurvivesOAuthRefreshFailure(t *testing.T) {
	host := &authFlowHost{options: InteractiveAuthOptions{Logout: []InteractiveAuthProvider{{
		ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth,
	}}}}
	mode := newAnthropicWarningTestModeWithResolver(t, host, func(context.Context, ai.ProviderID) (*agent.RequestAuth, error) {
		return nil, errors.New("refresh failed")
	})

	mode.maybeWarnAboutAnthropicSubscriptionAuth(context.Background(), mode.session.State().Model)

	if !anthropicWarningShown(mode) {
		t.Fatal("stored OAuth warning was hidden by a refresh failure")
	}
}

// LOG-M4: an exact /model change immediately checks the newly active model,
// matching handleModelCommand's upstream warning call site.
func TestLOGM4DirectModelChangeWarnsForAnthropicSubscriptionAuth(t *testing.T) {
	mode := newAnthropicWarningTestMode(t, "sk-ant-oat-model-command", &authFlowHost{})
	if err := mode.session.SetModel(context.Background(), ai.Model{Provider: "openai", ID: "gpt-5.5"}); err != nil {
		t.Fatal(err)
	}

	mode.handleModelCommand("anthropic/claude-opus-4-8")

	if !anthropicWarningShown(mode) {
		t.Fatal("direct model change did not run the Anthropic warning")
	}
}

// LOG-M4: selecting a model from the interactive model dialog checks the
// selected model after SetModel succeeds.
func TestLOGM4ModelSelectorWarnsForAnthropicSubscriptionAuth(t *testing.T) {
	mode := newAnthropicWarningTestMode(t, "sk-ant-oat-model-selector", &authFlowHost{})
	if err := mode.session.SetModel(context.Background(), ai.Model{Provider: "openai", ID: "gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	mode.interactiveUI = NewInteractiveUI(mode)

	mode.showModelSelector("")
	var selector *ExtensionSelectorComponent
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mode.interactiveUI.mu.Lock()
		selector = mode.interactiveUI.activeSelector
		mode.interactiveUI.mu.Unlock()
		if selector != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if selector == nil {
		t.Fatal("model selector did not appear")
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\r"})
	for time.Now().Before(deadline) && !anthropicWarningShown(mode) {
		time.Sleep(time.Millisecond)
	}
	if !anthropicWarningShown(mode) {
		t.Fatal("model selector did not run the Anthropic warning")
	}
}

// LOG-M4: both model-cycle actions check the model returned by the runtime,
// matching upstream cycleModel(direction).
func TestLOGM4ModelCycleWarnsForAnthropicSubscriptionAuth(t *testing.T) {
	tests := []struct {
		name   string
		action string
		key    tui.KeyID
		raw    string
	}{
		{name: "forward", action: "app.model.cycleForward", key: "ctrl+p", raw: "\x10"},
		{name: "backward", action: "app.model.cycleBackward", key: "ctrl+b", raw: "\x02"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode := newAnthropicWarningTestMode(t, "sk-ant-oat-model-cycle", &authFlowHost{})
			if err := mode.session.SetModel(context.Background(), ai.Model{Provider: "openai", ID: "gpt-5.5"}); err != nil {
				t.Fatal(err)
			}
			mode.keybindings = NewAppKeybindings(tui.KeybindingsConfig{test.action: []tui.KeyID{test.key}})
			mode.editor = NewCustomEditor(mode.ui, tui.EditorTheme{}, mode.keybindings)
			mode.setupKeyHandlers()

			mode.editor.HandleInput(tui.KeyEvent{Raw: test.raw})

			selected := mode.session.State().Model
			if selected == nil || selected.Provider != "anthropic" {
				t.Fatalf("%s model cycle selected %#v", test.name, selected)
			}
			if !anthropicWarningShown(mode) {
				t.Fatalf("%s model cycle did not run the Anthropic warning", test.name)
			}
		})
	}
}

// LOG-M4: successful login completion checks the resulting active model
// through the same warning path as startup and model changes.
func TestLOGM4LoginCompletionWarnsForAnthropicSubscriptionAuth(t *testing.T) {
	mode := newAnthropicWarningTestMode(t, "sk-ant-oat-login-completion", &authFlowHost{})

	mode.completeProviderAuthentication(context.Background(), InteractiveAuthProvider{
		ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth,
	}, mode.session.State().Model)

	if !anthropicWarningShown(mode) {
		t.Fatal("login completion did not run the Anthropic warning")
	}
}

func waitForAuthSelector(t *testing.T, container *tui.Container) *OAuthSelectorComponent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, child := range container.Children() {
			if component, ok := child.(*OAuthSelectorComponent); ok {
				return component
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("provider selector never appeared")
	return nil
}

// LOG-M3: /login with a non-exact fuzzy ref opens the provider selector
// pre-filtered with the ref as search text instead of erroring (upstream
// interactive-mode.ts:4849-4873).
func TestLOGM3LoginUnknownRefOpensPrefilteredSelector(t *testing.T) {
	host := &authFlowHost{
		options: InteractiveAuthOptions{Login: []InteractiveAuthProvider{
			{ID: "groq", Name: "Groq", AuthType: aiauth.AuthTypeAPIKey, LoginAvailable: true},
			{ID: "openai", Name: "OpenAI", AuthType: aiauth.AuthTypeAPIKey, LoginAvailable: true},
		}},
		loginProvider: make(chan string, 1),
	}
	mode := newAuthFlowTestMode(host)

	mode.handleLoginCommand("gro")
	component := waitForAuthSelector(t, mode.editorContainer)
	if got := component.searchInput.GetValue(); got != "gro" {
		t.Fatalf("selector search = %q, want pre-filled ref", got)
	}
	if len(component.filteredProviders) != 1 || component.filteredProviders[0].ID != "groq" {
		t.Fatalf("pre-filtered providers = %#v", component.filteredProviders)
	}
	component.HandleInput(tui.KeyEvent{Raw: "\r"})
	select {
	case provider := <-host.loginProvider:
		if provider != "groq" {
			t.Fatalf("login started for %q", provider)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("confirming the selection did not start the login")
	}
}

// LOG-m2: logout completion strings match upstream
// interactive-mode.ts:5013-5020.
func TestLOGm2LogoutMessagesMatchUpstream(t *testing.T) {
	host := &authFlowHost{}
	mode := newAuthFlowTestMode(host)

	mode.runLogout(InteractiveAuthProvider{ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth})
	rendered := selectorANSI.ReplaceAllString(strings.Join(mode.chat.Render(120), "\n"), "")
	if !strings.Contains(rendered, "Logged out of Anthropic") {
		t.Fatalf("oauth logout message missing:\n%s", rendered)
	}

	mode.runLogout(InteractiveAuthProvider{ID: "groq", Name: "Groq", AuthType: aiauth.AuthTypeAPIKey})
	rendered = selectorANSI.ReplaceAllString(strings.Join(mode.chat.Render(120), "\n"), "")
	if !strings.Contains(rendered, "Removed stored API key for Groq. Environment variables and models.json config are unchanged.") {
		t.Fatalf("api-key logout message missing:\n%s", rendered)
	}

	failing := newAuthFlowTestMode(&authFlowHost{logoutErr: errors.New("boom")})
	failing.runLogout(InteractiveAuthProvider{ID: "groq", Name: "Groq", AuthType: aiauth.AuthTypeAPIKey})
	rendered = selectorANSI.ReplaceAllString(strings.Join(failing.chat.Render(120), "\n"), "")
	if !strings.Contains(rendered, "Error: Logout failed: boom") {
		t.Fatalf("logout failure message missing:\n%s", rendered)
	}
}

// LOG-m4: the amazon-bedrock guidance belongs to the ambient login component,
// then disappears with that component instead of becoming chat history
// (upstream interactive-mode.ts:5121-5127).
func TestLOGm4BedrockGuidanceUsesTemporaryLoginUI(t *testing.T) {
	host := &bedrockPromptHost{promptStarted: make(chan struct{})}
	mode := newAnthropicWarningTestMode(t, "sk-ant-api-test", host)
	mode.interactiveUI = NewInteractiveUI(mode)
	done := make(chan struct{})
	go func() {
		defer close(done)
		mode.runLogin(InteractiveAuthProvider{
			ID: "amazon-bedrock", Name: "Amazon Bedrock", AuthType: aiauth.AuthTypeAPIKey, LoginAvailable: true,
		})
	}()
	<-host.promptStarted

	var input *ExtensionInputComponent
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		children := mode.editorContainer.Children()
		if len(children) == 1 {
			input, _ = children[0].(*ExtensionInputComponent)
			if input != nil {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if input == nil {
		t.Fatal("Bedrock login input did not become active")
	}
	guidance := "You can also use an AWS profile, IAM keys, or role-based credentials."
	ambient := renderPlain(t, mode.editorContainer, 160)
	if !strings.Contains(ambient, guidance) || !strings.Contains(ambient, "providers.md") {
		t.Errorf("Bedrock guidance is not in the temporary login UI:\n%s", ambient)
	}
	if chat := renderPlain(t, mode.chat, 160); strings.Contains(chat, guidance) || strings.Contains(chat, "providers.md") {
		t.Errorf("Bedrock guidance leaked into chat history:\n%s", chat)
	}

	input.HandleInput(tui.KeyEvent{Raw: "token"})
	input.HandleInput(tui.KeyEvent{Raw: "\r"})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Bedrock login did not complete")
	}
	if ambient = renderPlain(t, mode.editorContainer, 160); strings.Contains(ambient, guidance) || strings.Contains(ambient, "providers.md") {
		t.Errorf("Bedrock guidance remained after the login UI closed:\n%s", ambient)
	}
	if chat := renderPlain(t, mode.chat, 160); strings.Contains(chat, guidance) || strings.Contains(chat, "providers.md") {
		t.Errorf("Bedrock guidance persisted in chat after login:\n%s", chat)
	}
}

// LOG-M4: default-model selection diagnostics after login match upstream
// completeProviderAuthentication (interactive-mode.ts:5040-5069).
func TestLOGM4ResolveDefaultModelSelectionDiagnostics(t *testing.T) {
	label := "Logged in to Anthropic"
	if _, message := resolveDefaultModelSelection(label, "no-such-provider", nil); message != `Logged in to Anthropic, but no default model is configured for provider "no-such-provider". Use /model to select a model.` {
		t.Fatalf("missing-default message = %q", message)
	}
	if _, message := resolveDefaultModelSelection(label, "anthropic", nil); message != "Logged in to Anthropic, but no models are available for that provider. Use /model to select a model." {
		t.Fatalf("no-models message = %q", message)
	}
	other := []ai.Model{{Provider: "anthropic", ID: "claude-elsewhere"}}
	if _, message := resolveDefaultModelSelection(label, "anthropic", other); message != `Logged in to Anthropic, but its default model "claude-opus-4-8" is not available. Use /model to select a model.` {
		t.Fatalf("default-unavailable message = %q", message)
	}
	available := []ai.Model{{Provider: "openai", ID: "gpt-5.5"}, {Provider: "anthropic", ID: "claude-opus-4-8"}}
	selected, message := resolveDefaultModelSelection(label, "anthropic", available)
	if message != "" || selected == nil || selected.ID != "claude-opus-4-8" || selected.Provider != "anthropic" {
		t.Fatalf("selection = %#v, message = %q", selected, message)
	}
}

// LOG-M4: post-login completion messages match upstream
// interactive-mode.ts:5071-5083.
func TestLOGM4CompletionMessagesAndWarningText(t *testing.T) {
	mode := newAuthFlowTestMode(&authFlowHost{})
	mode.completeProviderAuthentication(context.Background(),
		InteractiveAuthProvider{ID: "groq", Name: "Groq", AuthType: aiauth.AuthTypeAPIKey}, nil)
	rendered := selectorANSI.ReplaceAllString(strings.Join(mode.chat.Render(160), "\n"), "")
	if !strings.Contains(rendered, "Saved API key for Groq. Credentials saved to ") {
		t.Fatalf("api-key completion message missing:\n%s", rendered)
	}

	mode = newAuthFlowTestMode(&authFlowHost{})
	mode.completeProviderAuthentication(context.Background(),
		InteractiveAuthProvider{ID: "anthropic", Name: "Anthropic", AuthType: aiauth.AuthTypeOAuth}, nil)
	rendered = selectorANSI.ReplaceAllString(strings.Join(mode.chat.Render(160), "\n"), "")
	if !strings.Contains(rendered, "Logged in to Anthropic. Credentials saved to ") {
		t.Fatalf("oauth completion message missing:\n%s", rendered)
	}

	want := "Anthropic subscription auth is active. Third-party harness usage draws from extra usage and is billed per token, not your Claude plan limits. Manage extra usage at https://claude.ai/settings/usage. Disable this warning in /settings."
	if anthropicSubscriptionAuthWarning != want {
		t.Fatalf("warning text = %q", anthropicSubscriptionAuthWarning)
	}
	if !isAnthropicSubscriptionAuthKey("sk-ant-oat-1234") || isAnthropicSubscriptionAuthKey("sk-ant-api-1234") {
		t.Fatal("subscription key detection diverged from upstream")
	}
}

// LOG-M1: the interactive login flow auto-opens the browser when the OAuth
// flow publishes its auth URL (upstream login-dialog.ts:111), and only then.
func TestLOGM1AuthURLEventOpensBrowser(t *testing.T) {
	var opened []string
	original := openAuthURLInBrowser
	openAuthURLInBrowser = func(target string) { opened = append(opened, target) }
	defer func() { openAuthURLInBrowser = original }()

	interaction := tuiAuthInteraction{mode: newAuthFlowTestMode(&authFlowHost{})}
	interaction.Notify(aiauth.AuthEvent{Type: aiauth.EventAuthURL, URL: "https://example.test/auth", Instructions: "Open the URL"})
	if len(opened) != 1 || opened[0] != "https://example.test/auth" {
		t.Fatalf("opened = %#v", opened)
	}
	interaction.Notify(aiauth.AuthEvent{Type: aiauth.EventDeviceCode, VerificationURI: "https://example.test/device", UserCode: "CODE"})
	interaction.Notify(aiauth.AuthEvent{Type: aiauth.EventProgress, Message: "working"})
	if len(opened) != 1 {
		t.Fatalf("non-auth_url events opened the browser: %#v", opened)
	}
}

// LOG-m4: ambient providers get a titled dialog with a close hint (upstream
// interactive-mode.ts:5086-5107).
func TestLOGm4AmbientAuthDialogTitleMessageAndClose(t *testing.T) {
	closedCount := 0
	dialog := newAmbientAuthDialogComponent("Amazon Bedrock setup", "Bedrock credential chain is configured outside pigo.", func() { closedCount++ })
	rendered := selectorANSI.ReplaceAllString(strings.Join(dialog.Render(100), "\n"), "")
	if !strings.Contains(rendered, "Amazon Bedrock setup") {
		t.Fatalf("dialog title missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Bedrock credential chain is configured outside pigo.") {
		t.Fatalf("dialog message missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "to close") {
		t.Fatalf("close hint missing:\n%s", rendered)
	}
	dialog.HandleInput(tui.KeyEvent{Raw: "x"})
	if closedCount != 0 {
		t.Fatal("non-cancel input closed the dialog")
	}
	dialog.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	if closedCount != 1 {
		t.Fatalf("cancel input closed the dialog %d times", closedCount)
	}
}
