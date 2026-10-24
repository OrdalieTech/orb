package extensions

import (
	"context"
	"maps"
	"slices"
)

type promptUI struct {
	UI
	runner *Runner
}

func (ui *promptUI) Select(ctx context.Context, title string, options []string, opts *DialogOptions) (string, bool, error) {
	finish := ui.runner.beginUIPrompt(UIPromptSelect, title)
	defer finish()
	return ui.UI.Select(ctx, title, options, opts)
}
func (ui *promptUI) Confirm(ctx context.Context, title, message string, opts *DialogOptions) (bool, error) {
	finish := ui.runner.beginUIPrompt(UIPromptConfirm, title)
	defer finish()
	return ui.UI.Confirm(ctx, title, message, opts)
}
func (ui *promptUI) Input(ctx context.Context, title string, placeholder *string, opts *DialogOptions) (string, bool, error) {
	finish := ui.runner.beginUIPrompt(UIPromptInput, title)
	defer finish()
	return ui.UI.Input(ctx, title, placeholder, opts)
}
func (ui *promptUI) Editor(ctx context.Context, title string, prefill *string) (string, bool, error) {
	finish := ui.runner.beginUIPrompt(UIPromptEditor, title)
	defer finish()
	return ui.UI.Editor(ctx, title, prefill)
}
func (ui *promptUI) Custom(ctx context.Context, factory CustomFactory, options *CustomOptions) (any, bool, error) {
	finish := ui.runner.beginUIPrompt(UIPromptCustom, "")
	defer finish()
	return ui.UI.Custom(ctx, factory, options)
}

func (runner *Runner) beginUIPrompt(kind UIPromptKind, title string) func() {
	runner.mu.Lock()
	if runner.uiPromptDepth == 0 {
		prompt := UIPromptStartEvent{Reason: "ui_prompt", Kind: kind}
		if title != "" {
			prompt.Title = &title
		}
		runner.activeUIPrompt = prompt
		runner.queueUIPromptEvent(prompt)
	}
	runner.uiPromptDepth++
	runner.mu.Unlock()
	return func() {
		runner.mu.Lock()
		runner.uiPromptDepth--
		if runner.uiPromptDepth == 0 {
			runner.queueUIPromptEvent(UIPromptEndEvent(runner.activeUIPrompt))
			runner.activeUIPrompt = UIPromptStartEvent{}
		}
		runner.mu.Unlock()
	}
}

// UI calls must reach their driver without waiting for extension observers.
func (runner *Runner) queueUIPromptEvent(event Event) {
	previous := runner.uiPromptTail
	done := make(chan struct{})
	runner.uiPromptTail = done
	go func() {
		defer close(done)
		if previous != nil {
			<-previous
		}
		runner.Emit(context.Background(), event)
	}()
}

// Slot is what extensions show beside a conversation rather than in it: a status line, a widget's
// lines, or the last notice until the next run starts. Apps following a session on Bridge draw them.
type Slot struct {
	Key   string   `json:"key"`
	Lines []string `json:"lines"`
	Alarm bool     `json:"alarm,omitempty"`
}

func (ui *promptUI) SetStatus(key string, text *string) {
	var lines []string
	if text != nil {
		lines = []string{*text}
	}
	ui.runner.slot(Slot{Key: "status:" + key, Lines: lines})
	ui.UI.SetStatus(key, text)
}

func (ui *promptUI) SetWidget(key string, widget *Widget, options *WidgetOptions) {
	var lines []string
	if widget != nil {
		lines = widget.Lines
	}
	ui.runner.slot(Slot{Key: "widget:" + key, Lines: lines})
	ui.UI.SetWidget(key, widget, options)
}

func (ui *promptUI) Notify(message string, kind NotificationType) {
	ui.runner.slot(Slot{Key: "notice", Lines: []string{message}, Alarm: kind == NotifyError})
	ui.UI.Notify(message, kind)
}

// slot keeps one slot, or drops it once it holds nothing.
func (runner *Runner) slot(s Slot) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(s.Lines) == 0 {
		delete(runner.slots, s.Key)
		return
	}
	if runner.slots == nil {
		runner.slots = map[string]Slot{}
	}
	s.Lines = slices.Clone(s.Lines)
	runner.slots[s.Key] = s
}

// Slots are the extensions' slots, in key order.
func (runner *Runner) Slots() []Slot {
	runner.mu.RLock()
	defer runner.mu.RUnlock()
	keys := slices.Sorted(maps.Keys(runner.slots))
	out := make([]Slot, len(keys))
	for i, k := range keys {
		out[i] = runner.slots[k]
	}
	return out
}
