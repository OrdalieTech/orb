package harness

import (
	"context"

	"github.com/OrdalieTech/orb/ai"
)

// CompleteSimpleWithRetries applies the shared assistant retry policy to one
// harness completion request.
func CompleteSimpleWithRetries(
	ctx context.Context,
	complete CompleteFunc,
	model *ai.Model,
	request ai.Context,
	options *ai.SimpleStreamOptions,
	retry *ai.RetryPolicy,
	callbacks *ai.RetryCallbacks,
) (*ai.AssistantMessage, error) {
	requestOptions := &ai.SimpleStreamOptions{}
	if options != nil {
		*requestOptions = *options
	}
	retention := ai.CacheRetentionNone
	sessionID, err := ai.UUIDv7()
	if err != nil {
		return nil, err
	}
	requestOptions.CacheRetention = &retention
	requestOptions.SessionID = &sessionID
	return ai.RetryAssistantCall(ctx, func() (*ai.AssistantMessage, error) {
		return complete(ctx, model, request, requestOptions)
	}, retry, callbacks)
}
