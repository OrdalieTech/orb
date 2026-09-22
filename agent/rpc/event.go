package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
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
	encoded, err := ai.MarshalAssistantMessageEvent(update.AssistantMessageEvent)
	if err != nil {
		return nil, err
	}
	delta, err := deleteObjectMember(encoded, "partial")
	if err != nil {
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
		delta, err = ai.Marshal(struct {
			Type         string `json:"type"`
			ContentIndex int    `json:"contentIndex"`
			ID           string `json:"id"`
			ToolName     string `json:"toolName"`
		}{"toolcall_start", toolStart.ContentIndex, toolCall.ID, toolCall.Name})
		if err != nil {
			return nil, err
		}
	}
	// Member order matches upstream's object literal: type, usage, assistantMessageEvent.
	return ai.Marshal(struct {
		Type                  engine.AgentEventType `json:"type"`
		Usage                 ai.Usage              `json:"usage"`
		AssistantMessageEvent json.RawMessage       `json:"assistantMessageEvent"`
	}{engine.EventMessageUpdate, message.Usage, delta})
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
	callbacks sync.WaitGroup
	accepting bool
	closed    bool
	err       error
}

// NewFrameWriter starts the writer goroutine; Close stops it.
func NewFrameWriter(writer io.Writer) *FrameWriter {
	output := &FrameWriter{
		writer: writer, lines: make(chan []byte, 64), done: make(chan struct{}), accepting: true,
	}
	go output.run()
	return output
}

func (output *FrameWriter) run() {
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

// WriteFrame queues one encoded frame; the terminating LF is added.
func (output *FrameWriter) WriteFrame(value []byte) {
	output.lines <- bytes.Clone(value)
}

// WriteEvent encodes a session event in the stdout JSON/RPC shape
// (message_update delta-only) and queues it. Events arriving after Close
// started are dropped.
func (output *FrameWriter) WriteEvent(event any) {
	output.mu.Lock()
	if !output.accepting {
		output.mu.Unlock()
		return
	}
	output.callbacks.Add(1)
	output.mu.Unlock()
	defer output.callbacks.Done()

	encoded, err := marshalJSONEvent(event)
	if err != nil {
		output.fail(err)
		return
	}
	output.WriteFrame(encoded)
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

// Close stops accepting events, flushes queued frames and returns the first
// write or encoding error.
func (output *FrameWriter) Close() error {
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
