package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

func TestInteractiveHostRefreshesAuthInPlaceAndPreservesExtensionProviders(t *testing.T) {
	fixture := newHostFixture(t)
	modelsPath := filepath.Join(fixture.agentDir, "models.json")
	if err := os.MkdirAll(fixture.agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"local":{"baseUrl":"http://localhost/v1","api":"openai-completions","apiKey":"dummy","models":[{"id":"stable"}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(fixture.agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("local", "stable"); !ok {
		t.Fatal("initial models.json model is missing")
	}
	stream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return nil, nil
	}
	if err := registry.RegisterProvider(extensions.Provider{
		ID: "extension-auth", Name: "Extension Auth",
		Auth: aiauth.ProviderAuth{APIKey: fixedInteractiveAPIKeyAuth{key: "extension-secret"}},
		GetModels: func() ([]ai.Model, error) {
			return []ai.Model{{ID: "extension-model", Provider: "extension-auth", API: ai.APIOpenAIResponses}}, nil
		},
		Stream: stream, StreamSimple: stream,
	}); err != nil {
		t.Fatal(err)
	}
	storage, err := config.NewAuthStorage(filepath.Join(fixture.agentDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.host.mu.Lock()
	fixture.host.inputs.ModelRegistry = registry
	fixture.host.inputs.Auth = storage
	fixture.host.mu.Unlock()
	if err := os.WriteFile(modelsPath, []byte(`{"providers":`), 0o600); err != nil {
		t.Fatal(err)
	}

	original := fixture.host.Session()
	createCalls := fixture.createCalls
	fixture.recorder.events = nil
	if err := fixture.host.Login(context.Background(), "extension-auth", aiauth.AuthTypeAPIKey, fixedPromptInteraction{}); err != nil {
		t.Fatal(err)
	}

	if !registry.GetProviderAuthStatus("extension-auth", nil).Configured {
		t.Fatal("model registry did not observe the stored credential")
	}
	if _, ok := registry.Provider("extension-auth"); !ok {
		t.Fatal("auth refresh discarded the registered extension provider")
	}
	if _, ok := registry.Find("local", "stable"); !ok || registry.Error() != "" {
		t.Fatalf("auth refresh reloaded unrelated model config: error=%q", registry.Error())
	}
	if fixture.host.Session() != original || fixture.createCalls != createCalls {
		t.Fatal("auth replaced the interactive SessionRuntime")
	}
	if shutdown := fixture.recorder.byKind("session_shutdown"); len(shutdown) != 0 {
		t.Fatalf("auth emitted session shutdown events: %#v", shutdown)
	}
}
