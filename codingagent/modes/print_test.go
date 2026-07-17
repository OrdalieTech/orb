package modes

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
)

func TestRunPrintModeTextPromptsSeriallyAndPrintsTextBlocks(t *testing.T) {
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage("initial answer"),
		faux.AssistantMessage("next answer"),
		faux.AssistantMessage(ai.AssistantContent{
			faux.Text("first"),
			faux.Thinking("hidden"),
			faux.Text("second\n"),
		}),
	})
	session := newPrintAgent(provider)
	var stdout, stderr bytes.Buffer

	exitCode := RunPrintMode(context.Background(), session, PrintModeOptions{
		InitialMessage: "initial",
		Messages:       []string{"next", ""},
		Stdout:         &stdout,
		Stderr:         &stderr,
	})

	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if got, want := stdout.String(), "first\nsecond\n\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if got, want := userPromptTexts(t, session.State()), []string{"initial", "next", ""}; !slices.Equal(got, want) {
		t.Fatalf("user prompts = %#v, want %#v", got, want)
	}
}

func TestRunPrintModeAssistantFailuresReturnOne(t *testing.T) {
	for _, test := range []struct {
		name       string
		stopReason ai.StopReason
		errorText  *string
		wantStderr string
	}{
		{name: "error message", stopReason: ai.StopReasonError, errorText: stringPointer("provider failed"), wantStderr: "provider failed\n"},
		{name: "aborted fallback", stopReason: ai.StopReasonAborted, errorText: stringPointer(""), wantStderr: "Request aborted\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := faux.New()
			provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage(
				ai.AssistantContent{},
				faux.AssistantMessageOptions{StopReason: test.stopReason, ErrorMessage: test.errorText},
			)})
			session := newPrintAgent(provider)
			var stdout, stderr bytes.Buffer

			exitCode := RunPrintMode(context.Background(), session, PrintModeOptions{
				InitialMessage: "hello",
				Stdout:         &stdout,
				Stderr:         &stderr,
			})

			if exitCode != 1 {
				t.Fatalf("exit code = %d", exitCode)
			}
			if got := stderr.String(); got != test.wantStderr {
				t.Fatalf("stderr = %q, want %q", got, test.wantStderr)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestRunPrintModePromptErrorReturnsOne(t *testing.T) {
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("unused")})
	session := newPrintAgent(provider)
	session.Subscribe(func(context.Context, agent.AgentEvent) error {
		return errors.New("prompt failed")
	})
	var stdout, stderr bytes.Buffer

	exitCode := RunPrintMode(context.Background(), session, PrintModeOptions{
		InitialMessage: "hello",
		Stdout:         &stdout,
		Stderr:         &stderr,
	})

	if exitCode != 1 || stderr.String() != "prompt failed\n" {
		t.Fatalf("exit = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunPrintModeSkipsEmptyInitialMessage(t *testing.T) {
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("done")})
	session := newPrintAgent(provider)
	var stdout, stderr bytes.Buffer

	exitCode := RunPrintMode(context.Background(), session, PrintModeOptions{
		Messages: []string{"later"},
		Stdout:   &stdout,
		Stderr:   &stderr,
	})

	if got := userPromptTexts(t, session.State()); exitCode != 0 || !slices.Equal(got, []string{"later"}) {
		t.Fatalf("exit = %d, prompts = %#v, stderr = %q", exitCode, got, stderr.String())
	}
}

func TestRunPrintModeSignalShutdown(t *testing.T) {
	for _, test := range []struct {
		name     string
		signal   os.Signal
		wantCode int
	}{
		{name: "term", signal: syscall.SIGTERM, wantCode: 143},
		{name: "hup", signal: syscall.SIGHUP, wantCode: 129},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := faux.New()
			provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("unused")})
			session := newPrintAgent(provider)
			started := make(chan struct{})
			var startedOnce sync.Once
			session.Subscribe(func(ctx context.Context, _ agent.AgentEvent) error {
				startedOnce.Do(func() { close(started) })
				<-ctx.Done()
				return ctx.Err()
			})
			signals := make(chan os.Signal, 1)
			var order []string
			var stderr bytes.Buffer
			done := make(chan int, 1)
			go func() {
				done <- runPrintMode(context.Background(), session, PrintModeOptions{
					InitialMessage: "hello",
					Stderr:         &stderr,
				}, printModeControl{
					signals: signals,
					killDetachedChildren: func() {
						order = append(order, "kill")
					},
				})
			}()

			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("prompt did not start")
			}
			signals <- test.signal
			select {
			case code := <-done:
				if code != test.wantCode || stderr.Len() != 0 || !slices.Equal(order, []string{"kill"}) {
					t.Fatalf("code=%d stderr=%q order=%#v", code, stderr.String(), order)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("signal shutdown did not finish")
			}
		})
	}
}

func TestPrintModeSignalSetExcludesInterrupt(t *testing.T) {
	if slices.Contains(printModeSignals(), os.Interrupt) {
		t.Fatalf("signals = %#v; upstream does not intercept SIGINT", printModeSignals())
	}
}

func newPrintAgent(provider *faux.Provider) *agent.Agent {
	return agent.NewAgent(
		agent.WithInitialState(agent.AgentState{Model: provider.GetModel()}),
		agent.WithStreamFn(provider.StreamSimple),
	)
}

func userPromptTexts(t *testing.T, state agent.AgentState) []string {
	t.Helper()
	var prompts []string
	for _, message := range state.Messages {
		user, ok := message.(*ai.UserMessage)
		if !ok {
			continue
		}
		if len(user.Content.Blocks) != 1 {
			t.Fatalf("user content = %#v", user.Content)
		}
		text, ok := user.Content.Blocks[0].(*ai.TextContent)
		if !ok {
			t.Fatalf("user block = %#v", user.Content.Blocks[0])
		}
		prompts = append(prompts, text.Text)
	}
	return prompts
}

func stringPointer(value string) *string { return &value }
