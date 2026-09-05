package extensions

import (
	"context"
	"testing"
	"time"
)

type blockingPromptUI struct {
	NoopUI
	entered    chan string
	selectDone chan struct{}
	inputDone  chan struct{}
}

func (ui *blockingPromptUI) Select(context.Context, string, []string, *DialogOptions) (string, bool, error) {
	ui.entered <- "select"
	<-ui.selectDone
	return "", false, nil
}
func (ui *blockingPromptUI) Input(context.Context, string, *string, *DialogOptions) (string, bool, error) {
	ui.entered <- "input"
	<-ui.inputDone
	return "", false, nil
}
func TestUIPromptOverlapRetainsOutermostScope(t *testing.T) {
	registry := NewRegistry(t.TempDir())
	events := make(chan Event, 4)
	if err := registry.Register("observer", func(api API) error {
		for _, kind := range []EventType{EventUIPromptStart, EventUIPromptEnd} {
			api.On(kind, func(_ context.Context, event Event, _ Context) (any, error) { events <- event; return nil, nil })
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ui := &blockingPromptUI{entered: make(chan string, 2), selectDone: make(chan struct{}), inputDone: make(chan struct{})}
	runner := NewRunner(registry, RunnerOptions{UI: ui, Mode: ModeRPC})
	done := make(chan struct{}, 2)
	go func() { _, _, _ = runner.UI().Select(context.Background(), "outer", nil, nil); done <- struct{}{} }()
	<-ui.entered
	select {
	case event := <-events:
		if event.Type() != EventUIPromptStart {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt start missing")
	}
	go func() { _, _, _ = runner.UI().Input(context.Background(), "inner", nil, nil); done <- struct{}{} }()
	<-ui.entered
	close(ui.selectDone)
	<-done
	select {
	case event := <-events:
		t.Fatalf("overlapping prompt emitted premature event %#v", event)
	default:
	}
	close(ui.inputDone)
	<-done
	select {
	case event := <-events:
		end, ok := event.(UIPromptEndEvent)
		if !ok || end.Kind != UIPromptSelect || end.Title == nil || *end.Title != "outer" {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt end missing")
	}
}
