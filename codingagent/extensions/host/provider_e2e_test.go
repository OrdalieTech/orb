package host

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

type providerTestAuthContext map[string]string

func (authContext providerTestAuthContext) Env(_ context.Context, name string) (string, bool) {
	value, ok := authContext[name]
	return value, ok
}

func (providerTestAuthContext) FileExists(context.Context, string) bool { return false }

type providerTestInteraction struct {
	mu      sync.Mutex
	prompts []aiauth.AuthPrompt
	events  []aiauth.AuthEvent
	answer  string
}

func (interaction *providerTestInteraction) Prompt(_ context.Context, prompt aiauth.AuthPrompt) (string, error) {
	interaction.mu.Lock()
	interaction.prompts = append(interaction.prompts, prompt)
	interaction.mu.Unlock()
	return interaction.answer, nil
}

func (interaction *providerTestInteraction) Notify(event aiauth.AuthEvent) {
	interaction.mu.Lock()
	interaction.events = append(interaction.events, event)
	interaction.mu.Unlock()
}

func TestRealHostRegistersProviderAuthCallbacksAndRecoversAfterRestart(t *testing.T) {
	runtime := requireRuntime(t)
	cwd := t.TempDir()
	var diagnosticsMu sync.Mutex
	var diagnostics []extensions.Diagnostic
	manager := NewManager(Options{
		AgentDir: t.TempDir(), CWD: cwd, Version: "test", Runtime: &runtime,
		RequestTimeout: 5 * time.Second, ShutdownTimeout: time.Second,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		OnDiagnostic: func(diagnostic extensions.Diagnostic) {
			diagnosticsMu.Lock()
			diagnostics = append(diagnostics, diagnostic)
			diagnosticsMu.Unlock()
		},
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})

	modelRegistry, err := config.NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extensionRegistry := extensions.NewRegistry(cwd)
	var registrationErrors []extensions.ExtensionError
	extensionRegistry.BindModelRegistry(modelRegistry, func(value extensions.ExtensionError) {
		registrationErrors = append(registrationErrors, value)
	})
	result := manager.RegisterInto(context.Background(), extensionRegistry, []string{fixturePath(t, "provider.mjs")})
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 || len(registrationErrors) != 0 {
		t.Fatalf("load result = %#v, registration errors = %#v", result, registrationErrors)
	}

	provider, ok := modelRegistry.RegisteredNativeProvider("host-provider")
	if !ok || provider.Name != "Host Provider" {
		t.Fatalf("registered provider = %#v, %v", provider, ok)
	}
	models, err := provider.GetModels()
	if err != nil || len(models) != 1 || models[0].ID != "host-model" || models[0].API != ai.API("openai-responses") {
		t.Fatalf("provider models = %#v, %v", models, err)
	}
	stream, err := provider.StreamSimple(context.Background(), &models[0], ai.Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, streamErr := range stream {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		t.Fatal("empty fixture stream emitted an event")
	}

	methods := modelRegistry.ProviderAuth("host-provider")
	resolved, err := methods.APIKey.Resolve(context.Background(), providerTestAuthContext{"HOST_PROVIDER_KEY": "ambient-key"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.Auth.APIKey == nil || *resolved.Auth.APIKey != "ambient-key" || resolved.Source != "HOST_PROVIDER_KEY" {
		t.Fatalf("resolved auth = %#v", resolved)
	}
	checkMethod, ok := methods.APIKey.(aiauth.APIKeyCheck)
	if !ok {
		t.Fatal("provider api-key auth has no check callback")
	}
	checked, err := checkMethod.Check(context.Background(), providerTestAuthContext{"HOST_PROVIDER_KEY": "ambient-key"}, nil)
	if err != nil || checked == nil || checked.Type != aiauth.CredentialAPIKey {
		t.Fatalf("checked auth = %#v, %v", checked, err)
	}

	derived, err := methods.OAuth.ToAuth(aiauth.OAuthCredential("refresh", "access", 4102444800000))
	if err != nil {
		t.Fatal(err)
	}
	if derived.APIKey == nil || *derived.APIKey != "oauth:access" || derived.BaseURL == nil || *derived.BaseURL != "https://oauth.provider.invalid/v1" {
		t.Fatalf("derived oauth auth = %#v", derived)
	}
	refreshed, err := methods.OAuth.Refresh(context.Background(), aiauth.OAuthCredential("refresh", "access", 1))
	if err != nil || refreshed.Access != "access:refreshed" {
		t.Fatalf("refreshed oauth credential = %#v, %v", refreshed, err)
	}

	_, err = methods.APIKey.Resolve(context.Background(), providerTestAuthContext{}, aiauth.APIKeyCredential("throw"))
	var invokeError *ProviderInvokeError
	if !errors.As(err, &invokeError) || invokeError.Retryable() {
		t.Fatalf("extension callback error = %#v", err)
	}
	diagnosticsMu.Lock()
	stackReported := len(diagnostics) > 0 && strings.Contains(diagnostics[len(diagnostics)-1].Message, "wire resolve failed") && strings.Contains(diagnostics[len(diagnostics)-1].Message, "provider.mjs")
	diagnosticsMu.Unlock()
	if !stackReported {
		t.Fatalf("provider callback stack diagnostics = %#v", diagnostics)
	}

	_, err = methods.APIKey.Resolve(context.Background(), providerTestAuthContext{}, aiauth.APIKeyCredential("crash"))
	if !errors.As(err, &invokeError) || !invokeError.Retryable() {
		t.Fatalf("crashed in-flight callback error = %#v", err)
	}
	waitForProviderRestart(t, manager)

	resolved, err = methods.APIKey.Resolve(context.Background(), providerTestAuthContext{"HOST_PROVIDER_KEY": "after-restart"}, nil)
	if err != nil || resolved == nil || resolved.Auth.APIKey == nil || *resolved.Auth.APIKey != "after-restart" {
		t.Fatalf("resolved auth after restart = %#v, %v", resolved, err)
	}
	registeredIDs := modelRegistry.RegisteredProviderIDs()
	if len(registeredIDs) != 1 || registeredIDs[0] != "host-provider" {
		t.Fatalf("provider ids after restart = %#v", registeredIDs)
	}

	interaction := &providerTestInteraction{answer: "pasted-code"}
	credential, err := methods.OAuth.Login(context.Background(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Access != "access:pasted-code" || credential.Refresh != "refresh:pasted-code" {
		t.Fatalf("oauth credential = %#v", credential)
	}
	interaction.mu.Lock()
	defer interaction.mu.Unlock()
	if len(interaction.events) != 1 || interaction.events[0].Type != aiauth.EventAuthURL || interaction.events[0].URL != "https://provider.invalid/oauth" {
		t.Fatalf("oauth interaction events = %#v", interaction.events)
	}
	if len(interaction.prompts) != 1 || interaction.prompts[0].Type != aiauth.PromptManualCode {
		t.Fatalf("oauth interaction prompts = %#v", interaction.prompts)
	}
}

func waitForProviderRestart(t *testing.T, manager *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for manager.RestartCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if manager.RestartCount() == 0 {
		t.Fatal("manager did not restart the crashed provider host")
	}
}
