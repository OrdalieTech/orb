package plugins

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/usage"
)

// ProviderUsage attaches a bounded quota reader through the existing footer
// status API. It does no work until an interactive session starts.
func ProviderUsage(client usage.Client) extensions.Factory {
	return func(api extensions.API) error {
		client := client
		if client.Cache == nil {
			client.Cache = &usage.Cache{}
		}
		var mu sync.Mutex
		var stop context.CancelFunc
		var requestCancel context.CancelFunc
		var done chan struct{}
		var ui extensions.UI
		var generation uint64
		wake := make(chan struct{}, 1)
		invalidate := func() {
			mu.Lock()
			generation++
			if requestCancel != nil {
				requestCancel()
			}
			if ui != nil {
				ui.SetStatus("provider-usage", nil)
			}
			mu.Unlock()
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		shutdown := func() {
			mu.Lock()
			cancel, finished := stop, done
			stop, done = nil, nil
			mu.Unlock()
			if cancel != nil {
				cancel()
				<-finished
			}
		}
		api.Events().On("orb.accounts.changed", func(context.Context, any) error { invalidate(); return nil })
		api.On(extensions.EventModelSelect, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			invalidate()
			return nil, nil
		})
		api.On(extensions.EventSessionShutdown, func(context.Context, extensions.Event, extensions.Context) (any, error) { shutdown(); return nil, nil })
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, session extensions.Context) (any, error) {
			shutdown()
			if session.Mode() != extensions.ModeTUI || !session.HasUI() {
				return nil, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			finished := make(chan struct{})
			mu.Lock()
			stop, done, ui = cancel, finished, session.UI()
			mu.Unlock()
			go func() {
				defer close(finished)
				ticker := time.NewTicker(time.Minute)
				defer ticker.Stop()
				refresh := func() {
					mu.Lock()
					version := generation
					mu.Unlock()
					model := session.Model()
					if model == nil {
						return
					}
					provider := string(model.Provider)
					label := ""
					switch provider {
					case "openai-codex":
						label = "Codex"
					case "opencode-go":
						label = "Go"
					default:
						return
					}
					requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()
					mu.Lock()
					requestCancel = cancel
					mu.Unlock()
					resolved, err := session.ModelRegistry().ResolveProviderAuth(requestCtx, provider, nil)
					text := label + " usage unavailable"
					if err == nil && resolved != nil && resolved.Auth.APIKey != nil {
						usage, fetchErr := client.Fetch(requestCtx, provider, resolved.Auth)
						if fetchErr == nil {
							remaining := 100.0
							for _, window := range usage.Windows {
								remaining = min(remaining, window.Remaining)
							}
							text = fmt.Sprintf("%s %.0f%% left", label, remaining)
						}
					}
					mu.Lock()
					if ctx.Err() == nil && generation == version {
						session.UI().SetStatus("provider-usage", &text)
					}
					mu.Unlock()
				}
				refresh()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						refresh()
					case <-wake:
						refresh()
					}
				}
			}()
			return nil, nil
		})
		return nil
	}
}
