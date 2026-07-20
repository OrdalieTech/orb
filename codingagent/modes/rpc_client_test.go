package modes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

func TestRPCClientLifecycleAndStrictRouting(t *testing.T) {
	client := NewRPCClient(RPCClientOptions{CLIPath: rpcClientScript(t, `
read _
printf 'not-json\n'
printf '{"type":"queue_update","steering":["a b c"],"followUp":[]}\r\n'
printf '{"id":"req_1","type":"response","command":"get_state","success":true,"data":{"thinkingLevel":"off","isStreaming":false,"isCompacting":false,"steeringMode":"all","followUpMode":"all","sessionId":"session","autoCompactionEnabled":true,"messageCount":2,"pendingMessageCount":0}}\n'
while read _; do :; done
`)})
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
	client := NewRPCClient(RPCClientOptions{CLIPath: rpcClientScript(t, `
read _
printf 'child diagnostic' >&2
exit 43
`)})
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
	client := NewRPCClient(RPCClientOptions{CLIPath: rpcClientScript(t, `
read _
printf 'waiting' >&2
while read _; do :; done
`)})
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
	client := NewRPCClient(RPCClientOptions{CLIPath: rpcClientScript(t, `
trap '' TERM
sleep 1 &
while :; do sleep 1; done
`)})
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
	client := NewRPCClient(RPCClientOptions{
		CLIPath: rpcClientScript(t, `
while read line; do
	case "$line" in
		*'"type":"prompt"'*'"message":"hello"'*) printf '{"id":"req_1","type":"response","command":"prompt","success":true}\n' ;;
		*'"type":"set_model"'*'"provider":"openai"'*'"modelId":"gpt-test"'*) printf '{"id":"req_2","type":"response","command":"set_model","success":true,"data":{"id":"gpt-test","name":"Test","api":"openai-responses","provider":"openai","baseUrl":"https://example.test","reasoning":false,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":1000,"maxTokens":100}}\n' ;;
		*'"type":"get_available_thinking_levels"'*) printf '{"id":"req_3","type":"response","command":"get_available_thinking_levels","success":true,"data":{"levels":["off","high"]}}\n' ;;
		*'"type":"clone"'*) printf '{"id":"req_4","type":"response","command":"clone","success":true,"data":{"cancelled":false}}\n' ;;
		*) printf '{"id":"unknown","type":"response","command":"unknown","success":false,"error":"bad command"}\n' ;;
	esac
done
`),
		Provider: "openai",
		Model:    "gpt-test",
		Args:     []string{"--no-session"},
	})
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
	if len(levels) != 2 || levels[1] != agent.ThinkingHigh {
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
	client := NewRPCClient(RPCClientOptions{CLIPath: rpcClientScript(t, `
while read line; do
	case "$line" in
		*'"type":"prompt"'*)
			printf '{"type":"queue_update","steering":["one"],"followUp":[]}\n'
			printf '{"type":"response","command":"prompt","success":true,"id":"req_1"}\n'
			;;
		*'"type":"get_state"'*)
			printf '{"type":"response","command":"get_state","success":true,"data":{"thinkingLevel":"off","isStreaming":false,"isCompacting":false,"steeringMode":"all","followUpMode":"all","sessionId":"reentrant","autoCompactionEnabled":true,"messageCount":0,"pendingMessageCount":0},"id":"req_2"}\n'
			;;
	esac
done
`)})
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	listenerDone := make(chan error, 1)
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
		listenerDone <- err
	})
	client.OnEvent(func(event RPCEvent) {
		if event.Type == "queue_update" {
			orderMu.Lock()
			order = append(order, "second")
			orderMu.Unlock()
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
	client := NewRPCClient(RPCClientOptions{CLIPath: rpcClientScript(t, `
read _
printf '{"type":"queue_update","steering":["one"],"followUp":[]}\n'
printf '{"type":"response","command":"prompt","success":true,"id":"req_1"}\n'
printf '{"type":"queue_update","steering":["two"],"followUp":[]}\n'
while read _; do :; done
`)})
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
	client := NewRPCClient(RPCClientOptions{CLIPath: rpcClientScript(t, `
read _
printf '{"type":"queue_update","steering":[],"followUp":[]}\n'
while read _; do :; done
`)})
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
		command RPCCommand
		want    string
	}{
		{"prompt empty images", RPCCommand{Type: "prompt", Message: "", Images: []*ai.ImageContent{}, ID: "req_1"}, `{"type":"prompt","message":"","images":[],"id":"req_1"}`},
		{"false bool", RPCCommand{Type: "set_auto_retry", Enabled: &falseValue, ID: "req_2"}, `{"type":"set_auto_retry","enabled":false,"id":"req_2"}`},
		{"model empty strings", RPCCommand{Type: "set_model", Provider: "", ModelID: "", ID: "req_3"}, `{"type":"set_model","provider":"","modelId":"","id":"req_3"}`},
		{"bash empty command", RPCCommand{Type: "bash", Command: "", ID: "req_4"}, `{"type":"bash","command":"","id":"req_4"}`},
		{"switch empty path", RPCCommand{Type: "switch_session", SessionPath: "", ID: "req_5"}, `{"type":"switch_session","sessionPath":"","id":"req_5"}`},
		{"fork empty id", RPCCommand{Type: "fork", EntryID: "", ID: "req_6"}, `{"type":"fork","entryId":"","id":"req_6"}`},
		{"name empty", RPCCommand{Type: "set_session_name", Name: "", ID: "req_7"}, `{"type":"set_session_name","name":"","id":"req_7"}`},
		{"new session nil", RPCCommand{Type: "new_session", ID: "req_8"}, `{"type":"new_session","id":"req_8"}`},
		{"new session empty", RPCCommand{Type: "new_session", ParentSession: empty, parentSessionSet: true, ID: "req_9"}, `{"type":"new_session","parentSession":"","id":"req_9"}`},
		{"compact nil", RPCCommand{Type: "compact", ID: "req_10"}, `{"type":"compact","id":"req_10"}`},
		{"compact empty", RPCCommand{Type: "compact", CustomInstructions: empty, customInstructionsSet: true, ID: "req_11"}, `{"type":"compact","customInstructions":"","id":"req_11"}`},
		{"export nil", RPCCommand{Type: "export_html", ID: "req_12"}, `{"type":"export_html","id":"req_12"}`},
		{"export empty", RPCCommand{Type: "export_html", OutputPath: empty, outputPathSet: true, ID: "req_13"}, `{"type":"export_html","outputPath":"","id":"req_13"}`},
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
	client := NewRPCClient(RPCClientOptions{CLIPath: rpcClientScript(t, `while read _; do :; done`)})
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

func rpcClientScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rpc-client-helper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
