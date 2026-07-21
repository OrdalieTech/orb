package codingagent_test

import (
	"context"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent"
)

func TestSDKPublicSurfaceMatchesUpstreamSessionControls(t *testing.T) {
	t.Helper()
	requireFunction[func(*codingagent.AgentSession) *agent.Agent]((*codingagent.AgentSession).Agent)
	requireFunction[func(*codingagent.AgentSession) []string]((*codingagent.AgentSession).GetActiveToolNames)
	requireFunction[func(*codingagent.AgentSession, []string) error]((*codingagent.AgentSession).SetActiveToolsByName)
	requireFunction[func(*codingagent.AgentSession, context.Context, string, *codingagent.PromptOptions) error]((*codingagent.AgentSession).PromptWithOptions)
	requireFunction[func(*codingagent.AgentSession, context.Context, ai.UserContent, *codingagent.SendUserMessageOptions) error]((*codingagent.AgentSession).SendUserMessage)
	requireFunction[func(*codingagent.AgentSession, context.Context, codingagent.CustomMessage, *codingagent.SendCustomMessageOptions) error]((*codingagent.AgentSession).SendCustomMessage)
}

func TestSDKPublicSurfaceMatchesUpstreamServiceFactories(t *testing.T) {
	t.Helper()
	requireFunction[func(codingagent.CreateAgentSessionServicesOptions) (*codingagent.AgentSessionServices, error)](codingagent.CreateAgentSessionServices)
	requireFunction[func(codingagent.CreateAgentSessionFromServicesOptions) (*codingagent.AgentSessionResult, error)](codingagent.CreateAgentSessionFromServices)
}

func requireFunction[T any](function T) { _ = function }
