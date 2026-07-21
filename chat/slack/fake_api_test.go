package slack

// fake_api_test.go provides the httptest fake Slack Web API used across the
// adapter tests: it records every call in order, serves sensible defaults
// per method, and lets tests stub responses (including HTTP 429s with a
// Retry-After header).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

const (
	testToken   = "xoxb-test-token"
	testSecret  = "test-signing-secret"
	testBotUser = "U0BOT"
)

type apiCall struct {
	method string
	params map[string]any
}

// stubResponse is one scripted response: status 0 means 200.
type stubResponse struct {
	status     int
	retryAfter int
	body       string
}

type fakeAPI struct {
	t      *testing.T
	server *httptest.Server

	mu     sync.Mutex
	calls  []apiCall
	stubs  map[string][]stubResponse
	nextTS int
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, stubs: map[string][]stubResponse{}, nextTS: 100}
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
		SigningSecret:      testSecret,
		BaseURL:            f.server.URL,
		BotUserID:          testBotUser,
		PreviewMinInterval: time.Nanosecond,
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

// stub queues one scripted response for the next call to method.
func (f *fakeAPI) stub(method string, response stubResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stubs[method] = append(f.stubs[method], response)
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
	method, ok := strings.CutPrefix(r.URL.Path, "/api/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		f.t.Errorf("api call %s: Authorization = %q", method, got)
	}
	params := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&params)

	f.mu.Lock()
	f.calls = append(f.calls, apiCall{method: method, params: params})
	if queue := f.stubs[method]; len(queue) > 0 {
		stubbed := queue[0]
		f.stubs[method] = queue[1:]
		f.mu.Unlock()
		if stubbed.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(stubbed.retryAfter))
		}
		if stubbed.status != 0 {
			w.WriteHeader(stubbed.status)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubbed.body))
		return
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "auth.test":
		_, _ = fmt.Fprintf(w, `{"ok":true,"user_id":%q,"bot_id":"B0BOT","team":"pigo"}`, testBotUser)
	case "chat.postMessage":
		f.mu.Lock()
		ts := fmt.Sprintf("1700000000.%06d", f.nextTS)
		f.nextTS++
		f.mu.Unlock()
		channel, _ := params["channel"].(string)
		_, _ = fmt.Fprintf(w, `{"ok":true,"channel":%q,"ts":%q}`, channel, ts)
	case "chat.update":
		ts, _ := params["ts"].(string)
		_, _ = fmt.Fprintf(w, `{"ok":true,"ts":%q}`, ts)
	default:
		_, _ = fmt.Fprint(w, `{"ok":false,"error":"unknown_method"}`)
	}
}

// sign computes the v0 signature headers for body at the given timestamp.
func sign(body []byte, timestamp string) (string, string) {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	return timestamp, "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// postEvent sends body to the handler with a valid signature stamped at the
// adapter's current clock and returns the recorder.
func postEvent(t *testing.T, adapter *Adapter, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(body))
	ts, sig := sign([]byte(body), strconv.FormatInt(adapter.now().Unix(), 10))
	request.Header.Set(timestampHeader, ts)
	request.Header.Set(signatureHeader, sig)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// messageEvent builds an event_callback body for a message-family event.
// overrides patches the inner event object.
func messageEvent(t *testing.T, eventType string, overrides map[string]any) string {
	t.Helper()
	event := map[string]any{
		"type":    eventType,
		"channel": "C0CHAN",
		"user":    "U0USER",
		"text":    "hello",
		"ts":      "1700000000.000100",
	}
	for key, value := range overrides {
		if value == nil {
			delete(event, key)
			continue
		}
		event[key] = value
	}
	envelope := map[string]any{
		"type":     "event_callback",
		"team_id":  "T0TEAM",
		"event_id": "Ev0000000001",
		"event":    event,
		"authorizations": []map[string]any{
			{"user_id": testBotUser, "is_bot": true},
		},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return string(body)
}

// capturePublish returns a publish func recording every message, plus the
// captured slice pointer.
func capturePublish() (func(chat.Message) error, *[]chat.Message) {
	published := &[]chat.Message{}
	return func(m chat.Message) error {
		*published = append(*published, m)
		return nil
	}, published
}
