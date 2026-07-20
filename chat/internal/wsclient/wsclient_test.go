package wsclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// acceptOf recomputes Sec-WebSocket-Accept server-side.
func acceptOf(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// wsURL rewrites an httptest server URL to the ws/wss scheme.
func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// upgrade hijacks the request, writes a valid 101 response, and returns the
// raw connection with a 5s safety deadline.
func upgrade(t *testing.T, w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.Reader) {
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
		t.Fatalf("set deadline: %v", err)
	}
	_, _ = fmt.Fprintf(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		acceptOf(r.Header.Get("Sec-WebSocket-Key")))
	return c, rw.Reader
}

// wsHandler upgrades each request and hands the raw connection to fn,
// signalling done when fn returns so tests can wait on server assertions.
func wsHandler(t *testing.T, fn func(c net.Conn, br *bufio.Reader)) (http.Handler, chan struct{}) {
	done := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, br := upgrade(t, w, r)
		defer func() { _ = c.Close() }()
		fn(c, br)
		done <- struct{}{}
	}), done
}

func newServer(t *testing.T, fn func(c net.Conn, br *bufio.Reader)) (*httptest.Server, chan struct{}) {
	t.Helper()
	h, done := wsHandler(t, fn)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, done
}

func newTLSServer(t *testing.T, fn func(c net.Conn, br *bufio.Reader)) (*httptest.Server, chan struct{}) {
	t.Helper()
	h, done := wsHandler(t, fn)
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv, done
}

func waitDone(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server handler did not finish")
	}
}

func mustDial(t *testing.T, srv *httptest.Server, opts *Options) *Conn {
	t.Helper()
	conn, err := Dial(context.Background(), wsURL(srv), nil, opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func mustRead(t *testing.T, r io.Reader, buf []byte) {
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Errorf("server read: %v", err)
		panic(http.ErrAbortHandler)
	}
}

type frame struct {
	fin     bool
	op      byte
	masked  bool
	maskKey [4]byte
	payload []byte
}

// readFrame reads and unmasks one client frame server-side.
func readFrame(t *testing.T, br *bufio.Reader) frame {
	var h [8]byte
	mustRead(t, br, h[:2])
	f := frame{fin: h[0]&0x80 != 0, op: h[0] & 0x0F, masked: h[1]&0x80 != 0}
	n := int(h[1] & 0x7F)
	switch n {
	case 126:
		mustRead(t, br, h[:2])
		n = int(binary.BigEndian.Uint16(h[:2]))
	case 127:
		mustRead(t, br, h[:8])
		n = int(binary.BigEndian.Uint64(h[:8]))
	}
	if f.masked {
		mustRead(t, br, f.maskKey[:])
	}
	f.payload = make([]byte, n)
	mustRead(t, br, f.payload)
	if f.masked {
		for i := range f.payload {
			f.payload[i] ^= f.maskKey[i&3]
		}
	}
	return f
}

// writeFrame writes one unmasked server frame with minimal length encoding.
func writeFrame(t *testing.T, c net.Conn, fin bool, op byte, payload []byte) {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	buf := []byte{b0}
	n := len(payload)
	switch {
	case n <= 125:
		buf = append(buf, byte(n))
	case n <= 0xFFFF:
		buf = append(buf, 126, byte(n>>8), byte(n))
	default:
		buf = append(buf, 127)
		buf = binary.BigEndian.AppendUint64(buf, uint64(n))
	}
	if _, err := c.Write(append(buf, payload...)); err != nil {
		t.Errorf("server write: %v", err)
		panic(http.ErrAbortHandler)
	}
}

// expectClose reads the client's close frame and asserts its status code.
func expectClose(t *testing.T, br *bufio.Reader, code int) {
	f := readFrame(t, br)
	if f.op != 8 || !f.masked {
		t.Errorf("close frame: op %#x masked %v, want masked op 0x8", f.op, f.masked)
		return
	}
	if len(f.payload) < 2 {
		t.Errorf("close frame payload %d bytes, want >= 2", len(f.payload))
		return
	}
	if got := int(binary.BigEndian.Uint16(f.payload[:2])); got != code {
		t.Errorf("close code = %d, want %d", got, code)
	}
}

func closePayload(code int, reason string) []byte {
	p := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(p, uint16(code))
	copy(p[2:], reason)
	return p
}

// pattern returns n deterministic bytes.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func TestDialHandshakeHeaders(t *testing.T) {
	done := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.URL.RequestURI(); got != "/gateway?v=10&encoding=json" {
			t.Errorf("request uri = %q", got)
		}
		if got := r.Header.Get("Upgrade"); got != "websocket" {
			t.Errorf("Upgrade = %q", got)
		}
		if got := r.Header.Get("Connection"); got != "Upgrade" {
			t.Errorf("Connection = %q", got)
		}
		if got := r.Header.Get("Sec-WebSocket-Version"); got != "13" {
			t.Errorf("Sec-WebSocket-Version = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bot fake" {
			t.Errorf("Authorization = %q, caller header not passed through", got)
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
			t.Errorf("Sec-WebSocket-Key = %q, want base64 of 16 bytes", key)
		}
		c, _ := upgrade(t, w, r)
		defer func() { _ = c.Close() }()
		done <- struct{}{}
	}))
	t.Cleanup(srv.Close)

	conn, err := Dial(context.Background(), wsURL(srv)+"/gateway?v=10&encoding=json",
		http.Header{"Authorization": {"Bot fake"}}, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	waitDone(t, done)
}

func TestDialRejectsBadHandshake(t *testing.T) {
	raw := func(f func(key string) string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer func() { _ = c.Close() }()
			_, _ = io.WriteString(c, f(r.Header.Get("Sec-WebSocket-Key")))
		})
	}
	cases := []struct {
		name    string
		handler http.Handler
		want    []string
	}{
		{
			name: "non-101 status",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"message":"unknown gateway"}`)
			}),
			want: []string{"403", "unknown gateway"},
		},
		{
			name: "accept mismatch",
			handler: raw(func(string) string {
				return "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: AAAAAAAAAAAAAAAAAAAAAAAAAAA=\r\n\r\n"
			}),
			want: []string{"Sec-WebSocket-Accept"},
		},
		{
			name: "missing upgrade header",
			handler: raw(func(key string) string {
				return "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + acceptOf(key) + "\r\n\r\n"
			}),
			want: []string{"Upgrade"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			t.Cleanup(srv.Close)
			_, err := Dial(context.Background(), wsURL(srv), nil, nil)
			if err == nil {
				t.Fatal("dial succeeded, want error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

func TestClientMasksFrames(t *testing.T) {
	keys := make(chan [4]byte, 2)
	srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
		for _, want := range []string{"hello", "world"} {
			f := readFrame(t, br)
			if !f.masked {
				t.Error("client frame not masked")
			}
			if !f.fin || f.op != 1 {
				t.Errorf("frame fin %v op %#x, want final text", f.fin, f.op)
			}
			if string(f.payload) != want {
				t.Errorf("payload = %q, want %q", f.payload, want)
			}
			keys <- f.maskKey
		}
	})
	conn := mustDial(t, srv, nil)
	if err := conn.WriteText([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := conn.WriteText([]byte("world")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitDone(t, done)
	if k1, k2 := <-keys, <-keys; k1 == k2 {
		t.Error("mask key repeated across frames")
	}
}

func TestReadLengthEncodings(t *testing.T) {
	sizes := []int{125, 126, 60000, 66000} // 7-bit, 16-bit min, 16-bit, 64-bit
	srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
		for _, n := range sizes {
			writeFrame(t, c, true, 2, pattern(n))
		}
	})
	conn := mustDial(t, srv, nil)
	for _, n := range sizes {
		op, payload, err := conn.ReadMessage(context.Background())
		if err != nil {
			t.Fatalf("read %d-byte message: %v", n, err)
		}
		if op != OpBinary {
			t.Errorf("opcode = %d, want OpBinary", op)
		}
		if !bytes.Equal(payload, pattern(n)) {
			t.Errorf("%d-byte payload mismatch (got %d bytes)", n, len(payload))
		}
	}
	waitDone(t, done)
}

func TestFragmentReassemblyWithInterleavedPing(t *testing.T) {
	srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
		writeFrame(t, c, false, 1, []byte("Hel"))
		writeFrame(t, c, true, 9, []byte("beat"))
		writeFrame(t, c, false, 0, []byte("lo "))
		writeFrame(t, c, true, 0, []byte("World"))
		pong := readFrame(t, br)
		if pong.op != 0xA || !pong.masked || string(pong.payload) != "beat" {
			t.Errorf("pong = op %#x masked %v payload %q, want masked 0xA %q",
				pong.op, pong.masked, pong.payload, "beat")
		}
	})
	conn := mustDial(t, srv, nil)
	op, payload, err := conn.ReadMessage(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if op != OpText || string(payload) != "Hello World" {
		t.Errorf("message = op %d %q, want text %q", op, payload, "Hello World")
	}
	waitDone(t, done)
}

func TestAutoPongEchoesPayload(t *testing.T) {
	srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
		writeFrame(t, c, true, 9, []byte("heartbeat-42"))
		pong := readFrame(t, br)
		if pong.op != 0xA || !pong.masked || string(pong.payload) != "heartbeat-42" {
			t.Errorf("pong = op %#x masked %v payload %q", pong.op, pong.masked, pong.payload)
		}
		writeFrame(t, c, true, 1, []byte("after"))
	})
	conn := mustDial(t, srv, nil)
	op, payload, err := conn.ReadMessage(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if op != OpText || string(payload) != "after" {
		t.Errorf("message = op %d %q, want text %q", op, payload, "after")
	}
	waitDone(t, done)
}

func TestCleanCloseHandshake(t *testing.T) {
	srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
		writeFrame(t, c, true, 8, closePayload(1000, "bye"))
		expectClose(t, br, 1000)
	})
	conn := mustDial(t, srv, nil)
	_, _, err := conn.ReadMessage(context.Background())
	var ce *CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *CloseError", err)
	}
	if ce.Code != 1000 || ce.Reason != "bye" {
		t.Errorf("close = %d %q, want 1000 %q", ce.Code, ce.Reason, "bye")
	}
	waitDone(t, done)
}

func TestWriteClose(t *testing.T) {
	srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
		f := readFrame(t, br)
		if f.op != 8 || !f.masked {
			t.Errorf("close frame: op %#x masked %v", f.op, f.masked)
		}
		if len(f.payload) < 2 || binary.BigEndian.Uint16(f.payload[:2]) != 1000 || string(f.payload[2:]) != "done" {
			t.Errorf("close payload = %q", f.payload)
		}
		writeFrame(t, c, true, 8, f.payload) // echo
	})
	conn := mustDial(t, srv, nil)
	if err := conn.WriteClose(1000, "done"); err != nil {
		t.Fatalf("write close: %v", err)
	}
	if err := conn.WriteText([]byte("x")); err == nil {
		t.Error("WriteText after WriteClose succeeded, want error")
	}
	_, _, err := conn.ReadMessage(context.Background())
	var ce *CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *CloseError", err)
	}
	if ce.Code != 1000 || ce.Reason != "done" {
		t.Errorf("close = %d %q, want 1000 %q", ce.Code, ce.Reason, "done")
	}
	waitDone(t, done)
}

func TestOversizeMessage(t *testing.T) {
	opts := &Options{MaxMessageSize: 1024}
	t.Run("single frame", func(t *testing.T) {
		srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
			writeFrame(t, c, true, 2, make([]byte, 2048))
			expectClose(t, br, 1009)
		})
		conn := mustDial(t, srv, opts)
		_, _, err := conn.ReadMessage(context.Background())
		if err == nil || !strings.Contains(err.Error(), "1009") {
			t.Errorf("err = %v, want 1009 failure", err)
		}
		waitDone(t, done)
	})
	t.Run("fragmented", func(t *testing.T) {
		srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
			writeFrame(t, c, false, 2, make([]byte, 700))
			writeFrame(t, c, true, 0, make([]byte, 700))
			expectClose(t, br, 1009)
		})
		conn := mustDial(t, srv, opts)
		_, _, err := conn.ReadMessage(context.Background())
		if err == nil || !strings.Contains(err.Error(), "1009") {
			t.Errorf("err = %v, want 1009 failure", err)
		}
		waitDone(t, done)
	})
	t.Run("fragment length overflow", func(t *testing.T) {
		srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
			writeFrame(t, c, false, 2, []byte("x"))
			header := []byte{0x80, 127}
			header = binary.BigEndian.AppendUint64(header, uint64(math.MaxInt64))
			if _, err := c.Write(header); err != nil {
				t.Errorf("server write continuation header: %v", err)
				return
			}
			expectClose(t, br, 1009)
		})
		conn := mustDial(t, srv, opts)
		_, _, err := conn.ReadMessage(context.Background())
		if err == nil || !strings.Contains(err.Error(), "1009") {
			t.Errorf("err = %v, want 1009 failure", err)
		}
		waitDone(t, done)
	})
}

func TestInvalidUTF8Text(t *testing.T) {
	srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
		writeFrame(t, c, true, 1, []byte{0xff, 0xfe, 0xfd})
		expectClose(t, br, 1007)
	})
	conn := mustDial(t, srv, nil)
	_, _, err := conn.ReadMessage(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1007") {
		t.Errorf("err = %v, want 1007 failure", err)
	}
	waitDone(t, done)
}

func TestRuneSplitAcrossFragments(t *testing.T) {
	msg := []byte("héllo") // fragment boundary splits the 2-byte é
	srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
		writeFrame(t, c, false, 1, msg[:2])
		writeFrame(t, c, true, 0, msg[2:])
	})
	conn := mustDial(t, srv, nil)
	op, payload, err := conn.ReadMessage(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if op != OpText || string(payload) != "héllo" {
		t.Errorf("message = op %d %q, want text %q", op, payload, "héllo")
	}
	waitDone(t, done)
}

func TestProtocolViolations(t *testing.T) {
	maskedFrame := func() []byte {
		key := [4]byte{1, 2, 3, 4}
		p := []byte("abc")
		for i := range p {
			p[i] ^= key[i&3]
		}
		return append([]byte{0x81, 0x80 | 3, 1, 2, 3, 4}, p...)
	}
	cases := []struct {
		name string
		raw  []byte
	}{
		{"masked server frame", maskedFrame()},
		{"unknown opcode", []byte{0x83, 0x00}},
		{"fragmented ping", []byte{0x09, 0x00}},
		{"unexpected continuation", []byte{0x80, 0x00}},
		{"nonzero rsv bits", []byte{0xC1, 0x00}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
				if _, err := c.Write(tc.raw); err != nil {
					t.Errorf("server write: %v", err)
					return
				}
				expectClose(t, br, 1002)
			})
			conn := mustDial(t, srv, nil)
			_, _, err := conn.ReadMessage(context.Background())
			if err == nil || !strings.Contains(err.Error(), "1002") {
				t.Errorf("err = %v, want 1002 failure", err)
			}
			waitDone(t, done)
		})
	}
}

func TestReadMessageCtxCancel(t *testing.T) {
	srv, done := newServer(t, func(c net.Conn, br *bufio.Reader) {
		_, _ = io.Copy(io.Discard, c) // send nothing; drain until the client tears down
	})
	conn := mustDial(t, srv, nil)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(30*time.Millisecond, cancel)
	defer timer.Stop()
	start := time.Now()
	_, _, err := conn.ReadMessage(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("cancellation took %v, want prompt return", elapsed)
	}
	waitDone(t, done)
}

func TestWSS(t *testing.T) {
	srv, done := newTLSServer(t, func(c net.Conn, br *bufio.Reader) {
		writeFrame(t, c, true, 1, []byte("secure"))
	})
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	conn, err := Dial(context.Background(), wsURL(srv), nil,
		&Options{TLSConfig: &tls.Config{RootCAs: pool}})
	if err != nil {
		t.Fatalf("dial wss: %v", err)
	}
	defer func() { _ = conn.Close() }()
	op, payload, err := conn.ReadMessage(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if op != OpText || string(payload) != "secure" {
		t.Errorf("message = op %d %q, want text %q", op, payload, "secure")
	}
	waitDone(t, done)
}
