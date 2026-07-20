package modes

import (
	"encoding/json"

	"github.com/OrdalieTech/pi-go/ai"
)

// RPCResponse is the common response envelope emitted by RPC mode. Field
// order follows upstream's object construction order because transcript bytes
// are a public wire format.
type RPCResponse struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	HasID   bool   `json:"-"`
	HasData bool   `json:"-"`
}

func (response RPCResponse) MarshalJSON() ([]byte, error) {
	var id *string
	if response.HasID {
		id = &response.ID
	}
	if !response.Success {
		return ai.Marshal(struct {
			ID      *string `json:"id,omitempty"`
			Type    string  `json:"type"`
			Command string  `json:"command"`
			Success bool    `json:"success"`
			Error   string  `json:"error"`
		}{id, response.Type, response.Command, false, response.Error})
	}
	if response.HasData {
		return ai.Marshal(struct {
			ID      *string `json:"id,omitempty"`
			Type    string  `json:"type"`
			Command string  `json:"command"`
			Success bool    `json:"success"`
			Data    any     `json:"data"`
		}{id, response.Type, response.Command, true, response.Data})
	}
	return ai.Marshal(struct {
		ID      *string `json:"id,omitempty"`
		Type    string  `json:"type"`
		Command string  `json:"command"`
		Success bool    `json:"success"`
	}{id, response.Type, response.Command, true})
}

type RPCSessionState struct {
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

type RPCThinkingLevels struct {
	Levels []ai.ModelThinkingLevel `json:"levels"`
}

type RPCSlashCommand struct {
	Name        string        `json:"name"`
	Description *string       `json:"description,omitempty"`
	Source      string        `json:"source"`
	SourceInfo  RPCSourceInfo `json:"sourceInfo"`
}

type RPCSourceInfo struct {
	Path    string  `json:"path"`
	Source  string  `json:"source"`
	Scope   string  `json:"scope"`
	Origin  string  `json:"origin"`
	BaseDir *string `json:"baseDir,omitempty"`
}

type RPCCommand struct {
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

type RPCExtensionUIResponse struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	Value     *string `json:"value,omitempty"`
	Confirmed *bool   `json:"confirmed,omitempty"`
	Cancelled bool    `json:"cancelled,omitempty"`
}

// rawRPCObject retains command members for validation and diagnostics without
// normalizing their JSON values.
type rawRPCObject map[string]json.RawMessage
