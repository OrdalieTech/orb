package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonwire"
)

const (
	CompactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	CompactionSummarySuffix = "\n</summary>"
	BranchSummaryPrefix     = "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n"
	BranchSummarySuffix     = "</summary>"
)

type codingAgentMessage struct {
	Role               string          `json:"role"`
	Content            json.RawMessage `json:"content"`
	Summary            string          `json:"summary"`
	Timestamp          int64           `json:"timestamp"`
	Command            string          `json:"command"`
	Output             string          `json:"output"`
	ExitCode           *int            `json:"exitCode"`
	Cancelled          bool            `json:"cancelled"`
	Truncated          bool            `json:"truncated"`
	FullOutputPath     *string         `json:"fullOutputPath"`
	ExcludeFromContext bool            `json:"excludeFromContext"`
}

func (message *codingAgentMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role               json.RawMessage `json:"role"`
		Content            json.RawMessage `json:"content"`
		Summary            json.RawMessage `json:"summary"`
		Timestamp          int64           `json:"timestamp"`
		Command            json.RawMessage `json:"command"`
		Output             json.RawMessage `json:"output"`
		ExitCode           *int            `json:"exitCode"`
		Cancelled          bool            `json:"cancelled"`
		Truncated          bool            `json:"truncated"`
		FullOutputPath     json.RawMessage `json:"fullOutputPath"`
		ExcludeFromContext bool            `json:"excludeFromContext"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	role, err := decodeWireString(raw.Role)
	if err != nil {
		return err
	}
	summary, err := decodeWireString(raw.Summary)
	if err != nil {
		return err
	}
	command, err := decodeWireString(raw.Command)
	if err != nil {
		return err
	}
	output, err := decodeWireString(raw.Output)
	if err != nil {
		return err
	}
	fullOutputPath, err := decodeOptionalWireString(raw.FullOutputPath)
	if err != nil {
		return err
	}
	*message = codingAgentMessage{
		Role:               role,
		Content:            raw.Content,
		Summary:            summary,
		Timestamp:          raw.Timestamp,
		Command:            command,
		Output:             output,
		ExitCode:           raw.ExitCode,
		Cancelled:          raw.Cancelled,
		Truncated:          raw.Truncated,
		FullOutputPath:     fullOutputPath,
		ExcludeFromContext: raw.ExcludeFromContext,
	}
	return nil
}

// ConvertToLLM preserves coding-agent messages in runtime state and projects
// them to provider messages only at the agent-loop boundary.
func ConvertToLLM(_ context.Context, messages agent.AgentMessages) (ai.MessageList, error) {
	converted := make(ai.MessageList, 0, len(messages))
	for _, message := range messages {
		if standard, ok := message.(ai.Message); ok {
			converted = append(converted, standard)
			continue
		}

		encoded, err := ai.Marshal(message)
		if err != nil {
			return nil, err
		}
		var custom codingAgentMessage
		if err := json.Unmarshal(encoded, &custom); err != nil {
			return nil, err
		}
		switch custom.Role {
		case "user", "assistant", "toolResult":
			standard, err := ai.UnmarshalMessage(encoded)
			if err != nil {
				return nil, fmt.Errorf("codingagent: decode standard message: %w", err)
			}
			converted = append(converted, standard)
		case "custom":
			user, err := customUserMessage(custom.Content, custom.Timestamp)
			if err != nil {
				return nil, fmt.Errorf("codingagent: decode custom message: %w", err)
			}
			converted = append(converted, user)
		case "branchSummary":
			converted = append(converted, textUserMessage(BranchSummaryPrefix+custom.Summary+BranchSummarySuffix, custom.Timestamp))
		case "compactionSummary":
			converted = append(converted, textUserMessage(CompactionSummaryPrefix+custom.Summary+CompactionSummarySuffix, custom.Timestamp))
		case "bashExecution":
			if !custom.ExcludeFromContext {
				converted = append(converted, textUserMessage(bashExecutionText(custom), custom.Timestamp))
			}
		}
	}
	return converted, nil
}

func customUserMessage(content json.RawMessage, timestamp int64) (*ai.UserMessage, error) {
	content = bytes.TrimSpace(content)
	if len(content) == 0 || bytes.Equal(content, []byte("null")) {
		return &ai.UserMessage{Content: ai.NewUserContent(), Timestamp: timestamp}, nil
	}
	if content[0] == '"' {
		text, err := jsonwire.UnmarshalString(content)
		if err != nil {
			return nil, err
		}
		return textUserMessage(text, timestamp), nil
	}
	var blocks ai.UserContentBlocks
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, err
	}
	return &ai.UserMessage{Content: ai.NewUserContent(blocks...), Timestamp: timestamp}, nil
}

func decodeWireString(data json.RawMessage) (string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return "", nil
	}
	return jsonwire.UnmarshalString(data)
}

func decodeOptionalWireString(data json.RawMessage) (*string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	value, err := jsonwire.UnmarshalString(data)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func textUserMessage(text string, timestamp int64) *ai.UserMessage {
	return &ai.UserMessage{
		Content:   ai.NewUserContent(&ai.TextContent{Text: text}),
		Timestamp: timestamp,
	}
}

func bashExecutionText(message codingAgentMessage) string {
	text := fmt.Sprintf("Ran `%s`\n", message.Command)
	if message.Output != "" {
		text += "```\n" + message.Output + "\n```"
	} else {
		text += "(no output)"
	}
	if message.Cancelled {
		text += "\n\n(command cancelled)"
	} else if message.ExitCode != nil && *message.ExitCode != 0 {
		text += fmt.Sprintf("\n\nCommand exited with code %d", *message.ExitCode)
	}
	if message.Truncated && message.FullOutputPath != nil && *message.FullOutputPath != "" {
		text += "\n\n[Output truncated. Full output: " + *message.FullOutputPath + "]"
	}
	return text
}
