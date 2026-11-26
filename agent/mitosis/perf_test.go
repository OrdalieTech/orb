package mitosis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	runtime "github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
)

func benchAttached(tb testing.TB, steps ...faux.ResponseStep) (*Attachment, *runtime.AgentSessionRuntime) {
	tb.Helper()
	ctx := tb.Context()
	cwd := tb.TempDir()
	manager, _ := session.InMemory(cwd)
	provider := faux.New(faux.Options{})
	provider.SetResponses(steps)
	host, err := runtime.NewAgentSessionRuntime(ctx, runtime.AgentSessionOptions{
		CWD: cwd, AgentDir: tb.TempDir(), SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple,
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { host.Dispose(context.Background()) })
	a, err := Attach(ctx, host, Options{
		InstanceID: protocol.NewID(), Store: &document.Memory{},
		Authorize: func(mitosis.Request) bool { return true },
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = a.Close() })
	if err = a.SetGeneration("1"); err != nil {
		tb.Fatal(err)
	}
	return a, host
}

func BenchmarkAttachmentPulse(b *testing.B) {
	a, _ := benchAttached(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if a.pulse() == "" {
			b.Fatal("empty pulse")
		}
	}
}

func BenchmarkAttachmentStretch(b *testing.B) {
	for _, n := range []int{20, 100, 500} {
		b.Run(fmt.Sprintf("events=%d", n), func(b *testing.B) {
			a, _ := benchAttached(b)
			stream := a.stream
			startCursor := stream.Cursor()
			for i := range n {
				payload := fmt.Sprintf(`{"type":"message_update","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}`, strings.Repeat("word ", i%40+1))
				stream.Publish(json.RawMessage(payload))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				out, err := a.stretch(startCursor, protocol.MaxPage)
				if err != nil || len(out.Events) == 0 {
					b.Fatalf("stretch: %d, %v", len(out.Events), err)
				}
			}
		})
	}
}

func TestAttachmentEventWakeupLatency(t *testing.T) {
	a, _ := benchAttached(t)
	cursor := a.stream.Cursor()
	state := a.pulse()

	var total time.Duration
	const samples = 10
	for i := range samples {
		done := make(chan time.Time, 1)
		go func() {
			_, err := a.await(t.Context(), cursor, state, protocol.MaxPage)
			if err != nil {
				t.Errorf("await: %v", err)
			}
			done <- time.Now()
		}()
		// Give await a moment to enter its wait loop, then publish an event.
		time.Sleep(15 * time.Millisecond)
		sentAt := time.Now()
		a.stream.Publish(json.RawMessage(fmt.Sprintf(`{"type":"message_update","seq":%d}`, i)))
		wokeAt := <-done
		total += wokeAt.Sub(sentAt)
		cursor = a.stream.Cursor()
	}
	avg := total / samples
	t.Logf("await event wakeup avg latency over %d samples: %v", samples, avg)
}
