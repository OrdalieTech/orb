// Package tailcat supplies stream transport only. Configuration and persistence
// are explicit; constructing a Node does not start networking.
package tailcat

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	tc "github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

type saved struct {
	Version int                        `json:"version"`
	Key     key.NodePrivate            `json:"key"`
	PSK     tc.PresharedKey            `json:"psk"`
	Region  tailcfg.DERPRegionID       `json:"region"`
	Clients map[string]key.NodePrivate `json:"clients"`
}
type Node struct {
	mu     sync.Mutex
	state  saved
	save   func(json.RawMessage) error
	mapURL string
	server *tc.Server
	// listening: server's address exists only once Listen has returned.
	listening bool
	clients   map[string]*client
	closed    bool
	failed    bool
	relays    relayMap
}

func Open(raw json.RawMessage, save func(json.RawMessage) error, mapURL string) (*Node, error) {
	if save == nil || !strings.HasPrefix(mapURL, "https://") {
		return nil, errors.New("explicit storage and HTTPS relay map required")
	}
	n := &Node{save: save, mapURL: mapURL, clients: map[string]*client{}}
	if len(raw) == 0 {
		n.state = saved{Version: 1, Key: key.NewNode(), PSK: tc.NewPresharedKey(), Clients: map[string]key.NodePrivate{}}
		if err := save(mitosis.JSON(n.state)); err != nil {
			return nil, err
		}
	} else {
		if err := protocol.Decode(raw, &n.state); err != nil {
			return nil, err
		}
		if n.state.Version != 1 || n.state.Key.IsZero() || n.state.PSK.IsZero() || n.state.Clients == nil {
			return nil, errors.New("invalid transport state")
		}
	}
	return n, nil
}
func (n *Node) Listen(ctx context.Context) (net.Listener, error) {
	n.mu.Lock()
	if n.closed || n.failed || n.server != nil {
		n.mu.Unlock()
		return nil, mitosis.Fail("unavailable")
	}
	region := &tc.ConnInfo{RegionID: cmp.Or(n.state.Region, tailcfg.DERPRegionID(-1))}
	if err := region.Expand(ctx, tc.ExpandForServer, tc.DERPMapURL(n.mapURL), &n.relays); err != nil {
		n.mu.Unlock()
		return nil, mitosis.Fail("unavailable")
	}
	selected := region.Region[0].RegionID
	s := &tc.Server{Region: region.Region[0], DERPMapCache: &n.relays, Key: n.state.Key, PresharedKey: n.state.PSK, RegionID: n.state.Region, DERPMapURL: n.mapURL, Logf: func(string, ...any) {}}
	n.server = s
	n.mu.Unlock()
	listener, err := s.Listen(ctx, "tcp", ":443")
	if err != nil {
		n.mu.Lock()
		n.server = nil // tried again later
		n.mu.Unlock()
		return nil, mitosis.Fail("unavailable")
	}
	n.mu.Lock()
	n.state.Region, n.listening = selected, true
	err = n.save(mitosis.JSON(n.state))
	n.mu.Unlock()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}
func (n *Node) Locator() (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.listening || n.state.Region <= 0 {
		return "", mitosis.Fail("unavailable")
	}
	ci, err := tc.ParseAddr(n.server.TailcatAddr())
	if err != nil {
		return "", err
	}
	ci.Region = nil
	ci.RegionID = n.state.Region
	return string(ci.Addr()), nil
}
func parse(locator string) (tc.Addr, error) {
	if len(locator) > 8192 || !strings.HasPrefix(locator, "tc") {
		return "", mitosis.Fail("unauthorized")
	}
	ci, err := tc.ParseAddr(tc.Addr(locator))
	if err != nil || len(ci.Region) != 0 || ci.RegionID <= 0 || ci.RegionID > 65535 || ci.PresharedKey.IsZero() || ci.ServerPublic.IsZero() || ci.ServerDiscoPublic.IsZero() {
		return "", mitosis.Fail("unauthorized")
	}
	return tc.Addr(locator), nil
}
func (n *Node) Dial(ctx context.Context, peer, locator string) (net.Conn, error) {
	addr, err := parse(locator)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	if n.closed || n.failed {
		n.mu.Unlock()
		return nil, mitosis.Fail("unavailable")
	}
	c := n.clients[peer]
	if c != nil && c.Server != addr {
		_ = c.Close()
		delete(n.clients, peer)
		c = nil
	}
	if c == nil {
		if len(n.clients) >= 128 {
			n.mu.Unlock()
			return nil, mitosis.Fail("resource_exhausted")
		}
		k := n.state.Clients[peer]
		if k.IsZero() {
			k = key.NewNode()
			n.state.Clients[peer] = k
			if err = n.save(mitosis.JSON(n.state)); err != nil {
				n.failed = true
				n.mu.Unlock()
				return nil, err
			}
		}
		c = &client{Client: &tc.Client{DERPMapCache: &n.relays, Server: addr, Key: k, DERPMapURL: n.mapURL, Logf: func(string, ...any) {}}}
		n.clients[peer] = c
	}
	c.users++
	n.mu.Unlock()
	// Re-register after a remote restart even when the local client is already up.
	var stream net.Conn
	if _, err = c.Ping(ctx); err == nil {
		stream, err = c.DialTCPPort(ctx, 443)
	}
	if err != nil {
		n.release(peer, c)
		return nil, mitosis.Fail("unavailable")
	}
	return &clientStream{Conn: stream, release: sync.OnceFunc(func() { n.release(peer, c) })}, nil
}

// A client is a whole WireGuard engine (its own relay connection, sockets and goroutines), so
// it lives only while it carries a stream or a dial.
type client struct {
	*tc.Client
	users int
}
type clientStream struct {
	net.Conn
	release func()
}

func (s *clientStream) Close() error {
	err := s.Conn.Close()
	s.release()
	return err
}
func (n *Node) release(peer string, c *client) {
	n.mu.Lock()
	c.users--
	idle := c.users == 0
	if idle && n.clients[peer] == c {
		delete(n.clients, peer)
	}
	n.mu.Unlock()
	if idle {
		_ = c.Close()
	}
}
func (n *Node) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil
	}
	n.closed = true
	for _, c := range n.clients {
		_ = c.Close()
	}
	if n.server != nil {
		return n.server.Close()
	}
	return nil
}

// relayMap keeps a Node's relay map rather than Tailcat's process cache: Tailcat uses it for an
// hour, then revalidates it, and falls back on it while the map server is unreachable, so a dial
// waits on no map fetch and works while the relays do.
type relayMap struct {
	mu   sync.Mutex
	data []byte
	etag string
	at   time.Time
}

func (m *relayMap) Get(string) ([]byte, string, time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data, m.etag, m.at, m.data != nil
}
func (m *relayMap) Put(_ string, data []byte, etag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data, m.etag, m.at = data, etag, time.Now()
	return nil
}
