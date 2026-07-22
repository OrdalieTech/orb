package harness

import (
	"context"

	"github.com/OrdalieTech/pigo/ai"
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
	return ai.RetryAssistantCall(ctx, func() (*ai.AssistantMessage, error) {
		return complete(ctx, model, request, options)
	}, retry, callbacks)
}

// RetryingCompleteFunc decorates the existing compaction completion seam. It
// lets compaction retry each generated summary without changing its core API.
func RetryingCompleteFunc(complete CompleteFunc, retry *ai.RetryPolicy, callbacks *ai.RetryCallbacks) CompleteFunc {
	if complete == nil {
		return nil
	}
	return func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		return CompleteSimpleWithRetries(ctx, complete, model, request, options, retry, callbacks)
	}
}
