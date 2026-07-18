package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func TestSessionEventWireShapes(t *testing.T) {
	cases := []struct {
		name  string
		event any
		want  string
	}{
		{"queue", QueueUpdateEvent{Steering: []string{"s"}, FollowUp: []string{}}, `{"type":"queue_update","steering":["s"],"followUp":[]}`},
		{"compaction-start", CompactionStartEvent{Reason: "threshold"}, `{"type":"compaction_start","reason":"threshold"}`},
		{"compaction-end-error", CompactionEndEvent{Reason: "overflow", ErrorMessage: stringPointer("failed")}, `{"type":"compaction_end","reason":"overflow","aborted":false,"willRetry":false,"errorMessage":"failed"}`},
		{"compaction-end-result", CompactionEndEvent{Reason: "threshold", Result: &harness.CompactionResult{Summary: "s", FirstKeptEntryID: "id", TokensBefore: 12, Details: harness.CompactionDetails{ReadFiles: []string{}, ModifiedFiles: []string{}}}}, `{"type":"compaction_end","reason":"threshold","result":{"summary":"s","firstKeptEntryId":"id","tokensBefore":12,"estimatedTokensAfter":0,"details":{"readFiles":[],"modifiedFiles":[]}},"aborted":false,"willRetry":false}`},
		{"retry-start", AutoRetryStartEvent{Attempt: 1, MaxAttempts: 3, DelayMS: 2000, ErrorMessage: "overloaded"}, `{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"overloaded"}`},
		{"retry-end", AutoRetryEndEvent{Success: true, Attempt: 2}, `{"type":"auto_retry_end","success":true,"attempt":2}`},
		{"entry-appended", EntryAppendedEvent{Entry: sessionstore.SessionEntry{Type: "custom", ID: "entry", Timestamp: "2026-01-02T03:04:05.000Z"}}, `{"type":"entry_appended","entry":{"type":"custom","customType":"","id":"entry","parentId":null,"timestamp":"2026-01-02T03:04:05.000Z"}}`},
		{"session-info-missing", SessionInfoChangedEvent{}, `{"type":"session_info_changed"}`},
		{"session-info", SessionInfoChangedEvent{Name: stringPointer("named")}, `{"type":"session_info_changed","name":"named"}`},
		{"thinking-level", ThinkingLevelChangedEvent{Level: ai.ModelThinkingHigh}, `{"type":"thinking_level_changed","level":"high"}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := MarshalSessionEvent(test.event)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("wire = %s\nwant = %s", got, test.want)
			}
		})
	}
}

func TestSessionRuntimeSettlesWhenInitialRunFails(t *testing.T) {
	provider := testFaux(1000)
	runtime, _ := newTestRuntime(t, provider, map[string]any{"compaction": map[string]any{"enabled": false}})
	settled := 0
	runtime.Subscribe(func(event any) {
		if _, ok := event.(AgentSettledEvent); ok {
			settled++
		}
	})
	want := errors.New("initial failure")
	if err := runtime.runPolicies(context.Background(), func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("run error = %v, want %v", err, want)
	}
	if settled != 1 {
		t.Fatalf("settled events = %d, want 1", settled)
	}
}

func TestSessionRuntimeWaitForIdleIncludesSettledListeners(t *testing.T) {
	provider := testFaux(1000)
	runtime, _ := newTestRuntime(t, provider, map[string]any{"compaction": map[string]any{"enabled": false}})
	listenerStarted := make(chan struct{})
	releaseListener := make(chan struct{})
	runtime.Subscribe(func(event any) {
		if _, ok := event.(AgentSettledEvent); ok {
			close(listenerStarted)
			<-releaseListener
		}
	})
	runDone := make(chan error, 1)
	go func() {
		runDone <- runtime.runPolicies(context.Background(), func() error { return nil })
	}()
	select {
	case <-listenerStarted:
	case <-time.After(time.Second):
		t.Fatal("settled listener did not start")
	}
	idleDone := make(chan error, 1)
	go func() { idleDone <- runtime.WaitForIdle(context.Background()) }()
	select {
	case err := <-idleDone:
		t.Fatalf("idle returned before settled listener completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseListener)
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if err := <-idleDone; err != nil {
		t.Fatal(err)
	}
}

func TestSessionRuntimeDisposeDropsListeners(t *testing.T) {
	provider := testFaux(1000)
	runtime, _ := newTestRuntime(t, provider, map[string]any{"compaction": map[string]any{"enabled": false}})
	called := false
	runtime.Subscribe(func(any) { called = true })
	runtime.Dispose()
	runtime.emit(AgentSettledEvent{})
	if called {
		t.Fatal("disposed runtime retained event listeners")
	}
}

func TestSessionRuntimeDefaultCompletionAppliesRequestAuth(t *testing.T) {
	provider := testFaux(1000)
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
		Model: provider.GetModel(), SystemPrompt: "test", Messages: agent.AgentMessages{}, Tools: []agent.AgentTool{},
	}))
	baseURL := "https://vertex.example.test/v1"
	configuredHeader := "configured"
	stream := func(
		_ context.Context,
		model *ai.Model,
		_ ai.Context,
		options *ai.SimpleStreamOptions,
	) (ai.AssistantMessageEventStream, error) {
		if model.BaseURL != baseURL {
			t.Fatalf("completion base URL = %q, want %q", model.BaseURL, baseURL)
		}
		if options.Env["GOOGLE_CLOUD_PROJECT"] != "configured-project" || options.Env["GOOGLE_CLOUD_LOCATION"] != "us-central1" {
			t.Fatalf("completion environment = %#v", options.Env)
		}
		if options.Headers["authorization"] == nil || *options.Headers["authorization"] != configuredHeader {
			t.Fatalf("completion headers = %#v", options.Headers)
		}
		if _, duplicate := options.Headers["Authorization"]; duplicate {
			t.Fatalf("case-insensitive completion auth override left duplicate headers: %#v", options.Headers)
		}
		message := runtimeAssistant(provider, "summary", 2)
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: message}, nil)
		}, nil
	}
	modelHeadersResolved := false
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, StreamFn: stream,
		GetRequestAuth: func(context.Context, ai.ProviderID) (*agent.RequestAuth, error) {
			return &agent.RequestAuth{
				Env: ai.ProviderEnv{
					"GOOGLE_CLOUD_PROJECT":  "resolved-project",
					"GOOGLE_CLOUD_LOCATION": "us-central1",
				},
				Headers: map[string]string{"Authorization": "resolved"},
				BaseURL: &baseURL,
			}, nil
		},
		GetModelHeaders: func(_ context.Context, _ *ai.Model, _ *string, env ai.ProviderEnv) (*map[string]string, error) {
			modelHeadersResolved = true
			if env["GOOGLE_CLOUD_PROJECT"] != "configured-project" || env["GOOGLE_CLOUD_LOCATION"] != "us-central1" {
				t.Fatalf("completion model-header environment = %#v", env)
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	options := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		Env:     ai.ProviderEnv{"GOOGLE_CLOUD_PROJECT": "configured-project"},
		Headers: ai.ProviderHeaders{"authorization": &configuredHeader},
	}}
	if _, err := runtime.complete(context.Background(), provider.GetModel(), ai.Context{}, options); err != nil {
		t.Fatal(err)
	}
	if len(options.Env) != 1 || options.Env["GOOGLE_CLOUD_LOCATION"] != "" {
		t.Fatalf("caller completion options were mutated: %#v", options.Env)
	}
	if !modelHeadersResolved {
		t.Fatal("completion model headers were not resolved")
	}
}

func TestSessionRuntimeAbortCompactionCancelsManualAndAutomaticOperations(t *testing.T) {
	provider := testFaux(1000)
	runtime, _ := newTestRuntime(t, provider, map[string]any{"compaction": map[string]any{"enabled": false}})
	manualContext, manualCancel := context.WithCancel(context.Background())
	autoContext, autoCancel := context.WithCancel(context.Background())
	runtime.mu.Lock()
	runtime.compactionCancel = manualCancel
	runtime.autoCompactionCancel = autoCancel
	runtime.mu.Unlock()
	runtime.AbortCompaction()
	if manualContext.Err() == nil || autoContext.Err() == nil {
		t.Fatalf("compaction contexts after abort = (manual %v, auto %v)", manualContext.Err(), autoContext.Err())
	}
}

func TestSessionRuntimeCompactionCancellationEventsAreAborted(t *testing.T) {
	provider := testFaux(1000)
	runtime, manager := newTestRuntime(t, provider, map[string]any{
		"compaction": map[string]any{"enabled": true, "reserveTokens": 50, "keepRecentTokens": 1},
	})
	if _, err := manager.AppendMessage(userMessage("request")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(runtimeAssistant(provider, "answer", 20)); err != nil {
		t.Fatal(err)
	}
	runtime.syncAgentMessages()
	runtime.complete = func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		return nil, context.Canceled
	}
	var ends []CompactionEndEvent
	runtime.Subscribe(func(event any) {
		if ended, ok := event.(CompactionEndEvent); ok {
			ends = append(ends, ended)
		}
	})

	continued, err := runtime.runAutoCompaction(context.Background(), "threshold", false)
	if err != nil || continued {
		t.Fatalf("auto compaction = (%v, %v), want (false, nil)", continued, err)
	}
	if len(ends) != 1 || !ends[0].Aborted || ends[0].ErrorMessage != nil {
		t.Fatalf("auto compaction end = %#v", ends)
	}

	ends = nil
	runtime.complete = func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		return nil, errors.New("summary unavailable")
	}
	if _, err := runtime.runAutoCompaction(context.Background(), "threshold", false); err != nil {
		t.Fatalf("failed auto compaction returned error: %v", err)
	}
	if len(ends) != 1 || ends[0].Aborted || ends[0].ErrorMessage == nil || !strings.Contains(*ends[0].ErrorMessage, "summary unavailable") {
		t.Fatalf("failed auto compaction end = %#v", ends)
	}

	ends = nil
	runtime.complete = func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		return nil, context.Canceled
	}
	if _, err := runtime.Compact(context.Background(), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("manual compaction error = %v, want context canceled", err)
	}
	if len(ends) != 1 || !ends[0].Aborted || ends[0].ErrorMessage != nil {
		t.Fatalf("manual compaction end = %#v", ends)
	}
}

func TestSessionRuntimeCancellationWinsOverNonCooperativeCompactionResult(t *testing.T) {
	tests := []struct {
		name string
		run  func(*SessionRuntime) error
	}{
		{
			name: "automatic",
			run: func(runtime *SessionRuntime) error {
				_, err := runtime.runAutoCompaction(context.Background(), "threshold", false)
				return err
			},
		},
		{
			name: "manual",
			run: func(runtime *SessionRuntime) error {
				_, err := runtime.Compact(context.Background(), "")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := testFaux(1000)
			runtime, manager := newTestRuntime(t, provider, map[string]any{
				"compaction": map[string]any{"enabled": true, "reserveTokens": 50, "keepRecentTokens": 1},
			})
			_, _ = manager.AppendMessage(userMessage("request"))
			_, _ = manager.AppendMessage(runtimeAssistant(provider, "response", 100))
			runtime.syncAgentMessages()
			started := make(chan struct{})
			release := make(chan struct{})
			runtime.complete = func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
				close(started)
				<-release
				return runtimeAssistant(provider, "summary despite cancellation", 10), nil
			}
			var end CompactionEndEvent
			runtime.Subscribe(func(event any) {
				if typed, ok := event.(CompactionEndEvent); ok {
					end = typed
				}
			})
			done := make(chan error, 1)
			go func() { done <- test.run(runtime) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("compaction did not start")
			}
			runtime.AbortCompaction()
			close(release)
			err := <-done
			if test.name == "manual" {
				if err == nil || err.Error() != "Compaction cancelled" {
					t.Fatalf("manual cancellation error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !end.Aborted || end.Result != nil || end.ErrorMessage != nil {
				t.Fatalf("compaction end = %#v", end)
			}
			for _, entry := range manager.GetEntries() {
				if entry.Type == "compaction" {
					t.Fatalf("cancelled compaction was persisted: %#v", entry)
				}
			}
		})
	}
}

func TestSessionRuntimeRetriesAndEmitsLifecycle(t *testing.T) {
	provider := testFaux(1000)
	provider.SetResponses([]faux.ResponseStep{
		runtimeError(provider, "overloaded_error"),
		runtimeAssistant(provider, "recovered", 20),
	})
	runtime, manager := newTestRuntime(t, provider, map[string]any{
		"compaction": map[string]any{"enabled": false},
		"retry":      map[string]any{"enabled": true, "maxRetries": 3, "baseDelayMs": 1},
	})
	runtime.sleep = func(context.Context, time.Duration) error { return nil }
	var retryEvents []string
	var willRetry []bool
	runtime.Subscribe(func(event any) {
		switch typed := event.(type) {
		case AutoRetryStartEvent:
			retryEvents = append(retryEvents, "start")
		case AutoRetryEndEvent:
			if typed.Success {
				retryEvents = append(retryEvents, "success")
			}
		case SessionAgentEndEvent:
			willRetry = append(willRetry, typed.WillRetry)
		}
	})
	if err := runtime.Prompt(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if provider.State().CallCount != 2 || !reflect.DeepEqual(retryEvents, []string{"start", "success"}) || !reflect.DeepEqual(willRetry, []bool{true, false}) {
		t.Fatalf("calls=%d retry=%#v willRetry=%#v", provider.State().CallCount, retryEvents, willRetry)
	}
	entries := manager.GetEntries()
	if len(entries) != 3 {
		t.Fatalf("persisted entries = %d", len(entries))
	}
}

func TestSessionRuntimeRetryExhaustionAndCancellation(t *testing.T) {
	provider := testFaux(1000)
	provider.SetResponses([]faux.ResponseStep{
		runtimeError(provider, "503 server error"), runtimeError(provider, "503 server error"), runtimeError(provider, "503 server error"),
	})
	runtime, _ := newTestRuntime(t, provider, map[string]any{
		"compaction": map[string]any{"enabled": false},
		"retry":      map[string]any{"enabled": true, "maxRetries": 2, "baseDelayMs": 1},
	})
	runtime.sleep = func(context.Context, time.Duration) error { return nil }
	var final *AutoRetryEndEvent
	runtime.Subscribe(func(event any) {
		if typed, ok := event.(AutoRetryEndEvent); ok && !typed.Success {
			copy := typed
			final = &copy
		}
	})
	if err := runtime.Prompt(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if provider.State().CallCount != 3 || final == nil || final.Attempt != 2 {
		t.Fatalf("calls=%d final=%#v", provider.State().CallCount, final)
	}

	cancelProvider := testFaux(1000)
	cancelProvider.SetResponses([]faux.ResponseStep{runtimeError(cancelProvider, "overloaded")})
	cancelRuntime, _ := newTestRuntime(t, cancelProvider, map[string]any{
		"compaction": map[string]any{"enabled": false},
		"retry":      map[string]any{"enabled": true, "maxRetries": 3, "baseDelayMs": 1},
	})
	cancelRuntime.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	var cancelled string
	cancelRuntime.Subscribe(func(event any) {
		if typed, ok := event.(AutoRetryEndEvent); ok && typed.FinalError != nil {
			cancelled = *typed.FinalError
		}
	})
	if err := cancelRuntime.Prompt(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if cancelled != "Retry cancelled" || cancelProvider.State().CallCount != 1 {
		t.Fatalf("cancelled=%q calls=%d", cancelled, cancelProvider.State().CallCount)
	}
}

func TestSessionRuntimeQueueUpdatesDrainBeforeQueuedMessageStart(t *testing.T) {
	provider := testFaux(1000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "first", 10), runtimeAssistant(provider, "second", 20)})
	runtime, _ := newTestRuntime(t, provider, map[string]any{
		"compaction": map[string]any{"enabled": false}, "retry": map[string]any{"enabled": false},
	})
	var queueLengths []int
	queued := false
	runtime.Subscribe(func(event any) {
		if queue, ok := event.(QueueUpdateEvent); ok {
			queueLengths = append(queueLengths, len(queue.Steering))
		}
		if start, ok := event.(agent.MessageStartEvent); ok {
			if _, assistantStart := start.Message.(*ai.AssistantMessage); assistantStart && !queued {
				queued = true
				runtime.Steer("queued")
			}
		}
	})
	if err := runtime.Prompt(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(queueLengths, []int{1, 0}) {
		t.Fatalf("queue lengths = %#v", queueLengths)
	}
	if provider.State().CallCount != 2 {
		t.Fatalf("provider calls = %d", provider.State().CallCount)
	}
}

func TestSessionRuntimePromptCompactsResumedOversizedAssistantBeforeRequest(t *testing.T) {
	provider := testFaux(100)
	order := []string{}
	provider.SetResponses([]faux.ResponseStep{faux.Factory(func(_ context.Context, requestContext ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		order = append(order, "prompt")
		foundLengthAssistant := false
		for _, message := range requestContext.Messages {
			if assistant := asAssistant(message); assistant != nil && assistant.StopReason == ai.StopReasonLength {
				foundLengthAssistant = true
			}
		}
		if !foundLengthAssistant {
			return nil, errors.New("resumed length assistant was dropped after pre-prompt compaction")
		}
		return runtimeAssistant(provider, "after resume", 10), nil
	})})
	runtime, manager := newTestRuntime(t, provider, map[string]any{
		"compaction": map[string]any{"enabled": true, "reserveTokens": 20, "keepRecentTokens": 1},
		"retry":      map[string]any{"enabled": false},
	})
	if _, err := manager.AppendMessage(userMessage("before resume")); err != nil {
		t.Fatal(err)
	}
	oversized := runtimeAssistant(provider, "oversized response", 100)
	oversized.StopReason = ai.StopReasonLength
	if _, err := manager.AppendMessage(oversized); err != nil {
		t.Fatal(err)
	}
	runtime.syncAgentMessages()
	runtime.complete = func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		order = append(order, "compact")
		return runtimeAssistant(provider, "## Goal\nResume compacted", 10), nil
	}
	var compactionEnd CompactionEndEvent
	runtime.Subscribe(func(event any) {
		if typed, ok := event.(CompactionEndEvent); ok {
			compactionEnd = typed
		}
	})

	if err := runtime.Prompt(context.Background(), "after resume"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"compact", "prompt"}) {
		t.Fatalf("request order = %#v, want pre-prompt compaction first", order)
	}
	if !compactionEnd.WillRetry || compactionEnd.Reason != "overflow" || provider.State().CallCount != 1 {
		t.Fatalf("pre-prompt compaction end = %#v, provider calls = %d", compactionEnd, provider.State().CallCount)
	}
	branch := manager.GetBranch()
	compactionIndex := -1
	for index := range branch {
		if branch[index].Type == "compaction" {
			compactionIndex = index
		}
	}
	if compactionIndex < 0 || compactionIndex >= len(branch)-2 {
		t.Fatalf("compaction index = %d in branch %#v", compactionIndex, branch)
	}
}

func TestSessionRuntimeRejectsConcurrentPromptDuringRetryPolicy(t *testing.T) {
	provider := testFaux(1000)
	provider.SetResponses([]faux.ResponseStep{runtimeError(provider, "503 overloaded")})
	runtime, _ := newTestRuntime(t, provider, map[string]any{
		"compaction": map[string]any{"enabled": false},
		"retry":      map[string]any{"enabled": true, "maxRetries": 2, "baseDelayMs": 1},
	})
	retryStarted := make(chan struct{})
	runtime.sleep = func(ctx context.Context, _ time.Duration) error {
		close(retryStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- runtime.Prompt(context.Background(), "first") }()
	select {
	case <-retryStarted:
	case <-time.After(time.Second):
		t.Fatal("retry policy did not start")
	}
	err := runtime.Prompt(context.Background(), "second")
	const want = "Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message."
	if err == nil || err.Error() != want {
		t.Fatalf("concurrent prompt error = %v, want %q", err, want)
	}
	if provider.State().CallCount != 1 {
		t.Fatalf("provider calls after concurrent prompt = %d, want 1", provider.State().CallCount)
	}
	runtime.Abort()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestSessionRuntimeManualCompactionDisconnectsAndWaitsForActiveRun(t *testing.T) {
	provider := testFaux(1000)
	requestStarted := make(chan struct{})
	provider.SetResponses([]faux.ResponseStep{faux.Factory(func(ctx context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		close(requestStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	})})
	runtime, manager := newTestRuntime(t, provider, map[string]any{
		"compaction": map[string]any{"enabled": true, "reserveTokens": 50, "keepRecentTokens": 1},
		"retry":      map[string]any{"enabled": false},
	})
	if _, err := manager.AppendMessage(userMessage("seed request")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(runtimeAssistant(provider, "seed response", 100)); err != nil {
		t.Fatal(err)
	}
	runtime.syncAgentMessages()
	idleBeforeSummary := false
	runtime.complete = func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		idleBeforeSummary = !runtime.agent.State().IsStreaming
		return runtimeAssistant(provider, "## Goal\nManual compacted", 10), nil
	}

	promptDone := make(chan error, 1)
	go func() { promptDone <- runtime.Prompt(context.Background(), "active request") }()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("active request did not start")
	}
	if _, err := runtime.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-promptDone:
		if err != nil {
			t.Fatalf("aborted prompt error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("aborted prompt did not settle")
	}
	if !idleBeforeSummary {
		t.Fatal("manual compaction snapshotted while the agent run was still active")
	}
	for _, entry := range manager.GetEntries() {
		if entry.Type != "message" {
			continue
		}
		if assistant := asAssistant(decodeSessionMessage(entry.Message)); assistant != nil && assistant.StopReason == ai.StopReasonAborted {
			t.Fatalf("aborted assistant was persisted during manual compaction: %#v", assistant)
		}
	}
	entriesBeforeReconnect := len(manager.GetEntries())
	provider.AppendResponses([]faux.ResponseStep{runtimeAssistant(provider, "after reconnect", 10)})
	if err := runtime.Prompt(context.Background(), "after compact"); err != nil {
		t.Fatal(err)
	}
	if entriesAfterReconnect := len(manager.GetEntries()); entriesAfterReconnect != entriesBeforeReconnect+2 {
		t.Fatalf("entries after reconnect = %d, want %d", entriesAfterReconnect, entriesBeforeReconnect+2)
	}
}

func TestSessionRuntimeManualCompactionWaitsForRetryPolicyIdle(t *testing.T) {
	provider := testFaux(1000)
	provider.SetResponses([]faux.ResponseStep{runtimeError(provider, "503 overloaded")})
	runtime, manager := newTestRuntime(t, provider, map[string]any{
		"compaction": map[string]any{"enabled": true, "reserveTokens": 50, "keepRecentTokens": 1},
		"retry":      map[string]any{"enabled": true, "maxRetries": 2, "baseDelayMs": 1},
	})
	_, _ = manager.AppendMessage(userMessage("seed request"))
	_, _ = manager.AppendMessage(runtimeAssistant(provider, "seed response", 100))
	runtime.syncAgentMessages()
	retryStarted := make(chan struct{})
	runtime.sleep = func(ctx context.Context, _ time.Duration) error {
		close(retryStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	idleBeforeSummary := false
	runtime.complete = func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		runtime.mu.Lock()
		idleBeforeSummary = runtime.activeRuns == 0
		runtime.mu.Unlock()
		return runtimeAssistant(provider, "## Goal\nRetry compacted", 10), nil
	}
	promptDone := make(chan error, 1)
	go func() { promptDone <- runtime.Prompt(context.Background(), "retrying request") }()
	select {
	case <-retryStarted:
	case <-time.After(time.Second):
		t.Fatal("retry policy did not start")
	}
	if _, err := runtime.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if err := <-promptDone; err != nil {
		t.Fatalf("retrying prompt error = %v", err)
	}
	if !idleBeforeSummary {
		t.Fatal("manual compaction began before retry policy settled")
	}
}

func TestSessionRuntimeAbortPreservesQueuesUntilClearQueue(t *testing.T) {
	provider := testFaux(1000)
	runtime, _ := newTestRuntime(t, provider, map[string]any{"compaction": map[string]any{"enabled": false}})
	runtime.Steer("steer")
	runtime.FollowUp("follow")

	runtime.Abort()
	if !reflect.DeepEqual(runtime.steering, []string{"steer"}) || !reflect.DeepEqual(runtime.followUps, []string{"follow"}) {
		t.Fatalf("queues after abort = (%#v, %#v)", runtime.steering, runtime.followUps)
	}
	if !runtime.agent.HasQueuedMessages() {
		t.Fatal("agent queues were cleared by ordinary abort")
	}
	cleared := runtime.ClearQueue()
	if !reflect.DeepEqual(cleared.Steering, []string{"steer"}) || !reflect.DeepEqual(cleared.FollowUp, []string{"follow"}) {
		t.Fatalf("cleared queues = %#v", cleared)
	}
	if len(runtime.steering) != 0 || len(runtime.followUps) != 0 || runtime.agent.HasQueuedMessages() {
		t.Fatalf("queues remain after ClearQueue: (%#v, %#v)", runtime.steering, runtime.followUps)
	}
}

func TestSessionRuntimeTargetedCompactionAndBranchSummaryCancellation(t *testing.T) {
	t.Run("compaction", func(t *testing.T) {
		provider := testFaux(1000)
		runtime, manager := newTestRuntime(t, provider, map[string]any{
			"compaction": map[string]any{"enabled": true, "reserveTokens": 50, "keepRecentTokens": 1},
		})
		_, _ = manager.AppendMessage(userMessage("request"))
		_, _ = manager.AppendMessage(runtimeAssistant(provider, "response", 100))
		runtime.syncAgentMessages()
		started := make(chan context.Context, 1)
		runtime.complete = func(ctx context.Context, _ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
			started <- ctx
			<-ctx.Done()
			return nil, ctx.Err()
		}
		done := make(chan error, 1)
		go func() {
			_, err := runtime.Compact(context.Background(), "")
			done <- err
		}()
		select {
		case summaryContext := <-started:
			runtime.Abort()
			if summaryContext.Err() != nil {
				t.Fatalf("ordinary abort cancelled compaction: %v", summaryContext.Err())
			}
		case <-time.After(time.Second):
			t.Fatal("compaction did not start")
		}
		runtime.AbortCompaction()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("compaction cancellation error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("compaction cancellation did not settle")
		}
	})

	t.Run("branch summary", func(t *testing.T) {
		provider := testFaux(1000)
		runtime, manager := newTestRuntime(t, provider, map[string]any{
			"branchSummary": map[string]any{"reserveTokens": 100},
			"compaction":    map[string]any{"enabled": false},
		})
		first, _ := manager.AppendMessage(userMessage("first"))
		_, _ = manager.AppendMessage(runtimeAssistant(provider, "first answer", 10))
		_, _ = manager.AppendMessage(userMessage("second"))
		_, _ = manager.AppendMessage(runtimeAssistant(provider, "second answer", 20))
		runtime.syncAgentMessages()
		started := make(chan context.Context, 1)
		runtime.complete = func(ctx context.Context, _ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
			started <- ctx
			<-ctx.Done()
			return nil, ctx.Err()
		}
		done := make(chan NavigateTreeResult, 1)
		errs := make(chan error, 1)
		go func() {
			result, err := runtime.NavigateTree(context.Background(), first, NavigateTreeOptions{Summarize: true})
			done <- result
			errs <- err
		}()
		select {
		case summaryContext := <-started:
			runtime.Abort()
			if summaryContext.Err() != nil {
				t.Fatalf("ordinary abort cancelled branch summary: %v", summaryContext.Err())
			}
		case <-time.After(time.Second):
			t.Fatal("branch summary did not start")
		}
		runtime.AbortBranchSummary()
		select {
		case result := <-done:
			if err := <-errs; err != nil || !result.Cancelled || !result.Aborted {
				t.Fatalf("branch cancellation = (%#v, %v)", result, err)
			}
		case <-time.After(time.Second):
			t.Fatal("branch cancellation did not settle")
		}
	})
}

func TestSessionRuntimeListenersEmitInSubscriptionOrder(t *testing.T) {
	provider := testFaux(1000)
	runtime, _ := newTestRuntime(t, provider, map[string]any{"compaction": map[string]any{"enabled": false}})
	for iteration := 0; iteration < 100; iteration++ {
		order := []int{}
		unsubscribers := make([]func(), 0, 3)
		for listener := 1; listener <= 3; listener++ {
			listener := listener
			unsubscribers = append(unsubscribers, runtime.Subscribe(func(any) { order = append(order, listener) }))
		}
		runtime.emit(AgentSettledEvent{})
		for _, unsubscribe := range unsubscribers {
			unsubscribe()
		}
		if !reflect.DeepEqual(order, []int{1, 2, 3}) {
			t.Fatalf("iteration %d listener order = %#v", iteration, order)
		}
	}
}

func TestLongFauxSessionCompactsAtHarnessBoundary(t *testing.T) {
	provider := testFaux(200)
	provider.SetResponses([]faux.ResponseStep{
		runtimeAssistant(provider, "one", 50), runtimeAssistant(provider, "two", 100),
		runtimeAssistant(provider, "three", 160), runtimeAssistant(provider, "## Goal\nCompacted", 10),
	})
	runtime, manager := newTestRuntime(t, provider, map[string]any{
		"compaction": map[string]any{"enabled": true, "reserveTokens": 50, "keepRecentTokens": 8},
		"retry":      map[string]any{"enabled": false},
	})
	for _, prompt := range []string{
		"first " + strings.Repeat("a", 200),
		"second " + strings.Repeat("b", 200),
		"third " + strings.Repeat("c", 200),
	} {
		if err := runtime.Prompt(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	branch := manager.GetBranch()
	if branch[len(branch)-1].Type != "compaction" {
		t.Fatalf("last entry = %#v", branch[len(branch)-1])
	}
	before := projectSessionEntries(branch[:len(branch)-1])
	want, err := harness.PrepareCompaction(before, harness.CompactionSettings{Enabled: true, ReserveTokens: 50, KeepRecentTokens: 8})
	if err != nil {
		t.Fatal(err)
	}
	compaction := branch[len(branch)-1]
	if want == nil || compaction.FirstKeptEntryID != want.FirstKeptEntryID || compaction.TokensBefore != want.TokensBefore {
		t.Fatalf("compaction boundary=(%s,%d), want=(%#v)", compaction.FirstKeptEntryID, compaction.TokensBefore, want)
	}
	usage := runtime.GetContextUsage()
	if usage == nil || usage.Tokens != nil || usage.Percent != nil {
		t.Fatalf("post-compaction usage = %#v", usage)
	}

	provider.AppendResponses([]faux.ResponseStep{runtimeAssistant(provider, "after", 20)})
	if err := runtime.Prompt(context.Background(), "after compaction"); err != nil {
		t.Fatal(err)
	}
	usage = runtime.GetContextUsage()
	state := runtime.State()
	lastAssistant := asAssistant(state.Messages[len(state.Messages)-1])
	wantTokens := harness.CalculateContextTokens(lastAssistant.Usage)
	if usage == nil || usage.Tokens == nil || *usage.Tokens != wantTokens {
		t.Fatalf("post-response usage = %#v", usage)
	}
}

func TestNavigateTreeCreatesBranchSummary(t *testing.T) {
	provider := testFaux(1000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "## Goal\nBranch work", 10)})
	runtime, manager := newTestRuntime(t, provider, map[string]any{
		"branchSummary": map[string]any{"reserveTokens": 100}, "compaction": map[string]any{"enabled": false},
	})
	first, _ := manager.AppendMessage(userMessage("first"))
	_, _ = manager.AppendMessage(runtimeAssistant(provider, "first answer", 10))
	_, _ = manager.AppendMessage(userMessage("second"))
	_, _ = manager.AppendMessage(runtimeAssistant(provider, "second answer", 20))
	runtime.syncAgentMessages()
	result, err := runtime.NavigateTree(context.Background(), first, NavigateTreeOptions{Summarize: true, Label: "return"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Cancelled || result.EditorText != "first" || result.SummaryEntry == nil || result.SummaryEntry.Type != "branch_summary" {
		t.Fatalf("result = %#v", result)
	}
	if result.SummaryEntry.ParentID != nil || result.SummaryEntry.FromID != "root" {
		t.Fatalf("summary location = %#v", result.SummaryEntry)
	}
	if label := manager.GetLabel(result.SummaryEntry.ID); label == nil || *label != "return" {
		t.Fatalf("label = %#v", label)
	}
}

func TestSessionRuntimeResolvesModelHeadersForSummaryRequests(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *SessionRuntime, *sessionstore.SessionManager, *faux.Provider)
	}{
		{
			name: "compaction",
			run: func(t *testing.T, runtime *SessionRuntime, manager *sessionstore.SessionManager, provider *faux.Provider) {
				t.Helper()
				_, _ = manager.AppendMessage(userMessage("first request"))
				_, _ = manager.AppendMessage(runtimeAssistant(provider, "first answer", 10))
				_, _ = manager.AppendMessage(userMessage("second request"))
				runtime.syncAgentMessages()
				if _, err := runtime.Compact(context.Background(), ""); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "branch summary",
			run: func(t *testing.T, runtime *SessionRuntime, manager *sessionstore.SessionManager, provider *faux.Provider) {
				t.Helper()
				first, _ := manager.AppendMessage(userMessage("first"))
				_, _ = manager.AppendMessage(runtimeAssistant(provider, "first answer", 10))
				_, _ = manager.AppendMessage(userMessage("second"))
				_, _ = manager.AppendMessage(runtimeAssistant(provider, "second answer", 20))
				runtime.syncAgentMessages()
				if _, err := runtime.NavigateTree(context.Background(), first, NavigateTreeOptions{Summarize: true}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := testFaux(1000)
			seenHeaders := make([]map[string]string, 0, 2)
			response := faux.ResponseFactory(func(_ context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, model *ai.Model) (*ai.AssistantMessage, error) {
				headers := map[string]string{}
				if model.Headers != nil {
					for name, value := range *model.Headers {
						headers[name] = value
					}
				}
				seenHeaders = append(seenHeaders, headers)
				return runtimeAssistant(provider, "summary", 10), nil
			})
			provider.SetResponses([]faux.ResponseStep{response, response})
			resolveCalls := 0
			runtime, manager := newTestRuntimeWithHeaders(t, provider, map[string]any{
				"compaction":    map[string]any{"enabled": true, "reserveTokens": 50, "keepRecentTokens": 1},
				"branchSummary": map[string]any{"reserveTokens": 100},
			}, func(_ context.Context, _ *ai.Model, apiKey *string, env ai.ProviderEnv) (*map[string]string, error) {
				if apiKey == nil || *apiKey != "test" {
					t.Fatalf("header resolver api key = %#v", apiKey)
				}
				if env != nil {
					t.Fatalf("header resolver environment = %#v", env)
				}
				resolveCalls++
				headers := map[string]string{"X-Dynamic": "request-time"}
				return &headers, nil
			})

			test.run(t, runtime, manager, provider)
			if resolveCalls == 0 || len(seenHeaders) == 0 {
				t.Fatalf("resolver calls = %d, provider requests = %d", resolveCalls, len(seenHeaders))
			}
			for _, headers := range seenHeaders {
				if headers["X-Dynamic"] != "request-time" {
					t.Fatalf("provider headers = %#v", headers)
				}
			}
		})
	}
}

func newTestRuntime(t *testing.T, provider *faux.Provider, settings map[string]any) (*SessionRuntime, *sessionstore.SessionManager) {
	return newTestRuntimeWithHeaders(t, provider, settings, nil)
}

func newTestRuntimeWithHeaders(t *testing.T, provider *faux.Provider, settings map[string]any, getModelHeaders agent.GetModelHeadersFunc) (*SessionRuntime, *sessionstore.SessionManager) {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	settingsManager, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{Model: provider.GetModel(), SystemPrompt: "test", Messages: agent.AgentMessages{}, Tools: []agent.AgentTool{}}),
		agent.WithStreamFn(provider.StreamSimple), agent.WithConvertToLLM(ConvertToLLM),
	)
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settingsManager, StreamFn: provider.StreamSimple,
		GetAPIKey:       func(context.Context, ai.ProviderID) (*string, error) { return stringPointer("test"), nil },
		GetModelHeaders: getModelHeaders,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	return runtime, manager
}

func testFaux(contextWindow float64) *faux.Provider {
	maxTokens := float64(100)
	return faux.New(faux.Options{
		API: "faux", Provider: "faux",
		Models:    []faux.ModelDefinition{{ID: "faux-1", ContextWindow: &contextWindow, MaxTokens: &maxTokens}},
		TokenSize: faux.FixedTokenSize(1000),
	})
}

func runtimeAssistant(provider *faux.Provider, text string, tokens int64) *ai.AssistantMessage {
	message := faux.AssistantMessage(text)
	model := provider.GetModel()
	message.API = model.API
	message.Provider = model.Provider
	message.Model = model.ID
	message.Usage = ai.Usage{Input: tokens, TotalTokens: tokens, Cost: ai.Cost{}}
	return message
}

func runtimeError(provider *faux.Provider, text string) *ai.AssistantMessage {
	message := runtimeAssistant(provider, "", 0)
	message.StopReason = ai.StopReasonError
	message.ErrorMessage = stringPointer(text)
	return message
}

func stringPointer(value string) *string { return &value }
