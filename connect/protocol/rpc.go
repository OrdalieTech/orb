package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

type Handler func(context.Context, string, json.RawMessage) (json.RawMessage, error)
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

type frame struct {
	Version string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type Conn struct {
	frameLimit atomic.Int64
	stream     io.ReadWriteCloser
	handler    Handler
	ctx        context.Context
	cancel     context.CancelFunc
	once       sync.Once
	mu         sync.Mutex
	pending    map[string]chan frame
	incoming   map[string]bool
	queued     int
	output     chan []byte
}

func NewConn(stream io.ReadWriteCloser, handler Handler) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{stream: stream, handler: handler, ctx: ctx, cancel: cancel, pending: map[string]chan frame{}, incoming: map[string]bool{}, output: make(chan []byte, MaxPending*2)}
	c.frameLimit.Store(MaxFrame)
	go c.read()
	go c.write()
	return c
}
func (c *Conn) Done() <-chan struct{} { return c.ctx.Done() }
func (c *Conn) Close() error          { c.once.Do(func() { c.cancel(); _ = c.stream.Close() }); return nil }
func (c *Conn) Invoke(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	var r json.RawMessage
	err := c.Call(ctx, method, params, &r)
	return r, err
}
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := NewID()
	ch := make(chan frame, 1)
	c.mu.Lock()
	if len(c.pending) >= MaxPending {
		c.mu.Unlock()
		return &RPCError{Code: -32000, Message: "resource_exhausted"}
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err = c.send(frame{Version: "2.0", ID: id, Method: method, Params: b}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return io.ErrClosedPipe
	case r := <-ch:
		if r.Error != nil {
			return r.Error
		}
		if result != nil {
			return json.Unmarshal(r.Result, result)
		}
		return nil
	}
}
func (c *Conn) send(f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(b) > int(c.frameLimit.Load()) {
		return errors.New("resource_exhausted")
	}
	c.mu.Lock()
	if c.queued+len(b) > MaxOutput {
		c.mu.Unlock()
		_ = c.Close()
		return io.ErrShortBuffer
	}
	c.queued += len(b)
	c.mu.Unlock()
	select {
	case <-c.ctx.Done():
		return io.ErrClosedPipe
	case c.output <- b:
		return nil
	default:
		_ = c.Close()
		return io.ErrShortBuffer
	}
}
func (c *Conn) write() {
	defer func() { _ = c.Close() }()
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.output:
			if Write(c.stream, b) != nil {
				return
			}
			c.mu.Lock()
			c.queued -= len(b)
			c.mu.Unlock()
		}
	}
}
func (c *Conn) read() {
	defer func() { _ = c.Close() }()
	for {
		b, err := Read(c.stream)
		if err != nil {
			return
		}
		var f frame
		if len(b) > int(c.frameLimit.Load()) || Decode(b, &f) != nil || f.Version != "2.0" || f.ID == "" {
			return
		}
		if f.Method == "" {
			if (f.Error == nil) == (len(f.Result) == 0) || len(f.Params) != 0 {
				return
			}
			c.mu.Lock()
			ch := c.pending[f.ID]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- f:
				default:
					return
				}
			}
			continue
		}
		if f.Error != nil || len(f.Result) != 0 {
			return
		}
		if len(f.Params) == 0 {
			f.Params = json.RawMessage(`{}`)
		}
		if _, err = Canonical(f.Params); err != nil {
			return
		}
		c.mu.Lock()
		if c.incoming[f.ID] || len(c.incoming) >= MaxPending {
			c.mu.Unlock()
			return
		}
		c.incoming[f.ID] = true
		c.mu.Unlock()
		go c.handle(f)
	}
}
func (c *Conn) handle(f frame) {
	defer func() { c.mu.Lock(); delete(c.incoming, f.ID); c.mu.Unlock() }()
	r := frame{Version: "2.0", ID: f.ID}
	var err error
	if c.handler == nil {
		err = &RPCError{Code: -32601, Message: "method_not_found"}
	} else {
		r.Result, err = c.handler(c.ctx, f.Method, f.Params)
	}
	if err != nil {
		var e *RPCError
		if errors.As(err, &e) {
			r.Error = e
		} else {
			r.Error = &RPCError{Code: -32000, Message: "unavailable"}
		}
		r.Result = nil
	} else if len(r.Result) == 0 {
		r.Result = json.RawMessage(`null`)
	}
	if err := c.send(r); err != nil {
		r.Result = nil
		r.Error = &RPCError{Code: -32000, Message: "resource_exhausted"}
		if c.send(r) != nil {
			_ = c.Close()
		}
	}
}

func (c *Conn) SetFrameLimit(limit int) {
	c.frameLimit.Store(int64(min(MaxFrame, max(1024, limit))))
}

type pageLimitKey struct{}

func WithPageLimit(ctx context.Context, limit int) context.Context {
	return context.WithValue(ctx, pageLimitKey{}, min(MaxPage, max(1, limit)))
}
func PageLimit(ctx context.Context) int {
	if limit, ok := ctx.Value(pageLimitKey{}).(int); ok {
		return limit
	}
	return MaxPage
}
