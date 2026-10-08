package footer

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/plugins/usage"
)

// Extension shows the session's provider in the footer with its tightest plan limit. Providers
// the client reads are polled once a minute and after each turn; any other provider publishes
// its readings on usage.Event. When a turn ends on a limit and the host supplies its accounts,
// it offers the provider's other accounts, most quota first, and continues on the one chosen.
// It does no work until an interactive session starts.
func Extension(client usage.Client, book usage.Accounts) extensions.Factory {
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
				text += fmt.Sprintf(" %s %.0f%% left", window.Name, window.Remaining)
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
		var limited bool
		api.On(extensions.EventAgentEnd, func(_ context.Context, event extensions.Event, _ extensions.Context) (any, error) {
			poke()
			end, _ := event.(extensions.AgentEndEvent)
			mu.Lock()
			limited = len(end.Messages) > 0 && hitLimit(end.Messages[len(end.Messages)-1])
			mu.Unlock()
			return nil, nil
		})
		// Settled, not ended: the session retries a passing rate limit before giving up on it.
		api.On(extensions.EventAgentSettled, func(_ context.Context, _ extensions.Event, settled extensions.Context) (any, error) {
			mu.Lock()
			hit := limited
			limited = false
			mu.Unlock()
			if hit && book != nil && settled.Mode() == extensions.ModeTUI && settled.HasUI() && settled.Model() != nil {
				go offer(api, book, settled, string(settled.Model().Provider))
			}
			return nil, nil
		})
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

var limitPattern = regexp.MustCompile(`(?i)usage.?limit|rate.?limit|limit reached|hit your limit|too many requests|\b429\b|quota|ResourceExhausted`)

// hitLimit reports whether a turn failed on an account limit rather than on the request or network.
func hitLimit(message any) bool {
	assistant, ok := message.(*ai.AssistantMessage)
	return ok && assistant.StopReason == ai.StopReasonError && assistant.ErrorMessage != nil && limitPattern.MatchString(*assistant.ErrorMessage)
}

// offer lets the user pick another account of the provider, most quota left first, then switches to
// it and continues the turn there. Accounts known to be spent are left out; nothing is asked when
// the provider has no other account.
func offer(api extensions.API, book usage.Accounts, session extensions.Context, provider string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rows, err := book.List(ctx)
	if err != nil {
		return
	}
	type candidate struct {
		account accounts.Account
		left    float64
		label   string
	}
	current, candidates := "", []candidate{}
	for _, row := range rows {
		switch {
		case row.Provider != provider || row.ID == "runtime":
		case row.Active:
			current = row.Name
		default:
			option := candidate{account: row, left: -1, label: row.Name + " · quota unknown"}
			if snapshot, err := book.Usage(ctx, row.Provider, row.ID); err == nil {
				if window, ok := tightest(snapshot); ok {
					if window.Remaining < 1 {
						continue
					}
					option.left, option.label = window.Remaining, fmt.Sprintf("%s · %s %.0f%% left", row.Name, window.Name, window.Remaining)
				}
			}
			candidates = append(candidates, option)
		}
	}
	if len(candidates) == 0 {
		return
	}
	slices.SortStableFunc(candidates, func(a, b candidate) int { return cmp.Compare(b.left, a.left) })
	options := make([]string, 0, len(candidates)+1)
	for _, option := range candidates {
		options = append(options, "Continue on "+option.label)
	}
	options = append(options, "Not now")
	choice, ok, err := session.UI().Select(ctx, current+" hit its limit", options, nil)
	index := slices.Index(options, choice)
	if err != nil || !ok || index < 0 || index == len(candidates) {
		return
	}
	chosen := candidates[index]
	if err := book.Use(ctx, provider, chosen.account.ID); err != nil {
		session.UI().Notify(err.Error(), extensions.NotifyError)
		return
	}
	trigger := true
	text := fmt.Sprintf("%s hit its usage limit, so Orb switched to the account %s. Continue where you left off.", current, chosen.account.Name)
	// The continued turn runs under this call, so the offer's deadline must not bound it.
	if err := api.SendMessage(context.WithoutCancel(ctx), extensions.CustomMessage{CustomType: "provider-usage", Content: text, Display: true}, &extensions.SendMessageOptions{TriggerTurn: &trigger}); err != nil {
		session.UI().Notify(err.Error(), extensions.NotifyError)
	}
}
