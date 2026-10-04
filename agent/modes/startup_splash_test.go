package modes

import (
	"context"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/internal/orbalogo"
	"github.com/OrdalieTech/orb/tui"
)

func emptyChatFixture(bodyHeight int) *emptyChatState {
	empty := &emptyChatState{bodyHeight: func() int { return bodyHeight }, left: 3}
	empty.frame.Store(orbalogo.FrameCount - 1)
	return empty
}

func TestLogoUnfoldCancellationDoesNotWaitForDecoration(t *testing.T) {
	mode := &InteractiveMode{ui: tui.NewTUI(newLifecycleTerminal(80, 24))}
	mode.emptyState = emptyChatFixture(19)
	first := int32(0)
	mode.emptyState.frame.Store(first)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		mode.playLogoUnfold(ctx, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled logo unfold blocked shutdown")
	}
	if got := mode.emptyState.frame.Load(); got != first {
		t.Fatalf("cancelled unfold advanced to frame %d", got)
	}
}
