package rpc

import (
	"bytes"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/OrdalieTech/orb/engine"
)

type blockedFrameWriter struct {
	bytes.Buffer
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (writer *blockedFrameWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() { close(writer.started) })
	<-writer.release
	return writer.Buffer.Write(data)
}

func TestFrameWriterAbortReleasesBlockedProducersAndClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writer := &blockedFrameWriter{started: make(chan struct{}), release: make(chan struct{})}
		defer close(writer.release)
		output := NewFrameWriter(writer)
		output.WriteFrame([]byte(`{"first":true}`))
		<-writer.started
		for range cap(output.lines) {
			output.WriteFrame([]byte(`{"queued":true}`))
		}
		produced := make(chan struct{})
		go func() {
			output.WriteEvent(engine.AgentStartEvent{})
			close(produced)
		}()
		closed := make(chan error, 1)
		go func() { closed <- output.Close() }()
		synctest.Wait()
		output.Abort()
		output.Abort()
		select {
		case <-produced:
		case <-time.After(time.Second):
			t.Fatal("abort did not release the producer")
		}
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("abort did not interrupt close")
		}
		if err := output.Close(); err != nil {
			t.Fatal(err)
		}
		output.WriteFrame([]byte(`{"late":true}`))
		output.WriteEvent(engine.AgentStartEvent{})
	})
}

func TestFrameWriterNormalCloseDrainsBlockedWriterLosslessly(t *testing.T) {
	writer := &blockedFrameWriter{started: make(chan struct{}), release: make(chan struct{})}
	output := NewFrameWriter(writer)
	output.WriteFrame([]byte(`{"first":true}`))
	<-writer.started
	output.WriteEvent(engine.AgentStartEvent{})
	closed := make(chan error, 1)
	go func() { closed <- output.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("close returned before the writer drained: %v", err)
	default:
	}
	close(writer.release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if got, want := writer.String(), "{\"first\":true}\n{\"type\":\"agent_start\"}\n"; got != want {
		t.Fatalf("frames = %q, want %q", got, want)
	}
}
