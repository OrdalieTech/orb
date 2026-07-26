package providers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/providers"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type anthropicProviderFixture struct {
	ID      ai.ProviderID `json:"id"`
	Name    string        `json:"name"`
	BaseURL string        `json:"baseUrl"`
	APIs    []ai.API      `json:"apis"`
	Auth    struct {
		Kind      providers.AuthKind `json:"kind"`
		Name      string             `json:"name"`
		OAuthName string             `json:"oauthName"`
		Env       []string           `json:"env"`
		Resolved  json.RawMessage    `json:"resolved"`
		Source    string             `json:"source"`
	} `json:"auth"`
}

// anthropicEnvRecorder mirrors the fixture extraction's unresolvable context:
// it records which env names the resolver probes, in order.
type anthropicEnvRecorder struct{ names *[]string }

func (recorder anthropicEnvRecorder) Env(_ context.Context, name string) (string, bool) {
	*recorder.names = append(*recorder.names, name)
	return "", false
}

func (anthropicEnvRecorder) FileExists(context.Context, string) bool { return false }

type anthropicEnvMap map[string]string

func (values anthropicEnvMap) Env(_ context.Context, name string) (string, bool) {
	value, ok := values[name]
	return value, ok && value != ""
}

func (anthropicEnvMap) FileExists(context.Context, string) bool { return false }

func TestAnthropicProvider(t *testing.T) {
	var fixture anthropicProviderFixture
	runner.LoadJSON(t, "F2", "anthropic-provider.json", &fixture)
	if len(fixture.APIs) != 1 {
		t.Fatalf("upstream Anthropic provider API shapes = %v, want exactly one", fixture.APIs)
	}
	provider, ok := providers.Get(fixture.ID)
	if !ok {
		t.Fatalf("%s provider is not registered", fixture.ID)
	}
	if provider.ID != fixture.ID || provider.Name != fixture.Name || provider.API != fixture.APIs[0] || provider.BaseURL != fixture.BaseURL {
		t.Fatalf("unexpected provider: %#v", provider)
	}
	if provider.Auth != fixture.Auth.Kind || !slices.Equal(provider.Env, fixture.Auth.Env) {
		t.Fatalf("unexpected auth metadata: %#v", provider)
	}
	if provider.Methods.APIKey == nil || provider.Methods.APIKey.Name() != fixture.Auth.Name || provider.Methods.OAuth == nil || provider.Methods.OAuth.Name() != fixture.Auth.OAuthName {
		t.Fatalf("unexpected auth methods: %#v", provider.Methods)
	}

	ctx := context.Background()
	method := provider.Methods.APIKey
	probed := []string{}
	unresolved, err := method.Resolve(ctx, anthropicEnvRecorder{names: &probed}, nil)
	if err != nil || unresolved != nil {
		t.Fatalf("resolve without credentials = %#v, %v", unresolved, err)
	}
	if !slices.Equal(probed, fixture.Auth.Env) {
		t.Fatalf("env probe order = %v, want %v", probed, fixture.Auth.Env)
	}

	// The fixture extraction resolves with every env var answering the same
	// value: ANTHROPIC_AUTH_TOKEN wins and yields bearer headers.
	resolved, err := method.Resolve(ctx, anthropicEnvMap{
		"ANTHROPIC_AUTH_TOKEN":  "fixture-anthropic-api-key",
		"ANTHROPIC_OAUTH_TOKEN": "fixture-anthropic-api-key",
		"ANTHROPIC_API_KEY":     "fixture-anthropic-api-key",
	}, nil)
	if err != nil || resolved == nil || resolved.Source != fixture.Auth.Source {
		t.Fatalf("resolved = %#v, %v, want source %q", resolved, err, fixture.Auth.Source)
	}
	actualAuth, err := json.Marshal(resolved.Auth)
	if err != nil {
		t.Fatal(err)
	}
	wantAuth, err := runner.CanonicalJSON(fixture.Auth.Resolved)
	if err != nil {
		t.Fatal(err)
	}
	gotAuth, err := runner.CanonicalJSON(actualAuth)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotAuth, wantAuth) {
		t.Fatalf("resolved auth = %s, want %s", actualAuth, fixture.Auth.Resolved)
	}

	// Without ANTHROPIC_AUTH_TOKEN the OAuth token keeps precedence over the
	// API key, both as plain apiKey auth.
	oauthResolved, err := method.Resolve(ctx, anthropicEnvMap{
		"ANTHROPIC_OAUTH_TOKEN": "oauth-token",
		"ANTHROPIC_API_KEY":     "api-key",
	}, nil)
	if err != nil || oauthResolved == nil || oauthResolved.Auth.APIKey == nil ||
		*oauthResolved.Auth.APIKey != "oauth-token" || oauthResolved.Auth.Headers != nil || oauthResolved.Source != "ANTHROPIC_OAUTH_TOKEN" {
		t.Fatalf("OAuth-token result = %#v, %v", oauthResolved, err)
	}

	// A stored credential still wins outright, even over ANTHROPIC_AUTH_TOKEN.
	stored, err := method.Resolve(ctx, anthropicEnvMap{"ANTHROPIC_AUTH_TOKEN": "ambient"}, auth.APIKeyCredential("stored-key"))
	if err != nil || stored == nil || stored.Auth.APIKey == nil || *stored.Auth.APIKey != "stored-key" || stored.Source != "stored credential" {
		t.Fatalf("stored result = %#v, %v", stored, err)
	}

	provider.Env[0] = "changed"
	if fresh := providers.Anthropic(); !slices.Equal(fresh.Env, fixture.Auth.Env) {
		t.Fatal("Anthropic returned mutable registry storage")
	}
	if !slices.ContainsFunc(providers.List(), func(provider providers.Provider) bool { return provider.ID == fixture.ID }) {
		t.Fatalf("registered providers do not contain %q", fixture.ID)
	}
}
