package harness

import (
	"encoding/json"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
)

// SessionV4ContextTransform rewrites the entry slice a context is built from.
type SessionV4ContextTransform func([]SessionV4Entry) []SessionV4Entry

// SessionV4ContextProjector maps one custom entry to context messages.
type SessionV4ContextProjector func(SessionV4Entry, int, []SessionV4Entry) agent.AgentMessages

// SessionV4ContextOptions mirrors upstream SessionContextBuildOptions.
type SessionV4ContextOptions struct {
	EntryTransforms []SessionV4ContextTransform
	EntryProjectors map[string]SessionV4ContextProjector
}

// DefaultSessionV4ContextTransform keeps only the latest compaction and the
// entries after it.
func DefaultSessionV4ContextTransform(pathEntries []SessionV4Entry) []SessionV4Entry {
	for index := len(pathEntries) - 1; index >= 0; index-- {
		if pathEntries[index].Type == "compaction" {
			selected := make([]SessionV4Entry, 0, len(pathEntries)-index)
			selected = append(selected, pathEntries[index])
			return append(selected, pathEntries[index+1:]...)
		}
	}
	return append([]SessionV4Entry(nil), pathEntries...)
}

func buildSessionV4ContextEntries(pathEntries []SessionV4Entry, options SessionV4ContextOptions) []SessionV4Entry {
	entries := DefaultSessionV4ContextTransform(pathEntries)
	for _, transform := range options.EntryTransforms {
		if transform != nil {
			entries = append([]SessionV4Entry(nil), transform(entries)...)
		}
	}
	return entries
}

func sessionV4MessageEnvelope(message json.RawMessage) (role, provider, model, stopReason string) {
	var envelope struct {
		Role       string `json:"role"`
		Provider   string `json:"provider"`
		Model      string `json:"model"`
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(message, &envelope)
	return envelope.Role, envelope.Provider, envelope.Model, envelope.StopReason
}

// BuildSessionV4Context reduces one root-to-leaf entry path into the model
// context, mirroring packages/agent/src/harness/session/context.ts.
func BuildSessionV4Context(pathEntries []SessionV4Entry, options ...SessionV4ContextOptions) SessionContext {
	var resolved SessionV4ContextOptions
	if len(options) > 0 {
		resolved = options[0]
	}
	contextState := SessionContext{ThinkingLevel: "off", Messages: []any{}}
	for _, entry := range pathEntries {
		switch entry.Type {
		case "thinking_level_change":
			contextState.ThinkingLevel = entry.ThinkingLevel
		case "model_change":
			contextState.Model = &SessionModel{Provider: entry.Provider, ModelID: entry.ModelID}
		case "message":
			if role, provider, model, _ := sessionV4MessageEnvelope(entry.Message); role == "assistant" {
				contextState.Model = &SessionModel{Provider: provider, ModelID: model}
			}
		case "active_tools_change":
			contextState.ActiveToolNames = cloneHarnessStrings(entry.ActiveToolNames)
		}
	}
	contextEntries := buildSessionV4ContextEntries(pathEntries, resolved)
	for index, entry := range contextEntries {
		switch entry.Type {
		case "message":
			if role, _, _, stopReason := sessionV4MessageEnvelope(entry.Message); role == "assistant" && stopReason == "deferred" {
				continue
			}
			if message, err := ai.UnmarshalMessage(entry.Message); err == nil {
				contextState.Messages = append(contextState.Messages, message)
			} else {
				contextState.Messages = append(contextState.Messages, cloneHarnessRaw(entry.Message))
			}
		case "compaction":
			contextState.Messages = append(contextState.Messages, &SummaryMessage{
				Role: "compactionSummary", Summary: entry.Summary, TokensBefore: entry.TokensBefore, Timestamp: entry.Timestamp,
			})
			contextState.Messages = append(contextState.Messages, decodeHarnessAgentMessages(entry.RetainedTail)...)
		case "branch_summary":
			if entry.Summary != "" {
				contextState.Messages = append(contextState.Messages, &SummaryMessage{
					Role: "branchSummary", Summary: entry.Summary, FromID: entry.FromID, Timestamp: entry.Timestamp,
				})
			}
		case "custom":
			if projector := resolved.EntryProjectors[entry.CustomType]; projector != nil {
				contextState.Messages = append(contextState.Messages, projector(entry.clone(), index, contextEntries)...)
			}
		}
	}
	return contextState
}
