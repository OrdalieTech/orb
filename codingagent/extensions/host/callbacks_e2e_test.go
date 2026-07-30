package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/codingagent/config"
	"github.com/OrdalieTech/orb/codingagent/extensions"
)

// Regression: extension callbacks used to inherit the manager RequestTimeout
// (default 30s) whenever the caller ctx had no deadline; upstream awaits them
// with no timeout at all.
func TestRealHostToolOutlivesRequestTimeout(t *testing.T) {
	runtime := requireRuntime(t)
	cwd := t.TempDir()
	manager := NewManager(Options{
		AgentDir: t.TempDir(), CWD: cwd, Version: "test", Runtime: &runtime,
		RequestTimeout: 2 * time.Second, ShutdownTimeout: time.Second,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	registry := extensions.NewRegistry(cwd)
	result := manager.RegisterInto(context.Background(), registry, []string{fixturePath(t, "signals.mjs")})
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd})
	slow := runner.ToolDefinition("host_slow")
	if slow == nil {
		t.Fatal("host_slow was not registered")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	final, err := slow.Execute(ctx, "call-slow", map[string]any{"ms": 3500}, nil, runner.CreateContext())
	if err != nil {
		t.Fatalf("tool exceeding RequestTimeout failed: %v", err)
	}
	if got := toolText(final); got != "slept" {
		t.Fatalf("tool result = %q", got)
	}
}

// Regression: the positional AbortSignal passed to tool.execute was a dead
// controller that never fired; Go-side ctx cancellation now aborts it over the
// protocol, matching upstream's live streaming signal.
func TestRealHostPositionalToolSignalFiresOnContextCancel(t *testing.T) {
	_, _, runner, result, cwd := startFixtureManager(t, fixturePath(t, "signals.mjs"))
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	tool := runner.ToolDefinition("host_wait_abort")
	if tool == nil {
		t.Fatal("host_wait_abort was not registered")
	}
	marker := filepath.Join(cwd, "abort-marker")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := tool.Execute(ctx, "call-abort", map[string]any{"marker": marker}, nil, runner.CreateContext())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled execute error = %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if content, readErr := os.ReadFile(marker); readErr == nil {
			if string(content) != "aborted" {
				t.Fatalf("marker content = %q", content)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("positional AbortSignal never fired in the host")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Regression: readline replays every line of one stdin chunk synchronously,
// so a cancel_request coalesced into the same chunk as the request it cancels
// ran before retainRequestAbort and was dropped forever; the host now remembers
// it and aborts the fresh controller immediately.
func TestRealHostCancelRequestCoalescedWithRequestStillAborts(t *testing.T) {
	manager, _, _, result, cwd := startFixtureManager(t, fixturePath(t, "signals.mjs"))
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	manager.mu.Lock()
	generation := manager.current
	manager.mu.Unlock()
	if generation == nil {
		t.Fatal("no live generation")
	}
	marker := filepath.Join(cwd, "abort-marker")
	requestValue, err := requestFrame("chunk-cancel-1", "execute_tool", struct {
		ExtensionID string      `json:"extensionId"`
		ToolName    string      `json:"toolName"`
		ToolCallID  string      `json:"toolCallId"`
		Params      any         `json:"params"`
		Context     wireContext `json:"context"`
	}{"ext-1", "host_wait_abort", "call-chunk", map[string]any{"marker": marker}, wireContext{CWD: cwd, Mode: extensions.ModePrint}})
	if err != nil {
		t.Fatal(err)
	}
	cancelValue, err := eventFrame("cancel_request", struct {
		RequestID string `json:"requestId"`
	}{"chunk-cancel-1"})
	if err != nil {
		t.Fatal(err)
	}
	encodedRequest, err := ai.Marshal(requestValue)
	if err != nil {
		t.Fatal(err)
	}
	encodedCancel, err := ai.Marshal(cancelValue)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 0, len(encodedRequest)+len(encodedCancel)+2)
	chunk = append(chunk, encodedRequest...)
	chunk = append(chunk, '\n')
	chunk = append(chunk, encodedCancel...)
	chunk = append(chunk, '\n')
	// A single pipe write delivers both frames in one readline chunk.
	if _, err := generation.stdin.Write(chunk); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if content, readErr := os.ReadFile(marker); readErr == nil {
			if string(content) != "aborted" {
				t.Fatalf("marker content = %q", content)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("coalesced cancel_request never aborted the tool signal")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Regression: the host stayed alive after orb died without sending shutdown
// (hard crash) whenever an extension held a live handle; stdin close must end
// the process.
func TestRealHostExitsWhenTransportClosesWithLiveHandles(t *testing.T) {
	manager, _, _, result, _ := startFixtureManager(t, fixturePath(t, "keepalive.mjs"))
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	manager.mu.Lock()
	generation := manager.current
	manager.mu.Unlock()
	if generation == nil {
		t.Fatal("no live generation")
	}
	generation.expected.Store(true)
	if err := generation.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-generation.waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("host process outlived transport close despite live extension handles")
	}
}

// Regression: console was frozen to five methods, so console.table/group/time
// and friends threw TypeError under orb while working upstream.
func TestRealHostProvidesFullConsoleSurface(t *testing.T) {
	runtime := requireRuntime(t)
	cwd := t.TempDir()
	var mu sync.Mutex
	var messages []string
	manager := NewManager(Options{
		AgentDir: t.TempDir(), CWD: cwd, Version: "test", Runtime: &runtime,
		ShutdownTimeout: time.Second,
		BackoffBase:     10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
		OnDiagnostic: func(diagnostic extensions.Diagnostic) {
			mu.Lock()
			messages = append(messages, diagnostic.Message)
			mu.Unlock()
		},
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	registry := extensions.NewRegistry(cwd)
	result := manager.RegisterInto(context.Background(), registry, []string{fixturePath(t, "consolefull.mjs")})
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd})
	if runner.ToolDefinition("host_console") == nil {
		t.Fatal("factory did not survive the console calls")
	}
	mu.Lock()
	joined := strings.Join(messages, "\n")
	mu.Unlock()
	for _, want := range []string{"count-label: 1", "Assertion failed: assert-label", "Trace: trace-label", "timer-label: ", "sub-console-label"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("console diagnostics missing %q in:\n%s", want, joined)
		}
	}
}

func startNativeProviderFixture(t *testing.T, fixture, providerID string) (extensions.Provider, string) {
	t.Helper()
	runtime := requireRuntime(t)
	cwd := t.TempDir()
	manager := NewManager(Options{
		AgentDir: t.TempDir(), CWD: cwd, Version: "test", Runtime: &runtime,
		ShutdownTimeout: time.Second,
		BackoffBase:     10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	modelRegistry, err := config.NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(cwd)
	registry.BindModelRegistry(modelRegistry, func(value extensions.ExtensionError) {
		t.Errorf("registration error: %#v", value)
	})
	result := manager.RegisterInto(context.Background(), registry, []string{fixturePath(t, fixture)})
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	provider, ok := modelRegistry.RegisteredNativeProvider(providerID)
	if !ok {
		t.Fatalf("%s was not registered", providerID)
	}
	return provider, cwd
}

// Regression: extension provider streams were fully buffered into the single
// provider_invoke response. The fixture's second event is gated on a file this
// test writes only after observing the first event, so completion proves
// incremental delivery.
func TestRealHostStreamsProviderEventsIncrementally(t *testing.T) {
	provider, cwd := startNativeProviderFixture(t, "streamprovider.mjs", "stream-provider")
	models, err := provider.GetModels()
	if err != nil || len(models) != 1 {
		t.Fatalf("provider models = %#v, %v", models, err)
	}
	stream, err := provider.StreamSimple(context.Background(), &models[0], ai.Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	for event, streamErr := range stream {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		var text string
		switch typed := event.(type) {
		case ai.TextDeltaEvent:
			text = typed.Delta
		case *ai.TextDeltaEvent:
			text = typed.Delta
		default:
			t.Fatalf("unexpected event %#v", event)
		}
		deltas = append(deltas, text)
		if len(deltas) == 1 {
			if err := os.WriteFile(filepath.Join(cwd, "stream-gate"), []byte("go"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(deltas) != 2 || deltas[0] != "first" || deltas[1] != "second" {
		t.Fatalf("stream deltas = %#v", deltas)
	}
}

// Regression: abandoning a provider event stream mid-iteration left the invoke
// goroutine and the host-side extension generator running forever, buffering
// every remaining event unboundedly. The stream closure now cancels the invoke
// on exit and the host terminates the extension generator; the fixture writes
// stream-stopped from its finally block only when that termination happens.
func TestRealHostAbandonedProviderStreamStopsHostGenerator(t *testing.T) {
	provider, cwd := startNativeProviderFixture(t, "infinitestream.mjs", "infinite-provider")
	models, err := provider.GetModels()
	if err != nil || len(models) != 1 {
		t.Fatalf("provider models = %#v, %v", models, err)
	}
	stream, err := provider.StreamSimple(context.Background(), &models[0], ai.Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, streamErr := range stream {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		break
	}
	marker := filepath.Join(cwd, "stream-stopped")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, readErr := os.Stat(marker); readErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host-side extension generator kept running after the stream was abandoned")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Regression: an undecodable incrementally-streamed event was silently dropped
// while the buffered result.Events path failed the stream; both now surface
// the decode error, and events past the malformed one are discarded.
func TestRealHostProviderStreamFailsOnUndecodableEvent(t *testing.T) {
	provider, _ := startNativeProviderFixture(t, "badstream.mjs", "bad-stream-provider")
	models, err := provider.GetModels()
	if err != nil || len(models) != 1 {
		t.Fatalf("provider models = %#v, %v", models, err)
	}
	stream, err := provider.StreamSimple(context.Background(), &models[0], ai.Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var streamFailure error
	var deltas []string
	for event, streamErr := range stream {
		if streamErr != nil {
			streamFailure = streamErr
			continue
		}
		if delta, ok := event.(*ai.TextDeltaEvent); ok {
			deltas = append(deltas, delta.Delta)
		}
	}
	if streamFailure == nil || !strings.Contains(streamFailure.Error(), "unknown assistant message event type") {
		t.Fatalf("stream error = %v", streamFailure)
	}
	if len(deltas) != 0 {
		t.Fatalf("events after the undecodable one were delivered: %#v", deltas)
	}
}
