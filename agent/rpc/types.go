package rpc

import (
	"encoding/json"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// Response is the common response envelope emitted by RPC mode. Field
// order follows upstream's object construction order because transcript bytes
// are a public wire format.
type Response struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	HasID   bool   `json:"-"`
	HasData bool   `json:"-"`
}

func (response Response) MarshalJSON() ([]byte, error) {
	// Members in the order the struct encodings before wrote them.
	output := []byte{'{'}
	if response.HasID {
		output = append(jsonwire.AppendString(append(output, `"id":`...), response.ID), ',')
	}
	output = jsonwire.AppendString(append(output, `"type":`...), response.Type)
	output = jsonwire.AppendString(append(output, `,"command":`...), response.Command)
	switch {
	case !response.Success:
		output = jsonwire.AppendString(append(output, `,"success":false,"error":`...), response.Error)
	case response.HasData:
		data, err := ai.Marshal(struct {
			Data any `json:"data"`
		}{response.Data})
		if err != nil {
			return nil, err
		}
		output = append(append(output, `,"success":true,`...), data[1:len(data)-1]...)
	default:
		output = append(output, `,"success":true`...)
	}
	return append(output, '}'), nil
}

type SessionState struct {
	Model                 *ai.Model             `json:"model,omitempty"`
	ThinkingLevel         ai.ModelThinkingLevel `json:"thinkingLevel"`
	IsStreaming           bool                  `json:"isStreaming"`
	IsCompacting          bool                  `json:"isCompacting"`
	SteeringMode          string                `json:"steeringMode"`
	FollowUpMode          string                `json:"followUpMode"`
	SessionFile           string                `json:"sessionFile,omitempty"`
	SessionID             string                `json:"sessionId"`
	SessionName           *string               `json:"sessionName,omitempty"`
	AutoCompactionEnabled bool                  `json:"autoCompactionEnabled"`
	MessageCount          int                   `json:"messageCount"`
	PendingMessageCount   int                   `json:"pendingMessageCount"`
}

type ThinkingLevels struct {
	Levels []ai.ModelThinkingLevel `json:"levels"`
}

type SlashCommand struct {
	Name        string     `json:"name"`
	Description *string    `json:"description,omitempty"`
	Source      string     `json:"source"`
	SourceInfo  SourceInfo `json:"sourceInfo"`
}

type SourceInfo struct {
	Path    string  `json:"path"`
	Source  string  `json:"source"`
	Scope   string  `json:"scope"`
	Origin  string  `json:"origin"`
	BaseDir *string `json:"baseDir,omitempty"`
}

type Command struct {
	ID                 string             `json:"id,omitempty"`
	Type               string             `json:"type"`
	Message            string             `json:"message,omitempty"`
	Images             []*ai.ImageContent `json:"images,omitempty"`
	StreamingBehavior  string             `json:"streamingBehavior,omitempty"`
	ParentSession      string             `json:"parentSession,omitempty"`
	Provider           string             `json:"provider,omitempty"`
	ModelID            string             `json:"modelId,omitempty"`
	Level              string             `json:"level,omitempty"`
	Mode               string             `json:"mode,omitempty"`
	CustomInstructions string             `json:"customInstructions,omitempty"`
	Enabled            *bool              `json:"enabled,omitempty"`
	Command            string             `json:"command,omitempty"`
	ExcludeFromContext *bool              `json:"excludeFromContext,omitempty"`
	OutputPath         string             `json:"outputPath,omitempty"`
	SessionPath        string             `json:"sessionPath,omitempty"`
	EntryID            string             `json:"entryId,omitempty"`
	Since              *string            `json:"since,omitempty"`
	Name               string             `json:"name,omitempty"`
	HasID              bool               `json:"-"`
}

type ExtensionUIResponse struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	Value     *string `json:"value,omitempty"`
	Confirmed *bool   `json:"confirmed,omitempty"`
	Cancelled bool    `json:"cancelled,omitempty"`
}

// rawRPCObject retains command members for validation and diagnostics without
// normalizing their JSON values.
type rawRPCObject map[string]json.RawMessage
