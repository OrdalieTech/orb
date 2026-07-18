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
	case ai.APIBedrockConverse:
		return StreamSimpleBedrockConverse(ctx, model, requestContext, options)
	case ai.APIAnthropicMessages:
		return StreamSimpleAnthropicMessages(ctx, model, requestContext, options)
	case ai.APIGoogleGenerativeAI:
		return StreamSimpleGoogleGenerativeAI(ctx, model, requestContext, options)
	case ai.APIGoogleVertex:
		return StreamSimpleGoogleVertex(ctx, model, requestContext, options)
	case ai.APIMistralConversations:
		return StreamSimpleMistralConversations(ctx, model, requestContext, options)
	case ai.APIAzureOpenAIResponses:
		return StreamSimpleAzureOpenAIResponses(ctx, model, requestContext, options)
	case ai.APIOpenAICodexResponses:
		return StreamSimpleOpenAICodexResponses(ctx, model, requestContext, options)
	case ai.APIOpenAIResponses:
		return StreamSimpleOpenAIResponses(ctx, model, requestContext, options)
	case ai.APIOpenAICompletions:
		return StreamSimpleOpenAICompletions(ctx, model, requestContext, options)
	case ai.APIPiMessages:
		return StreamSimplePiMessages(ctx, model, requestContext, options)
	default:
		return nil, fmt.Errorf("ai: unsupported API %q", model.API)
	}
}
