package whatsapp

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
		Token:         "test-token",
		PhoneNumberID: "106540352242922",
		AppSecret:     "app-secret",
		VerifyToken:   "verify-token",
	}
}

func TestNewRefusesMissingCredentials(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"token", func(o *Options) { o.Token = "" }},
		{"phone number id", func(o *Options) { o.PhoneNumberID = "" }},
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

func TestGraphVersionPinned(t *testing.T) {
	if GraphVersion != "v23.0" {
		t.Fatalf("GraphVersion = %q, want v23.0", GraphVersion)
	}
	adapter, err := New(validOptions())
	if err != nil {
		t.Fatal(err)
	}
	if got := adapter.messagesPath(); got != "/v23.0/106540352242922/messages" {
		t.Fatalf("messagesPath = %q", got)
	}
}

func TestFormatText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"bold", "this is **bold** text", "this is *bold* text"},
		{"bold underscores", "__bold__ start", "*bold* start"},
		{"strike", "old ~~gone~~ new", "old ~gone~ new"},
		{"heading", "## Results", "*Results*"},
		{"link", "see [the docs](https://example.com/a) now", "see the docs (https://example.com/a) now"},
		{
			"fence kept, language stripped, contents untouched",
			"before\n```go\nx := \"**not bold**\"\n```\nafter",
			"before\n```\nx := \"**not bold**\"\n```\nafter",
		},
		{"plain untouched", "just text with * loose asterisk", "just text with * loose asterisk"},
		{
			"inline triple-backtick span keeps content and fence state",
			"```ls -la``` lists files\nAnd **bold** stays",
			"`ls -la` lists files\nAnd *bold* stays",
		},
		{
			"one-line span between paragraphs does not open a fence",
			"before\n```echo hi```\nafter **b**",
			"before\n`echo hi`\nafter *b*",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatText(tc.in); got != tc.want {
				t.Fatalf("FormatText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestChunkText(t *testing.T) {
	t.Run("short text is one chunk", func(t *testing.T) {
		got := ChunkText("hello", 10)
		if len(got) != 1 || got[0] != "hello" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("empty text yields no chunks", func(t *testing.T) {
		if got := ChunkText("", 10); len(got) != 0 {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("prefers paragraph boundary", func(t *testing.T) {
		text := strings.Repeat("a", 6) + "\n\n" + strings.Repeat("b", 6)
		got := ChunkText(text, 10)
		want := []string{strings.Repeat("a", 6), strings.Repeat("b", 6)}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("falls back to space then hard cut", func(t *testing.T) {
		text := "word1 word2word2word2"
		got := ChunkText(text, 10)
		if len(got) != 3 || got[0] != "word1" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("every chunk is within the limit", func(t *testing.T) {
		text := strings.Repeat("lorem ipsum dolor sit amet\n", 700) // ~19k chars
		for _, chunk := range ChunkText(text, maxMessageLen) {
			if n := len([]rune(chunk)); n > maxMessageLen {
				t.Fatalf("chunk length %d exceeds %d", n, maxMessageLen)
			}
			if chunk == "" {
				t.Fatal("empty chunk")
			}
		}
	})
	t.Run("multibyte runes count as single characters", func(t *testing.T) {
		text := strings.Repeat("é", 15)
		got := ChunkText(text, 10)
		if len(got) != 2 || got[0] != strings.Repeat("é", 10) || got[1] != strings.Repeat("é", 5) {
			t.Fatalf("got %q", got)
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
// near-zero retry backoff.
func newTestAdapter(t *testing.T, baseURL string, onStatus func(Status)) *Adapter {
	t.Helper()
	opts := validOptions()
	opts.BaseURL = baseURL
	opts.OnStatus = onStatus
	adapter, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	adapter.backoff = func(int) time.Duration { return time.Millisecond }
	return adapter
}
