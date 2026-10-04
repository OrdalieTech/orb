package api

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/conformance/runner"
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
