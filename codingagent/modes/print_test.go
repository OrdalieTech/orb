package modes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

func TestRunPrintModeJSONWritesHeaderAndKeepsAssistantFailuresInStream(t *testing.T) {
	for _, stopReason := range []ai.StopReason{ai.StopReasonError, ai.StopReasonAborted} {
		t.Run(string(stopReason), func(t *testing.T) {
			version := sessionstore.CurrentVersion
			header := &sessionstore.SessionHeader{
				Type: "session", Version: &version, ID: "fixture-session",
				Timestamp: "2026-01-02T03:04:05.000Z", CWD: "/fixture/project",
			}
			message := faux.AssistantMessage(ai.AssistantContent{}, faux.AssistantMessageOptions{
				StopReason: stopReason, ErrorMessage: stringPointer("provider failed"),
			})
			session := &jsonPrintSession{
				state: agent.AgentState{Messages: agent.AgentMessages{message}},
				events: []any{
					agent.AgentStartEvent{},
					codingagent.SessionAgentEndEvent{Messages: agent.AgentMessages{message}, WillRetry: false},
					codingagent.AgentSettledEvent{},
				},
			}
			var stdout, stderr bytes.Buffer
			exitCode := RunPrintMode(context.Background(), session, PrintModeOptions{
				Mode: PrintOutputJSON, InitialMessage: "hello", SessionHeader: header,
				Stdout: &stdout, Stderr: &stderr,
			})
			if exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
			}
			lines := bytes.Split(bytes.TrimSuffix(stdout.Bytes(), []byte{'\n'}), []byte{'\n'})
			if len(lines) != 4 || string(lines[0]) != `{"type":"session","version":3,"id":"fixture-session","timestamp":"2026-01-02T03:04:05.000Z","cwd":"/fixture/project"}` {
				t.Fatalf("JSON lines = %q", lines)
			}
			if !bytes.Contains(lines[2], []byte(`"stopReason":"`+string(stopReason)+`"`)) || string(lines[3]) != `{"type":"agent_settled"}` {
				t.Fatalf("terminal JSON lines = %q", lines[2:])
			}
		})
	}
}

type jsonPrintSession struct {
	state        agent.AgentState
	events       []any
	listener     func(any)
	prompt       func()
	abort        func()
	unsubscribed bool
}

func (session *jsonPrintSession) Prompt(context.Context, any, ...*ai.ImageContent) error {
	if session.prompt != nil {
		session.prompt()
	}
	for _, event := range session.events {
		if session.listener != nil {
			session.listener(event)
		}
	}
	return nil
}

func (session *jsonPrintSession) Abort() {
	if session.abort != nil {
		session.abort()
	}
}

func (session *jsonPrintSession) State() agent.AgentState { return session.state }

func (session *jsonPrintSession) Subscribe(listener func(any)) func() {
	session.listener = listener
	return func() {
		session.listener = nil
		session.unsubscribed = true
	}
}

func TestRunPrintModeJSONWaitsForQueuedOutputBeforeReturning(t *testing.T) {
	startedWrite := make(chan struct{})
	releaseWrite := make(chan struct{})
	writer := &blockingWriter{started: startedWrite, release: releaseWrite}
	session := &jsonPrintSession{events: []any{agent.AgentStartEvent{}, codingagent.AgentSettledEvent{}}}
	done := make(chan int, 1)
	go func() {
		done <- RunPrintMode(context.Background(), session, PrintModeOptions{
			Mode: PrintOutputJSON, InitialMessage: "hello", Stdout: writer, Stderr: io.Discard,
		})
	}()
	select {
	case <-startedWrite:
	case <-time.After(time.Second):
		t.Fatal("JSON writer did not start")
	}
	select {
	case code := <-done:
		t.Fatalf("RunPrintMode returned before its queued writer drained: %d", code)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseWrite)
	if code := <-done; code != 0 || !session.unsubscribed {
		t.Fatalf("code=%d unsubscribed=%t", code, session.unsubscribed)
	}
	if got := writer.String(); got != "{\"type\":\"agent_start\"}\n{\"type\":\"agent_settled\"}\n" {
		t.Fatalf("writer = %q", got)
	}
}

type blockingWriter struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	data    []byte
}

func (writer *blockingWriter) Write(value []byte) (int, error) {
	writer.once.Do(func() {
		close(writer.started)
		<-writer.release
	})
	writer.mu.Lock()
	writer.data = append(writer.data, value...)
	writer.mu.Unlock()
	return len(value), nil
}

func (writer *blockingWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return string(writer.data)
}

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

func TestRunPrintModeJSONSignalTeardownStopsSessionAndClosesSerializer(t *testing.T) {
	version := sessionstore.CurrentVersion
	started := make(chan struct{})
	aborted := make(chan struct{})
	var order []string
	session := &jsonPrintSession{}
	session.prompt = func() {
		close(started)
		<-aborted
	}
	session.abort = func() {
		order = append(order, "abort")
		close(aborted)
	}
	signals := make(chan os.Signal, 1)
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runPrintMode(context.Background(), session, PrintModeOptions{
			Mode: PrintOutputJSON, InitialMessage: "hello",
			SessionHeader: &sessionstore.SessionHeader{
				Type: "session", Version: &version, ID: "signal", Timestamp: "2026-01-02T03:04:05.000Z", CWD: "/fixture",
			},
			Stdout: &stdout, Stderr: &stderr,
		}, printModeControl{
			signals: signals,
			killDetachedChildren: func() {
				order = append(order, "kill")
			},
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("JSON prompt did not start")
	}
	signals <- syscall.SIGTERM
	select {
	case code := <-done:
		if code != 143 || stderr.Len() != 0 || !slices.Equal(order, []string{"kill", "abort"}) {
			t.Fatalf("code=%d stderr=%q order=%#v", code, stderr.String(), order)
		}
		if !session.unsubscribed || stdout.String() != "{\"type\":\"session\",\"version\":3,\"id\":\"signal\",\"timestamp\":\"2026-01-02T03:04:05.000Z\",\"cwd\":\"/fixture\"}\n" {
			t.Fatalf("unsubscribed=%t stdout=%q", session.unsubscribed, stdout.String())
		}
	case <-time.After(time.Second):
		t.Fatal("JSON signal teardown did not finish")
	}
}

func TestPrintModeSignalSetExcludesInterrupt(t *testing.T) {
	if slices.Contains(printModeSignals(), os.Interrupt) {
		t.Fatalf("signals = %#v; upstream does not intercept SIGINT", printModeSignals())
	}
}

func newPrintAgent(provider *faux.Provider) *agent.Agent {
	return agent.NewAgent(
		provider.StreamSimple, agent.WithInitialState(agent.AgentState{Model: provider.GetModel()}),
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
