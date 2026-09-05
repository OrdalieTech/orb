package extensions

import "context"

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
