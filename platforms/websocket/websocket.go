// Package websocket carries Bridge's pinned TLS stream over browser WebSockets.
package websocket

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/bridge"
	"github.com/coder/websocket"
)

// ValidateURL permits cleartext outer transport only on loopback. Bridge still
// authenticates and encrypts the inner stream independently of HTTPS proxies.
func ValidateURL(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(address) > 8192 {
		return fmt.Errorf("provide a WebSocket URL without credentials, query, or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "wss" && (u.Scheme != "ws" || u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return fmt.Errorf("use wss:// for remote Bridges (ws:// is allowed on loopback)")
	}
	return nil
}

// Dial creates a stream only. The caller must authenticate it with ConnectClient.
func Dial(ctx context.Context, address string) (net.Conn, error) {
	if err := ValidateURL(address); err != nil {
		return nil, err
	}
	c, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		return nil, err
	}
	// Dial's context bounds establishment; the returned stream lives until Close.
	stream := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	c.SetReadLimit(64 << 10) // TLS records, not whole RPC frames.
	return stream, nil
}

// Handler exposes no admin API. Origins are exact scheme://host values, never
// wildcard patterns. Clients without Origin still require Bridge authentication.
func Handler(ctx context.Context, b *bridge.Bridge, origins []string) (http.Handler, error) {
	if b == nil {
		return nil, fmt.Errorf("bridge is required")
	}
	if err := ValidateOrigins(origins); err != nil {
		return nil, err
	}
	origins = slices.Clone(origins)
	slots := make(chan struct{}, 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !slices.Contains(origins, origin) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "too many connections", http.StatusServiceUnavailable)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: origins})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		stream := websocket.NetConn(ctx, c, websocket.MessageBinary)
		c.SetReadLimit(64 << 10)
		peer, _, err := b.Connect(ctx, stream, "", true)
		if err != nil {
			return
		}
		defer func() { _ = peer.Close() }()
		select {
		case <-ctx.Done():
		case <-peer.Done():
		}
	}), nil
}

func ValidateOrigins(origins []string) error {
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || origin != u.Scheme+"://"+u.Host || strings.ContainsAny(origin, "*?[]") {
			return fmt.Errorf("provide an exact HTTP(S) browser origin")
		}
	}
	return nil
}
