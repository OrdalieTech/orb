package rpc

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine"
)

type blockingDisposeHost struct {
	rpcTestHost
	begun, release, finished chan struct{}
}

type steppedWriter struct {
	started, firstRelease, laterRelease chan struct{}
	calls                               atomic.Int32
}

func (w *steppedWriter) Write(p []byte) (int, error) {
	if w.calls.Add(1) == 1 {
		close(w.started)
		<-w.firstRelease
	} else {
		<-w.laterRelease
	}
	return len(p), nil
}

func TestFrameWriterAbortStartsNoQueuedWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		extraWrites := 0
		for range 100 {
			w := &steppedWriter{started: make(chan struct{}),
				firstRelease: make(chan struct{}), laterRelease: make(chan struct{})}
			output := NewFrameWriter(w)
			output.WriteFrame([]byte("{}"))
			<-w.started
			output.WriteFrame([]byte("{}"))
			output.Abort()
			if err := output.Close(); err != nil {
				t.Fatal(err)
			}
			close(w.firstRelease)
			synctest.Wait()
			if w.calls.Load() > 1 {
				extraWrites++
			}
			close(w.laterRelease)
			<-output.done
		}
		if extraWrites != 0 {
			t.Errorf("%d/100 aborted writers started a NEW queued Write after the in-flight Write returned", extraWrites)
		}
	})
}

func (h *blockingDisposeHost) Dispose() {
	close(h.begun)
	<-h.release
	h.runtime.Dispose()
	close(h.finished)
}

func TestServeTerminateWaitsForRunningDispose(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		h := &blockingDisposeHost{rpcTestHost: rpcTestHost{runtime: runtime},
			begun: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
		terminate := make(chan int, 1)
		done := make(chan int, 1)
		go func() {
			done <- Serve(context.Background(), h, Options{
				Input: strings.NewReader(""), Output: io.Discard, Terminate: terminate,
			})
		}()
		<-h.begun
		synctest.Wait()
		terminate <- 143
		synctest.Wait()
		select {
		case code := <-done:
			t.Errorf("Serve returned %d while host.Dispose is still running (no blocked input/output)", code)
		default:
		}
		close(h.release)
		<-h.finished
		synctest.Wait()
	})
}
