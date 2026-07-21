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
			key := item.Auth.APIKey
			if item.Provider == "cloudflare-ai-gateway" {
				key = "fixture-cloudflare-key"
			}
			model := &ai.Model{Provider: item.Provider, BaseURL: item.BaseURL}
			options := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{APIKey: &key, Env: item.Env}}
			gotModel, gotOptions := prepareCloudflareRequest(model, options)
			if gotModel.BaseURL != item.ResolvedBaseURL {
				t.Fatalf("resolved base URL = %q, want %q", gotModel.BaseURL, item.ResolvedBaseURL)
			}
			if item.Provider == "cloudflare-ai-gateway" {
				if gotOptions.APIKey != nil || !providerHeadersEqual(gotOptions.Headers, item.Auth.Headers) {
					t.Fatalf("resolved auth = %#v, want %#v", gotOptions.StreamOptions, item.Auth)
				}
			} else if gotOptions.APIKey == nil || *gotOptions.APIKey != item.Auth.APIKey {
				t.Fatalf("resolved API key = %#v, want %q", gotOptions.APIKey, item.Auth.APIKey)
			}
		})
	}
}

func TestPrepareCloudflareGatewayRequest(t *testing.T) {
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
	if gotOptions.APIKey != nil {
		t.Fatalf("gateway API key was not moved to a header: %#v", gotOptions.APIKey)
	}
	if value, exists := cloudflareHeader(gotOptions.Headers, "cf-aig-authorization"); !exists || value == nil || *value != "Bearer fixture-key" {
		t.Fatalf("gateway authorization header = %#v", gotOptions.Headers)
	}
	for _, name := range []string{"authorization", "x-api-key"} {
		if value, exists := cloudflareHeader(gotOptions.Headers, name); !exists || value != nil {
			t.Fatalf("%s was not explicitly suppressed: %#v", name, gotOptions.Headers)
		}
	}
	if gotOptions.Headers["x-fixture"] != &keep {
		t.Fatalf("custom header was not retained: %#v", gotOptions.Headers)
	}
	if model.BaseURL == gotModel.BaseURL || options.APIKey == nil || options.Headers["cf-aig-authorization"] != nil {
		t.Fatal("provider preparation mutated its inputs")
	}
}

func TestPrepareCloudflareGatewayPreservesExplicitHeaderOverrides(t *testing.T) {
	key := "fixture-key"
	customGateway := "custom-gateway"
	customAuthorization := "Bearer custom-origin"
	customAPIKey := "custom-api-key"
	options := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		APIKey: &key,
		Headers: ai.ProviderHeaders{
			"CF-AIG-Authorization": &customGateway,
			"authorization":        &customAuthorization,
			"X-API-KEY":            &customAPIKey,
		},
	}}
	_, got := prepareCloudflareRequest(&ai.Model{Provider: "cloudflare-ai-gateway"}, options)
	for name, want := range map[string]string{
		"cf-aig-authorization": customGateway,
		"authorization":        customAuthorization,
		"x-api-key":            customAPIKey,
	} {
		value, exists := cloudflareHeader(got.Headers, name)
		if !exists || value == nil || *value != want {
			t.Fatalf("explicit %s override = %#v, want %q", name, value, want)
		}
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

func TestPrepareCloudflareRequestUsesAmbientScopeWithoutMutatingUnresolvedPlaceholders(t *testing.T) {
	t.Setenv(cloudflareAccountID, "ambient-account")
	t.Setenv(cloudflareGatewayID, "")
	model := &ai.Model{
		Provider: "cloudflare-ai-gateway",
		BaseURL:  "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat",
	}
	got, _ := prepareCloudflareRequest(model, nil)
	if got.BaseURL != "https://gateway.ai.cloudflare.com/v1/ambient-account/{CLOUDFLARE_GATEWAY_ID}/compat" {
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
