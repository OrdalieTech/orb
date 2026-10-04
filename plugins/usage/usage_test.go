package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
)

func TestUsageDoesNotLeakErrorsOrFollowRedirects(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	key := "secret-must-not-appear"
	fetch := Client{CodexURL: server.URL}
	_, err := fetch.Fetch(t.Context(), "openai-codex", auth.ModelAuth{APIKey: &key})
	if err == nil || strings.Contains(err.Error(), key) || leaked {
		t.Fatalf("unsafe redirect: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if _, err := fetch.Fetch(ctx, "openai-codex", auth.ModelAuth{APIKey: &key}); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestCacheCoalescesAndDoesNotRestoreClearedResults(t *testing.T) {
	cache := &Cache{}
	started, release := make(chan struct{}), make(chan struct{})
	calls := atomic.Int32{}
	fetch := func(context.Context) (Snapshot, error) {
		calls.Add(1)
		close(started)
		<-release
		return Snapshot{Windows: []Window{{Name: "5h", Remaining: 50}}, CheckedAt: time.Now()}, nil
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = cache.Fetch(t.Context(), "account", fetch) }()
	<-started
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cache.Fetch(cancelled, "account", fetch); !errors.Is(err, context.Canceled) {
		t.Fatalf("joiner ignored cancellation: %v", err)
	}
	cache.Clear()
	close(release)
	<-done
	if _, ok := cache.Peek("account"); ok {
		t.Fatal("in-flight result restored a cleared account")
	}
	if calls.Load() != 1 {
		t.Fatal("duplicated an in-flight fetch")
	}
	for i := range 100 {
		_, err := cache.Fetch(t.Context(), strconv.Itoa(i), func(context.Context) (Snapshot, error) { return Snapshot{CheckedAt: time.Now()}, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) > 64 {
		t.Fatal("unbounded usage cache")
	}
}

func TestCanceledUsageFetchCanRetryImmediately(t *testing.T) {
	var cache Cache
	ctx, cancel := context.WithCancel(t.Context())
	_, _ = cache.Fetch(ctx, "account", func(context.Context) (Snapshot, error) { cancel(); return Snapshot{}, ctx.Err() })
	called := false
	_, err := cache.Fetch(t.Context(), "account", func(context.Context) (Snapshot, error) { called = true; return Snapshot{}, nil })
	if err != nil || !called {
		t.Fatal("cancellation cached as an account failure", err)
	}
}

func TestClientsShareQuotaCacheAndSeparateCredentials(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"usage":{"rolling":{"percent":25,"resetsAt":"2030-01-01T00:00:00Z"}}}`))
	}))
	defer server.Close()
	cache := &Cache{}
	footer := Client{Cache: cache, OpenCodeGoURL: server.URL}
	accounts := Client{Cache: cache, OpenCodeGoURL: server.URL}
	key := "first-account"
	credential := auth.ModelAuth{APIKey: &key}
	for _, client := range []Client{footer, accounts} {
		if _, err := client.Fetch(t.Context(), "opencode-go", credential); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("footer and account screen made %d requests", calls.Load())
	}
	key = "second-account"
	if _, err := footer.Fetch(t.Context(), "opencode-go", credential); err != nil {
		t.Fatal(err)
	}
	cache.Clear()
	if _, err := accounts.Fetch(t.Context(), "opencode-go", credential); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("account changes/invalidation made %d requests, want 3", calls.Load())
	}
}
