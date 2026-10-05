package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// marshalJSONEvent ports upstream toJsonEvent (modes/json-event.ts): the
// stdout JSON and RPC protocols emit message_update delta-only, dropping the
// cumulative partial snapshot and the top-level message echo. message_start
// provides the initial message, deltas build it, and message_end provides the
// final authoritative message. Cumulative usage remains available because its
// size is constant.
func marshalJSONEvent(event any) ([]byte, error) {
	update, ok := event.(engine.MessageUpdateEvent)
	if !ok {
		return agent.MarshalSessionEvent(event)
	}
	message, ok := update.Message.(*ai.AssistantMessage)
	if !ok || message == nil {
		return nil, errors.New("message_update message is not an assistant message")
	}
	encoded, err := ai.MarshalAssistantMessageEvent(withoutPartial(update.AssistantMessageEvent))
	if err != nil {
		return nil, err
	}
	delta, cut := bytes.CutSuffix(encoded, []byte(`,"partial":null}`))
	if cut {
		delta = append(delta, '}')
	} else if delta, err = deleteObjectMember(encoded, "partial"); err != nil {
		return nil, err
	}
	var toolStart *ai.ToolCallStartEvent
	switch event := update.AssistantMessageEvent.(type) {
	case ai.ToolCallStartEvent:
		toolStart = &event
	case *ai.ToolCallStartEvent:
		toolStart = event
	}
	if toolStart != nil {
		var toolCall *ai.ToolCall
		if toolStart.Partial != nil && toolStart.ContentIndex >= 0 && toolStart.ContentIndex < len(toolStart.Partial.Content) {
			toolCall, _ = toolStart.Partial.Content[toolStart.ContentIndex].(*ai.ToolCall)
		}
		if toolCall == nil {
			return nil, fmt.Errorf("toolcall_start content at index %d is not a tool call", toolStart.ContentIndex)
		}
		delta = strconv.AppendInt([]byte(`{"type":"toolcall_start","contentIndex":`), int64(toolStart.ContentIndex), 10)
		delta = jsonwire.AppendString(append(delta, `,"id":`...), toolCall.ID)
		delta = append(jsonwire.AppendString(append(delta, `,"toolName":`...), toolCall.Name), '}')
	}
	// Member order matches upstream's object literal: type, usage, assistantMessageEvent.
	usage, err := message.Usage.MarshalJSON()
	if err != nil {
		return nil, err
	}
	frame := append(append([]byte(`{"type":"message_update","usage":`), usage...), `,"assistantMessageEvent":`...)
	return append(append(frame, delta...), '}'), nil
}

// withoutPartial copies a stream event with its partial message cleared. The
// event types encode it last, so the delta is cut from a small encoding
// instead of re-parsing one that holds the whole message so far.
func withoutPartial(event ai.AssistantMessageEvent) ai.AssistantMessageEvent {
	switch typed := event.(type) {
	case ai.StartEvent:
		typed.Partial = nil
		return typed
	case ai.TextStartEvent:
		typed.Partial = nil
		return typed
	case ai.TextDeltaEvent:
		typed.Partial = nil
		return typed
	case ai.TextEndEvent:
		typed.Partial = nil
		return typed
	case ai.ThinkingStartEvent:
		typed.Partial = nil
		return typed
	case ai.ThinkingDeltaEvent:
		typed.Partial = nil
		return typed
	case ai.ThinkingEndEvent:
		typed.Partial = nil
		return typed
	case ai.ToolCallStartEvent:
		typed.Partial = nil
		return typed
	case ai.ToolCallDeltaEvent:
		typed.Partial = nil
		return typed
	case ai.ToolCallEndEvent:
		typed.Partial = nil
		return typed
	case ai.RawAssistantMessageEvent:
		typed.Partial = nil
		return typed
	}
	return event
}

// deleteObjectMember removes one member from an encoded JSON object while
// leaving every other member's bytes and order untouched.
func deleteObjectMember(object []byte, name string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(object))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("rpc: assistant message event must be an object")
	}
	var output bytes.Buffer
	output.WriteByte('{')
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		member, ok := token.(string)
		if !ok {
			return nil, errors.New("rpc: assistant message event has a non-string member name")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if member == name {
			continue
		}
		encodedName, err := ai.Marshal(member)
		if err != nil {
			return nil, err
		}
		if output.Len() > 1 {
			output.WriteByte(',')
		}
		output.Write(encodedName)
		output.WriteByte(':')
		output.Write(value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

// FrameWriter serializes JSONL frames onto one writer from a single goroutine,
// so session events, command responses and extension UI requests never
// interleave. The first write error stops output; Close reports it. Print
// mode's JSON output shares it, being the same event stream (json-event.ts).
type FrameWriter struct {
	mu        sync.Mutex
	writer    io.Writer
	lines     chan []byte
	done      chan struct{}
	aborted   chan struct{}
	callbacks sync.WaitGroup
	accepting bool
	closed    bool
	err       error
}

// NewFrameWriter starts the writer goroutine; Close stops it.
func NewFrameWriter(writer io.Writer) *FrameWriter {
	output := &FrameWriter{
		writer: writer, lines: make(chan []byte, 64), done: make(chan struct{}), aborted: make(chan struct{}), accepting: true,
	}
	go output.run()
	return output
}

func (output *FrameWriter) run() {
	defer close(output.done)
	// Each frame and its LF go out through one buffer the writer reuses,
	// since an io.Writer keeps nothing it is handed.
	var line []byte
	for {
		var frame []byte
		select {
		case <-output.aborted:
			return
		case value, open := <-output.lines:
			if !open {
				return
			}
			frame = value
		}
		// Abort and a queued frame can be ready together; never start a write after Abort.
		select {
		case <-output.aborted:
			return
		default:
		}
		output.mu.Lock()
		failed := output.err != nil
		output.mu.Unlock()
		if failed {
			continue
		}
		line = append(append(line[:0], frame...), '\n')
		if err := writeLine(output.writer, line); err != nil {
			output.fail(err)
		}
		if cap(line) > 1<<20 {
			line = nil
		}
	}
}

// WriteFrame queues one encoded frame, which it takes over; the terminating
// LF is added.
func (output *FrameWriter) WriteFrame(value []byte) {
	if !output.beginWrite() {
		return
	}
	defer output.callbacks.Done()
	output.queueFrame(value)
}

func (output *FrameWriter) queueFrame(value []byte) {
	select {
	case output.lines <- value:
	case <-output.aborted:
	}
}

func (output *FrameWriter) beginWrite() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	if !output.accepting {
		return false
	}
	output.callbacks.Add(1)
	return true
}

// WriteEvent encodes a session event in the stdout JSON/RPC shape
// (message_update delta-only) and queues it. Events arriving after Close
// started are dropped.
func (output *FrameWriter) WriteEvent(event any) {
	if !output.beginWrite() {
		return
	}
	defer output.callbacks.Done()

	encoded, err := marshalJSONEvent(event)
	if err != nil {
		output.fail(err)
		return
	}
	output.queueFrame(encoded)
}

func (output *FrameWriter) fail(err error) {
	if err == nil {
		return
	}
	output.mu.Lock()
	if output.err == nil {
		output.err = err
	}
	output.mu.Unlock()
}

// Abort releases queued producers and Close without waiting for a stalled
// writer. An in-flight io.Writer.Write cannot be interrupted; it may finish
// after Abort returns, but no caller must wait for it during forced shutdown.
func (output *FrameWriter) Abort() {
	output.mu.Lock()
	defer output.mu.Unlock()
	output.accepting = false
	select {
	case <-output.aborted:
	default:
		close(output.aborted)
	}
}

// Close stops accepting frames, drains output unless aborted, and returns the
// first write or encoding error. It is safe to call concurrently.
func (output *FrameWriter) Close() error {
	output.mu.Lock()
	output.accepting = false
	if !output.closed {
		output.closed = true
		go func() {
			output.callbacks.Wait()
			close(output.lines)
		}()
	}
	output.mu.Unlock()
	select {
	case <-output.done:
	case <-output.aborted:
	}

	output.mu.Lock()
	defer output.mu.Unlock()
	return output.err
}

func writeLine(writer io.Writer, line []byte) error {
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
