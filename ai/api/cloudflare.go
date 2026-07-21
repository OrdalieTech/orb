package api

import (
	"strings"

	"github.com/OrdalieTech/pigo/ai"
)

const (
	cloudflareAccountID = "CLOUDFLARE_ACCOUNT_ID"
	cloudflareGatewayID = "CLOUDFLARE_GATEWAY_ID"
)

// prepareCloudflareRequest mirrors upstream cloudflare-stream.ts: the account
// and gateway endpoint placeholders materialize from the resolved provider env
// only. Auth headers are produced by auth-resolve, never at stream time. (OT-CF)
func prepareCloudflareRequest(model *ai.Model, options *ai.SimpleStreamOptions) (*ai.Model, *ai.SimpleStreamOptions) {
	if model == nil || (model.Provider != "cloudflare-workers-ai" && model.Provider != "cloudflare-ai-gateway") {
		return model, options
	}
	if options == nil || options.Env == nil {
		return model, options
	}
	resolvedModel := *model
	resolvedModel.BaseURL = replaceProviderPlaceholder(resolvedModel.BaseURL, cloudflareAccountID, options.Env)
	resolvedModel.BaseURL = replaceProviderPlaceholder(resolvedModel.BaseURL, cloudflareGatewayID, options.Env)
	if resolvedModel.BaseURL == model.BaseURL {
		return model, options
	}
	return &resolvedModel, options
}

func replaceProviderPlaceholder(baseURL, name string, env ai.ProviderEnv) string {
	value, exists := env[name]
	if !exists {
		return baseURL
	}
	return strings.ReplaceAll(baseURL, "{"+name+"}", value)
}

func cloneProviderHeaders(source ai.ProviderHeaders) ai.ProviderHeaders {
	result := make(ai.ProviderHeaders, len(source)+3)
	for name, value := range source {
		result[name] = value
	}
	return result
}
