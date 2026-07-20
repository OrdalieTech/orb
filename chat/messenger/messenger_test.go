package messenger

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// validOptions returns a complete Options for tests to mutate.
func validOptions() Options {
	return Options{
		Token:       "test-token",
		PageID:      "1906385232743851",
		AppSecret:   "app-secret",
		VerifyToken: "verify-token",
	}
}

func TestNewRefusesMissingCredentials(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"token", func(o *Options) { o.Token = "" }},
		{"page id", func(o *Options) { o.PageID = "" }},
		{"app secret", func(o *Options) { o.AppSecret = "" }},
		{"verify token", func(o *Options) { o.VerifyToken = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := validOptions()
			tc.mutate(&opts)
			if _, err := New(opts); err == nil {
				t.Fatalf("New accepted options with missing %s", tc.name)
			}
		})
	}
	if _, err := New(validOptions()); err != nil {
		t.Fatalf("New rejected complete options: %v", err)
	}
}

func TestIdentity(t *testing.T) {
	adapter, err := New(validOptions())
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Platform() != "messenger" {
		t.Fatalf("Platform = %q", adapter.Platform())
	}
	if adapter.Account() != "1906385232743851" {
		t.Fatalf("Account = %q, want the page id", adapter.Account())
	}
	if GraphVersion != "v23.0" {
		t.Fatalf("GraphVersion = %q, want v23.0", GraphVersion)
	}
	if got := adapter.sendPath(); got != "/v23.0/me/messages" {
		t.Fatalf("sendPath = %q", got)
	}
}

func TestChunkText(t *testing.T) {
	t.Run("short text is one chunk", func(t *testing.T) {
		got := chunkText("hello", 10)
		if len(got) != 1 || got[0] != "hello" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("empty text yields no chunks", func(t *testing.T) {
		if got := chunkText("", 10); len(got) != 0 {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("prefers paragraph boundary", func(t *testing.T) {
		text := strings.Repeat("a", 6) + "\n\n" + strings.Repeat("b", 6)
		got := chunkText(text, 10)
		want := []string{strings.Repeat("a", 6), strings.Repeat("b", 6)}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("falls back to space then hard cut", func(t *testing.T) {
		got := chunkText("word1 word2word2word2", 10)
		if len(got) != 3 || got[0] != "word1" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("every chunk is within the send limit", func(t *testing.T) {
		text := strings.Repeat("all work and no play makes jack a dull boy\n", 300) // ~13k chars
		for _, chunk := range chunkText(text, chunkLimit) {
			if n := len([]rune(chunk)); n > chunkLimit {
				t.Fatalf("chunk length %d exceeds %d", n, chunkLimit)
			}
			if chunk == "" {
				t.Fatal("empty chunk")
			}
		}
	})
	t.Run("multibyte runes count as single characters", func(t *testing.T) {
		text := strings.Repeat("é", 15)
		got := chunkText(text, 10)
		if len(got) != 2 || got[0] != strings.Repeat("é", 10) || got[1] != strings.Repeat("é", 5) {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("chunk limit stays under the platform cap", func(t *testing.T) {
		if chunkLimit >= maxMessageLen {
			t.Fatalf("chunkLimit %d must stay under the %d-character cap", chunkLimit, maxMessageLen)
		}
	})
}

func TestRegainAfterParsing(t *testing.T) {
	t.Run("takes the max across entries", func(t *testing.T) {
		header := `{"123":[{"type":"messenger","call_count":100,"estimated_time_to_regain_access":1},{"type":"messenger","call_count":100,"estimated_time_to_regain_access":5}]}`
		if got := regainAfter(header); got != 5*time.Minute {
			t.Fatalf("regainAfter = %v, want 5m", got)
		}
	})
	t.Run("absent hint is zero", func(t *testing.T) {
		if got := regainAfter(`{"123":[{"type":"messenger","call_count":10}]}`); got != 0 {
			t.Fatalf("regainAfter = %v, want 0", got)
		}
	})
	t.Run("empty and malformed headers are zero", func(t *testing.T) {
		if got := regainAfter(""); got != 0 {
			t.Fatalf("regainAfter(empty) = %v", got)
		}
		if got := regainAfter("not json"); got != 0 {
			t.Fatalf("regainAfter(garbage) = %v", got)
		}
	})
}

// capturedRequest records one request seen by the fake Graph server.
type capturedRequest struct {
	Method string
	Path   string
	Body   []byte
	Header http.Header
}

// fakeGraph is a concurrency-safe request recorder for httptest handlers.
type fakeGraph struct {
	mu       sync.Mutex
	requests []capturedRequest
}

func (f *fakeGraph) record(r capturedRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
}

func (f *fakeGraph) recorded() []capturedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capturedRequest(nil), f.requests...)
}

// newTestAdapter builds an adapter against a fake Graph base URL with a
// near-zero retry backoff and a fast typing refresh.
func newTestAdapter(t *testing.T, baseURL string, onWatermark func(Watermark)) *Adapter {
	t.Helper()
	opts := validOptions()
	opts.BaseURL = baseURL
	opts.OnWatermark = onWatermark
	opts.TypingInterval = 10 * time.Millisecond
	adapter, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	adapter.backoff = func(int) time.Duration { return time.Millisecond }
	return adapter
}
