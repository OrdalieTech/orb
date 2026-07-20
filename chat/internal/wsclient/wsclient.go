// Package wsclient implements a minimal RFC 6455 WebSocket client on the
// standard library alone: client role only, no extensions, no compression,
// no subprotocol negotiation. Its first consumer is the Discord Gateway
// adapter.
//
// Concurrency contract: exactly one goroutine calls [Conn.ReadMessage];
// [Conn.WriteText], [Conn.WriteClose], and the automatic pong replies are
// serialized by an internal mutex and are safe from any goroutine.
// Heartbeats are the caller's job — this layer is protocol-only. After any
// error the Conn is dead; callers reconnect rather than recover.
package wsclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Data opcodes returned by [Conn.ReadMessage]. Control opcodes (close, ping,
// pong) are handled internally and never surface to the caller.
const (
	// OpText identifies a UTF-8 text message.
	OpText = 1
	// OpBinary identifies a binary message.
	OpBinary = 2
)

const (
	opContinuation = 0
	opClose        = 8
	opPing         = 9
	opPong         = 10
)

// RFC 6455 section 4.2.2 handshake GUID.
const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

var aLongTimeAgo = time.Unix(1, 0)

// Options configures [Dial]. A nil *Options selects every default.
type Options struct {
	// MaxMessageSize caps the size in bytes of a reassembled incoming
	// message. A larger message fails the connection with close code 1009.
	// Default 4 MiB.
	MaxMessageSize int64
	// WriteTimeout is the per-frame write deadline. Default 10s.
	WriteTimeout time.Duration
	// TLSConfig overrides the TLS configuration for wss URLs, e.g. to trust
	// a test server certificate. Nil uses defaults, with ServerName taken
	// from the URL.
	TLSConfig *tls.Config
}

func (o *Options) resolve() Options {
	r := Options{MaxMessageSize: 4 << 20, WriteTimeout: 10 * time.Second}
	if o != nil {
		if o.MaxMessageSize > 0 {
			r.MaxMessageSize = o.MaxMessageSize
		}
		if o.WriteTimeout > 0 {
			r.WriteTimeout = o.WriteTimeout
		}
		r.TLSConfig = o.TLSConfig
	}
	return r
}

// CloseError is the error returned by [Conn.ReadMessage] when the connection
// ends with a close frame, or with a locally synthesized code on abnormal
// transport loss.
type CloseError struct {
	// Code is the close status code. 1005 means the peer's close frame
	// carried no code; 1006 never travels on the wire and is synthesized
	// locally on abnormal TCP loss.
	Code int
	// Reason is the close reason, possibly empty.
	Reason string
}

// Error implements the error interface.
func (e *CloseError) Error() string {
	return fmt.Sprintf("wsclient: close %d %q", e.Code, e.Reason)
}

// Conn is a WebSocket client connection established by [Dial]. See the
// package comment for the concurrency contract.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader

	maxMessageSize int64
	writeTimeout   time.Duration

	writeMu   sync.Mutex
	sentClose bool // guarded by writeMu

	closeOnce sync.Once
	closeErr  error
}

var reservedHeaders = map[string]bool{
	"Host":                  true,
	"Upgrade":               true,
	"Connection":            true,
	"Sec-Websocket-Key":     true,
	"Sec-Websocket-Version": true,
}

// Dial opens a WebSocket connection to rawURL (ws or wss scheme), performs
// the RFC 6455 opening handshake with the extra request headers in header
// (which may be nil), and returns the established connection. ctx bounds
// the whole dial: TCP connect, TLS, and the HTTP handshake.
//
// ponytail: no proxy, redirect, or subprotocol support, and no
// permessage-deflate — the Discord gateway needs none of it.
func Dial(ctx context.Context, rawURL string, header http.Header, opts *Options) (*Conn, error) {
	o := opts.resolve()
	u, err := url.Parse(rawURL)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // strip the echoed URL: query strings may carry tokens
		}
		return nil, fmt.Errorf("wsclient: parse url: %w", err)
	}
	var defaultPort string
	switch u.Scheme {
	case "ws":
		defaultPort = "80"
	case "wss":
		defaultPort = "443"
	default:
		return nil, fmt.Errorf("wsclient: unsupported scheme %q", u.Scheme)
	}
	host := u.Host
	if host == "" {
		return nil, errors.New("wsclient: url has no host")
	}
	addr := host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), defaultPort)
	}

	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("wsclient: dial %s: %w", host, err)
	}
	// The watcher interrupts handshake I/O when ctx ends; per-operation
	// deadlines on the returned Conn make any leftover deadline harmless.
	stop := watchCtx(ctx, func() { _ = nc.SetDeadline(aLongTimeAgo) })
	defer stop()

	conn := net.Conn(nc)
	fail := func(err error) (*Conn, error) {
		_ = conn.Close()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("wsclient: handshake with %s: %w", host, ctx.Err())
		}
		return nil, err
	}

	if u.Scheme == "wss" {
		cfg := &tls.Config{}
		if o.TLSConfig != nil {
			cfg = o.TLSConfig.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		tc := tls.Client(nc, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fail(fmt.Errorf("wsclient: tls handshake with %s: %w", host, err))
		}
		conn = tc
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return fail(fmt.Errorf("wsclient: generate key: %w", err))
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	var req bytes.Buffer
	fmt.Fprintf(&req, "GET %s HTTP/1.1\r\n", u.RequestURI())
	fmt.Fprintf(&req, "Host: %s\r\n", host)
	req.WriteString("Upgrade: websocket\r\nConnection: Upgrade\r\n")
	fmt.Fprintf(&req, "Sec-WebSocket-Key: %s\r\n", key)
	req.WriteString("Sec-WebSocket-Version: 13\r\n")
	for k, vs := range header {
		if reservedHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			fmt.Fprintf(&req, "%s: %s\r\n", k, v)
		}
	}
	req.WriteString("\r\n")
	if _, err := conn.Write(req.Bytes()); err != nil {
		return fail(fmt.Errorf("wsclient: write handshake to %s: %w", host, err))
	}

	// The bufio.Reader may buffer frame bytes past the handshake response;
	// it must stay attached to the returned Conn.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return fail(fmt.Errorf("wsclient: read handshake response from %s: %w", host, err))
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return fail(fmt.Errorf("wsclient: handshake with %s rejected: %s: %s",
			host, resp.Status, bytes.TrimSpace(head)))
	}
	if !headerHasToken(resp.Header.Get("Upgrade"), "websocket") {
		return fail(fmt.Errorf("wsclient: handshake with %s: missing Upgrade: websocket", host))
	}
	if !headerHasToken(resp.Header.Get("Connection"), "Upgrade") {
		return fail(fmt.Errorf("wsclient: handshake with %s: Connection header lacks Upgrade token", host))
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		return fail(fmt.Errorf("wsclient: handshake with %s: Sec-WebSocket-Accept mismatch", host))
	}

	if err := nc.SetDeadline(time.Time{}); err != nil {
		return fail(fmt.Errorf("wsclient: clear handshake deadline for %s: %w", host, err))
	}
	return &Conn{
		conn:           conn,
		br:             br,
		maxMessageSize: o.MaxMessageSize,
		writeTimeout:   o.WriteTimeout,
	}, nil
}

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func headerHasToken(value, token string) bool {
	for t := range strings.SplitSeq(value, ",") {
		if strings.EqualFold(strings.TrimSpace(t), token) {
			return true
		}
	}
	return false
}

// stop waits for the watcher, preventing a late deadline on a returned Conn.
func watchCtx(ctx context.Context, interrupt func()) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			interrupt()
		case <-done:
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// ReadMessage reads the next complete data message, transparently handling
// control frames: pings are answered with pongs, pongs are dropped, and a
// close frame ends the connection and returns a *[CloseError]. Fragmented
// messages are reassembled before returning; opcode is [OpText] or
// [OpBinary]. Exactly one goroutine may call ReadMessage. Cancelling ctx
// interrupts a blocked read and returns ctx.Err(); as after any other
// error, the Conn is then unusable.
func (c *Conn) ReadMessage(ctx context.Context) (opcode int, payload []byte, err error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	// Clear any deadline left by a previously cancelled ctx.
	if err := c.conn.SetReadDeadline(time.Time{}); err != nil {
		return 0, nil, fmt.Errorf("wsclient: set read deadline: %w", err)
	}
	stop := watchCtx(ctx, func() { _ = c.conn.SetReadDeadline(aLongTimeAgo) })
	opcode, payload, err = c.readMessage()
	stop()
	if err != nil {
		var ne net.Error
		if ctx.Err() != nil && errors.As(err, &ne) && ne.Timeout() {
			_ = c.Close()
			return 0, nil, ctx.Err()
		}
		return 0, nil, err
	}
	return opcode, payload, nil
}

func (c *Conn) readMessage() (int, []byte, error) {
	var (
		assembled []byte
		msgOp     int
		inFrag    bool
	)
	for {
		fin, op, n, err := c.readFrameHeader()
		if err != nil {
			return 0, nil, err
		}
		if op == opClose || op == opPing || op == opPong {
			payload := make([]byte, n) // n <= 125, validated by readFrameHeader
			if _, err := io.ReadFull(c.br, payload); err != nil {
				return 0, nil, c.abnormal("read control payload", err)
			}
			switch op {
			case opPing:
				if err := c.pong(payload); err != nil {
					_ = c.Close()
					return 0, nil, err
				}
			case opPong:
				// ponytail: pongs are dropped — Discord heartbeat ACKs are
				// op-11 JSON messages, never ws pongs.
			case opClose:
				return 0, nil, c.handleClose(payload)
			}
			continue
		}
		switch op {
		case OpText, OpBinary:
			if inFrag {
				return 0, nil, c.fail(1002, "new data frame during fragmented message")
			}
			msgOp, inFrag = op, true
		case opContinuation:
			if !inFrag {
				return 0, nil, c.fail(1002, "continuation frame without message in progress")
			}
		default:
			return 0, nil, c.fail(1002, fmt.Sprintf("unknown opcode %#x", op))
		}
		if n > c.maxMessageSize-int64(len(assembled)) {
			return 0, nil, c.fail(1009, fmt.Sprintf("message exceeds %d-byte limit", c.maxMessageSize))
		}
		off := len(assembled)
		assembled = append(assembled, make([]byte, int(n))...)
		if _, err := io.ReadFull(c.br, assembled[off:]); err != nil {
			return 0, nil, c.abnormal("read frame payload", err)
		}
		if !fin {
			continue
		}
		if msgOp == OpText && !utf8.Valid(assembled) {
			return 0, nil, c.fail(1007, "invalid UTF-8 in text message")
		}
		return msgOp, assembled, nil
	}
}

func (c *Conn) readFrameHeader() (fin bool, op int, n int64, err error) {
	var h [8]byte
	if _, err := io.ReadFull(c.br, h[:2]); err != nil {
		return false, 0, 0, c.abnormal("read frame header", err)
	}
	if h[0]&0x70 != 0 {
		return false, 0, 0, c.fail(1002, "nonzero RSV bits")
	}
	fin = h[0]&0x80 != 0
	op = int(h[0] & 0x0F)
	if h[1]&0x80 != 0 {
		return false, 0, 0, c.fail(1002, "masked server frame")
	}
	n = int64(h[1] & 0x7F)
	switch n {
	case 126:
		if _, err := io.ReadFull(c.br, h[:2]); err != nil {
			return false, 0, 0, c.abnormal("read extended length", err)
		}
		n = int64(binary.BigEndian.Uint16(h[:2]))
	case 127:
		if _, err := io.ReadFull(c.br, h[:8]); err != nil {
			return false, 0, 0, c.abnormal("read extended length", err)
		}
		v := binary.BigEndian.Uint64(h[:8])
		if v>>63 != 0 {
			return false, 0, 0, c.fail(1002, "64-bit length with MSB set")
		}
		n = int64(v)
	}
	if op >= opClose {
		if !fin {
			return false, 0, 0, c.fail(1002, "fragmented control frame")
		}
		if n > 125 {
			return false, 0, 0, c.fail(1002, "control frame payload over 125 bytes")
		}
	}
	return fin, op, n, nil
}

func (c *Conn) handleClose(payload []byte) error {
	if len(payload) == 1 {
		return c.fail(1002, "close frame with 1-byte payload")
	}
	code, reason := 1005, ""
	if len(payload) >= 2 {
		code = int(binary.BigEndian.Uint16(payload[:2]))
		if !validCloseCode(code) {
			return c.fail(1002, fmt.Sprintf("invalid close code %d", code))
		}
		if !utf8.Valid(payload[2:]) {
			return c.fail(1007, "invalid UTF-8 in close reason")
		}
		reason = string(payload[2:])
	}
	echo := code
	if echo == 1005 {
		echo = 1000
	}
	_ = c.sendClose(echo, "") // best effort; no-op if we already sent a close
	_ = c.Close()
	return &CloseError{Code: code, Reason: reason}
}

// Reserved 1004-1006/1015 never travel on the wire; Discord's private 4xxx do.
func validCloseCode(code int) bool {
	switch {
	case code >= 1000 && code <= 1003:
		return true
	case code >= 1007 && code <= 1014:
		return true
	case code >= 3000 && code <= 4999:
		return true
	}
	return false
}

func (c *Conn) fail(code int, msg string) error {
	_ = c.sendClose(code, "")
	_ = c.Close()
	return fmt.Errorf("wsclient: protocol error: %s (close %d)", msg, code)
}

func (c *Conn) abnormal(what string, err error) error {
	_ = c.Close()
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("wsclient: %s: %w", what, err)
	}
	return &CloseError{Code: 1006, Reason: what + ": " + err.Error()}
}

// WriteText sends data as a single masked, unfragmented text frame. It is
// safe to call from any goroutine and fails once a close frame was sent.
//
// ponytail: no WriteBinary, WritePing, or fragmented writes — Discord sends
// only whole text frames; add them when a consumer needs them.
func (c *Conn) WriteText(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.sentClose {
		return errors.New("wsclient: connection closing")
	}
	return c.writeFrameLocked(OpText, data)
}

// WriteClose sends a close frame with the given status code and reason
// (at most 123 bytes) and marks the connection closing; later writes fail.
// The caller should keep calling ReadMessage until it returns the peer's
// *[CloseError], then call [Conn.Close]. Calling WriteClose after a close
// frame was already sent is a no-op.
func (c *Conn) WriteClose(code int, reason string) error {
	if len(reason) > 123 {
		return fmt.Errorf("wsclient: close reason too long (%d bytes)", len(reason))
	}
	return c.sendClose(code, reason)
}

// Close tears down the TCP connection immediately, without a close
// handshake (the peer observes an abnormal closure, 1006). It is idempotent
// and safe from any goroutine.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.conn.Close() })
	return c.closeErr
}

func (c *Conn) pong(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.sentClose {
		return nil
	}
	return c.writeFrameLocked(opPong, payload)
}

func (c *Conn) sendClose(code int, reason string) error {
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	copy(payload[2:], reason)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.sentClose {
		return nil
	}
	c.sentClose = true
	return c.writeFrameLocked(opClose, payload)
}

func (c *Conn) writeFrameLocked(op int, payload []byte) error {
	buf := make([]byte, 0, 14+len(payload))
	buf = append(buf, 0x80|byte(op))
	n := len(payload)
	switch {
	case n <= 125:
		buf = append(buf, 0x80|byte(n))
	case n <= 0xFFFF:
		buf = append(buf, 0x80|126, byte(n>>8), byte(n))
	default:
		buf = append(buf, 0x80|127)
		buf = binary.BigEndian.AppendUint64(buf, uint64(n))
	}
	var key [4]byte
	if _, err := rand.Read(key[:]); err != nil {
		return fmt.Errorf("wsclient: mask key: %w", err)
	}
	buf = append(buf, key[:]...)
	off := len(buf)
	buf = append(buf, payload...)
	for i := range buf[off:] {
		buf[off+i] ^= key[i&3]
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
		return fmt.Errorf("wsclient: set write deadline: %w", err)
	}
	if _, err := c.conn.Write(buf); err != nil {
		return fmt.Errorf("wsclient: write frame: %w", err)
	}
	return nil
}
