package footer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/plugins/usage"
)

// Extension shows the session's provider in the footer with its tightest plan limit. Providers
// the client reads are polled once a minute and after each turn; any other provider publishes
// its readings on usage.Event. It does no work until an interactive session starts.
func Extension(client usage.Client) extensions.Factory {
	return func(api extensions.API) error {
		client := client
		if client.Cache == nil {
			client.Cache = &usage.Cache{}
		}
		var mu sync.Mutex
		var session extensions.Context
		var stop, requestCancel context.CancelFunc
		var done chan struct{}
		var generation uint64
		readings := map[string]usage.Snapshot{}
		wake := make(chan struct{}, 1)
		poke := func() {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		// show holds mu.
		show := func() {
			if session == nil {
				return
			}
			model := session.Model()
			if model == nil {
				session.UI().SetStatus("provider-usage", nil)
				return
			}
			provider := string(model.Provider)
			text, _, _ := strings.Cut(session.ModelRegistry().ProviderDisplayName(provider), " (")
			if window, ok := tightest(readings[provider]); ok {
				text += fmt.Sprintf(" %s %.0f%%", window.Name, window.Remaining)
			}
			session.UI().SetStatus("provider-usage", &text)
		}
		shutdown := func() {
			mu.Lock()
			cancel, finished := stop, done
			stop, done, session = nil, nil, nil
			mu.Unlock()
			if cancel != nil {
				cancel()
				<-finished
			}
		}
		api.Events().On("orb.accounts.changed", func(context.Context, any) error {
			mu.Lock()
			generation++
			if requestCancel != nil {
				requestCancel()
			}
			clear(readings)
			show()
			mu.Unlock()
			poke()
			return nil
		})
		api.Events().On(usage.Event, func(_ context.Context, data any) error {
			if reading, ok := data.(usage.Reading); ok {
				mu.Lock()
				readings[reading.Provider] = reading.Snapshot
				show()
				mu.Unlock()
			}
			return nil
		})
		api.On(extensions.EventModelSelect, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			mu.Lock()
			show()
			mu.Unlock()
			poke()
			return nil, nil
		})
		api.On(extensions.EventAgentEnd, func(context.Context, extensions.Event, extensions.Context) (any, error) { poke(); return nil, nil })
		api.On(extensions.EventSessionShutdown, func(context.Context, extensions.Event, extensions.Context) (any, error) { shutdown(); return nil, nil })
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, started extensions.Context) (any, error) {
			shutdown()
			if started.Mode() != extensions.ModeTUI || !started.HasUI() {
				return nil, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			finished := make(chan struct{})
			mu.Lock()
			stop, done, session = cancel, finished, started
			show()
			mu.Unlock()
			refresh := func() {
				mu.Lock()
				version := generation
				mu.Unlock()
				model := started.Model()
				if model == nil || !client.Reads(string(model.Provider)) {
					mu.Lock()
					show() // drops windows that have reset
					mu.Unlock()
					return
				}
				provider := string(model.Provider)
				requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				mu.Lock()
				requestCancel = cancel
				mu.Unlock()
				resolved, err := started.ModelRegistry().ResolveProviderAuth(requestCtx, provider, nil)
				var snapshot usage.Snapshot
				if err == nil && resolved != nil {
					snapshot, err = client.Fetch(requestCtx, provider, resolved.Auth)
				}
				mu.Lock()
				defer mu.Unlock()
				if ctx.Err() != nil || generation != version {
					return
				}
				if err == nil && resolved != nil {
					readings[provider] = snapshot
				}
				show()
			}
			go func() {
				defer close(finished)
				ticker := time.NewTicker(time.Minute)
				defer ticker.Stop()
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

// tightest is the window closest to its limit among those not yet reset.
func tightest(snapshot usage.Snapshot) (usage.Window, bool) {
	var result usage.Window
	found := false
	for _, window := range snapshot.Windows {
		if (window.ResetsAt.IsZero() || window.ResetsAt.After(time.Now())) && (!found || window.Remaining < result.Remaining) {
			result, found = window, true
		}
	}
	return result, found
}
