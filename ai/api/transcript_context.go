package api

import "github.com/OrdalieTech/orb/ai"

func providerBoolPointer(value bool) *bool { return &value }

// collapseProviderContext projects transcript state onto the legacy Context
// fields used by transports that cannot carry mid-conversation system state.
func collapseProviderContext(requestContext ai.Context) ai.Context {
	return projectProviderContext(requestContext, false)
}

func projectProviderContext(requestContext ai.Context, supportsMidConvoSystemMessages bool) ai.Context {
	transcript := ai.NormalizeContext(requestContext)
	return projectTranscriptContext(transcript, supportsMidConvoSystemMessages)
}

func projectTranscriptContext(transcript ai.TranscriptContext, supportsMidConvoSystemMessages bool) ai.Context {
	if !supportsMidConvoSystemMessages {
		transcript = ai.CollapseSystemMessages(transcript)
	}
	result := ai.Context{Messages: ai.WithoutInitialSystemMessage(transcript.Messages)}
	if initial := ai.InitialSystemMessage(transcript.Messages); initial != nil {
		prompt := ai.SystemMessageText(initial)
		result.SystemPrompt = &prompt
	}
	if tools := ai.CurrentTools(transcript.Messages); len(tools) > 0 {
		result.Tools = &tools
	}
	return result
}

func transcriptToolPlacement(messages ai.MessageList, supportsAdditions bool) ([]ai.Tool, map[string]ai.Tool, bool) {
	resolved := ai.ResolveTranscriptTools(messages, supportsAdditions)
	if !resolved.AnchorsAdditions {
		return resolved.RequestTools, nil, false
	}
	requestTools := resolved.RequestTools
	initialNames := make(map[string]struct{}, len(requestTools))
	for _, tool := range requestTools {
		initialNames[tool.Name] = struct{}{}
	}
	later := make(map[string]ai.Tool)
	for _, tool := range ai.DeclaredTools(messages) {
		if _, exists := initialNames[tool.Name]; !exists {
			later[tool.Name] = tool
		}
	}
	return requestTools, later, true
}
