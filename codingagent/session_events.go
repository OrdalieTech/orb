package codingagent

import (
	"errors"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
)

type SessionEventType string

const (
	EventAgentSettled    SessionEventType = "agent_settled"
	EventQueueUpdate     SessionEventType = "queue_update"
	EventCompactionStart SessionEventType = "compaction_start"
	EventCompactionEnd   SessionEventType = "compaction_end"
	EventAutoRetryStart  SessionEventType = "auto_retry_start"
	EventAutoRetryEnd    SessionEventType = "auto_retry_end"
)

type SessionAgentEndEvent struct {
	Messages  agent.AgentMessages `json:"messages"`
	WillRetry bool                `json:"willRetry"`
}

type AgentSettledEvent struct{}

type QueueUpdateEvent struct {
	Steering []string `json:"steering"`
	FollowUp []string `json:"followUp"`
}

type CompactionStartEvent struct {
	Reason string `json:"reason"`
}

type CompactionEndEvent struct {
	Reason       string                    `json:"reason"`
	Result       *harness.CompactionResult `json:"result,omitempty"`
	Aborted      bool                      `json:"aborted"`
	WillRetry    bool                      `json:"willRetry"`
	ErrorMessage *string                   `json:"errorMessage,omitempty"`
}

type AutoRetryStartEvent struct {
	Attempt      int    `json:"attempt"`
	MaxAttempts  int    `json:"maxAttempts"`
	DelayMS      int64  `json:"delayMs"`
	ErrorMessage string `json:"errorMessage"`
}

type AutoRetryEndEvent struct {
	Success    bool    `json:"success"`
	Attempt    int     `json:"attempt"`
	FinalError *string `json:"finalError,omitempty"`
}

func MarshalSessionEvent(event any) ([]byte, error) {
	if core, ok := event.(agent.AgentEvent); ok {
		return agent.MarshalAgentEvent(core)
	}
	switch typed := event.(type) {
	case SessionAgentEndEvent:
		return ai.Marshal(struct {
			Type      SessionEventType    `json:"type"`
			Messages  agent.AgentMessages `json:"messages"`
			WillRetry bool                `json:"willRetry"`
		}{SessionEventType(agent.EventAgentEnd), typed.Messages, typed.WillRetry})
	case AgentSettledEvent:
		return ai.Marshal(struct {
			Type SessionEventType `json:"type"`
		}{EventAgentSettled})
	case QueueUpdateEvent:
		return ai.Marshal(struct {
			Type     SessionEventType `json:"type"`
			Steering []string         `json:"steering"`
			FollowUp []string         `json:"followUp"`
		}{EventQueueUpdate, typed.Steering, typed.FollowUp})
	case CompactionStartEvent:
		return ai.Marshal(struct {
			Type   SessionEventType `json:"type"`
			Reason string           `json:"reason"`
		}{EventCompactionStart, typed.Reason})
	case CompactionEndEvent:
		return ai.Marshal(struct {
			Type         SessionEventType          `json:"type"`
			Reason       string                    `json:"reason"`
			Result       *harness.CompactionResult `json:"result,omitempty"`
			Aborted      bool                      `json:"aborted"`
			WillRetry    bool                      `json:"willRetry"`
			ErrorMessage *string                   `json:"errorMessage,omitempty"`
		}{EventCompactionEnd, typed.Reason, typed.Result, typed.Aborted, typed.WillRetry, typed.ErrorMessage})
	case AutoRetryStartEvent:
		return ai.Marshal(struct {
			Type         SessionEventType `json:"type"`
			Attempt      int              `json:"attempt"`
			MaxAttempts  int              `json:"maxAttempts"`
			DelayMS      int64            `json:"delayMs"`
			ErrorMessage string           `json:"errorMessage"`
		}{EventAutoRetryStart, typed.Attempt, typed.MaxAttempts, typed.DelayMS, typed.ErrorMessage})
	case AutoRetryEndEvent:
		return ai.Marshal(struct {
			Type       SessionEventType `json:"type"`
			Success    bool             `json:"success"`
			Attempt    int              `json:"attempt"`
			FinalError *string          `json:"finalError,omitempty"`
		}{EventAutoRetryEnd, typed.Success, typed.Attempt, typed.FinalError})
	default:
		return nil, errors.New("codingagent: unknown session event")
	}
}
