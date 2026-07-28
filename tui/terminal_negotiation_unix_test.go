//go:build unix

package tui

import (
	"testing"
	"time"
)

// Same race as TestStdinBufferStaleTimerCannotFlushFreshSequence, for the
// negotiation fragment timer: Timer.Stop cannot cancel a fired AfterFunc
// parked on terminal.mu, so a stale callback surviving a re-arm must not
// dispatch the fresh fragment before its full completion window elapses.
func TestNegotiationStaleTimerCannotDispatchFreshFragment(t *testing.T) {
	dispatched := make(chan time.Time, 2)
	terminal := NewProcessTerminalFiles(nil, nil)
	terminal.started = true
	terminal.inputHandler = func(string) { dispatched <- time.Now() }

	terminal.mu.Lock()
	terminal.setNegotiationBufferLocked("\x1b[?")
	// Let the first fragment timer fire and park on mu, then re-arm while it
	// is still waiting for the lock.
	time.Sleep(keyboardProtocolFragmentTimeout + 50*time.Millisecond)
	terminal.setNegotiationBufferLocked("\x1b[?1")
	armed := time.Now()
	terminal.mu.Unlock()

	select {
	case at := <-dispatched:
		if elapsed := at.Sub(armed); elapsed < keyboardProtocolFragmentTimeout {
			t.Fatalf("fragment dispatched %v after re-arm, want the full %v window", elapsed, keyboardProtocolFragmentTimeout)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fresh fragment never dispatched")
	}
	select {
	case <-dispatched:
		t.Fatal("fragment dispatched twice")
	case <-time.After(100 * time.Millisecond):
	}
}
