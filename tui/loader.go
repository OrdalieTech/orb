package tui

import (
	"sync"
	"time"
)

var defaultLoaderFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type LoaderIndicatorOptions struct {
	Frames   []string
	Interval time.Duration
}

// Loader is an optional animated indicator followed by a message.
type Loader struct {
	*Text
	mu             sync.Mutex
	ui             RenderRequester
	spinnerColor   StyleFunc
	messageColor   StyleFunc
	message        string
	frames         []string
	interval       time.Duration
	current        int
	stop           chan struct{}
	renderVerbatim bool
}

func NewLoader(ui RenderRequester, spinnerColor, messageColor StyleFunc, message string, indicator *LoaderIndicatorOptions) *Loader {
	if spinnerColor == nil {
		spinnerColor = func(value string) string { return value }
	}
	if messageColor == nil {
		messageColor = func(value string) string { return value }
	}
	if message == "" {
		message = "Loading..."
	}
	loader := &Loader{Text: NewText("", 1, 0, nil), ui: ui, spinnerColor: spinnerColor, messageColor: messageColor, message: message}
	loader.SetIndicator(indicator)
	return loader
}

func (loader *Loader) Render(width int) []string {
	return append([]string{""}, loader.Text.Render(width)...)
}

func (loader *Loader) Start() {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	loader.updateDisplayLocked()
	loader.restartLocked()
}

func (loader *Loader) Stop() {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	loader.stopLocked()
}

func (loader *Loader) SetMessage(message string) {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	loader.message = message
	loader.updateDisplayLocked()
}

func (loader *Loader) SetIndicator(indicator *LoaderIndicatorOptions) {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	loader.renderVerbatim = indicator != nil
	loader.frames = append(loader.frames[:0], defaultLoaderFrames...)
	loader.interval = 80 * time.Millisecond
	if indicator != nil {
		if indicator.Frames != nil {
			loader.frames = append(loader.frames[:0], indicator.Frames...)
		}
		if indicator.Interval > 0 {
			loader.interval = indicator.Interval
		}
	}
	loader.current = 0
	loader.updateDisplayLocked()
	loader.restartLocked()
}

func (loader *Loader) restartLocked() {
	loader.stopLocked()
	if len(loader.frames) <= 1 {
		return
	}
	stop := make(chan struct{})
	loader.stop = stop
	interval := loader.interval
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				loader.mu.Lock()
				if loader.stop != stop {
					loader.mu.Unlock()
					return
				}
				loader.current = (loader.current + 1) % len(loader.frames)
				loader.updateDisplayLocked()
				loader.mu.Unlock()
			case <-stop:
				return
			}
		}
	}()
}

func (loader *Loader) stopLocked() {
	if loader.stop != nil {
		close(loader.stop)
		loader.stop = nil
	}
}

func (loader *Loader) updateDisplayLocked() {
	frame := ""
	if len(loader.frames) > 0 {
		frame = loader.frames[loader.current]
	}
	rendered := frame
	if !loader.renderVerbatim {
		rendered = loader.spinnerColor(frame)
	}
	if frame != "" {
		rendered += " "
	}
	loader.SetText(rendered + loader.messageColor(loader.message))
	if loader.ui != nil {
		loader.ui.RequestRender()
	}
}
