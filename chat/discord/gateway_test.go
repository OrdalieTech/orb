package discord

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/chat"
	"github.com/OrdalieTech/pigo/chat/internal/wsclient"
)

// --- fake gateway plumbing (scripted server over a hijacked connection) ---

// gwAccept recomputes Sec-WebSocket-Accept server-side.
func gwAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// fakeConn is one scripted server-side gateway connection.
type fakeConn struct {
	t  *testing.T
	c  net.Conn
	br *bufio.Reader
}

// clientPayload is a decoded client gateway frame.
type clientPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
}

// upgradeGateway hijacks the request and completes the WebSocket handshake.
func upgradeGateway(t *testing.T, w http.ResponseWriter, r *http.Request) *fakeConn {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Error("response writer is not a Hijacker")
		panic(http.ErrAbortHandler)
	}
	c, rw, err := hj.Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		panic(http.ErrAbortHandler)
	}
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set gateway deadline: %v", err)
	}
	_, _ = fmt.Fprintf(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		gwAccept(r.Header.Get("Sec-WebSocket-Key")))
	return &fakeConn{t: t, c: c, br: rw.Reader}
}

// send writes one unmasked server text frame with the given gateway fields.
// s < 0 omits the sequence.
func (f *fakeConn) send(op int, s int64, typ string, d any) {
	payload := map[string]any{"op": op, "d": d}
	if s >= 0 {
		payload["s"] = s
	}
	if typ != "" {
		payload["t"] = typ
	}
	data, err := json.Marshal(payload)
	if err != nil {
		f.t.Errorf("marshal server payload: %v", err)
		return
	}
	f.writeFrame(0x1, data)
}

// sendClose writes a close frame carrying code.
func (f *fakeConn) sendClose(code int) {
	payload := []byte{byte(code >> 8), byte(code)}
	f.writeFrame(0x8, payload)
}

func (f *fakeConn) writeFrame(op byte, payload []byte) {
	buf := []byte{0x80 | op}
	n := len(payload)
	switch {
	case n <= 125:
		buf = append(buf, byte(n))
	case n <= 0xFFFF:
		buf = append(buf, 126, byte(n>>8), byte(n))
	default:
		f.t.Errorf("test frame too large: %d", n)
		return
	}
	buf = append(buf, payload...)
	if _, err := f.c.Write(buf); err != nil {
		f.t.Logf("server write: %v", err)
	}
}

// readFrame reads and unmasks one client frame. ok is false on EOF/close.
func (f *fakeConn) readFrame() (op byte, payload []byte, ok bool) {
	var h [8]byte
	if _, err := io.ReadFull(f.br, h[:2]); err != nil {
		return 0, nil, false
	}
	op = h[0] & 0x0F
	masked := h[1]&0x80 != 0
	n := int(h[1] & 0x7F)
	switch n {
	case 126:
		if _, err := io.ReadFull(f.br, h[:2]); err != nil {
			return 0, nil, false
		}
		n = int(binary.BigEndian.Uint16(h[:2]))
	case 127:
		if _, err := io.ReadFull(f.br, h[:8]); err != nil {
			return 0, nil, false
		}
		n = int(binary.BigEndian.Uint64(h[:8]))
	}
	var key [4]byte
	if masked {
		if _, err := io.ReadFull(f.br, key[:]); err != nil {
			return 0, nil, false
		}
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(f.br, payload); err != nil {
		return 0, nil, false
	}
	if masked {
		for i := range payload {
			payload[i] ^= key[i&3]
		}
	}
	return op, payload, true
}

// next returns the next non-heartbeat client payload, acking heartbeats
// along the way when ack is true. ok is false once the client closes.
func (f *fakeConn) next(ack bool) (clientPayload, bool) {
	for {
		op, data, ok := f.readFrame()
		if !ok {
			return clientPayload{}, false
		}
		if op == 0x8 { // close frame
			return clientPayload{}, false
		}
		if op != 0x1 {
			continue
		}
		var payload clientPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			f.t.Errorf("undecodable client payload: %v", err)
			continue
		}
		if payload.Op == opHeartbeat {
			if ack {
				f.send(opHeartbeatACK, -1, "", nil)
			}
			continue
		}
		return payload, true
	}
}

// gatewayEnv wires a REST fake (serving /gateway/bot) and a scripted
// WebSocket server together.
type gatewayEnv struct {
	adapter *Adapter
	wsURL   string
	sleeps  chan time.Duration
}

// newGatewayEnv builds the fake pair. script runs per WebSocket connection
// with the 1-based connection number and the request path.
func newGatewayEnv(t *testing.T, script func(n int32, path string, f *fakeConn)) *gatewayEnv {
	t.Helper()
	var connN atomic.Int32
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := upgradeGateway(t, w, r)
		defer func() { _ = f.c.Close() }()
		script(connN.Add(1), r.URL.Path, f)
	}))
	t.Cleanup(wsSrv.Close)
	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http")

	restSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gateway/bot" || r.Method != http.MethodGet {
			t.Errorf("unexpected REST call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bot ") {
			t.Errorf("gateway/bot Authorization = %q, want Bot prefix", got)
		}
		_, _ = fmt.Fprintf(w, `{"url":%q,"shards":1,"session_start_limit":{"total":1000,"remaining":999,"reset_after":0,"max_concurrency":1}}`, wsURL)
	}))
	t.Cleanup(restSrv.Close)

	adapter, err := New(Options{Token: "OTk5.fake.token", BaseURL: restSrv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := &gatewayEnv{adapter: adapter, wsURL: wsURL, sleeps: make(chan time.Duration, 64)}
	adapter.sleep = func(ctx context.Context, d time.Duration) error {
		select {
		case env.sleeps <- d:
		default:
		}
		return ctx.Err()
	}
	adapter.jitter = func() float64 { return 1.0 } // first beat after a full interval
	return env
}

// requireIdentify asserts payload is a well-formed IDENTIFY.
func requireIdentify(t *testing.T, payload clientPayload) {
	t.Helper()
	if payload.Op != opIdentify {
		t.Fatalf("op = %d, want identify (2)", payload.Op)
	}
	var d struct {
		Token      string `json:"token"`
		Intents    int    `json:"intents"`
		Properties struct {
			OS      string `json:"os"`
			Browser string `json:"browser"`
			Device  string `json:"device"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(payload.D, &d); err != nil {
		t.Fatalf("decode identify: %v", err)
	}
	if d.Token != "OTk5.fake.token" {
		t.Errorf("identify token = %q, want the bot token", d.Token)
	}
	if d.Intents != 37377 {
		t.Errorf("identify intents = %d, want 37377", d.Intents)
	}
	if d.Properties.Browser != "pigo" || d.Properties.Device != "pigo" || d.Properties.OS == "" {
		t.Errorf("identify properties = %+v, want os set and browser/device pigo", d.Properties)
	}
}

func TestGatewayIdentifyDispatchResumeAndFatal(t *testing.T) {
	messages := make(chan string, 16)
	var env *gatewayEnv
	env = newGatewayEnv(t, func(n int32, path string, f *fakeConn) {
		switch {
		case n == 1:
			if path != "/" {
				t.Errorf("first connection path = %q, want /", path)
			}
			f.send(opHello, -1, "", map[string]any{"heartbeat_interval": 60000})
			payload, ok := f.next(true)
			if !ok {
				t.Error("no identify received")
				return
			}
			requireIdentify(t, payload)
			f.send(opDispatch, 1, "READY", map[string]any{
				"session_id":         "sess-1",
				"resume_gateway_url": env.wsURL + "/resume",
				"user":               map[string]any{"id": "999", "username": "pibot", "bot": true},
			})
			f.send(opDispatch, 2, "MESSAGE_CREATE", map[string]any{
				"id": "m1", "channel_id": "c1", "guild_id": "g1",
				"content":   "<@999> hello there",
				"timestamp": "2026-07-19T10:00:00+00:00",
				"author":    map[string]any{"id": "42", "username": "lea"},
				"mentions":  []map[string]any{{"id": "999"}},
			})
			f.sendClose(4000) // resumable: the client must RESUME, not IDENTIFY
			f.next(false)     // drain until the client's close echo
		case path == "/resume":
			f.send(opHello, -1, "", map[string]any{"heartbeat_interval": 60000})
			payload, ok := f.next(true)
			if !ok {
				t.Error("no resume received")
				return
			}
			if payload.Op != opResume {
				t.Errorf("op = %d, want resume (6)", payload.Op)
			}
			var d struct {
				Token     string `json:"token"`
				SessionID string `json:"session_id"`
				Seq       int64  `json:"seq"`
			}
			if err := json.Unmarshal(payload.D, &d); err != nil {
				t.Errorf("decode resume: %v", err)
			}
			if d.Token != "OTk5.fake.token" || d.SessionID != "sess-1" || d.Seq != 2 {
				t.Errorf("resume = %+v, want token/sess-1/seq 2", d)
			}
			f.send(opDispatch, 3, "RESUMED", nil)
			f.send(opDispatch, 4, "MESSAGE_CREATE", map[string]any{
				"id": "m2", "channel_id": "d1",
				"content":   "hi in dm",
				"timestamp": "2026-07-19T10:01:00+00:00",
				"author":    map[string]any{"id": "42", "username": "lea"},
			})
			f.sendClose(4004) // fatal: authentication failed
			f.next(false)
		default:
			t.Errorf("unexpected connection %d to %q", n, path)
		}
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- env.adapter.Run(t.Context(), func(m chat.Message) error {
			messages <- m.EventID + "|" + m.ChatType + "|" + m.Text + "|" + m.Account
			return nil
		})
	}()

	want := []string{
		"dc:c1:m1|group|hello there|999",
		"dc:d1:m2|dm|hi in dm|999",
	}
	for _, expected := range want {
		select {
		case got := <-messages:
			if got != expected {
				t.Errorf("published %q, want %q", got, expected)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %q", expected)
		}
	}
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "4004") || !strings.Contains(err.Error(), "authentication") {
			t.Errorf("Run error = %v, want fatal 4004 authentication error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on the fatal close code")
	}
}

func TestGatewayHeartbeatAckLossResumes(t *testing.T) {
	resumed := make(chan struct{})
	var env *gatewayEnv
	env = newGatewayEnv(t, func(n int32, path string, f *fakeConn) {
		switch {
		case n == 1:
			// Short heartbeat interval, and heartbeats are never acked.
			f.send(opHello, -1, "", map[string]any{"heartbeat_interval": 40})
			var sawIdentify, sawHeartbeat, sawClose bool
			for {
				op, data, ok := f.readFrame()
				if !ok {
					break
				}
				if op == 0x8 {
					sawClose = true
					if len(data) >= 2 {
						if code := int(binary.BigEndian.Uint16(data[:2])); code != 4000 {
							t.Errorf("client close code = %d, want 4000 (non-1000 keeps the session resumable)", code)
						}
					}
					break
				}
				var payload clientPayload
				if err := json.Unmarshal(data, &payload); err != nil {
					continue
				}
				switch payload.Op {
				case opIdentify:
					sawIdentify = true
					f.send(opDispatch, 1, "READY", map[string]any{
						"session_id":         "sess-hb",
						"resume_gateway_url": env.wsURL + "/resume",
						"user":               map[string]any{"id": "999", "username": "pibot"},
					})
				case opHeartbeat:
					sawHeartbeat = true // deliberately not acked
				}
			}
			if !sawIdentify || !sawHeartbeat || !sawClose {
				t.Errorf("identify=%t heartbeat=%t close=%t, want all true", sawIdentify, sawHeartbeat, sawClose)
			}
		case path == "/resume":
			f.send(opHello, -1, "", map[string]any{"heartbeat_interval": 60000})
			payload, ok := f.next(true)
			if !ok {
				t.Error("no resume after heartbeat loss")
				close(resumed)
				return
			}
			if payload.Op != opResume {
				t.Errorf("op after heartbeat loss = %d, want resume (6)", payload.Op)
			}
			var d struct {
				SessionID string `json:"session_id"`
				Seq       int64  `json:"seq"`
			}
			_ = json.Unmarshal(payload.D, &d)
			if d.SessionID != "sess-hb" || d.Seq != 1 {
				t.Errorf("resume = %+v, want sess-hb/seq 1", d)
			}
			f.send(opDispatch, 2, "RESUMED", nil)
			close(resumed)
			f.next(true) // hold the connection open until the client leaves
		default:
			t.Errorf("unexpected connection %d to %q", n, path)
		}
	})
	env.adapter.jitter = func() float64 { return 0.5 }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- env.adapter.Run(ctx, func(m chat.Message) error { return nil })
	}()

	select {
	case <-resumed:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not resume after heartbeat ack loss")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on ctx cancel")
	}
}

func TestServerRequestedHeartbeatNearTimerDoesNotRecycle(t *testing.T) {
	published := make(chan struct{}, 1)
	var env *gatewayEnv
	env = newGatewayEnv(t, func(_ int32, _ string, f *fakeConn) {
		f.send(opHello, -1, "", map[string]any{"heartbeat_interval": 500})
		payload, ok := f.next(true)
		if !ok {
			t.Error("no identify received")
			return
		}
		requireIdentify(t, payload)
		f.send(opDispatch, 1, "READY", map[string]any{
			"session_id":         "sess-op1",
			"resume_gateway_url": env.wsURL,
			"user":               map[string]any{"id": "999", "username": "pibot"},
		})
		time.Sleep(300 * time.Millisecond)
		f.send(opHeartbeat, -1, "", nil)
		op, data, ok := f.readFrame()
		if !ok || op != 0x1 {
			t.Error("no heartbeat response to server op 1")
			return
		}
		var beat clientPayload
		if err := json.Unmarshal(data, &beat); err != nil || beat.Op != opHeartbeat {
			t.Errorf("heartbeat response = %s: %v", data, err)
			return
		}
		// Cross the local 500ms timer before acknowledging the server-requested
		// heartbeat. The connection is healthy and must remain open.
		time.Sleep(300 * time.Millisecond)
		f.send(opHeartbeatACK, -1, "", nil)
		f.send(opDispatch, 2, "MESSAGE_CREATE", map[string]any{
			"id": "m-op1", "channel_id": "d1", "content": "still here",
			"timestamp": "2026-07-19T10:00:00+00:00",
			"author":    map[string]any{"id": "42", "username": "lea"},
		})
		f.sendClose(4004)
		f.next(false)
	})
	env.adapter.jitter = func() float64 { return 1 }
	errCh := make(chan error, 1)
	go func() {
		errCh <- env.adapter.Run(t.Context(), func(chat.Message) error {
			published <- struct{}{}
			return nil
		})
	}()
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy connection was recycled before the delayed heartbeat ACK")
	}
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after fatal close")
	}
}

func TestResumeDialFailuresClearDeadURL(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := "ws" + strings.TrimPrefix(dead.URL, "http")
	dead.Close()
	env := newGatewayEnv(t, func(_ int32, _ string, _ *fakeConn) {})
	st := &gatewayState{}
	st.setSeq(7)
	st.setSession("sess-dead", deadURL)
	for range 3 {
		if established, err := env.adapter.gatewayCycle(t.Context(), st, func(chat.Message) error { return nil }); err != nil || established {
			t.Fatalf("gatewayCycle = (%t, %v), want transient dial failure", established, err)
		}
	}
	if _, _, _, ok := st.session(); ok {
		t.Fatal("dead resume URL remained resumable after repeated dial failures")
	}
}

func TestGatewayDialHasDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	env := newGatewayEnv(t, func(_ int32, _ string, _ *fakeConn) {})
	env.adapter.gatewayDialTimeout = 30 * time.Millisecond
	st := &gatewayState{}
	st.setSeq(1)
	st.setSession("sess-stall", "ws://"+listener.Addr().String())
	done := make(chan error, 1)
	go func() {
		_, err := env.adapter.gatewayCycle(context.Background(), st, func(chat.Message) error { return nil })
		done <- err
	}()
	var stalled net.Conn
	select {
	case stalled = <-accepted:
		defer func() { _ = stalled.Close() }()
	case <-time.After(time.Second):
		t.Fatal("gateway dial never connected to the stalling server")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gatewayCycle returned fatal error for a timed-out dial: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("gateway dial ignored its timeout")
	}
}

func TestPeerCleanCloseKeepsResumeSession(t *testing.T) {
	for _, code := range []int{1000, 1001} {
		t.Run(fmt.Sprintf("code_%d", code), func(t *testing.T) {
			adapter, err := New(Options{Token: "OTk5.fake.token"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			st := &gatewayState{}
			st.setSeq(9)
			st.setSession("sess-clean", "wss://resume.example")
			if err := adapter.classifyGatewayError(st, &wsclient.CloseError{Code: code}); err != nil {
				t.Fatalf("classifyGatewayError: %v", err)
			}
			if _, _, _, ok := st.session(); !ok {
				t.Fatalf("peer close %d cleared a resumable session", code)
			}
		})
	}
}

func TestHeartbeatWatchdogStateIsPerConnection(t *testing.T) {
	now := time.Now()
	oldConnection := &heartbeatState{}
	oldConnection.arm(now)
	newConnection := &heartbeatState{}
	if pending, _ := newConnection.pending(now, time.Second); pending {
		t.Fatal("new connection inherited the old connection's pending heartbeat")
	}
	oldConnection.arm(now.Add(time.Second))
	if pending, _ := newConnection.pending(now.Add(time.Second), time.Second); pending {
		t.Fatal("retiring connection mutated the new connection's watchdog")
	}
}

func TestGatewayDisallowedIntentsFatal(t *testing.T) {
	env := newGatewayEnv(t, func(n int32, path string, f *fakeConn) {
		f.send(opHello, -1, "", map[string]any{"heartbeat_interval": 60000})
		if _, ok := f.next(true); !ok {
			t.Error("no identify received")
			return
		}
		f.sendClose(4014)
		f.next(false)
	})
	err := env.adapter.Run(t.Context(), func(m chat.Message) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "Message Content Intent") {
		t.Errorf("Run error = %v, want actionable Message Content Intent error", err)
	}
}

func TestGatewayReconnectAndInvalidSessionReidentify(t *testing.T) {
	events := make(chan string, 8)
	var env *gatewayEnv
	env = newGatewayEnv(t, func(n int32, path string, f *fakeConn) {
		switch {
		case n == 1 && path == "/":
			f.send(opHello, -1, "", map[string]any{"heartbeat_interval": 60000})
			payload, _ := f.next(true)
			requireIdentify(t, payload)
			events <- "identify"
			f.send(opDispatch, 1, "READY", map[string]any{
				"session_id":         "sess-r",
				"resume_gateway_url": env.wsURL + "/resume",
				"user":               map[string]any{"id": "999", "username": "pibot"},
			})
			f.send(opReconnect, -1, "", nil) // op 7: cycle the connection
			f.next(false)
		case path == "/resume":
			f.send(opHello, -1, "", map[string]any{"heartbeat_interval": 60000})
			payload, _ := f.next(true)
			if payload.Op != opResume {
				t.Errorf("op = %d, want resume (6) after op 7 reconnect", payload.Op)
			}
			events <- "resume"
			f.send(opInvalidSession, -1, "", false) // d:false → fresh identify
			f.next(false)
		default: // fresh connect after the invalidated session
			f.send(opHello, -1, "", map[string]any{"heartbeat_interval": 60000})
			payload, _ := f.next(true)
			if payload.Op != opIdentify {
				t.Errorf("op = %d, want identify (2) after invalid session d:false", payload.Op)
			}
			events <- "reidentify"
			f.next(true) // hold open
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- env.adapter.Run(ctx, func(m chat.Message) error { return nil })
	}()

	for _, expected := range []string{"identify", "resume", "reidentify"} {
		select {
		case got := <-events:
			if got != expected {
				t.Fatalf("gateway event = %q, want %q", got, expected)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %q", expected)
		}
	}
	// The invalid-session path must wait 1-5s (via the sleep seam) before
	// re-identifying.
	sawInvalidSessionWait := false
	for {
		select {
		case d := <-env.sleeps:
			if d >= time.Second {
				sawInvalidSessionWait = true
			}
			continue
		default:
		}
		break
	}
	if !sawInvalidSessionWait {
		t.Error("no >=1s wait recorded before re-identify after invalid session")
	}
	cancel()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on ctx cancel")
	}
}
