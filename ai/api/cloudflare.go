package api

import (
	"os"
	"strings"

	"github.com/OrdalieTech/pigo/ai"
)

const (
	cloudflareAPIKey    = "CLOUDFLARE_API_KEY"
	cloudflareAccountID = "CLOUDFLARE_ACCOUNT_ID"
	cloudflareGatewayID = "CLOUDFLARE_GATEWAY_ID"
)

func prepareCloudflareRequest(model *ai.Model, options *ai.SimpleStreamOptions) (*ai.Model, *ai.SimpleStreamOptions) {
	if model == nil || (model.Provider != "cloudflare-workers-ai" && model.Provider != "cloudflare-ai-gateway") {
		return model, options
	}
	resolvedModel := *model
	resolvedOptions := ai.SimpleStreamOptions{}
	if options != nil {
		resolvedOptions = *options
	}
	value := func(name string) string {
		if resolvedOptions.Env[name] != "" {
			return resolvedOptions.Env[name]
		}
		return os.Getenv(name)
	}
	resolvedModel.BaseURL = replaceProviderPlaceholder(resolvedModel.BaseURL, cloudflareAccountID, value(cloudflareAccountID))
	resolvedModel.BaseURL = replaceProviderPlaceholder(resolvedModel.BaseURL, cloudflareGatewayID, value(cloudflareGatewayID))
	if model.Provider == "cloudflare-ai-gateway" {
		key := value(cloudflareAPIKey)
		if resolvedOptions.APIKey != nil && *resolvedOptions.APIKey != "" {
			key = *resolvedOptions.APIKey
		}
		if key != "" {
			headers := cloneProviderHeaders(resolvedOptions.Headers)
			bearer := "Bearer " + key
			setProviderHeaderDefault(headers, "cf-aig-authorization", &bearer)
			setProviderHeaderDefault(headers, "Authorization", nil)
			setProviderHeaderDefault(headers, "x-api-key", nil)
			resolvedOptions.Headers = headers
			resolvedOptions.APIKey = nil
		}
	}
	return &resolvedModel, &resolvedOptions
}

func replaceProviderPlaceholder(baseURL, name, value string) string {
	if value == "" {
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

func setProviderHeaderDefault(headers ai.ProviderHeaders, name string, value *string) {
	for existing := range headers {
		if strings.EqualFold(existing, name) {
			return
		}
	}
	headers[name] = value
}
