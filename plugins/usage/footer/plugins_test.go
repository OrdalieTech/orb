package footer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/usage"
)

func mustOK(err error) {
	if err != nil {
		panic(err)
	}
}

type quotaRegistry struct {
	extensions.ModelRegistry
	mu  sync.Mutex
	key string
}

func (registry *quotaRegistry) ResolveProviderAuth(context.Context, string, map[string]string) (*aiauth.AuthResult, error) {
	registry.mu.Lock()
	key := registry.key
	registry.mu.Unlock()
	return &aiauth.AuthResult{Auth: aiauth.ModelAuth{APIKey: &key}}, nil
}

func (*quotaRegistry) ProviderDisplayName(string) string { return "Go" }

type quotaUI struct {
	extensions.NoopUI
	statuses chan string
}

func (ui *quotaUI) SetStatus(_ string, text *string) {
	value := ""
	if text != nil {
		value = *text
	}
	ui.statuses <- value
}

func TestProviderUsageCancelsOldAccountRequestsAndStopsOnShutdown(t *testing.T) {
	started, stopped := make(chan struct{}, 2), make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer blocked" {
			started <- struct{}{}
			<-r.Context().Done()
			stopped <- struct{}{}
			return
		}
		_, _ = io.WriteString(w, `{"usage":{"rolling":{"percent":25,"resetsAt":"2030-01-01T00:00:00Z"}}}`)
	}))
	defer server.Close()
	registry := extensions.NewRegistry(t.TempDir())
	mustOK(registry.Register("quota", Extension(usage.Client{OpenCodeGoURL: server.URL}, nil)))
	credentials := &quotaRegistry{key: "blocked"}
	ui := &quotaUI{statuses: make(chan string, 20)}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: ui, ModelRegistry: credentials, ContextActions: extensions.ContextActions{GetModel: func() *ai.Model { return &ai.Model{Provider: "opencode-go"} }}})
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("quota lifecycle timed out")
		}
	}
	runner.Emit(t.Context(), extensions.SessionStartEvent{})
	wait(started)
	credentials.mu.Lock()
	credentials.key = "new-account"
	credentials.mu.Unlock()
	registry.Events().Emit(t.Context(), "orb.accounts.changed", nil)
	wait(stopped)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case value := <-ui.statuses:
			if strings.Contains(value, "75% left") {
				goto refreshed
			}
			if value != "Go" {
				t.Fatalf("stale or unexpected status: %q", value)
			}
		case <-deadline:
			t.Fatal("new account quota did not reach footer")
		}
	}
refreshed:
	credentials.mu.Lock()
	credentials.key = "blocked"
	credentials.mu.Unlock()
	registry.Events().Emit(t.Context(), "orb.accounts.changed", nil)
	wait(started)
	runner.Emit(t.Context(), extensions.SessionShutdownEvent{})
	wait(stopped)
}

type limitBook struct {
	mu   sync.Mutex
	used string
}

func (*limitBook) List(context.Context) ([]accounts.Account, error) {
	return []accounts.Account{
		{Provider: "opencode-go", ID: "work", Name: "Work", Active: true},
		{Provider: "opencode-go", ID: "spent", Name: "Spent"},
		{Provider: "opencode-go", ID: "low", Name: "Low"},
		{Provider: "opencode-go", ID: "full", Name: "Full"},
		{Provider: "other", ID: "elsewhere", Name: "Elsewhere"},
	}, nil
}

func (*limitBook) Usage(_ context.Context, _, id string) (usage.Snapshot, error) {
	left := map[string]float64{"spent": 0, "low": 20, "full": 90}[id]
	return usage.Snapshot{Windows: []usage.Window{{Name: "5h", Remaining: left}}}, nil
}

func (book *limitBook) Use(_ context.Context, _, id string) error {
	book.mu.Lock()
	book.used = id
	book.mu.Unlock()
	return nil
}

type pickUI struct {
	quotaUI
	offered chan []string
}

func (ui *pickUI) Select(_ context.Context, _ string, options []string, _ *extensions.DialogOptions) (string, bool, error) {
	ui.offered <- options
	return options[0], true, nil
}

func TestLimitOffersOtherAccountsMostQuotaFirstAndContinuesOnTheChosenOne(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	book := &limitBook{}
	mustOK(registry.Register("quota", Extension(usage.Client{}, book)))
	ui := &pickUI{quotaUI: quotaUI{statuses: make(chan string, 20)}, offered: make(chan []string, 1)}
	sent := make(chan *extensions.SendMessageOptions, 1)
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{
		Mode: extensions.ModeTUI, UI: ui, ModelRegistry: &quotaRegistry{},
		Actions: extensions.Actions{SendMessage: func(_ context.Context, _ extensions.CustomMessage, options *extensions.SendMessageOptions) error {
			sent <- options
			return nil
		}},
		ContextActions: extensions.ContextActions{GetModel: func() *ai.Model { return &ai.Model{Provider: "opencode-go"} }},
	})
	reason := "GoUsageLimitError: usage limit reached"
	runner.Emit(t.Context(), extensions.AgentEndEvent{Messages: engine.AgentMessages{&ai.AssistantMessage{StopReason: ai.StopReasonError, ErrorMessage: &reason}}})
	runner.Emit(t.Context(), extensions.AgentSettledEvent{})
	select {
	case options := <-ui.offered:
		if len(options) != 3 || !strings.Contains(options[0], "Full") || !strings.Contains(options[1], "Low") {
			t.Fatalf("offer should list unspent accounts of the provider, most quota first: %q", options)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no account offered after a usage limit")
	}
	select {
	case options := <-sent:
		if options == nil || options.TriggerTurn == nil || !*options.TriggerTurn {
			t.Fatal("the turn did not continue on the new account")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the turn did not continue on the new account")
	}
	book.mu.Lock()
	defer book.mu.Unlock()
	if book.used != "full" {
		t.Fatalf("switched to %q, want the chosen account", book.used)
	}
}
