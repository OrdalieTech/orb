package api

import (
	"context"
	"fmt"

	"github.com/OrdalieTech/pi-go/ai"
)

// StreamSimple dispatches a model to its provider wire-shape adapter. Provider
// work packages extend this switch as each upstream API shape lands.
func StreamSimple(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, fmt.Errorf("ai: model is nil")
	}
	switch model.API {
	case ai.APIAnthropicMessages:
		return StreamSimpleAnthropicMessages(ctx, model, requestContext, options)
	case ai.APIGoogleGenerativeAI:
		return StreamSimpleGoogleGenerativeAI(ctx, model, requestContext, options)
	case ai.APIGoogleVertex:
		return StreamSimpleGoogleVertex(ctx, model, requestContext, options)
	case ai.APIOpenAIResponses, ai.APIAzureOpenAIResponses, ai.APIOpenAICodexResponses:
		return StreamSimpleOpenAIResponses(ctx, model, requestContext, options)
	case ai.APIOpenAICompletions:
		return StreamSimpleOpenAICompletions(ctx, model, requestContext, options)
	default:
		return nil, fmt.Errorf("ai: unsupported API %q", model.API)
	}
}
