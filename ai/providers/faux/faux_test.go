package faux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

func TestHelpersModelsFactoriesAndRewriting(t *testing.T) {
	timestamp := int64(17)
	call := ToolCall("echo", map[string]any{"text": "hi"})
	if !strings.HasPrefix(call.ID, "tool:") {
		t.Fatalf("tool ID = %q", call.ID)
	}
	if explicitEmpty := ToolCall("echo", nil, ToolCallOptions{ID: ""}); explicitEmpty.ID != "" {
		t.Fatalf("explicit empty tool ID was defaulted to %q", explicitEmpty.ID)
	}
	source := AssistantMessage(ai.AssistantContent{Thinking("think"), call, Text("done")}, AssistantMessageOptions{
		StopReason: ai.StopReasonToolUse,
		Timestamp:  &timestamp,
	})
	fastName := "Faux Fast"
	provider := New(Options{
		API:      "faux:test",
		Provider: "faux-provider",
		Models: []ModelDefinition{
			{ID: "faux-fast", Name: &fastName},
			{ID: "faux-thinker", Reasoning: true},
		},
	})
	provider.SetResponses([]ResponseStep{
		Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, state State, model *ai.Model) (*ai.AssistantMessage, error) {
			if len(request.Messages) != 1 || state.CallCount != 1 || model.ID != "faux-thinker" {
				t.Fatalf("factory inputs: messages=%d state=%d model=%q", len(request.Messages), state.CallCount, model.ID)
			}
			return source, nil
		}),
	})

	models := provider.Models()
	if len(models) != 2 || models[0].Name != fastName || models[1].Name != "faux-thinker" || !models[1].Reasoning {
		t.Fatalf("models = %#v", models)
	}
	if provider.GetModel() != models[0] || provider.GetModel("faux-thinker") != models[1] || provider.GetModel("missing") != nil {
		t.Fatal("model lookup mismatch")
	}
	message := collectMessage(t, provider, models[1], baseContext(), nil)
	if message.API != "faux:test" || message.Provider != "faux-provider" || message.Model != "faux-thinker" {
		t.Fatalf("rewritten message = api:%q provider:%q model:%q", message.API, message.Provider, message.Model)
	}
	if message.Timestamp != timestamp || message.StopReason != ai.StopReasonToolUse {
		t.Fatalf("message metadata = %#v", message)
	}
	if source.API != defaultAPI || source.Provider != defaultProvider || source.Model != defaultModelID {
		t.Fatalf("source message was rewritten: %#v", source)
	}

	message.Content[0].(*ai.ThinkingContent).Thinking = "mutated"
	message.Content[1].(*ai.ToolCall).Arguments["text"] = "mutated"
	message.Content[2].(*ai.TextContent).Text = "mutated"
	if source.Content[0].(*ai.ThinkingContent).Thinking != "think" ||
		source.Content[1].(*ai.ToolCall).Arguments["text"] != "hi" ||
		source.Content[2].(*ai.TextContent).Text != "done" {
		t.Fatal("resolved message was not deeply cloned")
	}
}

func TestDefaultModelsAndTokenSizeClamping(t *testing.T) {
	provider := New()
	model := provider.GetModel()
	if provider.API() == defaultAPI || !strings.HasPrefix(string(provider.API()), "faux:") {
		t.Fatalf("default API = %q", provider.API())
	}
	if provider.ProviderID() != defaultProvider || model.ID != defaultModelID || model.Name != defaultModelName ||
		model.BaseURL != defaultBaseURL || model.ContextWindow != 128000 || model.MaxTokens != 16384 {
		t.Fatalf("default model = %#v", model)
	}
	if len(model.Input) != 2 || model.Input[0] != ai.InputText || model.Input[1] != ai.InputImage {
		t.Fatalf("default input = %#v", model.Input)
	}

	minSize, maxSize := 8, 2
	clamped := New(Options{TokenSize: TokenSize{Min: &minSize, Max: &maxSize}})
	if clamped.minTokenSize != 2 || clamped.maxTokenSize != 2 {
		t.Fatalf("clamped sizes = %d..%d", clamped.minTokenSize, clamped.maxTokenSize)
	}
	negative := -4
	clamped = New(Options{TokenSize: TokenSize{Max: &negative}})
	if clamped.minTokenSize != 1 || clamped.maxTokenSize != 1 {
		t.Fatalf("negative max sizes = %d..%d", clamped.minTokenSize, clamped.maxTokenSize)
	}
}

func TestProviderClockControlsGeneratedIDsErrorsAndAborts(t *testing.T) {
	const fixedNow = int64(1700000000123)
	provider := New(Options{Now: func() int64 { return fixedNow }})
	if wantPrefix := "faux:1700000000123:"; !strings.HasPrefix(string(provider.API()), wantPrefix) {
		t.Fatalf("generated API = %q, want prefix %q", provider.API(), wantPrefix)
	}

	provider.SetResponses([]ResponseStep{Factory(func(context.Context, ai.Context, *ai.StreamOptions, State, *ai.Model) (*ai.AssistantMessage, error) {
		return nil, errors.New("clocked failure")
	})})
	failed := collectMessage(t, provider, provider.GetModel(), baseContext(), nil)
	if failed.Timestamp != fixedNow || failed.StopReason != ai.StopReasonError {
		t.Fatalf("factory error = timestamp:%d reason:%q", failed.Timestamp, failed.StopReason)
	}

	exhausted := collectMessage(t, provider, provider.GetModel(), baseContext(), nil)
	if exhausted.Timestamp != fixedNow || exhausted.ErrorMessage == nil || *exhausted.ErrorMessage != "No more faux responses queued" {
		t.Fatalf("exhausted response = %#v", exhausted)
	}

	provider.SetResponses([]ResponseStep{AssistantMessage("unused")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, err := provider.StreamSimple(ctx, provider.GetModel(), baseContext(), nil)
	if err != nil {
		t.Fatal(err)
	}
	aborted, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if aborted.Timestamp != fixedNow || aborted.StopReason != ai.StopReasonAborted {
		t.Fatalf("aborted response = timestamp:%d reason:%q", aborted.Timestamp, aborted.StopReason)
	}

	standalone := AssistantMessage("wall clock")
	if standalone.Timestamp == fixedNow {
		t.Fatal("provider clock leaked into standalone AssistantMessage helper")
	}
}

func TestQueueReservationReplaceAppendExhaustionAndHook(t *testing.T) {
	provider := New()
	provider.SetResponses([]ResponseStep{AssistantMessage("first"), AssistantMessage("second")})

	var hookCalls atomic.Int64
	options := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		OnResponse: func(_ context.Context, response ai.ProviderResponse, model *ai.Model) error {
			if response.Status != 200 || response.Headers == nil || len(response.Headers) != 0 || model != provider.GetModel() {
				t.Fatalf("response hook inputs = %#v model=%p", response, model)
			}
			hookCalls.Add(1)
			return nil
		},
		OnPayload: func(context.Context, any, *ai.Model) (any, bool, error) {
			t.Fatal("faux must not invoke onPayload")
			return nil, false, nil
		},
	}}

	firstStream, err := provider.StreamSimple(context.Background(), provider.GetModel(), baseContext(), options)
	if err != nil {
		t.Fatal(err)
	}
	secondStream, err := provider.StreamSimple(context.Background(), provider.GetModel(), baseContext(), options)
	if err != nil {
		t.Fatal(err)
	}
	if provider.PendingResponseCount() != 0 || provider.State().CallCount != 2 {
		t.Fatalf("reservation state = pending:%d calls:%d", provider.PendingResponseCount(), provider.State().CallCount)
	}
	if got := terminalText(t, secondStream); got != "second" {
		t.Fatalf("second reserved stream = %q", got)
	}
	if got := terminalText(t, firstStream); got != "first" {
		t.Fatalf("first reserved stream = %q", got)
	}

	provider.SetResponses([]ResponseStep{AssistantMessage("replacement")})
	provider.AppendResponses([]ResponseStep{AssistantMessage("appended")})
	if provider.PendingResponseCount() != 2 {
		t.Fatalf("pending = %d", provider.PendingResponseCount())
	}
	if got := collectMessage(t, provider, provider.GetModel(), baseContext(), options).Content[0].(*ai.TextContent).Text; got != "replacement" {
		t.Fatalf("replacement = %q", got)
	}
	if got := collectMessage(t, provider, provider.GetModel(), baseContext(), options).Content[0].(*ai.TextContent).Text; got != "appended" {
		t.Fatalf("appended = %q", got)
	}
	exhausted := collectMessage(t, provider, provider.GetModel(), baseContext(), options)
	if exhausted.StopReason != ai.StopReasonError || exhausted.ErrorMessage == nil || *exhausted.ErrorMessage != "No more faux responses queued" {
		t.Fatalf("exhausted message = %#v", exhausted)
	}
	if hookCalls.Load() != 5 || provider.State().CallCount != 5 {
		t.Fatalf("hook calls = %d, provider calls = %d", hookCalls.Load(), provider.State().CallCount)
	}
}

func TestHookAndFactoryErrorsAreTerminalEvents(t *testing.T) {
	provider := New()
	provider.SetResponses([]ResponseStep{AssistantMessage("unused")})
	stream, err := provider.StreamSimple(context.Background(), provider.GetModel(), baseContext(), &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{OnResponse: func(context.Context, ai.ProviderResponse, *ai.Model) error {
			return errors.New("hook boom")
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, stream)
	if len(events) != 1 {
		t.Fatalf("hook events = %d", len(events))
	}
	assertErrorEvent(t, events[0], ai.StopReasonError, "hook boom")
	if provider.PendingResponseCount() != 0 {
		t.Fatal("hook failure must still consume the reserved response")
	}

	provider.SetResponses([]ResponseStep{Factory(func(context.Context, ai.Context, *ai.StreamOptions, State, *ai.Model) (*ai.AssistantMessage, error) {
		return nil, errors.New("factory boom")
	})})
	stream, err = provider.StreamSimple(context.Background(), provider.GetModel(), baseContext(), nil)
	if err != nil {
		t.Fatal(err)
	}
	events = collectEvents(t, stream)
	if len(events) != 1 {
		t.Fatalf("factory events = %d", len(events))
	}
	assertErrorEvent(t, events[0], ai.StopReasonError, "factory boom")
}

func TestUsageEstimateMatchesSerializedContextAndUTF16(t *testing.T) {
	provider := New()
	call := ToolCall("prior_tool", map[string]any{})
	if err := ai.SetToolCallArgumentsJSON(call, []byte(`{"z":"😀","a":1}`)); err != nil {
		t.Fatal(err)
	}
	prior := AssistantMessage(ai.AssistantContent{Text("prior"), call})
	tools := []ai.Tool{{
		Name:        "echo",
		Description: "Echo back text",
		Parameters:  jsonschema.Schema(`{"type":"object","properties":{"text":{"type":"string"}}}`),
	}}
	system := "sys😀"
	requestContext := ai.Context{
		SystemPrompt: &system,
		Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserContent(
				&ai.TextContent{Text: "hello😀"},
				&ai.ImageContent{MimeType: "image/png", Data: "😀"},
			), Timestamp: 1},
			prior,
			&ai.ToolResultMessage{
				ToolCallID: "tool-1",
				ToolName:   "echo",
				Content:    ai.ToolResultContent{&ai.TextContent{Text: "tool out😀"}},
				Timestamp:  2,
			},
		},
		Tools: &tools,
	}
	provider.SetResponses([]ResponseStep{AssistantMessage("done😀")})
	response := collectMessage(t, provider, provider.GetModel(), requestContext, nil)
	promptText, err := serializeContext(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	wantInput := estimateTokens(promptText)
	wantOutput := estimateTokens("done😀")
	if response.Usage.Input != wantInput || response.Usage.Output != wantOutput ||
		response.Usage.TotalTokens != wantInput+wantOutput || response.Usage.CacheRead != 0 || response.Usage.CacheWrite != 0 {
		t.Fatalf("usage = %#v, want input=%d output=%d; prompt=%q", response.Usage, wantInput, wantOutput, promptText)
	}
	if !strings.Contains(promptText, "[image:image/png:2]") || !strings.Contains(promptText, `prior_tool:{"z":"😀","a":1}`) {
		t.Fatalf("serialized context = %q", promptText)
	}
	if estimateTokens("abc😀") != 2 || estimateTokens("😀😀") != 1 {
		t.Fatal("token estimate did not use UTF-16 code units")
	}
}

func TestPromptCachingIsSessionScopedAndHonorsNone(t *testing.T) {
	provider := New()
	provider.SetResponses([]ResponseStep{
		AssistantMessage("first"), AssistantMessage("second"), AssistantMessage("third"), AssistantMessage("fourth"),
	})
	sessionOne, sessionTwo := "session-1", "session-2"
	short, none := ai.CacheRetentionShort, ai.CacheRetentionNone
	requestContext := ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello😀"), Timestamp: 1}}}

	first := collectMessage(t, provider, provider.GetModel(), requestContext, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		SessionID: &sessionOne, CacheRetention: &short,
	}})
	if first.Usage.CacheRead != 0 || first.Usage.CacheWrite == 0 {
		t.Fatalf("first cache usage = %#v", first.Usage)
	}
	requestContext.Messages = append(requestContext.Messages, first, &ai.UserMessage{Content: ai.NewUserText("follow 😀"), Timestamp: 2})
	second := collectMessage(t, provider, provider.GetModel(), requestContext, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		SessionID: &sessionOne, CacheRetention: &short,
	}})
	if second.Usage.CacheRead == 0 || second.Usage.Input+second.Usage.CacheRead <= second.Usage.Input {
		t.Fatalf("second cache usage = %#v", second.Usage)
	}
	third := collectMessage(t, provider, provider.GetModel(), requestContext, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		SessionID: &sessionTwo, CacheRetention: &short,
	}})
	if third.Usage.CacheRead != 0 || third.Usage.CacheWrite == 0 {
		t.Fatalf("other session cache usage = %#v", third.Usage)
	}
	fourth := collectMessage(t, provider, provider.GetModel(), requestContext, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		SessionID: &sessionOne, CacheRetention: &none,
	}})
	if fourth.Usage.CacheRead != 0 || fourth.Usage.CacheWrite != 0 {
		t.Fatalf("none cache usage = %#v", fourth.Usage)
	}
}

func TestExactEventOrderAndToolArgumentDeltas(t *testing.T) {
	provider := New(Options{TokenSize: FixedTokenSize(1)})
	toolCall := ToolCall("echo", nil, ToolCallOptions{ID: "tool-1"})
	provider.SetResponses([]ResponseStep{AssistantMessage(
		ai.AssistantContent{Thinking("go"), Text("ok"), toolCall},
		AssistantMessageOptions{StopReason: ai.StopReasonToolUse},
	)})
	stream, err := provider.StreamSimple(context.Background(), provider.GetModel(), baseContext(), nil)
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, stream)
	gotTypes := make([]string, 0, len(events))
	var arguments strings.Builder
	for _, event := range events {
		gotTypes = append(gotTypes, eventType(event))
		if delta, ok := event.(ai.ToolCallDeltaEvent); ok {
			arguments.WriteString(delta.Delta)
		}
	}
	wantTypes := []string{
		"start", "thinking_start", "thinking_delta", "thinking_end", "text_start", "text_delta", "text_end",
		"toolcall_start", "toolcall_delta", "toolcall_end", "done",
	}
	if fmt.Sprint(gotTypes) != fmt.Sprint(wantTypes) {
		t.Fatalf("event types = %v, want %v", gotTypes, wantTypes)
	}
	if arguments.String() != `{}` {
		t.Fatalf("tool arguments = %q", arguments.String())
	}
	start := events[0].(ai.StartEvent)
	if len(start.Partial.Content) != 0 {
		t.Fatalf("start partial mutated after emission: %#v", start.Partial.Content)
	}
}

func TestZeroDelayQueuesEventsWithSharedFinalPartials(t *testing.T) {
	provider := New(Options{TokenSize: FixedTokenSize(1)})
	call := ToolCall("echo", nil, ToolCallOptions{ID: "tool-1"})
	if err := ai.SetToolCallArgumentsJSON(call, []byte(`{"value":"abcdefgh"}`)); err != nil {
		t.Fatal(err)
	}
	provider.SetResponses([]ResponseStep{AssistantMessage(
		ai.AssistantContent{Thinking("thinking"), Text("alphabet"), call},
		AssistantMessageOptions{StopReason: ai.StopReasonToolUse},
	)})
	stream, err := provider.StreamSimple(context.Background(), provider.GetModel(), baseContext(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var sawThinkingStart, sawTextStart, sawTextDelta, sawToolStart, sawToolDelta bool
	for event, streamErr := range stream {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		switch typed := event.(type) {
		case ai.StartEvent:
			if len(typed.Partial.Content) != 0 {
				t.Fatalf("global start partial = %#v", typed.Partial.Content)
			}
		case ai.ThinkingStartEvent:
			sawThinkingStart = true
			if got := typed.Partial.Content[0].(*ai.ThinkingContent).Thinking; got != "thinking" {
				t.Fatalf("zero-delay thinking_start partial = %q", got)
			}
		case ai.TextStartEvent:
			sawTextStart = true
			if got := typed.Partial.Content[1].(*ai.TextContent).Text; got != "alphabet" {
				t.Fatalf("zero-delay text_start partial = %q", got)
			}
		case ai.TextDeltaEvent:
			sawTextDelta = true
			if got := typed.Partial.Content[1].(*ai.TextContent).Text; got != "alphabet" {
				t.Fatalf("zero-delay text_delta partial = %q", got)
			}
		case ai.ToolCallStartEvent:
			sawToolStart = true
			if got := typed.Partial.Content[2].(*ai.ToolCall).Arguments["value"]; got != "abcdefgh" {
				t.Fatalf("zero-delay toolcall_start arguments = %#v", got)
			}
		case ai.ToolCallDeltaEvent:
			sawToolDelta = true
			if got := typed.Partial.Content[2].(*ai.ToolCall).Arguments["value"]; got != "abcdefgh" {
				t.Fatalf("zero-delay toolcall_delta arguments = %#v", got)
			}
		}
	}
	if !sawThinkingStart || !sawTextStart || !sawTextDelta || !sawToolStart || !sawToolDelta {
		t.Fatalf("missing aliased events: thinking=%v textStart=%v textDelta=%v toolStart=%v toolDelta=%v",
			sawThinkingStart, sawTextStart, sawTextDelta, sawToolStart, sawToolDelta)
	}
}

func TestPacedStreamExposesIncrementalPartialsAndRemainsAbortable(t *testing.T) {
	provider := New(Options{TokensPerSecond: 1000, TokenSize: FixedTokenSize(1)})
	provider.SetResponses([]ResponseStep{AssistantMessage("alphabet")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := provider.StreamSimple(ctx, provider.GetModel(), baseContext(), nil)
	if err != nil {
		t.Fatal(err)
	}

	textAtStart := "missing"
	textAtFirstDelta := "missing"
	var deltaCount int
	var sawError, sawEnd, sawDone bool
	for event, streamErr := range stream {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		switch typed := event.(type) {
		case ai.TextStartEvent:
			textAtStart = typed.Partial.Content[0].(*ai.TextContent).Text
		case ai.TextDeltaEvent:
			deltaCount++
			if deltaCount == 1 {
				textAtFirstDelta = typed.Partial.Content[0].(*ai.TextContent).Text
				cancel()
			}
		case ai.TextEndEvent:
			sawEnd = true
		case ai.DoneEvent:
			sawDone = true
		case ai.ErrorEvent:
			sawError = typed.Reason == ai.StopReasonAborted
		}
	}
	if textAtStart != "" || textAtFirstDelta != "alph" || deltaCount != 1 || !sawError || sawEnd || sawDone {
		t.Fatalf("paced stream: start=%q firstDelta=%q deltas=%d error=%v end=%v done=%v",
			textAtStart, textAtFirstDelta, deltaCount, sawError, sawEnd, sawDone)
	}
}

func TestExplicitErrorAndAbortedMessagesStreamContentThenError(t *testing.T) {
	for _, test := range []struct {
		name   string
		reason ai.StopReason
		text   string
	}{
		{name: "error", reason: ai.StopReasonError, text: "upstream failed"},
		{name: "aborted", reason: ai.StopReasonAborted, text: "Request was aborted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := New(Options{TokenSize: FixedTokenSize(2)})
			message := AssistantMessage("partial", AssistantMessageOptions{StopReason: test.reason, ErrorMessage: &test.text})
			provider.SetResponses([]ResponseStep{message})
			stream, err := provider.StreamSimple(context.Background(), provider.GetModel(), baseContext(), nil)
			if err != nil {
				t.Fatal(err)
			}
			events := collectEvents(t, stream)
			gotTypes := make([]string, len(events))
			for index, event := range events {
				gotTypes[index] = eventType(event)
			}
			if fmt.Sprint(gotTypes) != fmt.Sprint([]string{"start", "text_start", "text_delta", "text_end", "error"}) {
				t.Fatalf("events = %v", gotTypes)
			}
			assertErrorEvent(t, events[len(events)-1], test.reason, test.text)
		})
	}
}

func TestAbortBeforeAndDuringEachContentKind(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		provider := New(Options{TokensPerSecond: 50, TokenSize: FixedTokenSize(3)})
		provider.SetResponses([]ResponseStep{AssistantMessage("abcdefghijklmnopqrstuvwxyz")})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stream, err := provider.StreamSimple(ctx, provider.GetModel(), baseContext(), nil)
		if err != nil {
			t.Fatal(err)
		}
		events := collectEvents(t, stream)
		if len(events) != 1 {
			t.Fatalf("events = %v", events)
		}
		assertErrorEvent(t, events[0], ai.StopReasonAborted, "Request was aborted")
	})

	tests := []struct {
		name      string
		message   *ai.AssistantMessage
		deltaType string
		endType   string
	}{
		{name: "text", message: AssistantMessage("abcdefghijklmnopqrstuvwxyz"), deltaType: "text_delta", endType: "text_end"},
		{name: "thinking", message: AssistantMessage(Thinking("abcdefghijklmnopqrstuvwxyz")), deltaType: "thinking_delta", endType: "thinking_end"},
		{name: "toolcall", message: AssistantMessage(ToolCall("echo", map[string]any{"text": "abcdefghijklmnopqrstuvwxyz", "count": 123456789}, ToolCallOptions{ID: "tool-1"}), AssistantMessageOptions{StopReason: ai.StopReasonToolUse}), deltaType: "toolcall_delta", endType: "toolcall_end"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := New(Options{TokensPerSecond: 100, TokenSize: FixedTokenSize(3)})
			provider.SetResponses([]ResponseStep{test.message})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := provider.StreamSimple(ctx, provider.GetModel(), baseContext(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var events []ai.AssistantMessageEvent
			deltaCount := 0
			for event, streamErr := range stream {
				if streamErr != nil {
					t.Fatal(streamErr)
				}
				events = append(events, event)
				typeName := eventType(event)
				if typeName == test.deltaType {
					deltaCount++
					if deltaCount == 1 {
						cancel()
					}
				}
			}
			hasError, hasEnd := false, false
			for _, event := range events {
				typeName := eventType(event)
				hasError = hasError || typeName == "error"
				hasEnd = hasEnd || typeName == test.endType
			}
			if deltaCount != 1 || !hasError || hasEnd {
				t.Fatalf("events = %v", eventTypes(events))
			}
		})
	}
}

func TestUTF16ChunkingPreservesLoneSurrogateDeltas(t *testing.T) {
	provider := New(Options{TokenSize: FixedTokenSize(1)})
	provider.SetResponses([]ResponseStep{AssistantMessage("abc😀z")})
	stream, err := provider.StreamSimple(context.Background(), provider.GetModel(), baseContext(), nil)
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, stream)
	var deltas []string
	var lastPartial string
	for _, event := range events {
		if delta, ok := event.(ai.TextDeltaEvent); ok {
			deltas = append(deltas, delta.Delta)
			lastPartial = delta.Partial.Content[0].(*ai.TextContent).Text
		}
	}
	if len(deltas) != 2 {
		t.Fatalf("deltas = %q", deltas)
	}
	high := string([]byte{0xed, 0xa0, 0xbd})
	low := string([]byte{0xed, 0xb8, 0x80})
	if deltas[0] != "abc"+high || deltas[1] != low+"z" {
		t.Fatalf("UTF-16 deltas = %q", deltas)
	}
	if lastPartial != "abc😀z" {
		t.Fatalf("recombined partial = %q", lastPartial)
	}
	encoded, err := ai.MarshalAssistantMessageEvent(events[2])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"delta":"abc\ud83d"`)) {
		t.Fatalf("lone surrogate wire delta = %s", encoded)
	}
}

func TestQueueIsRaceSafeAndConsumesEachResponseOnce(t *testing.T) {
	const count = 64
	provider := New(Options{TokenSize: FixedTokenSize(64)})
	responses := make([]ResponseStep, count)
	for index := range responses {
		responses[index] = AssistantMessage(fmt.Sprintf("response-%02d", index))
	}
	provider.SetResponses(responses)

	results := make(chan string, count)
	var workers sync.WaitGroup
	workers.Add(count)
	for range count {
		go func() {
			defer workers.Done()
			message := collectMessage(t, provider, provider.GetModel(), baseContext(), nil)
			results <- message.Content[0].(*ai.TextContent).Text
		}()
	}
	workers.Wait()
	close(results)
	got := make([]string, 0, count)
	for result := range results {
		got = append(got, result)
	}
	sort.Strings(got)
	for index, value := range got {
		if want := fmt.Sprintf("response-%02d", index); value != want {
			t.Fatalf("result %d = %q, want %q", index, value, want)
		}
	}
	if provider.State().CallCount != count || provider.PendingResponseCount() != 0 {
		t.Fatalf("state = %#v pending=%d", provider.State(), provider.PendingResponseCount())
	}
}

func TestStreamMethodAdaptsAIRequestAndRejectsNilModel(t *testing.T) {
	provider := New()
	provider.SetResponses([]ResponseStep{AssistantMessage("hello")})
	stream, err := provider.Stream(context.Background(), ai.Request{Model: provider.GetModel(), Context: baseContext()})
	if err != nil {
		t.Fatal(err)
	}
	if got := terminalText(t, stream); got != "hello" {
		t.Fatalf("text = %q", got)
	}
	if _, err := provider.Stream(context.Background(), ai.Request{}); err == nil {
		t.Fatal("nil request model accepted")
	}
	if _, err := provider.StreamSimple(context.Background(), nil, baseContext(), nil); err == nil {
		t.Fatal("nil simple model accepted")
	}
}

func baseContext() ai.Context {
	return ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hi"), Timestamp: 1}}}
}

func collectMessage(t *testing.T, provider *Provider, model *ai.Model, requestContext ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessage {
	t.Helper()
	stream, err := provider.StreamSimple(context.Background(), model, requestContext, options)
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func collectEvents(t *testing.T, stream ai.AssistantMessageEventStream) []ai.AssistantMessageEvent {
	t.Helper()
	var events []ai.AssistantMessageEvent
	for event, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func terminalText(t *testing.T, stream ai.AssistantMessageEventStream) string {
	t.Helper()
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	return message.Content[0].(*ai.TextContent).Text
}

func assertErrorEvent(t *testing.T, event ai.AssistantMessageEvent, reason ai.StopReason, message string) {
	t.Helper()
	errorEvent, ok := event.(ai.ErrorEvent)
	if !ok {
		t.Fatalf("event = %T, want ai.ErrorEvent", event)
	}
	if errorEvent.Reason != reason || errorEvent.Error.StopReason != reason ||
		errorEvent.Error.ErrorMessage == nil || *errorEvent.Error.ErrorMessage != message {
		t.Fatalf("error event = %#v", errorEvent)
	}
}

func eventTypes(events []ai.AssistantMessageEvent) []string {
	types := make([]string, len(events))
	for index, event := range events {
		types[index] = eventType(event)
	}
	return types
}

func eventType(event ai.AssistantMessageEvent) string {
	switch event.(type) {
	case ai.StartEvent:
		return "start"
	case ai.ThinkingStartEvent:
		return "thinking_start"
	case ai.ThinkingDeltaEvent:
		return "thinking_delta"
	case ai.ThinkingEndEvent:
		return "thinking_end"
	case ai.TextStartEvent:
		return "text_start"
	case ai.TextDeltaEvent:
		return "text_delta"
	case ai.TextEndEvent:
		return "text_end"
	case ai.ToolCallStartEvent:
		return "toolcall_start"
	case ai.ToolCallDeltaEvent:
		return "toolcall_delta"
	case ai.ToolCallEndEvent:
		return "toolcall_end"
	case ai.DoneEvent:
		return "done"
	case ai.ErrorEvent:
		return "error"
	default:
		return fmt.Sprintf("%T", event)
	}
}

func TestPacingDelaysChunksByEstimatedTokens(t *testing.T) {
	provider := New(Options{TokensPerSecond: 100, TokenSize: FixedTokenSize(3)})
	provider.SetResponses([]ResponseStep{AssistantMessage("abcdefghijkl")})
	started := time.Now()
	collectMessage(t, provider, provider.GetModel(), baseContext(), nil)
	if elapsed := time.Since(started); elapsed < 25*time.Millisecond {
		t.Fatalf("paced stream completed in %s", elapsed)
	}
}
