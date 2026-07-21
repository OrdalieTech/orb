package providers

import (
	"context"
	"testing"

	"github.com/OrdalieTech/pigo/ai/auth"
)

type cloudflareAuthContext map[string]string

func (authContext cloudflareAuthContext) Env(_ context.Context, name string) (string, bool) {
	value, ok := authContext[name]
	return value, ok
}

func (cloudflareAuthContext) FileExists(context.Context, string) bool { return false }

type cloudflareInteraction struct {
	answers []string
	prompts []auth.AuthPrompt
}

func (interaction *cloudflareInteraction) Prompt(_ context.Context, prompt auth.AuthPrompt) (string, error) {
	interaction.prompts = append(interaction.prompts, prompt)
	answer := interaction.answers[0]
	interaction.answers = interaction.answers[1:]
	return answer, nil
}

func (*cloudflareInteraction) Notify(auth.AuthEvent) {}

func TestCloudflareAuthMergesStoredCredentialPerField(t *testing.T) {
	ctx := context.Background()
	ambient := cloudflareAuthContext{
		cloudflareAPIKey:    "ambient-key",
		cloudflareAccountID: "ambient-account",
		cloudflareGatewayID: "ambient-gateway",
	}
	storedKey := "stored-key"
	credential := &auth.Credential{Type: auth.CredentialAPIKey, Key: &storedKey}

	workers, err := cloudflareWorkersAIAuth().Resolve(ctx, ambient, credential)
	if err != nil {
		t.Fatal(err)
	}
	assertCloudflareResolution(t, workers, "stored-key", "ambient-account", "", "stored credential")

	credential = &auth.Credential{
		Type: auth.CredentialAPIKey,
		Env:  map[string]string{cloudflareAccountID: "stored-account"},
	}
	gateway, err := cloudflareAIGatewayAuth().Resolve(ctx, ambient, credential)
	if err != nil {
		t.Fatal(err)
	}
	assertCloudflareResolution(t, gateway, "ambient-key", "stored-account", "ambient-gateway", "stored credential")

	credential.Env[cloudflareGatewayID] = ""
	gateway, err = cloudflareAIGatewayAuth().Resolve(ctx, ambient, credential)
	if err != nil {
		t.Fatal(err)
	}
	if gateway != nil {
		t.Fatalf("empty stored gateway ID must suppress ambient fallback: %#v", gateway)
	}
}

func TestCloudflareAuthUsesAmbientValuesAndRequiresCompleteScope(t *testing.T) {
	ctx := context.Background()
	ambient := cloudflareAuthContext{
		cloudflareAPIKey:    "ambient-key",
		cloudflareAccountID: "ambient-account",
		cloudflareGatewayID: "ambient-gateway",
	}
	workers, err := cloudflareWorkersAIAuth().Resolve(ctx, ambient, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertCloudflareResolution(t, workers, "ambient-key", "ambient-account", "", cloudflareAPIKey)

	delete(ambient, cloudflareGatewayID)
	gateway, err := cloudflareAIGatewayAuth().Resolve(ctx, ambient, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gateway != nil {
		t.Fatalf("gateway auth resolved without a gateway ID: %#v", gateway)
	}
}

func TestCloudflareAuthLoginMatchesUpstreamCredentialShape(t *testing.T) {
	tests := []struct {
		name        string
		method      auth.APIKeyAuth
		answers     []string
		messages    []string
		promptTypes []auth.PromptType
		wantJSON    string
	}{
		{
			name: "workers-ai", method: cloudflareWorkersAIAuth(), answers: []string{"key", "account"},
			messages:    []string{"Enter Cloudflare API key", "Enter Cloudflare account ID"},
			promptTypes: []auth.PromptType{auth.PromptSecret, auth.PromptText},
			wantJSON:    `{"type":"api_key","key":"key","env":{"CLOUDFLARE_ACCOUNT_ID":"account"}}`,
		},
		{
			name: "ai-gateway", method: cloudflareAIGatewayAuth(), answers: []string{"key", "account", "gateway"},
			messages:    []string{"Enter Cloudflare API key", "Enter Cloudflare account ID", "Enter Cloudflare AI Gateway ID"},
			promptTypes: []auth.PromptType{auth.PromptSecret, auth.PromptText, auth.PromptText},
			wantJSON:    `{"type":"api_key","key":"key","env":{"CLOUDFLARE_ACCOUNT_ID":"account","CLOUDFLARE_GATEWAY_ID":"gateway"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			login, ok := test.method.(auth.APIKeyLogin)
			if !ok {
				t.Fatal("Cloudflare API key auth does not expose login")
			}
			interaction := &cloudflareInteraction{answers: append([]string(nil), test.answers...)}
			credential, err := login.Login(context.Background(), interaction)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := credential.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.wantJSON {
				t.Fatalf("credential JSON = %s, want %s", encoded, test.wantJSON)
			}
			if len(interaction.prompts) != len(test.messages) {
				t.Fatalf("got %d prompts, want %d", len(interaction.prompts), len(test.messages))
			}
			for index, prompt := range interaction.prompts {
				if prompt.Message != test.messages[index] || prompt.Type != test.promptTypes[index] {
					t.Fatalf("prompt %d = %#v", index, prompt)
				}
			}
		})
	}
}

func assertCloudflareResolution(
	t *testing.T,
	result *auth.AuthResult,
	wantKey, wantAccount, wantGateway, wantSource string,
) {
	t.Helper()
	if result == nil {
		t.Fatalf("missing Cloudflare auth result: %#v", result)
	}
	if result.Env[cloudflareAccountID] != wantAccount || result.Source != wantSource {
		t.Fatalf("Cloudflare auth result = %#v", result)
	}
	if wantGateway == "" {
		if result.Auth.APIKey == nil || *result.Auth.APIKey != wantKey || result.Auth.Headers != nil {
			t.Fatalf("workers auth = %#v", result.Auth)
		}
		if _, exists := result.Env[cloudflareGatewayID]; exists {
			t.Fatalf("unexpected gateway ID in workers env: %#v", result.Env)
		}
		return
	}
	if result.Env[cloudflareGatewayID] != wantGateway {
		t.Fatalf("gateway ID = %q, want %q", result.Env[cloudflareGatewayID], wantGateway)
	}
	if result.Auth.APIKey != nil || result.Auth.Headers["cf-aig-authorization"] == nil || *result.Auth.Headers["cf-aig-authorization"] != "Bearer "+wantKey {
		t.Fatalf("gateway auth = %#v", result.Auth)
	}
	if value, exists := result.Auth.Headers["Authorization"]; !exists || value != nil {
		t.Fatalf("gateway Authorization suppression = %#v", result.Auth.Headers)
	}
	if value, exists := result.Auth.Headers["x-api-key"]; !exists || value != nil {
		t.Fatalf("gateway x-api-key suppression = %#v", result.Auth.Headers)
	}
}
