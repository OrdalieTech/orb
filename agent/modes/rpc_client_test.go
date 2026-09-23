package modes

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/rpc"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestRPCClientLifecycleAndStrictRouting(t *testing.T) {
	client := NewRPCClient(rpcClientHelper("lifecycle"))
	if _, err := client.GetState(context.Background()); err == nil || err.Error() != "Client not started" {
		t.Fatalf("GetState before Start error = %v", err)
	}
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })
	if err := client.Start(context.Background()); err == nil || err.Error() != "Client already started" {
		t.Fatalf("second Start error = %v", err)
	}

	events := make(chan RPCEvent, 2)
	unsubscribe := client.OnEvent(func(event RPCEvent) { events <- event })
	state, err := client.GetState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionID != "session" || state.ThinkingLevel != ai.ModelThinkingOff || state.MessageCount != 2 {
		t.Fatalf("state = %#v", state)
	}
	select {
	case event := <-events:
		if event.Type != "queue_update" || !strings.Contains(string(event.JSON), "a b c") {
			t.Fatalf("event = %s", event.JSON)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not routed")
	}
	select {
	case event := <-events:
		t.Fatalf("response or invalid JSON routed as event: %s", event.JSON)
	default:
	}
	unsubscribe()
	if err := client.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := client.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRPCClientRejectsPendingRequestOnExitAndCollectsStderr(t *testing.T) {
	client := NewRPCClient(rpcClientHelper("exit"))
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })
	_, err := client.GetCommands(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Agent process exited (code=43 signal=null). Stderr: child diagnostic") {
		t.Fatalf("GetCommands error = %v", err)
	}
	if got := client.GetStderr(); got != "child diagnostic" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestRPCClientRequestTimeoutIncludesStderr(t *testing.T) {
	client := NewRPCClient(rpcClientHelper("stderr"))
	client.requestTimeout = 20 * time.Millisecond
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })
	_, err := client.GetState(context.Background())
	if err == nil || err.Error() != "Timeout waiting for response to get_state. Stderr: waiting" {
		t.Fatalf("GetState error = %v", err)
	}
}

func TestRPCClientStopDoesNotWaitForDescendantStdout(t *testing.T) {
	client := NewRPCClient(rpcClientHelper("descendant"))
	client.stopTimeout = 20 * time.Millisecond
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := client.Stop(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("Stop waited for descendant stdout: %s", elapsed)
	}
}

func TestRPCClientTypedCommands(t *testing.T) {
	options := rpcClientHelper("typed")
	options.Provider, options.Model, options.Args = "openai", "gpt-test", []string{"--no-session"}
	client := NewRPCClient(options)
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })
	if err := client.Prompt(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	model, err := client.SetModel(context.Background(), "openai", "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if model.Provider != "openai" || model.ID != "gpt-test" {
		t.Fatalf("model = %#v", model)
	}
	levels, err := client.GetAvailableThinkingLevels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 2 || levels[1] != engine.ThinkingHigh {
		t.Fatalf("levels = %#v", levels)
	}
	cloned, err := client.Clone(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cloned.Cancelled {
		t.Fatal("clone was cancelled")
	}
}

func TestRPCClientListenerCanCallClientInOrder(t *testing.T) {
	client := NewRPCClient(rpcClientHelper("reentrant"))
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	listenerDone := make(chan error, 1)
	var listenerErr error
	order := make([]string, 0, 2)
	var orderMu sync.Mutex
	client.OnEvent(func(event RPCEvent) {
		if event.Type != "queue_update" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		state, err := client.GetState(ctx)
		if err == nil && state.SessionID != "reentrant" {
			err = errors.New("wrong reentrant state")
		}
		orderMu.Lock()
		order = append(order, "first")
		orderMu.Unlock()
		listenerErr = err
	})
	client.OnEvent(func(event RPCEvent) {
		if event.Type == "queue_update" {
			orderMu.Lock()
			order = append(order, "second")
			orderMu.Unlock()
			listenerDone <- listenerErr
		}
	})
	promptCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Prompt(promptCtx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if err := <-listenerDone; err != nil {
		t.Fatalf("reentrant GetState: %v", err)
	}
	orderMu.Lock()
	defer orderMu.Unlock()
	if strings.Join(order, ",") != "first,second" {
		t.Fatalf("listener order = %v", order)
	}
}

func TestRPCClientListenerPanicOnlyStopsCurrentEvent(t *testing.T) {
	client := NewRPCClient(rpcClientHelper("panic"))
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	done := make(chan struct{})
	order := make([]string, 0, 3)
	var mu sync.Mutex
	client.OnEvent(func(event RPCEvent) {
		if strings.Contains(string(event.JSON), `"one"`) {
			mu.Lock()
			order = append(order, "first-one")
			mu.Unlock()
			panic("listener failure")
		}
		if strings.Contains(string(event.JSON), `"two"`) {
			mu.Lock()
			order = append(order, "first-two")
			mu.Unlock()
		}
	})
	client.OnEvent(func(event RPCEvent) {
		if event.Type != "queue_update" {
			return
		}
		mu.Lock()
		if strings.Contains(string(event.JSON), `"one"`) {
			order = append(order, "second-one")
		} else {
			order = append(order, "second-two")
			close(done)
		}
		mu.Unlock()
	})
	if err := client.Prompt(context.Background(), "hello", nil); err != nil {
		t.Fatalf("response after listener panic: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("later event was not dispatched")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "first-one,first-two,second-two" {
		t.Fatalf("listener order after panic = %v", order)
	}
}

func TestRPCClientListenerCanStopClient(t *testing.T) {
	client := NewRPCClient(rpcClientHelper("event"))
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	client.OnEvent(func(event RPCEvent) {
		if event.Type == "queue_update" {
			stopped <- client.Stop()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	promptDone := make(chan error, 1)
	go func() { promptDone <- client.Prompt(ctx, "stop", nil) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("listener Stop deadlocked")
	}
	if err := <-promptDone; err == nil {
		t.Fatal("in-flight prompt survived stopped process")
	}
}

func TestMarshalRPCClientCommandMatchesObjectSpreadOrder(t *testing.T) {
	falseValue := false
	empty := ""
	tests := []struct {
		name    string
		command rpcClientRequest
		want    string
	}{
		{"prompt empty images", rpcClientRequest{Command: rpc.Command{Type: "prompt", Message: "", Images: []*ai.ImageContent{}, ID: "req_1"}}, `{"type":"prompt","message":"","images":[],"id":"req_1"}`},
		{"false bool", rpcClientRequest{Command: rpc.Command{Type: "set_auto_retry", Enabled: &falseValue, ID: "req_2"}}, `{"type":"set_auto_retry","enabled":false,"id":"req_2"}`},
		{"model empty strings", rpcClientRequest{Command: rpc.Command{Type: "set_model", Provider: "", ModelID: "", ID: "req_3"}}, `{"type":"set_model","provider":"","modelId":"","id":"req_3"}`},
		{"bash empty command", rpcClientRequest{Command: rpc.Command{Type: "bash", Command: "", ID: "req_4"}}, `{"type":"bash","command":"","id":"req_4"}`},
		{"switch empty path", rpcClientRequest{Command: rpc.Command{Type: "switch_session", SessionPath: "", ID: "req_5"}}, `{"type":"switch_session","sessionPath":"","id":"req_5"}`},
		{"fork empty id", rpcClientRequest{Command: rpc.Command{Type: "fork", EntryID: "", ID: "req_6"}}, `{"type":"fork","entryId":"","id":"req_6"}`},
		{"name empty", rpcClientRequest{Command: rpc.Command{Type: "set_session_name", Name: "", ID: "req_7"}}, `{"type":"set_session_name","name":"","id":"req_7"}`},
		{"new session nil", rpcClientRequest{Command: rpc.Command{Type: "new_session", ID: "req_8"}}, `{"type":"new_session","id":"req_8"}`},
		{"new session empty", rpcClientRequest{Command: rpc.Command{Type: "new_session", ParentSession: empty, ID: "req_9"}, parentSessionSet: true}, `{"type":"new_session","parentSession":"","id":"req_9"}`},
		{"compact nil", rpcClientRequest{Command: rpc.Command{Type: "compact", ID: "req_10"}}, `{"type":"compact","id":"req_10"}`},
		{"compact empty", rpcClientRequest{Command: rpc.Command{Type: "compact", CustomInstructions: empty, ID: "req_11"}, customInstructionsSet: true}, `{"type":"compact","customInstructions":"","id":"req_11"}`},
		{"export nil", rpcClientRequest{Command: rpc.Command{Type: "export_html", ID: "req_12"}}, `{"type":"export_html","id":"req_12"}`},
		{"export empty", rpcClientRequest{Command: rpc.Command{Type: "export_html", OutputPath: empty, ID: "req_13"}, outputPathSet: true}, `{"type":"export_html","outputPath":"","id":"req_13"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := marshalRPCClientCommand(test.command)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("command = %s, want %s", got, test.want)
			}
		})
	}
}

func TestRPCClientWaitForIdleAndContextCancellation(t *testing.T) {
	client := NewRPCClient(rpcClientHelper("idle"))
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := client.WaitForIdle(ctx); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "Stderr:") {
		t.Fatalf("WaitForIdle error = %v", err)
	}
}
