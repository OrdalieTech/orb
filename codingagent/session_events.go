package codingagent

import (
	"errors"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

type SessionEventType string

const (
	EventAgentSettled                   SessionEventType = "agent_settled"
	EventQueueUpdate                    SessionEventType = "queue_update"
	EventCompactionStart                SessionEventType = "compaction_start"
	EventCompactionEnd                  SessionEventType = "compaction_end"
	EventAutoRetryStart                 SessionEventType = "auto_retry_start"
	EventAutoRetryEnd                   SessionEventType = "auto_retry_end"
	EventSummarizationRetryScheduled    SessionEventType = "summarization_retry_scheduled"
	EventSummarizationRetryAttemptStart SessionEventType = "summarization_retry_attempt_start"
	EventSummarizationRetryFinished     SessionEventType = "summarization_retry_finished"
	EventEntryAppended                  SessionEventType = "entry_appended"
	EventSessionInfo                    SessionEventType = "session_info_changed"
	EventThinkingLevel                  SessionEventType = "thinking_level_changed"
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
	Reason       string                         `json:"reason"`
	Result       *sessionstore.CompactionResult `json:"result,omitempty"`
	Aborted      bool                           `json:"aborted"`
	WillRetry    bool                           `json:"willRetry"`
	ErrorMessage *string                        `json:"errorMessage,omitempty"`
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

type SummarizationRetryScheduledEvent struct {
	Attempt      int    `json:"attempt"`
	MaxAttempts  int    `json:"maxAttempts"`
	DelayMS      int64  `json:"delayMs"`
	ErrorMessage string `json:"errorMessage"`
}

type SummarizationRetryAttemptStartEvent struct {
	Source string `json:"source"`
	Reason string `json:"reason,omitempty"`
}

type SummarizationRetryFinishedEvent struct{}

type EntryAppendedEvent struct {
	Entry sessionstore.SessionEntry `json:"entry"`
}

type SessionInfoChangedEvent struct {
	Name *string `json:"name,omitempty"`
}

type ThinkingLevelChangedEvent struct {
	Level ai.ModelThinkingLevel `json:"level"`
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
			Type         SessionEventType               `json:"type"`
			Reason       string                         `json:"reason"`
			Result       *sessionstore.CompactionResult `json:"result,omitempty"`
			Aborted      bool                           `json:"aborted"`
			WillRetry    bool                           `json:"willRetry"`
			ErrorMessage *string                        `json:"errorMessage,omitempty"`
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
	case SummarizationRetryScheduledEvent:
		return ai.Marshal(struct {
			Type         SessionEventType `json:"type"`
			Attempt      int              `json:"attempt"`
			MaxAttempts  int              `json:"maxAttempts"`
			DelayMS      int64            `json:"delayMs"`
			ErrorMessage string           `json:"errorMessage"`
		}{EventSummarizationRetryScheduled, typed.Attempt, typed.MaxAttempts, typed.DelayMS, typed.ErrorMessage})
	case SummarizationRetryAttemptStartEvent:
		return ai.Marshal(struct {
			Type   SessionEventType `json:"type"`
			Source string           `json:"source"`
			Reason string           `json:"reason,omitempty"`
		}{EventSummarizationRetryAttemptStart, typed.Source, typed.Reason})
	case SummarizationRetryFinishedEvent:
		return ai.Marshal(struct {
			Type SessionEventType `json:"type"`
		}{EventSummarizationRetryFinished})
	case EntryAppendedEvent:
		return ai.Marshal(struct {
			Type  SessionEventType          `json:"type"`
			Entry sessionstore.SessionEntry `json:"entry"`
		}{EventEntryAppended, typed.Entry})
	case SessionInfoChangedEvent:
		return ai.Marshal(struct {
			Type SessionEventType `json:"type"`
			Name *string          `json:"name,omitempty"`
		}{EventSessionInfo, typed.Name})
	case ThinkingLevelChangedEvent:
		return ai.Marshal(struct {
			Type  SessionEventType      `json:"type"`
			Level ai.ModelThinkingLevel `json:"level"`
		}{EventThinkingLevel, typed.Level})
	default:
		return nil, errors.New("codingagent: unknown session event")
	}
}
