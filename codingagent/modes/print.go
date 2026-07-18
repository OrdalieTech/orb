package modes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
)

type PrintOutputMode string

const (
	PrintOutputText PrintOutputMode = "text"
	PrintOutputJSON PrintOutputMode = "json"
)

type PrintModeOptions struct {
	Mode           PrintOutputMode
	Messages       []string
	InitialMessage string
	SessionHeader  *session.SessionHeader
	Stdout         io.Writer
	Stderr         io.Writer
}

type printSession interface {
	Prompt(context.Context, any, ...*ai.ImageContent) error
	Abort()
	State() agent.AgentState
}

type printSessionSubscriber interface {
	Subscribe(func(any)) func()
}

// RunPrintMode sends each configured prompt serially and returns a process exit
// code. Model failures remain represented by the final assistant message.
func RunPrintMode(ctx context.Context, session printSession, options PrintModeOptions) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, printModeSignals()...)
	return runPrintMode(ctx, session, options, printModeControl{
		signals:              signals,
		stopSignals:          func() { signal.Stop(signals) },
		killDetachedChildren: tools.KillTrackedDetachedChildren,
	})
}

type printModeControl struct {
	signals              <-chan os.Signal
	stopSignals          func()
	killDetachedChildren func()
}

type printModeResult struct {
	texts []string
	err   error
}

func runPrintMode(ctx context.Context, session printSession, options PrintModeOptions, control printModeControl) int {
	stdout := options.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := options.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if control.stopSignals != nil {
		defer control.stopSignals()
	}
	mode := options.Mode
	if mode == "" {
		mode = PrintOutputText
	}
	var jsonOutput *serializedOutput
	var unsubscribe func()
	closeJSONOutput := func() error {
		if unsubscribe != nil {
			unsubscribe()
			unsubscribe = nil
		}
		if jsonOutput == nil {
			return nil
		}
		return jsonOutput.closeAndWait()
	}
	defer func() { _ = closeJSONOutput() }()
	if mode == PrintOutputJSON {
		jsonOutput = newSerializedOutput(stdout)
		if options.SessionHeader != nil {
			encoded, err := ai.Marshal(options.SessionHeader)
			if err != nil {
				writeError(stderr, err)
				return 1
			}
			jsonOutput.writeLine(encoded)
		}
		subscriber, ok := session.(printSessionSubscriber)
		if !ok {
			writeError(stderr, errors.New("print mode: JSON session does not support events"))
			return 1
		}
		unsubscribe = subscriber.Subscribe(jsonOutput.writeSessionEvent)
	}

	shutdown := func(received os.Signal) int {
		if control.killDetachedChildren != nil {
			control.killDetachedChildren()
		}
		if session != nil {
			session.Abort()
		}
		return printModeSignalExitCode(received)
	}

	executed := make(chan printModeResult, 1)
	go func() { executed <- executePrintMode(ctx, session, options, mode) }()

	var result printModeResult
	select {
	case received := <-control.signals:
		return shutdown(received)
	case result = <-executed:
	}
	select {
	case received := <-control.signals:
		return shutdown(received)
	default:
	}

	if mode == PrintOutputJSON {
		if err := closeJSONOutput(); result.err == nil {
			result.err = err
		}
	}
	exitCode := 0
	if result.err != nil {
		writeError(stderr, result.err)
		exitCode = 1
	} else if mode == PrintOutputText {
		rendered := make(chan error, 1)
		go func() {
			for _, text := range result.texts {
				if err := writeLine(stdout, []byte(text)); err != nil {
					rendered <- err
					return
				}
			}
			rendered <- nil
		}()
		select {
		case received := <-control.signals:
			return shutdown(received)
		case err := <-rendered:
			if err != nil {
				writeError(stderr, err)
				exitCode = 1
			}
		}
	}

	return exitCode
}

func executePrintMode(ctx context.Context, session printSession, options PrintModeOptions, mode PrintOutputMode) printModeResult {
	if session == nil {
		return printModeResult{err: errors.New("print mode: nil session")}
	}
	if options.InitialMessage != "" {
		if err := session.Prompt(ctx, options.InitialMessage); err != nil {
			return printModeResult{err: err}
		}
	}
	for _, message := range options.Messages {
		if err := session.Prompt(ctx, message); err != nil {
			return printModeResult{err: err}
		}
	}
	if mode == PrintOutputJSON {
		return printModeResult{}
	}

	assistant := lastAssistant(session.State())
	if assistant == nil {
		return printModeResult{}
	}
	if err := assistantFailure(assistant); err != nil {
		return printModeResult{err: err}
	}
	result := printModeResult{texts: make([]string, 0, len(assistant.Content))}
	for _, block := range assistant.Content {
		if text, ok := block.(*ai.TextContent); ok {
			result.texts = append(result.texts, text.Text)
		}
	}
	return result
}

type serializedOutput struct {
	mu        sync.Mutex
	writer    io.Writer
	lines     chan []byte
	done      chan struct{}
	callbacks sync.WaitGroup
	accepting bool
	closed    bool
	err       error
}

func newSerializedOutput(writer io.Writer) *serializedOutput {
	output := &serializedOutput{
		writer: writer, lines: make(chan []byte, 64), done: make(chan struct{}), accepting: true,
	}
	go output.run()
	return output
}

func (output *serializedOutput) run() {
	defer close(output.done)
	for line := range output.lines {
		output.mu.Lock()
		failed := output.err != nil
		output.mu.Unlock()
		if failed {
			continue
		}
		if err := writeLine(output.writer, line); err != nil {
			output.fail(err)
		}
	}
}

func (output *serializedOutput) writeLine(value []byte) {
	output.lines <- bytesClone(value)
}

func (output *serializedOutput) writeSessionEvent(event any) {
	output.mu.Lock()
	if !output.accepting {
		output.mu.Unlock()
		return
	}
	output.callbacks.Add(1)
	output.mu.Unlock()
	defer output.callbacks.Done()

	encoded, err := codingagent.MarshalSessionEvent(event)
	if err != nil {
		output.fail(err)
		return
	}
	output.writeLine(encoded)
}

func (output *serializedOutput) fail(err error) {
	if err == nil {
		return
	}
	output.mu.Lock()
	if output.err == nil {
		output.err = err
	}
	output.mu.Unlock()
}

func (output *serializedOutput) closeAndWait() error {
	output.mu.Lock()
	output.accepting = false
	output.mu.Unlock()
	output.callbacks.Wait()

	output.mu.Lock()
	if !output.closed {
		output.closed = true
		close(output.lines)
	}
	done := output.done
	output.mu.Unlock()
	<-done

	output.mu.Lock()
	defer output.mu.Unlock()
	return output.err
}

func bytesClone(value []byte) []byte {
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}

func lastAssistant(state agent.AgentState) *ai.AssistantMessage {
	if len(state.Messages) == 0 {
		return nil
	}
	switch message := state.Messages[len(state.Messages)-1].(type) {
	case *ai.AssistantMessage:
		return message
	case ai.AssistantMessage:
		return &message
	default:
		return nil
	}
}

func assistantFailure(message *ai.AssistantMessage) error {
	if message.StopReason != ai.StopReasonError && message.StopReason != ai.StopReasonAborted {
		return nil
	}
	if message.ErrorMessage != nil && *message.ErrorMessage != "" {
		return errors.New(*message.ErrorMessage)
	}
	return fmt.Errorf("Request %s", message.StopReason) //nolint:staticcheck // Upstream error capitalization is observable.
}

func writeLine(writer io.Writer, value []byte) error {
	line := make([]byte, len(value)+1)
	copy(line, value)
	line[len(value)] = '\n'
	for len(line) > 0 {
		written, err := writer.Write(line)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		line = line[written:]
	}
	return nil
}

func writeError(writer io.Writer, err error) {
	if err != nil {
		_, _ = fmt.Fprintln(writer, err)
	}
}
