package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
)

func TestReleasedModelControls(t *testing.T) {
	data, err := os.ReadFile("../conformance/fixtures/F3-session/model-controls.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]map[string]string
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	runtime, _ := newTestRuntime(t, provider, map[string]any{"defaultProvider": "configured", "defaultModel": "default", "defaultThinkingLevel": "high"})
	snapshot := func() map[string]string {
		return map[string]string{"provider": runtime.settings.GetDefaultProvider(), "model": runtime.settings.GetDefaultModel(), "thinking": string(runtime.settings.GetDefaultThinkingLevel())}
	}
	model := *provider.GetModel()
	if err := runtime.SetModel(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetThinkingLevel(ai.ModelThinkingLow); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(); !reflect.DeepEqual(got, expected["transient"]) {
		t.Fatalf("transient: %#v", got)
	}
	if err := runtime.SetModelWithOptions(context.Background(), model, ModelMutationOptions{Persist: true}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetThinkingLevelWithOptions(ai.ModelThinkingLow, ModelMutationOptions{Persist: true}); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(); !reflect.DeepEqual(got, expected["persisted"]) {
		t.Fatalf("persisted: %#v", got)
	}
}

func TestReleasedCustomDuringToolTrace(t *testing.T) {
	data, err := os.ReadFile("../conformance/fixtures/F3-session/custom-during-tool.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	project := func(raw []byte) string {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Role       string `json:"role"`
				CustomType string `json:"customType"`
				Content    any    `json:"content"`
			} `json:"message"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "message_update" {
			event.Message.Role = ""
		}
		value := event.Type + ":" + event.Message.Role
		if event.Message.Role == "custom" {
			value += fmt.Sprint(":", event.Message.CustomType, ":", event.Message.Content)
		}
		for _, message := range event.Messages {
			value += ":" + message.Role
		}
		return value
	}
	var want []string
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n"))[1:] {
		want = append(want, project(line))
	}
	provider := testFaux(100000)
	runtime, manager := newTestRuntime(t, provider, map[string]any{"compaction": map[string]any{"enabled": false}, "retry": map[string]any{"enabled": false}})
	toolResponse := runtimeAssistant(provider, "", 0)
	toolResponse.Content = ai.AssistantContent{&ai.ToolCall{ID: "fixture-call", Name: "fixture", Arguments: map[string]any{}}}
	toolResponse.StopReason = ai.StopReasonToolUse
	provider.SetResponses([]faux.ResponseStep{toolResponse, faux.ResponseFactory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		encoded, _ := json.Marshal(request.Messages)
		if !bytes.Contains(encoded, []byte("queued")) {
			t.Error("next assistant request omitted queued custom message")
		}
		return runtimeAssistant(provider, "done", 0), nil
	})})
	runtime.agent.SetTools([]engine.AgentTool{engine.AgentToolFunc{AgentToolSpec: engine.AgentToolSpec{Name: "fixture", Description: "fixture", Parameters: ai.JSONSchema(`{"type":"object","properties":{}}`)}, Run: func(ctx context.Context, _ string, _ any, _ engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
		trigger := false
		if err := runtime.sendExtensionMessage(ctx, extensions.CustomMessage{CustomType: "note", Content: "queued", Display: true}, &extensions.SendMessageOptions{TriggerTurn: &trigger}); err != nil {
			return engine.AgentToolResult{}, err
		}
		for _, entry := range manager.GetEntries() {
			if entry.Type == "custom_message" {
				t.Error("custom message persisted before tool completed")
			}
		}
		return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "result"}}}, nil
	}}})
	var got []string
	runtime.Subscribe(func(event any) {
		if _, settled := event.(AgentSettledEvent); settled && !runtime.IsIdle() {
			t.Error("agent_settled observer sees a running session")
		}
		raw, err := MarshalSessionEvent(event)
		if err != nil {
			t.Error(err)
			return
		}
		got = append(got, project(raw))
	})
	if err := runtime.Prompt(context.Background(), "run fixture"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event order\ngot %q\nwant %q", got, want)
	}
	messages := runtime.State().Messages
	if len(messages) != 5 || reflect.TypeOf(messages[2]) != reflect.TypeOf(&ai.ToolResultMessage{}) || reflect.TypeOf(messages[3]) != reflect.TypeOf(&harness.CustomMessage{}) {
		t.Fatalf("state ordering: %#v", messages)
	}
}

func TestReleasedCompactsOversizedToolBeforeNextAssistant(t *testing.T) {
	provider := testFaux(1000)
	runtime, manager := newTestRuntime(t, provider, map[string]any{"compaction": map[string]any{"enabled": true, "reserveTokens": 50, "keepRecentTokens": 1}})
	if _, err := manager.AppendMessage(userMessage("earlier request")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(runtimeAssistant(provider, "earlier answer", 10)); err != nil {
		t.Fatal(err)
	}
	runtime.syncAgentMessages()
	compacted := false
	runtime.complete = func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		compacted = true
		return runtimeAssistant(provider, "concise summary", 10), nil
	}
	toolResponse := runtimeAssistant(provider, "", 10)
	toolResponse.Content = ai.AssistantContent{&ai.ToolCall{ID: "large-call", Name: "large", Arguments: map[string]any{}}}
	toolResponse.StopReason = ai.StopReasonToolUse
	provider.SetResponses([]faux.ResponseStep{toolResponse, faux.ResponseFactory(func(_ context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		if !compacted {
			t.Error("oversized tool result reached the next assistant before compaction")
		}
		return runtimeAssistant(provider, "done", 10), nil
	})})
	runtime.agent.SetTools([]engine.AgentTool{engine.AgentToolFunc{AgentToolSpec: engine.AgentToolSpec{Name: "large", Parameters: ai.JSONSchema(`{"type":"object"}`)}, Run: func(context.Context, string, any, engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
		return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: strings.Repeat("large output ", 1000)}}}, nil
	}}})
	if err := runtime.Prompt(context.Background(), "read large output"); err != nil {
		t.Fatal(err)
	}
	if !compacted {
		t.Fatal("compaction did not run")
	}
}

func TestReleasedWaitForIdleIncludesAutoCompactionAndAbort(t *testing.T) {
	provider := testFaux(1000)
	runtime, manager := newTestRuntime(t, provider, map[string]any{"compaction": map[string]any{"enabled": true, "reserveTokens": 50, "keepRecentTokens": 1}})
	if _, err := manager.AppendMessage(userMessage("request")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(runtimeAssistant(provider, "answer", 20)); err != nil {
		t.Fatal(err)
	}
	runtime.syncAgentMessages()
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	runtime.complete = func(ctx context.Context, _ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := runtime.runAutoCompaction(context.Background(), "threshold", false); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never started")
	}
	if runtime.IsIdle() {
		t.Error("session idle during compaction")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runtime.WaitForIdle(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled idle wait = %v", err)
	}
	runtime.Abort()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Abort did not cancel compaction")
	}
	if runtime.IsIdle() {
		t.Error("session became idle before compaction returned")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := runtime.WaitForIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !runtime.IsIdle() {
		t.Fatal("session not idle after compaction settled")
	}
}
