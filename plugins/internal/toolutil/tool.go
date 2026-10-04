package toolutil

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func Decode(raw any, target any) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func TextResult(text string) engine.AgentToolResult {
	return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: text}}}
}

// ModelRequest prepares a direct call to model with the registry's auth, base URL and headers.
func ModelRequest(ctx context.Context, registry extensions.ModelRegistry, model *ai.Model) (ai.Model, *ai.SimpleStreamOptions) {
	request := *model
	options := &ai.SimpleStreamOptions{}
	if resolved, err := registry.ResolveProviderAuth(ctx, string(model.Provider), nil); err == nil && resolved != nil {
		options.APIKey, options.Headers, options.Env = resolved.Auth.APIKey, ai.ProviderHeaders(resolved.Auth.Headers), ai.ProviderEnv(resolved.Env)
		if resolved.Auth.BaseURL != nil {
			request.BaseURL = *resolved.Auth.BaseURL
		}
	}
	if headers, err := registry.ResolveModelHeaders(ctx, request, map[string]string(options.Env), options.APIKey); err == nil && headers != nil {
		merged := map[string]string{}
		if request.Headers != nil {
			maps.Copy(merged, *request.Headers)
		}
		maps.Copy(merged, *headers)
		request.Headers = &merged
	}
	return request, options
}
