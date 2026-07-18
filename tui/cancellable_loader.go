package tui

import "context"

// CancellableLoader couples Loader with the standard select-cancel binding.
type CancellableLoader struct {
	*Loader
	ctx     context.Context
	cancel  context.CancelFunc
	OnAbort func()
}

func NewCancellableLoader(ui RenderRequester, spinnerColor, messageColor StyleFunc, message string, indicator *LoaderIndicatorOptions) *CancellableLoader {
	ctx, cancel := context.WithCancel(context.Background())
	return &CancellableLoader{
		Loader: NewLoader(ui, spinnerColor, messageColor, message, indicator),
		ctx:    ctx,
		cancel: cancel,
	}
}

func (loader *CancellableLoader) Context() context.Context { return loader.ctx }
func (loader *CancellableLoader) Aborted() bool            { return loader.ctx.Err() != nil }

func (loader *CancellableLoader) HandleInput(event KeyEvent) {
	if !GetKeybindings().Matches(event.Raw, "tui.select.cancel") {
		return
	}
	loader.cancel()
	if loader.OnAbort != nil {
		loader.OnAbort()
	}
}

func (loader *CancellableLoader) Dispose() { loader.Stop() }
