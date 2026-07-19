package main

import (
	"context"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/auth"
)

func TestRequestAuthResolverPreservesNullableHeaders(t *testing.T) {
	credential := auth.APIKeyCredential("fixture")
	credential.Env = map[string]string{
		"CLOUDFLARE_ACCOUNT_ID": "account",
		"CLOUDFLARE_GATEWAY_ID": "gateway",
	}
	store := auth.NewMemoryStore(map[string]*auth.Credential{"cloudflare-ai-gateway": credential})
	request, err := requestAuthResolverForProvider(CLIArgs{}, nil, nil, store)(context.Background(), ai.ProviderID("cloudflare-ai-gateway"))
	if err != nil {
		t.Fatal(err)
	}
	if request.Headers["cf-aig-authorization"] == nil || *request.Headers["cf-aig-authorization"] != "Bearer fixture" {
		t.Fatalf("gateway authorization = %#v", request.Headers)
	}
	for _, name := range []string{"Authorization", "x-api-key"} {
		if value, exists := request.Headers[name]; !exists || value != nil {
			t.Fatalf("nullable %s header = %#v", name, request.Headers)
		}
	}
	*credential.Key = "mutated"
	if *request.Headers["cf-aig-authorization"] != "Bearer fixture" {
		t.Fatal("request auth retained the stored credential's mutable string pointer")
	}
}
