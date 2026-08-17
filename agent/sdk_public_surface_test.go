package agent_test

import (
	"context"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestSDKPublicSurfaceMatchesUpstreamSessionControls(t *testing.T) {
	t.Helper()
	requireFunction[func(*agent.AgentSession) *engine.Agent]((*agent.AgentSession).Agent)
	requireFunction[func(*agent.AgentSession) []string]((*agent.AgentSession).GetActiveToolNames)
	requireFunction[func(*agent.AgentSession, []string) error]((*agent.AgentSession).SetActiveToolsByName)
	requireFunction[func(*agent.AgentSession, context.Context, string, *agent.PromptOptions) error]((*agent.AgentSession).PromptWithOptions)
	requireFunction[func(*agent.AgentSession, context.Context, ai.UserContent, *agent.SendUserMessageOptions) error]((*agent.AgentSession).SendUserMessage)
	requireFunction[func(*agent.AgentSession, context.Context, agent.CustomMessage, *agent.SendCustomMessageOptions) error]((*agent.AgentSession).SendCustomMessage)
}

func TestSDKPublicSurfaceMatchesUpstreamServiceFactories(t *testing.T) {
	t.Helper()
	requireFunction[func(agent.CreateAgentSessionServicesOptions) (*agent.AgentSessionServices, error)](agent.CreateAgentSessionServices)
	requireFunction[func(agent.CreateAgentSessionFromServicesOptions) (*agent.AgentSessionResult, error)](agent.CreateAgentSessionFromServices)
}

func requireFunction[T any](function T) { _ = function }
