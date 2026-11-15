package providers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/conformance/runner"
)

type envMap map[string]string

func (env envMap) Env(_ context.Context, name string) (string, bool) {
	value, ok := env[name]
	return value, ok
}

func (envMap) FileExists(context.Context, string) bool { return false }

// envRecorder mirrors the fixture extraction's unresolvable context: it
// records which env names the resolver probes, in order.
type envRecorder struct{ names *[]string }

func (recorder envRecorder) Env(_ context.Context, name string) (string, bool) {
	*recorder.names = append(*recorder.names, name)
	return "", false
}

func (envRecorder) FileExists(context.Context, string) bool { return false }

func TestSingleProviderGoldens(t *testing.T) {
	for _, file := range []string{"provider.json", "anthropic-provider.json", "google-provider.json"} {
		t.Run(file, func(t *testing.T) {
			var fixture struct {
				ID      ai.ProviderID `json:"id"`
				Name    string        `json:"name"`
				BaseURL string        `json:"baseUrl"`
				APIs    []ai.API      `json:"apis"`
				Auth    struct {
					Kind      string          `json:"kind"`
					Name      string          `json:"name"`
					OAuthName string          `json:"oauthName"`
					Env       []string        `json:"env"`
					Resolved  json.RawMessage `json:"resolved"`
					Source    string          `json:"source"`
				} `json:"auth"`
			}
			runner.LoadJSON(t, "F2", file, &fixture)
			provider := mustProvider(t, fixture.ID)
			if provider.Name != fixture.Name || provider.BaseURL != fixture.BaseURL || !slices.Equal(provider.APIs, fixture.APIs) || provider.API != fixture.APIs[0] {
				t.Fatalf("provider = %#v", provider)
			}
			if authKind(provider) != fixture.Auth.Kind || !slices.Equal(provider.Env, fixture.Auth.Env) {
				t.Fatalf("auth metadata = %#v", provider)
			}
			method := provider.Methods.APIKey
			if method == nil || method.Name() != fixture.Auth.Name {
				t.Fatalf("API-key method = %#v", method)
			}
			if fixture.Auth.OAuthName != "" && (provider.Methods.OAuth == nil || provider.Methods.OAuth.Name() != fixture.Auth.OAuthName) {
				t.Fatalf("OAuth method = %#v", provider.Methods.OAuth)
			}

			ctx := context.Background()
			probed := []string{}
			if unresolved, err := method.Resolve(ctx, envRecorder{names: &probed}, nil); err != nil || unresolved != nil {
				t.Fatalf("resolve without credentials = %#v, %v", unresolved, err)
			}
			if !slices.Equal(probed, fixture.Auth.Env) {
				t.Fatalf("env probe order = %v, want %v", probed, fixture.Auth.Env)
			}
			// The fixture extraction answers every env name with one value.
			env := envMap{}
			for _, name := range fixture.Auth.Env {
				env[name] = "fixture-" + string(fixture.ID) + "-api-key"
			}
			resolved, err := method.Resolve(ctx, env, nil)
			if err != nil || resolved == nil || resolved.Source != fixture.Auth.Source {
				t.Fatalf("resolved = %#v, %v, want source %q", resolved, err, fixture.Auth.Source)
			}
			actual, err := json.Marshal(resolved.Auth)
			if err != nil {
				t.Fatal(err)
			}
			got, err := runner.CanonicalJSON(actual)
			if err != nil {
				t.Fatal(err)
			}
			want, err := runner.CanonicalJSON(fixture.Auth.Resolved)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("resolved auth = %s, want %s", actual, fixture.Auth.Resolved)
			}
		})
	}
}

func TestSubscriptionProviderGoldens(t *testing.T) {
	var fixture struct {
		Providers []struct {
			ID      ai.ProviderID `json:"id"`
			Name    string        `json:"name"`
			BaseURL string        `json:"baseUrl"`
			APIs    []ai.API      `json:"apis"`
			Auth    struct {
				APIKeyName string `json:"apiKeyName"`
				OAuthName  string `json:"oauthName"`
			} `json:"auth"`
		} `json:"providers"`
	}
	runner.LoadJSON(t, "F2", "subscription-providers.json", &fixture)
	for _, expected := range fixture.Providers {
		t.Run(string(expected.ID), func(t *testing.T) {
			provider := mustProvider(t, expected.ID)
			if provider.Name != expected.Name || provider.BaseURL != expected.BaseURL || !slices.Equal(provider.APIs, expected.APIs) {
				t.Fatalf("provider = %#v", provider)
			}
			if provider.Methods.OAuth == nil || provider.Methods.OAuth.Name() != expected.Auth.OAuthName {
				t.Fatalf("OAuth method = %#v", provider.Methods.OAuth)
			}
			if expected.Auth.APIKeyName == "" {
				if provider.Methods.APIKey != nil {
					t.Fatalf("API-key method = %#v, want nil", provider.Methods.APIKey)
				}
			} else if provider.Methods.APIKey == nil || provider.Methods.APIKey.Name() != expected.Auth.APIKeyName {
				t.Fatalf("API-key method = %#v", provider.Methods.APIKey)
			}
		})
	}
}
