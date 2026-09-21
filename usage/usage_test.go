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

func TestProviderWindowsAndUnavailable(t *testing.T) {
	for _, test := range []struct {
		provider, body string
		want           int
	}{
		{"openai-codex", `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_at":1900000000},"secondary_window":{"used_percent":80,"limit_window_seconds":604800,"reset_at":1900100000}}}`, 2},
		{"opencode-go", `{"usage":{"rolling":{"percent":10,"resetsAt":"2030-01-01T12:00:00Z"},"weekly":{"percent":40,"resetsAt":"2030-01-03T12:00:00Z"},"monthly":{"percent":65,"resetsAt":"2030-02-01T00:00:00Z"}}}`, 3},
		{"openai-codex", `{"rate_limit":{"primary_window":null}}`, 0},
		{"opencode-go", `{"usage":{"rolling":{"resetsAt":"2030-01-01T12:00:00Z"}}}`, 0},
	} {
		t.Run(test.provider+test.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer dummy-key" {
					t.Error("missing authentication")
				}
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			key := "dummy-key"
			fetch := Client{CodexURL: server.URL, OpenCodeGoURL: server.URL}
			usage, err := fetch.Fetch(t.Context(), test.provider, auth.ModelAuth{APIKey: &key})
			if test.want == 0 {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("missing usage presented as real: %v", err)
				}
				return
			}
			if err != nil || len(usage.Windows) != test.want {
				t.Fatalf("usage=%v error=%v", usage, err)
			}
			for _, window := range usage.Windows {
				if window.Remaining < 0 || window.Remaining > 100 || window.ResetsAt.IsZero() {
					t.Fatalf("invalid window: %v", window)
				}
			}
		})
	}
}

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
