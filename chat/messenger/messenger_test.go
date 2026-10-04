package messenger

import (
	"net/http"
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
