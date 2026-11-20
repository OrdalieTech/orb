package mitosis

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/mitosis/protocol"
)

const (
	streamMaxPerPeer  = 16
	streamMaxTotal    = 32
	streamChunk       = 256 << 10
	streamWindow      = 256 << 10
	streamPoll        = 15 * time.Second
	streamIdle        = 2 * time.Minute
	streamDialTimeout = 15 * time.Second
)

// relayedStream is one single-hop raw byte pipe a group member relays between two other members
// (e.g. a browser tab reaching a Tailcat peer). The relay carries opaque TLS ciphertext; the
// endpoints authenticate and authorize each other directly inside the stream.
type relayedStream struct {
	id, owner, target string
	conn              net.Conn
	idle              *time.Timer
	stopWatch         func() bool
	writeMu           sync.Mutex
	mu                sync.Mutex
	buf               []byte
	eof               bool
	closed            bool
	wake              chan struct{}
}

func (s *relayedStream) signal() {
	close(s.wake)
	s.wake = make(chan struct{})
}

func (s *relayedStream) pump() {
	tmp := make([]byte, 32<<10)
	for {
		s.mu.Lock()
		for len(s.buf) >= streamWindow && !s.closed {
			wake := s.wake
			s.mu.Unlock()
			<-wake
			s.mu.Lock()
		}
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		n, err := s.conn.Read(tmp)
		s.mu.Lock()
		if n > 0 {
			s.buf = append(s.buf, tmp[:n]...)
		}
		if err != nil {
			s.eof = true
		}
		s.signal()
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (s *relayedStream) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed, s.eof = true, true
	s.signal()
	s.mu.Unlock()
	if s.idle != nil {
		s.idle.Stop()
	}
	if s.stopWatch != nil {
		s.stopWatch()
	}
	_ = s.conn.Close()
}

func (b *Mitosis) setDial(d func(ctx context.Context, peer, locator string) (net.Conn, error)) {
	b.mu.Lock()
	b.dial = d
	b.mu.Unlock()
}

func (b *Mitosis) closeStream(id string) {
	b.mu.Lock()
	s := b.streams[id]
	delete(b.streams, id)
	b.mu.Unlock()
	if s != nil {
		s.close()
	}
}

func (b *Mitosis) takePeerStreamsLocked(peer string) []*relayedStream {
	var out []*relayedStream
	for id, s := range b.streams {
		if s.owner == peer || s.target == peer {
			delete(b.streams, id)
			out = append(out, s)
		}
	}
	return out
}

func (b *Mitosis) handleStream(ctx context.Context, caller, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "stream.open":
		return b.streamOpen(ctx, caller, params)
	case "stream.write":
		return b.streamWrite(caller, params)
	case "stream.read":
		return b.streamRead(ctx, caller, params)
	case "stream.close":
		return b.streamClose(caller, params)
	default:
		return nil, &protocol.RPCError{Code: -32601, Message: "method_not_found"}
	}
}

func (b *Mitosis) streamOpen(ctx context.Context, caller string, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		PeerID string `json:"peer_id"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	if _, err := ParsePeerID(p.PeerID); err != nil || p.PeerID == b.PeerID() || p.PeerID == caller {
		return nil, Fail("invalid_params")
	}
	callerP := Principal{PeerID: caller, Subject: Subject{Kind: "controller"}}
	targetP := Principal{PeerID: p.PeerID, Subject: Subject{Kind: "controller"}}
	b.mu.Lock()
	allowed := b.groupMember(callerP) && b.groupMember(targetP)
	dial := b.dial
	total, owned := len(b.streams), 0
	for _, s := range b.streams {
		if s.owner == caller {
			owned++
		}
	}
	b.mu.Unlock()
	if !allowed {
		return nil, Fail("unauthorized")
	}
	if dial == nil {
		return nil, Fail("unavailable")
	}
	if total >= streamMaxTotal || owned >= streamMaxPerPeer {
		return nil, Fail("resource_exhausted")
	}
	locator, err := b.PeerLocator(p.PeerID)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, streamDialTimeout)
	conn, err := dial(dialCtx, p.PeerID, locator)
	cancel()
	if err != nil {
		return nil, Fail("unavailable")
	}
	id := protocol.NewID()
	s := &relayedStream{id: id, owner: caller, target: p.PeerID, conn: conn, wake: make(chan struct{})}
	b.mu.Lock()
	if !b.groupMember(callerP) || !b.groupMember(targetP) {
		b.mu.Unlock()
		_ = conn.Close()
		return nil, Fail("unauthorized")
	}
	owned = 0
	for _, existing := range b.streams {
		if existing.owner == caller {
			owned++
		}
	}
	if len(b.streams) >= streamMaxTotal || owned >= streamMaxPerPeer {
		b.mu.Unlock()
		_ = conn.Close()
		return nil, Fail("resource_exhausted")
	}
	b.streams[id] = s
	s.idle = time.AfterFunc(streamIdle, func() { b.closeStream(id) })
	s.stopWatch = context.AfterFunc(ctx, func() { b.closeStream(id) })
	b.mu.Unlock()
	go s.pump()
	return JSON(map[string]string{"stream_id": id}), nil
}

func (b *Mitosis) streamLookup(caller, id string) (*relayedStream, error) {
	if !protocol.ValidID(id) {
		return nil, Fail("not_found")
	}
	b.mu.Lock()
	s := b.streams[id]
	if s == nil || s.owner != caller {
		b.mu.Unlock()
		return nil, Fail("not_found")
	}
	ok := b.groupMember(Principal{PeerID: caller, Subject: Subject{Kind: "controller"}}) &&
		b.groupMember(Principal{PeerID: s.target, Subject: Subject{Kind: "controller"}})
	if !ok {
		delete(b.streams, id)
		b.mu.Unlock()
		s.close()
		return nil, Fail("unauthorized")
	}
	b.mu.Unlock()
	return s, nil
}

func (b *Mitosis) streamWrite(caller string, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		ID   string `json:"stream_id"`
		Data []byte `json:"data"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	if len(p.Data) > streamChunk {
		return nil, Fail("resource_exhausted")
	}
	s, err := b.streamLookup(caller, p.ID)
	if err != nil {
		return nil, err
	}
	s.idle.Reset(streamIdle)
	if len(p.Data) > 0 {
		s.writeMu.Lock()
		_, err = s.conn.Write(p.Data)
		s.writeMu.Unlock()
		if err != nil {
			b.closeStream(p.ID)
			return nil, Fail("unavailable")
		}
	}
	return JSON(struct{}{}), nil
}

func (b *Mitosis) streamRead(ctx context.Context, caller string, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		ID string `json:"stream_id"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	s, err := b.streamLookup(caller, p.ID)
	if err != nil {
		return nil, err
	}
	s.idle.Reset(streamIdle)
	timer := time.NewTimer(streamPoll)
	defer timer.Stop()
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			n := min(len(s.buf), streamChunk)
			data := slices.Clone(s.buf[:n])
			s.buf = s.buf[n:]
			eof := s.eof && len(s.buf) == 0
			s.signal()
			s.mu.Unlock()
			if eof {
				b.closeStream(p.ID)
			}
			return JSON(struct {
				Data []byte `json:"data,omitempty"`
				EOF  bool   `json:"eof,omitempty"`
			}{data, eof}), nil
		}
		if s.eof {
			s.mu.Unlock()
			b.closeStream(p.ID)
			return JSON(struct {
				EOF bool `json:"eof"`
			}{true}), nil
		}
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-timer.C:
			return JSON(struct{}{}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (b *Mitosis) streamClose(caller string, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		ID string `json:"stream_id"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	if !protocol.ValidID(p.ID) {
		return JSON(struct{}{}), nil
	}
	b.mu.Lock()
	s := b.streams[p.ID]
	if s != nil && s.owner == caller {
		delete(b.streams, p.ID)
	} else {
		s = nil
	}
	b.mu.Unlock()
	if s != nil {
		s.close()
	}
	return JSON(struct{}{}), nil
}

// tunnelConn presents a relayed stream.* session over a peer's *protocol.Conn as a net.Conn so
// Mitosis.Connect runs its full pinned TLS 1.3 handshake and protocol end to end inside it.
type tunnelConn struct {
	conn        *protocol.Conn
	id          string
	ctx         context.Context
	cancel      context.CancelFunc
	once        sync.Once
	readMu      sync.Mutex
	buf         []byte
	eof         bool
	writeMu     sync.Mutex
	dlMu        sync.Mutex
	readDL      time.Time
	writeDL     time.Time
	readCancel  context.CancelFunc
	writeCancel context.CancelFunc
}

func newTunnelConn(conn *protocol.Conn, id string) *tunnelConn {
	ctx, cancel := context.WithCancel(context.Background())
	t := &tunnelConn{conn: conn, id: id, ctx: ctx, cancel: cancel}
	go func() {
		select {
		case <-conn.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return t
}

func (t *tunnelConn) opCtx(isRead bool) (context.Context, func(), error) {
	t.dlMu.Lock()
	dl := t.writeDL
	if isRead {
		dl = t.readDL
	}
	if !dl.IsZero() && !time.Now().Before(dl) {
		t.dlMu.Unlock()
		return nil, func() {}, os.ErrDeadlineExceeded
	}
	ctx, cancel := context.WithCancel(t.ctx)
	var timer *time.Timer
	if !dl.IsZero() {
		timer = time.AfterFunc(time.Until(dl), cancel)
	}
	if isRead {
		t.readCancel = cancel
	} else {
		t.writeCancel = cancel
	}
	t.dlMu.Unlock()
	return ctx, func() {
		if timer != nil {
			timer.Stop()
		}
		cancel()
		t.dlMu.Lock()
		if isRead {
			t.readCancel = nil
		} else {
			t.writeCancel = nil
		}
		t.dlMu.Unlock()
	}, nil
}

func (t *tunnelConn) deadlineErr(isRead bool, err error) error {
	t.dlMu.Lock()
	dl := t.writeDL
	if isRead {
		dl = t.readDL
	}
	t.dlMu.Unlock()
	if !dl.IsZero() && !time.Now().Before(dl) {
		return os.ErrDeadlineExceeded
	}
	if t.ctx.Err() != nil {
		return net.ErrClosed
	}
	return err
}

func (t *tunnelConn) Read(p []byte) (int, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()
	if len(t.buf) > 0 {
		n := copy(p, t.buf)
		t.buf = t.buf[n:]
		return n, nil
	}
	if t.eof {
		return 0, io.EOF
	}
	for {
		opCtx, done, err := t.opCtx(true)
		if err != nil {
			return 0, err
		}
		callCtx, cancel := context.WithTimeout(opCtx, streamPoll+10*time.Second)
		var res struct {
			Data []byte `json:"data"`
			EOF  bool   `json:"eof"`
		}
		err = t.conn.Call(callCtx, "stream.read", map[string]string{"stream_id": t.id}, &res)
		cancel()
		done()
		if err != nil {
			err = t.deadlineErr(true, err)
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return 0, err
			}
			t.eof = true
			if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
				return 0, io.EOF
			}
			return 0, err
		}
		if len(res.Data) > 0 {
			if res.EOF {
				t.eof = true
			}
			n := copy(p, res.Data)
			if n < len(res.Data) {
				t.buf = append(t.buf[:0], res.Data[n:]...)
			}
			return n, nil
		}
		if res.EOF {
			t.eof = true
			return 0, io.EOF
		}
	}
}

func (t *tunnelConn) Write(p []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	written := 0
	for len(p) > 0 {
		opCtx, done, err := t.opCtx(false)
		if err != nil {
			return written, err
		}
		chunk := p[:min(len(p), streamChunk)]
		callCtx, cancel := context.WithTimeout(opCtx, 20*time.Second)
		err = t.conn.Call(callCtx, "stream.write", map[string]any{"stream_id": t.id, "data": chunk}, nil)
		cancel()
		done()
		if err != nil {
			return written, t.deadlineErr(false, err)
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (t *tunnelConn) Close() error {
	t.once.Do(func() {
		t.cancel()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = t.conn.Call(ctx, "stream.close", map[string]string{"stream_id": t.id}, nil)
		}()
	})
	return nil
}

type tunnelAddr string

func (a tunnelAddr) Network() string { return "mitosis-stream" }
func (a tunnelAddr) String() string  { return string(a) }

func (t *tunnelConn) LocalAddr() net.Addr  { return tunnelAddr("local") }
func (t *tunnelConn) RemoteAddr() net.Addr { return tunnelAddr(t.id) }

func (t *tunnelConn) SetDeadline(dl time.Time) error {
	_ = t.SetReadDeadline(dl)
	return t.SetWriteDeadline(dl)
}

func (t *tunnelConn) SetReadDeadline(dl time.Time) error {
	t.dlMu.Lock()
	t.readDL = dl
	if t.readCancel != nil && !dl.IsZero() && !time.Now().Before(dl) {
		t.readCancel()
	}
	t.dlMu.Unlock()
	return nil
}

func (t *tunnelConn) SetWriteDeadline(dl time.Time) error {
	t.dlMu.Lock()
	t.writeDL = dl
	if t.writeCancel != nil && !dl.IsZero() && !time.Now().Before(dl) {
		t.writeCancel()
	}
	t.dlMu.Unlock()
	return nil
}
