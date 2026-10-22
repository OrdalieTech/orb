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

// transcriptToolPlacement splits the tools between the request's list and the system messages that add
// them in place: later holds the tools those add, and anchoredFrom counts the system messages after the
// initial one that the list already holds, so a transport anchors the additions of those past it.
func transcriptToolPlacement(messages ai.MessageList, supportsAdditions bool) (requestTools []ai.Tool, later map[string]ai.Tool, anchoredFrom int) {
	resolved := ai.ResolveTranscriptTools(messages, supportsAdditions)
	later = make(map[string]ai.Tool)
	systemIndex := 0
	for _, message := range messages {
		if system, ok := message.(*ai.SystemMessage); ok {
			if systemIndex >= resolved.FoldedSystemMessages {
				for _, tool := range system.ToolsAdded {
					later[tool.Name] = tool
				}
			}
			systemIndex++
		}
	}
	anchoredFrom = resolved.FoldedSystemMessages
	if ai.InitialSystemMessage(messages) != nil {
		anchoredFrom--
	}
	return resolved.RequestTools, later, max(anchoredFrom, 0)
}

// responsesToolChanges reports a transport that declares every tool in additional_tools items, emptying
// the request's list, and returns the tools the initial system message declares.
func responsesToolChanges(supported bool, messages ai.MessageList, requestTools *[]ai.Tool) (bool, []ai.Tool) {
	if !supported {
		return false, nil
	}
	*requestTools = nil
	if initial := ai.InitialSystemMessage(messages); initial != nil {
		return true, initial.ToolsAdded
	}
	return true, nil
}
