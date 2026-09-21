package native

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
)

type Auth struct {
	Credential string `json:"credential"`
	InstanceID string `json:"instance_id,omitempty"`
	BootID     string `json:"boot_id,omitempty"`
}
type Welcome struct {
	Generation string `json:"generation,omitempty"`
}

func Listen(ctx context.Context, path string, b *bridge.Bridge, adminSecret string, admin protocol.Handler, outbound protocol.Handler) (func(), error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute IPC path required")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("IPC path is not a socket")
		}
		if active, e := net.DialTimeout("unix", path, time.Second); e == nil {
			_ = active.Close()
			return nil, errors.New("IPC endpoint already active")
		}
		if err = os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	var mu sync.Mutex
	connections := map[*net.UnixConn]bool{}
	closed := false
	done := make(chan struct{})
	slots := make(chan struct{}, 128)
	closeAll := func() {
		mu.Lock()
		if closed {
			mu.Unlock()
			return
		}
		closed = true
		close(done)
		_ = listener.Close()
		for c := range connections {
			_ = c.Close()
		}
		mu.Unlock()
	}
	go func() {
		select {
		case <-ctx.Done():
			closeAll()
		case <-done:
		}
	}()
	go func() {
		for {
			c, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				_ = c.Close()
				continue
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				_ = c.Close()
				<-slots
				return
			}
			connections[c] = true
			mu.Unlock()
			go func() {
				defer func() { _ = c.Close(); mu.Lock(); delete(connections, c); mu.Unlock(); <-slots }()
				if !sameUser(c) {
					return
				}
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				raw, err := protocol.Read(c)
				if err != nil {
					return
				}
				var auth Auth
				if protocol.Decode(raw, &auth) != nil {
					return
				}
				var rpc *protocol.Conn
				generation := ""
				handler := admin
				if adminSecret != "" {
					if subtle.ConstantTimeCompare([]byte(auth.Credential), []byte(adminSecret)) != 1 || auth.InstanceID != "" || auth.BootID != "" {
						return
					}
				} else {
					if !protocol.ValidID(auth.InstanceID) || !protocol.ValidID(auth.BootID) {
						return
					}
					proxy := &pendingEndpoint{ready: make(chan struct{})}
					generation, err = b.Attach(auth.InstanceID, auth.Credential, auth.BootID, proxy)
					if err != nil {
						return
					}
					defer b.Detach(auth.InstanceID, generation)
					handler = func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
						switch method {
						case "authorize":
							var r connect.Request
							if err := protocol.Decode(params, &r); err != nil {
								return nil, err
							}
							if r.Call.InstanceID != auth.InstanceID {
								return nil, connect.Fail("unauthorized")
							}
							return connect.JSON(b.Authorize(r)), nil
						case "outbound":
							if outbound == nil {
								return nil, connect.Fail("unauthorized")
							}
							return outbound(context.WithValue(ctx, instanceKey{}, auth.InstanceID), method, params)
						default:
							return nil, connect.Fail("unauthorized")
						}
					}
					if protocol.Write(c, connect.JSON(Welcome{Generation: generation})) != nil {
						return
					}
					_ = c.SetDeadline(time.Time{})
					rpc = protocol.NewConn(c, redacted(handler))
					proxy.set(rpc)
				}
				if rpc == nil {
					if protocol.Write(c, connect.JSON(Welcome{})) != nil {
						return
					}
					_ = c.SetDeadline(time.Time{})
					rpc = protocol.NewConn(c, redacted(handler))
				}
				<-rpc.Done()
			}()
		}
	}()
	return closeAll, nil
}

type instanceKey struct{}

func InstanceFromContext(ctx context.Context) string {
	id, _ := ctx.Value(instanceKey{}).(string)
	return id
}

type pendingEndpoint struct {
	mu        sync.Mutex
	ready     chan struct{}
	conn      *protocol.Conn
	closed    bool
	readyOnce sync.Once
}

func (p *pendingEndpoint) set(c *protocol.Conn) {
	p.mu.Lock()
	p.conn = c
	closed := p.closed
	p.readyOnce.Do(func() { close(p.ready) })
	p.mu.Unlock()
	if closed {
		_ = c.Close()
	}
}
func (p *pendingEndpoint) Invoke(ctx context.Context, m string, b json.RawMessage) (json.RawMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.ready:
	}
	p.mu.Lock()
	c := p.conn
	p.mu.Unlock()
	if c == nil {
		return nil, connect.Fail("unavailable")
	}
	return c.Invoke(ctx, m, b)
}
func (p *pendingEndpoint) Close() error {
	p.mu.Lock()
	p.closed = true
	p.readyOnce.Do(func() { close(p.ready) })
	c := p.conn
	p.mu.Unlock()
	if c != nil {
		return c.Close()
	}
	return nil
}
func redacted(h protocol.Handler) protocol.Handler {
	if h == nil {
		return nil
	}
	return func(ctx context.Context, m string, p json.RawMessage) (json.RawMessage, error) {
		r, err := h(ctx, m, p)
		if err == nil {
			return r, nil
		}
		var e *protocol.RPCError
		if errors.As(err, &e) {
			return nil, e
		}
		return nil, &protocol.RPCError{Code: -32000, Message: connect.Code(err)}
	}
}
func Dial(ctx context.Context, path string, auth Auth, handler protocol.Handler, generation func(string) error) (*protocol.Conn, error) {
	d := net.Dialer{}
	raw, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	c := raw.(*net.UnixConn)
	if !sameUser(c) {
		_ = c.Close()
		return nil, connect.Fail("unauthorized")
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if err = protocol.Write(c, connect.JSON(auth)); err != nil {
		_ = c.Close()
		return nil, err
	}
	b, err := protocol.Read(c)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	var welcome Welcome
	if err = protocol.Decode(b, &welcome); err != nil {
		_ = c.Close()
		return nil, err
	}
	if generation != nil {
		if err = generation(welcome.Generation); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	_ = c.SetDeadline(time.Time{})
	return protocol.NewConn(c, redacted(handler)), nil
}
