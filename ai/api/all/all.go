// Package all is the default provider registry: the CLI and SDK sessions that
// pass no stream function dispatch through it. Light assemblies build their own
// api.Registry with only the families they select.
//
// A package that references this one links every family even when it always
// supplies its own stream function, because the default is a static reference.
// An embedder in that position (a Worker running agent.NewAgentSession with a
// custom StreamFn) builds with -tags orb_nodefaultproviders, which empties the
// default so no family's adapter or SDK is linked through it.
package all

import (
	"context"

	"github.com/OrdalieTech/orb/ai"
)

var registry = Registry()

// StreamSimple dispatches a model through the default registry.
func StreamSimple(ctx context.Context, model *ai.Model, requestContext ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
	return registry.StreamSimple(ctx, model, requestContext, options)
}
