package telegram

// fake_api_test.go provides the httptest fake Bot API used across the
// adapter tests: it records every call in order, serves sensible defaults
// per method, and lets tests stub error responses.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "42:TESTSECRET"

type apiCall struct {
	method string
	params map[string]any
}

type stubResponse struct {
	body string
}

type fakeAPI struct {
	t      *testing.T
	server *httptest.Server

	mu            sync.Mutex
	calls         []apiCall
	stubs         map[string][]stubResponse
	batches       [][]apiUpdate
	drained       chan struct{}
	drainedOnce   sync.Once
	nextMessageID int64
	filePaths     map[string]string // file_id → file_path
	files         map[string]string // file_path → content
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		t:             t,
		stubs:         map[string][]stubResponse{},
		drained:       make(chan struct{}),
		nextMessageID: 100,
		filePaths:     map[string]string{},
		files:         map[string]string{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

// newTestAdapter builds an adapter wired to the fake with fast test timings
// and a pre-seeded identity unless overridden.
func newTestAdapter(t *testing.T, f *fakeAPI, mutate ...func(*Options)) *Adapter {
	t.Helper()
	opts := Options{
		Token:              testToken,
		BaseURL:            f.server.URL,
		BotUsername:        "pigobot",
		PollTimeout:        time.Second,
		PreviewMinInterval: time.Nanosecond,
		TypingInterval:     time.Hour,
		MediaGroupDelay:    20 * time.Millisecond,
	}
	for _, m := range mutate {
		m(&opts)
	}
	adapter, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return adapter
}

// stub queues one raw response body for the next call to method.
func (f *fakeAPI) stub(method, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stubs[method] = append(f.stubs[method], stubResponse{body: body})
}

// pushBatch queues one getUpdates batch.
func (f *fakeAPI) pushBatch(updates ...apiUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, updates)
}

// callMethods returns the ordered method names of every recorded call.
func (f *fakeAPI) callMethods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	methods := make([]string, len(f.calls))
	for i, call := range f.calls {
		methods[i] = call.method
	}
	return methods
}

// callsTo returns the recorded calls to one method, in order.
func (f *fakeAPI) callsTo(method string) []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []apiCall
	for _, call := range f.calls {
		if call.method == method {
			out = append(out, call)
		}
	}
	return out
}

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if filePath, ok := strings.CutPrefix(path, "file/bot"+testToken+"/"); ok {
		content, ok := f.fileContent(filePath)
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(content))
		return
	}
	method, ok := strings.CutPrefix(path, "bot"+testToken+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	params := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&params)

	f.mu.Lock()
	f.calls = append(f.calls, apiCall{method: method, params: params})
	if queue := f.stubs[method]; len(queue) > 0 {
		stubbed := queue[0]
		f.stubs[method] = queue[1:]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubbed.body))
		return
	}
	f.mu.Unlock()

	switch method {
	case "getMe":
		writeResult(w, apiUser{ID: 42, IsBot: true, Username: "pigobot", FirstName: "pigo"})
	case "deleteWebhook", "sendChatAction":
		writeResult(w, true)
	case "sendMessage", "editMessageText":
		f.mu.Lock()
		id := f.nextMessageID
		f.nextMessageID++
		f.mu.Unlock()
		writeResult(w, apiMessage{MessageID: id})
	case "getUpdates":
		f.serveUpdates(w, r)
	case "getFile":
		fileID, _ := params["file_id"].(string)
		f.mu.Lock()
		filePath, ok := f.filePaths[fileID]
		f.mu.Unlock()
		if !ok {
			writeError(w, 400, "Bad Request: invalid file_id", 0)
			return
		}
		writeResult(w, apiFilePath{FilePath: filePath})
	default:
		writeError(w, 404, "Not Found: method not found", 0)
	}
}

// serveUpdates pops the next queued batch; once the queue is empty it signals
// drained and blocks until the request is canceled (a long poll with nothing
// to say).
func (f *fakeAPI) serveUpdates(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if len(f.batches) > 0 {
		batch := f.batches[0]
		f.batches = f.batches[1:]
		f.mu.Unlock()
		writeResult(w, batch)
		return
	}
	f.mu.Unlock()
	f.drainedOnce.Do(func() { close(f.drained) })
	<-r.Context().Done()
}

func (f *fakeAPI) fileContent(filePath string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	content, ok := f.files[filePath]
	return content, ok
}

func writeResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	payload, _ := json.Marshal(result)
	_, _ = fmt.Fprintf(w, `{"ok":true,"result":%s}`, payload)
}

func writeError(w http.ResponseWriter, code int, description string, retryAfter int) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, errorBody(code, description, retryAfter))
}

func errorBody(code int, description string, retryAfter int) string {
	if retryAfter > 0 {
		return fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%q,"parameters":{"retry_after":%d}}`, code, description, retryAfter)
	}
	return fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%q}`, code, description)
}
