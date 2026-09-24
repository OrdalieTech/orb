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

	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
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
	mu      sync.Mutex
	state   saved
	save    func(json.RawMessage) error
	mapURL  string
	server  *tc.Server
	clients map[string]*tc.Client
	closed  bool
	failed  bool
}

func Open(raw json.RawMessage, save func(json.RawMessage) error, mapURL string) (*Node, error) {
	if save == nil || !strings.HasPrefix(mapURL, "https://") {
		return nil, errors.New("explicit storage and HTTPS relay map required")
	}
	n := &Node{save: save, mapURL: mapURL, clients: map[string]*tc.Client{}}
	if len(raw) == 0 {
		n.state = saved{Version: 1, Key: key.NewNode(), PSK: tc.NewPresharedKey(), Clients: map[string]key.NodePrivate{}}
		if err := save(bridge.JSON(n.state)); err != nil {
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
		return nil, bridge.Fail("unavailable")
	}
	region := &tc.ConnInfo{RegionID: cmp.Or(n.state.Region, tailcfg.DERPRegionID(-1))}
	if err := region.Expand(ctx, tc.ExpandForServer, tc.DERPMapURL(n.mapURL), uncachedMap{}); err != nil {
		n.mu.Unlock()
		return nil, bridge.Fail("unavailable")
	}
	selected := region.Region[0].RegionID
	s := &tc.Server{Region: region.Region[0], DERPMapCache: uncachedMap{}, Key: n.state.Key, PresharedKey: n.state.PSK, RegionID: n.state.Region, DERPMapURL: n.mapURL, Logf: func(string, ...any) {}}
	n.server = s
	n.mu.Unlock()
	listener, err := s.Listen(ctx, "tcp", ":443")
	if err != nil {
		return nil, bridge.Fail("unavailable")
	}
	n.mu.Lock()
	n.state.Region = selected
	err = n.save(bridge.JSON(n.state))
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
	if n.server == nil || n.state.Region <= 0 {
		return "", bridge.Fail("unavailable")
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
		return "", bridge.Fail("unauthorized")
	}
	ci, err := tc.ParseAddr(tc.Addr(locator))
	if err != nil || len(ci.Region) != 0 || ci.RegionID <= 0 || ci.RegionID > 65535 || ci.PresharedKey.IsZero() || ci.ServerPublic.IsZero() || ci.ServerDiscoPublic.IsZero() {
		return "", bridge.Fail("unauthorized")
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
		return nil, bridge.Fail("unavailable")
	}
	client := n.clients[peer]
	if client != nil && client.Server != addr {
		_ = client.Close()
		delete(n.clients, peer)
		client = nil
	}
	if client == nil {
		if len(n.clients) >= 128 {
			n.mu.Unlock()
			return nil, bridge.Fail("resource_exhausted")
		}
		k := n.state.Clients[peer]
		if k.IsZero() {
			k = key.NewNode()
			n.state.Clients[peer] = k
			if err = n.save(bridge.JSON(n.state)); err != nil {
				n.failed = true
				n.mu.Unlock()
				return nil, err
			}
		}
		client = &tc.Client{DERPMapCache: uncachedMap{}, Server: addr, Key: k, DERPMapURL: n.mapURL, Logf: func(string, ...any) {}}
		n.clients[peer] = client
	}
	n.mu.Unlock()
	// Re-register after a remote restart even when the local client is already up.
	if _, err := client.Ping(ctx); err != nil {
		return nil, bridge.Fail("unavailable")
	}
	c, err := client.DialTCPPort(ctx, 443)
	if err != nil {
		return nil, bridge.Fail("unavailable")
	}
	return c, nil
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

// Relay maps are fetched explicitly rather than sharing Tailcat's process cache.
type uncachedMap struct{}

func (uncachedMap) Get(string) ([]byte, string, time.Time, bool) { return nil, "", time.Time{}, false }
func (uncachedMap) Put(string, []byte, string) error             { return nil }
