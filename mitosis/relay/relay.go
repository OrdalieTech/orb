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

// The host calls: Open names a request and carries the start of its body, Write the rest by
// offset (a frame holds 1 MiB), and the request goes once its body is whole; Read returns what
// came back from an offset on, waiting up to poll for more, and Close ends it.
const (
	Open  = "host.fetch"
	Write = "host.fetch.write"
	Read  = "host.fetch.read"
	Close = "host.fetch.close"
)

const (
	maxFetches = 16
	window     = 1 << 20   // what a device reads ahead of its peer
	maxChunk   = 256 << 10 // the most one call carries of a body
	maxBody    = 64 << 20
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
// resolves to: the host builds it, as the core dials nothing. Server takes https URLs only and
// follows redirects to https only.
type Server struct {
	Client  *http.Client
	mu      sync.Mutex
	fetches map[string]*fetch
}

type fetch struct {
	cancel context.CancelFunc
	start  func(body []byte) // until the body is whole
	body   []byte
	length int64
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
		Length int64       `json:"length,omitempty"`
		Offset int64       `json:"offset,omitempty"`
	}
	if err := protocol.Decode(params, &q); err != nil {
		return nil, err
	}
	if method == Open {
		return s.open(q.Method, q.URL, q.Header, q.Body, max(q.Length, int64(len(q.Body))))
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
	case Write:
		return mitosis.JSON(struct{}{}), f.write(q.Offset, q.Body)
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
		data := slices.Clone(f.out[:min(len(f.out), maxChunk)])
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

func (s *Server) open(method, raw string, header http.Header, body []byte, length int64) (json.RawMessage, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || s.Client == nil {
		return nil, mitosis.Fail("invalid_params")
	}
	if length > maxBody {
		return nil, mitosis.Fail("resource_exhausted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err = http.NewRequestWithContext(ctx, cmp.Or(method, http.MethodGet), u.String(), nil); err != nil {
		cancel()
		return nil, mitosis.Fail("invalid_params")
	}
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
	f := &fetch{cancel: cancel, length: length, wake: make(chan struct{})}
	f.start = func(body []byte) {
		request, _ := http.NewRequestWithContext(ctx, cmp.Or(method, http.MethodGet), u.String(), bytes.NewReader(body))
		request.Header = header.Clone()
		f.run(httpsOnly(s.Client), request)
	}
	f.idle = time.AfterFunc(idle, func() { s.end(id) })
	s.fetches[id] = f
	if err = f.write(0, body); err != nil {
		return nil, err
	}
	return mitosis.JSON(map[string]string{"fetch_id": id}), nil
}

// write adds the body's bytes at offset, once: a retried write that brings what f has is done.
// The request goes when the body is whole.
func (f *fetch) write(offset int64, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	have := int64(len(f.body))
	switch {
	case f.start == nil || offset+int64(len(data)) <= have:
	case offset != have || have+int64(len(data)) > f.length:
		return mitosis.Fail("invalid_params")
	default:
		f.body = append(f.body, data...)
	}
	if f.start != nil && int64(len(f.body)) == f.length {
		go f.start(f.body)
		f.start, f.body = nil, nil
	}
	return nil
}

// httpsOnly is client following redirects to https only: one to http would send the body and the
// credentials again in the clear.
func httpsOnly(client *http.Client) *http.Client {
	only := *client
	only.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		switch {
		case request.URL.Scheme != "https":
			return errors.New("relay: a redirect leaves https")
		case client.CheckRedirect != nil:
			return client.CheckRedirect(request, via)
		case len(via) >= 10:
			return errors.New("relay: stopped after 10 redirects")
		}
		return nil
	}
	return &only
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
	first := body[:min(len(body), maxChunk)]
	device, id, err := t.open(ctx, map[string]any{"url": request.URL.String(), "method": request.Method, "header": request.Header, "body": first, "length": len(body)})
	if err != nil {
		return nil, err
	}
	b := &reader{ctx: ctx, t: t, device: device, id: id}
	for at := len(first); at < len(body) && err == nil; at += maxChunk {
		err = t.Call(ctx, device, Write, map[string]any{"fetch_id": id, "offset": at, "body": body[at:min(at+maxChunk, len(body))]}, nil)
	}
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
		if code := mitosis.Code(err); code != "unauthorized" && code != "not_found" && code != "not_a_peer" && code != "method_not_found" {
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
