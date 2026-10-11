package serve

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Pipe is a transport over a channel the harness integration already has: it
// hands each message that arrives to Deliver, and write sends one out in the
// harness's own framing. One Pipe carries one connection.
type Pipe struct {
	write  func(json.RawMessage) error
	mu     sync.Mutex
	queue  []jsonrpc.Message
	ready  chan struct{}
	closed chan struct{}
	once   sync.Once
}

func NewPipe(write func(json.RawMessage) error) *Pipe {
	return &Pipe{write: write, ready: make(chan struct{}, 1), closed: make(chan struct{})}
}

// Deliver queues a message from the harness; it never waits on the server, so
// the harness's reader keeps reading while a tool runs.
func (p *Pipe) Deliver(message json.RawMessage) error {
	decoded, err := jsonrpc.DecodeMessage(message)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.queue = append(p.queue, decoded)
	p.mu.Unlock()
	select {
	case p.ready <- struct{}{}:
	default:
	}
	return nil
}

func (p *Pipe) Connect(context.Context) (mcp.Connection, error) { return p, nil }

func (p *Pipe) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		p.mu.Lock()
		if len(p.queue) > 0 {
			message := p.queue[0]
			p.queue = p.queue[1:]
			p.mu.Unlock()
			return message, nil
		}
		p.mu.Unlock()
		select {
		case <-p.ready:
		case <-p.closed:
			return nil, io.EOF
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (p *Pipe) Write(_ context.Context, message jsonrpc.Message) error {
	encoded, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		return err
	}
	return p.write(encoded)
}

func (p *Pipe) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func (p *Pipe) SessionID() string { return "" }
