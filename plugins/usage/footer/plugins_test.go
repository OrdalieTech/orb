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
	mustOK(registry.Register("quota", Extension(usage.Client{OpenCodeGoURL: server.URL})))
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
			if strings.Contains(value, "75%") {
				goto refreshed
			}
			if value != "" {
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
