package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingHandler struct {
	mu     sync.Mutex
	events []string
	fails  map[string]int
}

func (h *recordingHandler) Handle(_ context.Context, m Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fails[m.EventID] > 0 {
		h.fails[m.EventID]--
		return fmt.Errorf("transient failure for %s", m.EventID)
	}
	h.events = append(h.events, m.EventID)
	return nil
}

func (h *recordingHandler) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.events...)
}

func writeSpool(t *testing.T, path string, lines ...spoolLine) {
	t.Helper()
	var builder strings.Builder
	for _, line := range lines {
		encoded, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSpool(t *testing.T, path string) (messages []string, acks []string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, lineText := range strings.Split(string(raw), "\n") {
		if lineText == "" {
			continue
		}
		var line spoolLine
		if err := json.Unmarshal([]byte(lineText), &line); err != nil {
			t.Fatalf("bad spool line %q: %v", lineText, err)
		}
		if line.M != nil {
			messages = append(messages, line.M.EventID)
		}
		if line.Ack != "" {
			acks = append(acks, line.Ack)
		}
	}
	return messages, acks
}

func TestLocalPublishHandlesAndAcks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.jsonl")
	handler := &recordingHandler{}
	local, err := NewLocal(handler, path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := local.Publish(testMessage(fmt.Sprintf("ev-%d", i), fmt.Sprintf("chat-%d", i%2), "hi")); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, 2*time.Second, "all messages handled", func() bool {
		return len(handler.snapshot()) == 3
	})
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	messages, acks := readSpool(t, path)
	if len(messages) != 3 || len(acks) != 3 {
		t.Fatalf("spool after run: %d messages, %d acks", len(messages), len(acks))
	}

	// Reboot: everything acked, nothing redelivered, spool compacted empty.
	rebootHandler := &recordingHandler{}
	rebooted, err := NewLocal(rebootHandler, path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := rebooted.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := rebootHandler.snapshot(); len(got) != 0 {
		t.Fatalf("acked messages redelivered: %v", got)
	}
	messages, acks = readSpool(t, path)
	if len(messages) != 0 || len(acks) != 0 {
		t.Fatalf("spool not compacted: %d messages, %d acks", len(messages), len(acks))
	}
}

func TestLocalReplayAfterCrashSkipsAckedAndCompacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.jsonl")
	m1 := testMessage("ev-1", "chat-a", "one")
	m2 := testMessage("ev-2", "chat-a", "two")
	m3 := testMessage("ev-3", "chat-b", "three")
	writeSpool(t, path,
		spoolLine{M: &m1},
		spoolLine{M: &m2},
		spoolLine{M: &m3},
		spoolLine{Ack: "ev-1"},
	)

	handler := &recordingHandler{}
	local, err := NewLocal(handler, path)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, "unacked messages replayed", func() bool {
		return len(handler.snapshot()) == 2
	})
	got := handler.snapshot()
	sawTwo, sawThree := false, false
	for _, id := range got {
		switch id {
		case "ev-2":
			sawTwo = true
		case "ev-3":
			sawThree = true
		default:
			t.Fatalf("unexpected replayed event %q", id)
		}
	}
	if !sawTwo || !sawThree {
		t.Fatalf("replayed = %v, want ev-2 and ev-3", got)
	}
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	messages, acks := readSpool(t, path)
	// Boot compaction rewrote the spool to only the two unacked messages,
	// then processing appended their acks.
	if len(messages) != 2 || messages[0] != "ev-2" || messages[1] != "ev-3" {
		t.Fatalf("compacted spool messages = %v", messages)
	}
	if len(acks) != 2 {
		t.Fatalf("acks = %v", acks)
	}
}

func TestLocalKeyedFIFOOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.jsonl")
	handler := &recordingHandler{}
	local, err := NewLocal(handler, path)
	if err != nil {
		t.Fatal(err)
	}
	const count = 8
	for i := range count {
		if err := local.Publish(testMessage(fmt.Sprintf("ev-%d", i), "one-chat", "hi")); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, 2*time.Second, "all handled", func() bool {
		return len(handler.snapshot()) == count
	})
	got := handler.snapshot()
	for i, id := range got {
		if id != fmt.Sprintf("ev-%d", i) {
			t.Fatalf("per-key FIFO violated: %v", got)
		}
	}
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRetriesWithBackoffUntilSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.jsonl")
	handler := &recordingHandler{fails: map[string]int{"ev-flaky": 2}}
	local, err := NewLocal(handler, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Publish(testMessage("ev-flaky", "chat-1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "flaky message handled", func() bool {
		return len(handler.snapshot()) == 1
	})
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, acks := readSpool(t, path)
	if len(acks) != 1 || acks[0] != "ev-flaky" {
		t.Fatalf("acks = %v", acks)
	}
}

// handlerFunc adapts a function to the Handler interface.
type handlerFunc func(context.Context, Message) error

func (f handlerFunc) Handle(ctx context.Context, m Message) error { return f(ctx, m) }

func TestLocalRejectedMessageIsAckedAndDropped(t *testing.T) {
	// A permanently rejected message (unauthorized sender, unknown platform)
	// must not wedge its worker and key queue in a retry loop: it is acked
	// and dropped, and later messages on the same key still flow.
	path := filepath.Join(t.TempDir(), "spool.jsonl")
	var mu sync.Mutex
	var handled []string
	handler := handlerFunc(func(_ context.Context, m Message) error {
		if m.EventID == "ev-bad" {
			return fmt.Errorf("%w: sender banned", ErrRejected)
		}
		mu.Lock()
		handled = append(handled, m.EventID)
		mu.Unlock()
		return nil
	})
	local, err := NewLocal(handler, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Publish(testMessage("ev-bad", "chat-1", "hi")); err != nil {
		t.Fatal(err)
	}
	if err := local.Publish(testMessage("ev-good", "chat-1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, "good message handled behind the rejected one", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1 && handled[0] == "ev-good"
	})
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, acks := readSpool(t, path)
	if len(acks) != 2 {
		t.Fatalf("acks = %v, want ev-bad and ev-good", acks)
	}
}

func TestLocalStopBypassesBusyKey(t *testing.T) {
	// A /stop published while its conversation's turn is in flight must reach
	// the handler immediately — queued behind the per-key inflight gate it
	// could only run after the turn it is meant to abort.
	path := filepath.Join(t.TempDir(), "spool.jsonl")
	blocking := make(chan struct{})
	started := make(chan struct{}, 1)
	var mu sync.Mutex
	var handled []string
	handler := handlerFunc(func(_ context.Context, m Message) error {
		if m.EventID == "ev-long" {
			select {
			case started <- struct{}{}:
			default:
			}
			<-blocking
		}
		mu.Lock()
		handled = append(handled, m.EventID)
		mu.Unlock()
		return nil
	})
	local, err := NewLocal(handler, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Publish(testMessage("ev-long", "chat-1", "please think forever")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("long turn never started")
	}
	if err := local.Publish(testMessage("ev-stop", "chat-1", "/stop")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, "/stop handled while the turn is in flight", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 1 && handled[0] == "ev-stop"
	})
	close(blocking)
	waitUntil(t, 2*time.Second, "long turn finished", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(handled) == 2
	})
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, acks := readSpool(t, path)
	if len(acks) != 2 {
		t.Fatalf("acks = %v, want both events acked", acks)
	}
}

func TestLocalStopFanOutStaysBounded(t *testing.T) {
	// /stop skips the keyed queue, and it used to skip every bound with it:
	// one goroutine per message let anyone who can message the bot pick the
	// concurrency.
	path := filepath.Join(t.TempDir(), "spool.jsonl")
	release := make(chan struct{})
	var mu sync.Mutex
	var live, peak, done int
	handler := handlerFunc(func(context.Context, Message) error {
		mu.Lock()
		live++
		peak = max(peak, live)
		mu.Unlock()
		<-release
		mu.Lock()
		live--
		done++
		mu.Unlock()
		return nil
	})
	local, err := NewLocal(handler, path)
	if err != nil {
		t.Fatal(err)
	}
	baseline := goruntime.NumGoroutine()
	const flood = 200
	published := make(chan error, 1)
	go func() {
		for index := range flood {
			// Publish blocks once the stop pool is saturated — that
			// backpressure is the point, so it cannot run on the test
			// goroutine.
			if err := local.Publish(testMessage(fmt.Sprintf("ev-%d", index), "chat-1", "/stop")); err != nil {
				published <- err
				return
			}
		}
		published <- nil
	}()

	waitUntil(t, 30*time.Second, "the stop pool to saturate", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return live == localStopWorkers
	})
	// Every /stop past the pool waits in the spool, not in a goroutine. Poll
	// for a while: an unbounded dispatcher needs a moment to pile the whole
	// flood up behind the blocked handlers.
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if grown := goruntime.NumGoroutine() - baseline; grown > localStopWorkers {
			t.Fatalf("a /stop flood spawned %d goroutines beyond the pool of %d", grown, localStopWorkers)
		}
	}
	close(release)
	if err := <-published; err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 30*time.Second, "every /stop handled", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return done == flood
	})
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak > localStopWorkers {
		t.Fatalf("peak concurrent /stop handlers = %d, want at most %d", peak, localStopWorkers)
	}
}

func TestLocalGivesUpOnAPermanentlyFailingHandler(t *testing.T) {
	// Retrying forever pinned a worker and its key; an exhausted message stays
	// unacked on disk so the next boot replays it.
	path := filepath.Join(t.TempDir(), "spool.jsonl")
	var attempts int
	var mu sync.Mutex
	handler := handlerFunc(func(_ context.Context, m Message) error {
		mu.Lock()
		defer mu.Unlock()
		if m.EventID == "ev-broken" {
			attempts++
			return errors.New("always fails")
		}
		return nil
	})
	local, err := NewLocal(handler, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Publish(testMessage("ev-broken", "chat-1", "hi")); err != nil {
		t.Fatal(err)
	}
	// The key must be released for the message behind it.
	if err := local.Publish(testMessage("ev-next", "chat-1", "hi")); err != nil {
		t.Fatal(err)
	}
	_, acks := waitForAcks(t, local, path, 1)
	if len(acks) != 1 || acks[0] != "ev-next" {
		t.Fatalf("acks = %v, want only ev-next", acks)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != localMaxAttempts {
		t.Fatalf("handler attempts = %d, want the %d-attempt cap", attempts, localMaxAttempts)
	}
}

// waitForAcks closes local once the spool holds want acks.
func waitForAcks(t *testing.T, local *Local, path string, want int) (messages, acks []string) {
	t.Helper()
	waitUntil(t, 30*time.Second, "spool acks", func() bool {
		_, current := readSpool(t, path)
		return len(current) >= want
	})
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	return readSpool(t, path)
}

func TestLocalPublishAfterCloseFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.jsonl")
	local, err := NewLocal(&recordingHandler{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := local.Publish(testMessage("ev-late", "chat-1", "hi")); err == nil {
		t.Fatal("publish succeeded after close")
	}
}
