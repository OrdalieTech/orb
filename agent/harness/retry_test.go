package harness

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
)

func TestRetryingCompleteFuncRetriesCompactionWithoutChangingCompactionCore(t *testing.T) {
	preparation := &CompactionPreparation{
		FirstKeptEntryID:    "entry-0",
		MessagesToSummarize: agent.AgentMessages{user("work")},
		Settings:            CompactionSettings{ReserveTokens: 1000},
	}
	model := &ai.Model{MaxTokens: 100, ContextWindow: 1000}
	calls := 0
	events := []string{}
	complete := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		calls++
		if calls == 1 {
			message := "terminated"
			return &ai.AssistantMessage{StopReason: ai.StopReasonError, ErrorMessage: &message}, nil
		}
		return assistant("## Goal\nRecovered summary", 10), nil
	}
	retrying := RetryingCompleteFunc(complete, &ai.RetryPolicy{Enabled: true, MaxRetries: 1}, &ai.RetryCallbacks{
		OnRetryScheduled: func(attempt, maxAttempts int, delayMS int64, errorMessage string) error {
			events = append(events, "scheduled")
			return nil
		},
		OnRetryAttemptStart: func() error {
			events = append(events, "attempt-start")
			return nil
		},
		OnRetryFinished: func(success bool, attempt int, finalError *string) error {
			events = append(events, "finished")
			return nil
		},
	})

	result, err := Compact(context.Background(), preparation, model, retrying, "", ai.ModelThinkingOff)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(result.Summary, "Recovered summary") || !reflect.DeepEqual(events, []string{"scheduled", "attempt-start", "finished"}) {
		t.Fatalf("calls=%d result=%#v events=%#v", calls, result, events)
	}
}

func TestGenerateBranchSummaryRetriesTransientErrors(t *testing.T) {
	entries := linearEntries(user("branch request"), assistant("branch response", 20))
	model := &ai.Model{MaxTokens: 100, ContextWindow: 1000}
	calls := 0
	events := []string{}
	complete := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		calls++
		if calls == 1 {
			message := "terminated"
			return &ai.AssistantMessage{StopReason: ai.StopReasonError, ErrorMessage: &message}, nil
		}
		return assistant("## Goal\nRecovered branch summary", 10), nil
	}
	result, err := GenerateBranchSummary(context.Background(), entries, GenerateBranchSummaryOptions{
		Model:    model,
		Complete: complete,
		Retry:    &ai.RetryPolicy{Enabled: true, MaxRetries: 1},
		Callbacks: &ai.RetryCallbacks{
			OnRetryScheduled:    func(int, int, int64, string) error { events = append(events, "scheduled"); return nil },
			OnRetryAttemptStart: func() error { events = append(events, "attempt-start"); return nil },
			OnRetryFinished:     func(bool, int, *string) error { events = append(events, "finished"); return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(result.Summary, "Recovered branch summary") || !reflect.DeepEqual(events, []string{"scheduled", "attempt-start", "finished"}) {
		t.Fatalf("calls=%d result=%#v events=%#v", calls, result, events)
	}
}

func TestGenerateBranchSummaryDoesNotRetryQuotaErrors(t *testing.T) {
	entries := linearEntries(user("branch request"), assistant("branch response", 20))
	calls := 0
	message := "insufficient_quota"
	_, err := GenerateBranchSummary(context.Background(), entries, GenerateBranchSummaryOptions{
		Model: &ai.Model{MaxTokens: 100, ContextWindow: 1000},
		Complete: func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
			calls++
			return &ai.AssistantMessage{StopReason: ai.StopReasonError, ErrorMessage: &message}, nil
		},
		Retry: &ai.RetryPolicy{Enabled: true, MaxRetries: 3},
	})
	if err == nil || !strings.Contains(err.Error(), "insufficient_quota") || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
