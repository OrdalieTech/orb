// Package relay carries HTTP through a Mitosis peer. A device serves host.fetch to the peers it
// grants its machine (Server), and a peer that cannot reach an origin itself, such as a browser
// tab refused by an API that sends no CORS headers, sends its requests that way (Transport). The
// response streams back as a window the peer reads by offset, so a retried read loses nothing.
package relay

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// The host calls: Open starts a request and names it, Read returns what came from an offset on,
// waiting up to poll for more, and Close ends it.
const (
	Open  = "host.fetch"
	Read  = "host.fetch.read"
	Close = "host.fetch.close"
)

const (
	maxFetches = 16
	window     = 1 << 20   // what a device reads ahead of its peer
	maxRead    = 256 << 10 // the most one read returns
	poll       = 15 * time.Second
	idle       = 2 * time.Minute // a request its peer stopped reading ends
)

// Public reports whether ip is on the public internet: neither this machine, a private or shared
// network (Tailscale's 100.64.0.0/10 too), nor a link-local, multicast or unspecified address.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64}), 10).Contains(ip)
}

// Server answers the host calls. Client must dial only Public addresses, whatever a name
// resolves to: the host builds it, as the core dials nothing. Server takes https URLs only.
type Server struct {
	Client  *http.Client
	mu      sync.Mutex
	fetches map[string]*fetch
}

type fetch struct {
	cancel context.CancelFunc
	idle   *time.Timer
	mu     sync.Mutex
	status int
	header http.Header
	told   bool   // a read returned the status
	out    []byte // what the peer has not acknowledged yet
	end    int64  // the offset just past out
	failed string
	done   bool
	wake   chan struct{}
}

// signal wakes every wait on f; f.mu is held.
func (f *fetch) signal() {
	close(f.wake)
	f.wake = make(chan struct{})
}

func (s *Server) Handle(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	var q struct {
		ID     string      `json:"fetch_id,omitempty"`
		URL    string      `json:"url,omitempty"`
		Method string      `json:"method,omitempty"`
		Header http.Header `json:"header,omitempty"`
		Body   []byte      `json:"body,omitempty"`
		Offset int64       `json:"offset,omitempty"`
	}
	if err := protocol.Decode(params, &q); err != nil {
		return nil, err
	}
	if method == Open {
		return s.open(q.Method, q.URL, q.Header, q.Body)
	}
	s.mu.Lock()
	f := s.fetches[q.ID]
	s.mu.Unlock()
	if f == nil {
		return nil, mitosis.Fail("not_found")
	}
	f.idle.Reset(idle)
	switch method {
	case Close:
		s.end(q.ID)
		return mitosis.JSON(struct{}{}), nil
	case Read:
	default:
		return nil, mitosis.Fail("method_not_found")
	}
	timer := time.NewTimer(poll)
	defer timer.Stop()
	for {
		f.mu.Lock()
		start := f.end - int64(len(f.out))
		if q.Offset < start || q.Offset > f.end {
			f.mu.Unlock()
			return nil, mitosis.Fail("invalid_params")
		}
		if q.Offset > start { // the peer has what came before
			f.out = f.out[q.Offset-start:]
			f.signal()
		}
		data := slices.Clone(f.out[:min(len(f.out), maxRead)])
		c := chunk{Data: data, Done: f.done && len(data) == len(f.out), Error: f.failed}
		if f.status != 0 {
			c.Status, c.Header = f.status, f.header
		}
		ready := c.Error != "" || c.Status != 0 && (!f.told || len(data) > 0 || c.Done)
		f.told = f.told || c.Status != 0 && ready
		wake := f.wake
		f.mu.Unlock()
		if ready {
			return mitosis.JSON(c), nil
		}
		select {
		case <-wake:
		case <-timer.C:
			return mitosis.JSON(c), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (s *Server) open(method, raw string, header http.Header, body []byte) (json.RawMessage, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || s.Client == nil {
		return nil, mitosis.Fail("invalid_params")
	}
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, cmp.Or(method, http.MethodGet), u.String(), bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, mitosis.Fail("invalid_params")
	}
	request.Header = header.Clone()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.fetches) >= maxFetches {
		cancel()
		return nil, mitosis.Fail("resource_exhausted")
	}
	if s.fetches == nil {
		s.fetches = map[string]*fetch{}
	}
	id := protocol.NewID()
	f := &fetch{cancel: cancel, wake: make(chan struct{})}
	f.idle = time.AfterFunc(idle, func() { s.end(id) })
	s.fetches[id] = f
	go f.run(s.Client, request)
	return mitosis.JSON(map[string]string{"fetch_id": id}), nil
}

func (f *fetch) run(client *http.Client, request *http.Request) {
	response, err := client.Do(request)
	f.mu.Lock()
	if err != nil {
		f.failed, f.done = err.Error(), true
		f.signal()
		f.mu.Unlock()
		return
	}
	defer func() { _ = response.Body.Close() }()
	f.status, f.header = response.StatusCode, response.Header
	f.signal()
	buf := make([]byte, 32<<10)
	for {
		for len(f.out) >= window { // the peer reads slower than the origin sends
			wake := f.wake
			f.mu.Unlock()
			select {
			case <-wake:
			case <-request.Context().Done():
				return
			}
			f.mu.Lock()
		}
		f.mu.Unlock()
		n, err := response.Body.Read(buf)
		f.mu.Lock()
		f.out = append(f.out, buf[:n]...)
		f.end += int64(n)
		if err != nil {
			f.done = true
			if !errors.Is(err, io.EOF) {
				f.failed = err.Error()
			}
		}
		f.signal()
		if err != nil {
			f.mu.Unlock()
			return
		}
	}
}

func (s *Server) end(id string) {
	s.mu.Lock()
	f := s.fetches[id]
	delete(s.fetches, id)
	s.mu.Unlock()
	if f != nil {
		f.idle.Stop()
		f.cancel()
	}
}

type chunk struct {
	Status int         `json:"status,omitempty"`
	Header http.Header `json:"header,omitempty"`
	Data   []byte      `json:"data,omitempty"`
	Done   bool        `json:"done,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// Transport sends each request through a device: Devices lists the peers to try, and Call makes
// a Mitosis call to one. The device that served last is tried first; one that refuses the call
// (it grants this peer no machine, or predates host.fetch) passes to the next.
type Transport struct {
	Devices func(ctx context.Context) []string
	Call    func(ctx context.Context, device, method string, params, result any) error
	mu      sync.Mutex
	last    string
}

func (t *Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		_ = request.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	ctx := request.Context()
	device, id, err := t.open(ctx, map[string]any{"url": request.URL.String(), "method": request.Method, "header": request.Header, "body": body})
	if err != nil {
		return nil, err
	}
	b := &reader{ctx: ctx, t: t, device: device, id: id}
	var c chunk
	for err == nil && c.Status == 0 {
		c, err = b.next()
	}
	if err != nil {
		_ = b.Close()
		return nil, err
	}
	response := &http.Response{Status: strconv.Itoa(c.Status) + " " + http.StatusText(c.Status), StatusCode: c.Status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: c.Header, Body: b, ContentLength: -1, Request: request}
	if n, err := strconv.ParseInt(c.Header.Get("Content-Length"), 10, 64); err == nil && n >= 0 {
		response.ContentLength = n
	}
	return response, nil
}

func (t *Transport) open(ctx context.Context, params map[string]any) (device, id string, err error) {
	devices := t.Devices(ctx)
	t.mu.Lock()
	if i := slices.Index(devices, t.last); i > 0 {
		devices[0], devices[i] = devices[i], devices[0]
	}
	t.mu.Unlock()
	err = mitosis.Fail("unavailable")
	for _, device := range devices {
		var opened struct {
			ID string `json:"fetch_id"`
		}
		if err = t.Call(ctx, device, Open, params, &opened); err == nil {
			t.mu.Lock()
			t.last = device
			t.mu.Unlock()
			return device, opened.ID, nil
		}
		// Anything else may have reached the device, which may have sent the request already.
		if code := mitosis.Code(err); code != "unauthorized" && code != "not_found" && code != "method_not_found" {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("no online device of this group relays requests: %w", err)
}

type reader struct {
	ctx        context.Context
	t          *Transport
	device, id string
	offset     int64
	buf        []byte
	done       bool
	err        error
	closed     sync.Once
}

func (b *reader) next() (chunk, error) {
	var c chunk
	if err := b.t.Call(b.ctx, b.device, Read, map[string]any{"fetch_id": b.id, "offset": b.offset}, &c); err != nil {
		return c, err
	}
	if c.Error != "" {
		return c, errors.New(c.Error)
	}
	b.buf = append(b.buf, c.Data...)
	b.offset += int64(len(c.Data))
	b.done = c.Done
	return c, nil
}

func (b *reader) Read(p []byte) (int, error) {
	for len(b.buf) == 0 {
		if b.done {
			return 0, io.EOF
		}
		if b.err != nil {
			return 0, b.err
		}
		_, b.err = b.next()
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}

func (b *reader) Close() error {
	b.closed.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = b.t.Call(ctx, b.device, Close, map[string]string{"fetch_id": b.id}, nil)
		}()
	})
	return nil
}
