package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Handler consumes one message synchronously. [*Processor] implements it.
type Handler interface {
	Handle(ctx context.Context, m Message) error
}

// spoolLine is one durable spool record: either a published message or an ack.
type spoolLine struct {
	M   *Message `json:"m,omitempty"`
	Ack string   `json:"ack,omitempty"`
}

const localWorkers = 8

// Local is a durable single-process spool plus a keyed FIFO dispatcher:
// per-key FIFO order, at most one in-flight Handle per key, a global worker
// pool, and replay-with-compaction on boot. /stop messages bypass the keyed
// queue so they can preempt the in-flight turn they target, and messages the
// handler rejects permanently ([ErrRejected]) are acked and dropped instead
// of retried.
//
// ponytail: single-process spool; swap Publish for a broker in clustered
// deployments.
type Local struct {
	handler Handler

	fileMu sync.Mutex
	file   *os.File

	mu       sync.Mutex
	cond     *sync.Cond
	queues   map[string][]Message
	inflight map[string]bool
	stopped  bool

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewLocal opens (or creates) the spool at spoolPath, replays unacked
// messages, compacts the file, and starts the dispatcher.
func NewLocal(handler Handler, spoolPath string) (*Local, error) {
	if handler == nil {
		return nil, errors.New("chat: NewLocal requires a handler")
	}
	pending, err := replaySpool(spoolPath)
	if err != nil {
		return nil, err
	}
	if err := compactSpool(spoolPath, pending); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(spoolPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("chat: open spool: %w", err)
	}
	local := &Local{
		handler:  handler,
		file:     file,
		queues:   map[string][]Message{},
		inflight: map[string]bool{},
		stop:     make(chan struct{}),
	}
	local.cond = sync.NewCond(&local.mu)
	for range localWorkers {
		local.wg.Add(1)
		go local.worker()
	}
	for _, m := range pending {
		local.enqueue(m)
	}
	return local, nil
}

// Publish durably appends the message and hands it to the dispatcher. This is
// the callback given to adapter ingress.
func (l *Local) Publish(m Message) error {
	if m.EventID == "" {
		return errors.New("chat: publish requires an EventID")
	}
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return errors.New("chat: local runner is closed")
	}
	l.mu.Unlock()
	if err := l.appendLine(spoolLine{M: &m}); err != nil {
		return err
	}
	l.enqueue(m)
	return nil
}

// Close stops accepting work and waits for in-flight handling until ctx ends.
func (l *Local) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	alreadyStopped := l.stopped
	l.stopped = true
	l.mu.Unlock()
	if !alreadyStopped {
		close(l.stop)
	}
	l.cond.Broadcast()
	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	l.fileMu.Lock()
	defer l.fileMu.Unlock()
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("chat: close spool: %w", err)
	}
	return nil
}

func (l *Local) appendLine(line spoolLine) error {
	encoded, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("chat: encode spool line: %w", err)
	}
	l.fileMu.Lock()
	defer l.fileMu.Unlock()
	if _, err := l.file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("chat: append spool: %w", err)
	}
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("chat: sync spool: %w", err)
	}
	return nil
}

func (l *Local) enqueue(m Message) {
	// /stop preempts: queued behind the per-key inflight gate it could only
	// ever run after the turn it is meant to abort, so it dispatches directly.
	if parseCommand(m.Text) == "/stop" {
		l.mu.Lock()
		if l.stopped {
			l.mu.Unlock()
			return
		}
		l.wg.Add(1)
		l.mu.Unlock()
		go func() {
			defer l.wg.Done()
			l.handleUntilAcked(m)
		}()
		return
	}
	key := m.Key().String()
	l.mu.Lock()
	l.queues[key] = append(l.queues[key], m)
	l.mu.Unlock()
	l.cond.Signal()
}

// next blocks until a key with pending work and no in-flight turn exists,
// claims it, and returns its head message. ok is false on shutdown.
func (l *Local) next() (key string, m Message, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		if l.stopped {
			return "", Message{}, false
		}
		for candidate, queue := range l.queues {
			if len(queue) > 0 && !l.inflight[candidate] {
				l.inflight[candidate] = true
				return candidate, queue[0], true
			}
		}
		l.cond.Wait()
	}
}

func (l *Local) worker() {
	defer l.wg.Done()
	for {
		key, m, ok := l.next()
		if !ok {
			return
		}
		l.process(key, m)
	}
}

// handleUntilAcked runs Handle and the ack append with capped backoff until
// the event is acked or shutdown begins, reporting whether the ack was
// written. Permanent rejections ([ErrRejected]) are acked and dropped — they
// can never succeed, and retrying them would pin a worker (and its key)
// forever. A failed ack append retries inside the same backoff loop instead
// of respinning the head message with no delay.
func (l *Local) handleUntilAcked(m Message) bool {
	backoff := 25 * time.Millisecond
	for {
		err := l.handler.Handle(context.Background(), m)
		if err == nil || errors.Is(err, ErrRejected) {
			if ackErr := l.appendLine(spoolLine{Ack: m.EventID}); ackErr == nil {
				return true
			}
		}
		select {
		case <-l.stop:
			return false
		case <-time.After(backoff):
			backoff = min(backoff*2, time.Second)
		}
	}
}

// process drives one claimed head message to its ack, then pops it and
// releases the key.
func (l *Local) process(key string, m Message) {
	acked := l.handleUntilAcked(m)
	l.mu.Lock()
	if acked {
		if queue := l.queues[key]; len(queue) > 0 {
			queue = queue[1:]
			if len(queue) == 0 {
				delete(l.queues, key)
			} else {
				l.queues[key] = queue
			}
		}
	}
	delete(l.inflight, key)
	l.mu.Unlock()
	l.cond.Signal()
}

// replaySpool reads the spool and returns unacked messages in publish order.
func replaySpool(path string) ([]Message, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chat: read spool: %w", err)
	}
	defer func() { _ = file.Close() }()
	acked := map[string]int{}
	var pending []Message
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var line spoolLine
		if err := json.Unmarshal(raw, &line); err != nil {
			continue // torn tail line after a crash: skip
		}
		if line.Ack != "" {
			acked[line.Ack]++
		}
		if line.M != nil {
			pending = append(pending, *line.M)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("chat: scan spool: %w", err)
	}
	unacked := pending[:0]
	for _, m := range pending {
		if acked[m.EventID] > 0 {
			acked[m.EventID]--
			continue
		}
		unacked = append(unacked, m)
	}
	return unacked, nil
}

// compactSpool rewrites the spool to contain only the pending messages.
func compactSpool(path string, pending []Message) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("chat: create spool dir: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".spool-*")
	if err != nil {
		return fmt.Errorf("chat: compact spool: %w", err)
	}
	tempPath := temp.Name()
	for _, m := range pending {
		encoded, err := json.Marshal(spoolLine{M: &m})
		if err != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
			return fmt.Errorf("chat: compact spool: %w", err)
		}
		if _, err := temp.Write(append(encoded, '\n')); err != nil {
			_ = temp.Close()
			_ = os.Remove(tempPath)
			return fmt.Errorf("chat: compact spool: %w", err)
		}
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("chat: compact spool: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("chat: compact spool: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("chat: compact spool: %w", err)
	}
	return nil
}
