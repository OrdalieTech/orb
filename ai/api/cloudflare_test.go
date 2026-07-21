package api

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

func TestCloudflarePreparationMatchesPinnedProviderFixture(t *testing.T) {
	var fixture struct {
		Cloudflare []struct {
			Provider        ai.ProviderID  `json:"provider"`
			BaseURL         string         `json:"baseUrl"`
			Env             ai.ProviderEnv `json:"env"`
			ResolvedBaseURL string         `json:"resolvedBaseUrl"`
			Auth            struct {
				APIKey  string             `json:"apiKey"`
				Headers ai.ProviderHeaders `json:"headers"`
			} `json:"auth"`
		} `json:"cloudflare"`
	}
	runner.LoadJSON(t, "F2", "providers.json", &fixture)
	for _, item := range fixture.Cloudflare {
		t.Run(string(item.Provider), func(t *testing.T) {
			// Auth-resolve (ai/providers) produces the fixture auth shape; the
			// stream-time preparation only resolves endpoint placeholders and
			// must pass the resolved auth through untouched. (OT-CF)
			model := &ai.Model{Provider: item.Provider, BaseURL: item.BaseURL}
			options := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{Env: item.Env, Headers: item.Auth.Headers}}
			if item.Auth.APIKey != "" {
				key := item.Auth.APIKey
				options.APIKey = &key
			}
			gotModel, gotOptions := prepareCloudflareRequest(model, options)
			if gotModel.BaseURL != item.ResolvedBaseURL {
				t.Fatalf("resolved base URL = %q, want %q", gotModel.BaseURL, item.ResolvedBaseURL)
			}
			if gotOptions != options {
				t.Fatalf("stream-time preparation replaced the options: %#v", gotOptions)
			}
			if !providerHeadersEqual(gotOptions.Headers, item.Auth.Headers) {
				t.Fatalf("resolved headers = %#v, want %#v", gotOptions.Headers, item.Auth.Headers)
			}
			if item.Auth.APIKey != "" && (gotOptions.APIKey == nil || *gotOptions.APIKey != item.Auth.APIKey) {
				t.Fatalf("resolved API key = %#v, want %q", gotOptions.APIKey, item.Auth.APIKey)
			}
		})
	}
}

// OT-CF: upstream cloudflare-stream.ts resolves placeholders from options.env
// only; stream-time preparation must not rewrite auth or consult os.Getenv.
func TestPrepareCloudflareGatewayRequestLeavesAuthUntouched(t *testing.T) {
	key := "fixture-key"
	keep := "keep"
	model := &ai.Model{
		Provider: "cloudflare-ai-gateway",
		BaseURL:  "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai",
	}
	options := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		APIKey: &key,
		Env: ai.ProviderEnv{
			cloudflareAccountID: "account",
			cloudflareGatewayID: "gateway",
		},
		Headers: ai.ProviderHeaders{"x-fixture": &keep},
	}}
	gotModel, gotOptions := prepareCloudflareRequest(model, options)
	if gotModel.BaseURL != "https://gateway.ai.cloudflare.com/v1/account/gateway/openai" {
		t.Fatalf("resolved base URL = %q", gotModel.BaseURL)
	}
	if gotOptions.APIKey != &key {
		t.Fatalf("explicit API key was rewritten: %#v", gotOptions.APIKey)
	}
	for _, name := range []string{"cf-aig-authorization", "authorization", "x-api-key"} {
		if _, exists := cloudflareHeader(gotOptions.Headers, name); exists {
			t.Fatalf("stream-time preparation invented %s: %#v", name, gotOptions.Headers)
		}
	}
	if gotOptions.Headers["x-fixture"] != &keep {
		t.Fatalf("custom header was not retained: %#v", gotOptions.Headers)
	}
	if model.BaseURL == gotModel.BaseURL {
		t.Fatal("provider preparation mutated its input model")
	}
}

func TestPrepareCloudflareWorkersRequest(t *testing.T) {
	key := "fixture-key"
	model := &ai.Model{Provider: "cloudflare-workers-ai", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1"}
	options := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{APIKey: &key, Env: ai.ProviderEnv{cloudflareAccountID: "account"}}}
	gotModel, gotOptions := prepareCloudflareRequest(model, options)
	if gotModel.BaseURL != "https://api.cloudflare.com/client/v4/accounts/account/ai/v1" || gotOptions.APIKey != &key {
		t.Fatalf("workers request = %#v %#v", gotModel, gotOptions)
	}
}

// OT-CF: ambient process env must not leak into placeholder resolution, and
// placeholders without a provider env value stay unresolved.
func TestPrepareCloudflareRequestIgnoresAmbientProcessEnvironment(t *testing.T) {
	t.Setenv(cloudflareAccountID, "ambient-account")
	t.Setenv(cloudflareGatewayID, "ambient-gateway")
	model := &ai.Model{
		Provider: "cloudflare-ai-gateway",
		BaseURL:  "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat",
	}
	if got, _ := prepareCloudflareRequest(model, nil); got.BaseURL != model.BaseURL {
		t.Fatalf("nil options resolved from ambient env: %q", got.BaseURL)
	}
	options := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{cloudflareAccountID: "env-account"}}}
	got, _ := prepareCloudflareRequest(model, options)
	if got.BaseURL != "https://gateway.ai.cloudflare.com/v1/env-account/{CLOUDFLARE_GATEWAY_ID}/compat" {
		t.Fatalf("partially resolved URL = %q", got.BaseURL)
	}
}

func cloudflareHeader(headers ai.ProviderHeaders, name string) (*string, bool) {
	for existing, value := range headers {
		if strings.EqualFold(existing, name) {
			return value, true
		}
	}
	return nil, false
}

func providerHeadersEqual(left, right ai.ProviderHeaders) bool {
	if len(left) != len(right) {
		return false
	}
	for name, want := range right {
		got, exists := cloudflareHeader(left, name)
		if !exists || (got == nil) != (want == nil) {
			return false
		}
		if got != nil && *got != *want {
			return false
		}
	}
	return true
}
