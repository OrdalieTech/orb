package bridge

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/bridge/protocol"
)

func (b *Bridge) TLSConfig(expected string, server bool) (*tls.Config, error) {
	b.mu.Lock()
	key := append(ed25519.PrivateKey(nil), b.state.Key...)
	b.mu.Unlock()
	return tlsConfig(key, expected, server, func(id string) bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return !b.closed && !b.failed && !b.state.Blocked[id]
	})
}

func tlsConfig(key ed25519.PrivateKey, expected string, server bool, allowed func(string) bool) (*tls.Config, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, Fail("unauthorized")
	}
	if !server {
		if _, err := ParsePeerID(expected); err != nil {
			return nil, err
		}
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: protocol.Version}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, err
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, NextProtos: []string{protocol.Version}, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, SessionTicketsDisabled: true}
	if server {
		config.ClientAuth = tls.RequireAnyClientCert
	} else {
		config.InsecureSkipVerify = true
	} // The mandatory verifier below replaces CA/DNS authentication with identity pinning.
	config.VerifyConnection = func(s tls.ConnectionState) error {
		if s.NegotiatedProtocol != protocol.Version || len(s.PeerCertificates) != 1 {
			return Fail("unauthorized")
		}
		cert := s.PeerCertificates[0]
		pub, ok := cert.PublicKey.(ed25519.PublicKey)
		if !ok || len(pub) != 32 {
			return Fail("unauthorized")
		}
		id := PeerID(pub)
		if expected != "" && id != expected {
			return Fail("unauthorized")
		}
		if cert.Subject.CommonName != protocol.Version || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) || cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) != nil {
			return Fail("unauthorized")
		}
		if allowed != nil && !allowed(id) {
			return Fail("unauthorized")
		}
		return nil
	}
	return config, nil
}

// Connect authenticates a caller-supplied stream. It never creates a listener,
// locates a profile, or starts an agent runtime.
func (b *Bridge) Connect(ctx context.Context, stream net.Conn, expected string, server bool) (*protocol.Conn, string, error) {
	config, err := b.TLSConfig(expected, server)
	if err != nil {
		_ = stream.Close()
		return nil, "", err
	}
	c, id, err := connectPeer(ctx, stream, config, server, b.Hello(), b.Handle)
	if err != nil {
		return nil, "", err
	}

	b.mu.Lock()
	if b.closed || b.failed || b.state.Blocked[id] || len(b.channels) >= 128 {
		b.mu.Unlock()
		_ = c.Close()
		return nil, "", Fail("resource_exhausted")
	}
	if b.channels[id] == nil {
		b.channels[id] = map[*protocol.Conn]bool{}
	}
	if len(b.channels[id]) >= 4 {
		b.mu.Unlock()
		_ = c.Close()
		return nil, "", Fail("resource_exhausted")
	}
	b.channels[id][c] = true
	b.mu.Unlock()
	go func() {
		<-c.Done()
		b.mu.Lock()
		delete(b.channels[id], c)
		if len(b.channels[id]) == 0 {
			delete(b.channels, id)
		}
		b.mu.Unlock()
	}()
	return c, id, nil
}

// ConnectClient opens an outbound-only connection. It starts no listener or Bridge
// service, and rejects remote calls into the client. The caller owns key storage.
func ConnectClient(ctx context.Context, stream net.Conn, key ed25519.PrivateKey, expected string) (*protocol.Conn, error) {
	config, err := tlsConfig(key, expected, false, nil)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	hello := Hello{PeerID(key.Public().(ed25519.PublicKey)), protocol.NewID(), protocol.Version, protocol.MaxFrame, protocol.MaxPage}
	c, _, err := connectPeer(ctx, stream, config, false, hello, nil)
	return c, err
}

func connectPeer(ctx context.Context, stream net.Conn, config *tls.Config, server bool, local Hello, handle func(context.Context, string, string, json.RawMessage) (json.RawMessage, error)) (*protocol.Conn, string, error) {
	var secure *tls.Conn
	if server {
		secure = tls.Server(stream, config)
	} else {
		secure = tls.Client(stream, config)
	}
	handshake, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := secure.HandshakeContext(handshake); err != nil {
		_ = stream.Close()
		return nil, "", err
	}
	id := PeerID(secure.ConnectionState().PeerCertificates[0].PublicKey.(ed25519.PublicKey))
	var helloMu sync.Mutex
	ready := false
	pageLimit := protocol.MaxPage
	c := protocol.NewConn(secure, func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		helloMu.Lock()
		if method == "bridge.hello" {
			var h Hello
			if err := protocol.Decode(params, &h); err != nil || h.PeerID != id || h.Protocol != protocol.Version || h.MaxFrame < 1024 || h.MaxPage < 1 || !protocol.ValidID(h.BootID) {
				helloMu.Unlock()
				return nil, rpcError(Fail("unsupported_version"))
			}
			ready = true
			pageLimit = min(protocol.MaxPage, h.MaxPage)
			helloMu.Unlock()
			return JSON(local), nil
		}
		ok := ready
		limit := pageLimit
		helloMu.Unlock()
		if !ok {
			return nil, rpcError(Fail("unauthorized"))
		}
		if handle == nil {
			return nil, rpcError(Fail("not_found"))
		}
		result, err := handle(protocol.WithPageLimit(ctx, limit), id, method, params)
		return result, rpcError(err)
	})
	var remote Hello
	if err := c.Call(handshake, "bridge.hello", local, &remote); err != nil || remote.PeerID != id || remote.Protocol != protocol.Version || !protocol.ValidID(remote.BootID) || remote.MaxFrame < 1024 || remote.MaxPage < 1 {
		_ = c.Close()
		return nil, "", Fail("unsupported_version")
	}
	c.SetFrameLimit(remote.MaxFrame)
	return c, id, nil
}

type Hello struct {
	PeerID   string `json:"peer_id"`
	BootID   string `json:"bridge_boot_id"`
	Protocol string `json:"protocol"`
	MaxFrame int    `json:"max_frame"`
	MaxPage  int    `json:"max_page"`
}

func (b *Bridge) Hello() Hello {
	return Hello{b.PeerID(), b.boot, protocol.Version, protocol.MaxFrame, protocol.MaxPage}
}
func rpcError(err error) error {
	if err == nil {
		return nil
	}
	var e *protocol.RPCError
	if errors.As(err, &e) {
		return e
	}
	return &protocol.RPCError{Code: -32000, Message: Code(err), Data: JSON(map[string]string{"kind": Code(err)})}
}

type pageRequest struct {
	Cursor string `json:"cursor,omitempty"`
	Scope  string `json:"scope_id,omitempty"`
}
type pageResult[T any] struct {
	Items  []T    `json:"items"`
	Cursor string `json:"cursor,omitempty"`
}

func page[T any](items []T, cursor string, limits ...int) (pageResult[T], error) {
	limit := protocol.MaxPage
	if len(limits) > 0 {
		limit = min(limit, max(1, limits[0]))
	}
	digest := sha256.Sum256(JSON(items))
	epoch := base64.RawURLEncoding.EncodeToString(digest[:16])
	offset := uint64(0)
	var err error
	if cursor != "" {
		prefix, suffix, ok := strings.Cut(cursor, ":")
		if !ok || prefix != epoch {
			return pageResult[T]{}, Fail("cursor_expired")
		}
		offset, err = protocol.Counter(suffix)
	}
	if err != nil || offset > uint64(len(items)) {
		return pageResult[T]{}, Fail("cursor_expired")
	}
	out := pageResult[T]{Items: []T{}}
	for i := int(offset); i < len(items) && len(out.Items) < limit; i++ {
		candidate := append(out.Items, items[i])
		if len(JSON(candidate)) > protocol.MaxFrame/2 {
			break
		}
		out.Items = candidate
	}
	next := int(offset) + len(out.Items)
	if next < len(items) {
		if next == int(offset) {
			return out, Fail("resource_exhausted")
		}
		out.Cursor = epoch + ":" + strconv.Itoa(next)
	}
	return out, nil
}
func (b *Bridge) Handle(ctx context.Context, peer, method string, params json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	blocked := b.state.Blocked[peer] || b.closed || b.failed
	b.mu.Unlock()
	if blocked {
		return nil, Fail("unauthorized")
	}
	b.mu.Lock()
	known := b.state.Peers[peer] != ""
	for _, g := range b.state.Grants {
		if g.Principal.PeerID == peer {
			known = true
		}
	}
	for _, members := range b.state.Scopes {
		if slices.Contains(members, peer) {
			known = true
		}
	}
	b.mu.Unlock()
	if !known && method != "pair.claim" && method != "pair.status" {
		return nil, Fail("unauthorized")
	}
	principal := Principal{PeerID: peer, Subject: Subject{Kind: "controller"}}
	switch method {
	case "bridge.ping":
		var p struct{}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		return JSON(struct{}{}), nil
	case "pair.claim":
		var p struct {
			ID      string `json:"invitation_id"`
			Token   string `json:"token"`
			Locator string `json:"locator,omitempty"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		r, err := b.Claim(peer, p.ID, p.Token, p.Locator)
		return JSON(r), err
	case "pair.status":
		var p struct {
			ID string `json:"invitation_id"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		r, err := b.PairStatus(peer, p.ID)
		return JSON(r), err
	case "instances.list":
		var p pageRequest
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		r, err := page(b.Catalog(principal), p.Cursor, protocol.PageLimit(ctx))
		return JSON(r), err
	case "instances.call":
		var p struct {
			Call
			Subject *Subject `json:"subject,omitempty"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.Subject != nil {
			if p.Subject.Kind != "instance" || !protocol.ValidID(p.Subject.InstanceID) {
				return nil, Fail("unauthorized")
			}
			principal.Subject = *p.Subject
		}
		return b.Call(ctx, principal, p.Call)
	case "instances.describe", "operations.get", "events.subscribe", "events.unsubscribe":
		var p struct {
			InstanceID  string `json:"instance_id"`
			OperationID string `json:"operation_id,omitempty"`
			Cursor      string `json:"cursor,omitempty"`
			SnapshotID  string `json:"snapshot_id,omitempty"`
			Offset      string `json:"offset,omitempty"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.InstanceID == "" && (method == "events.subscribe" || method == "events.unsubscribe") {
			if method == "events.unsubscribe" {
				return JSON(struct{}{}), nil
			}
			items := b.Catalog(principal)
			digest := sha256.Sum256(JSON(items))
			snapshot := base64.RawURLEncoding.EncodeToString(digest[:16])
			cursor := "catalog:" + snapshot
			if p.Cursor != "" {
				if p.Cursor != cursor {
					return nil, Fail("cursor_expired")
				}
				return JSON(map[string]any{"events": []Event{}, "cursor": cursor}), nil
			}
			if p.SnapshotID != "" && p.SnapshotID != snapshot {
				return nil, Fail("cursor_expired")
			}
			result, err := page(items, p.Offset, protocol.PageLimit(ctx))
			if err != nil {
				return nil, err
			}
			return JSON(map[string]any{"items": result.Items, "snapshot_id": snapshot, "offset": result.Cursor, "cursor": cursor}), nil
		}
		b.mu.Lock()
		if !b.allowed(principal, p.InstanceID, "instance.inspect", "") {
			b.mu.Unlock()
			return nil, Fail("not_found")
		}
		r, ok := b.active[p.InstanceID]
		b.mu.Unlock()
		if !ok {
			return nil, Fail("unavailable")
		}
		result, err := r.endpoint.Invoke(ctx, method, JSON(struct {
			Principal Principal `json:"principal"`
			Limit     int       `json:"page_limit"`
			Params    any       `json:"params"`
		}{principal, protocol.PageLimit(ctx), p}))
		if err != nil || method != "instances.describe" {
			return result, err
		}
		var descriptor map[string]json.RawMessage
		if json.Unmarshal(result, &descriptor) != nil {
			return nil, Fail("unavailable")
		}
		var methods []string
		_ = json.Unmarshal(descriptor["methods"], &methods)
		allowed := []string{}
		for _, m := range methods {
			if b.Allowed(principal, p.InstanceID, permission(m)) {
				allowed = append(allowed, m)
			}
		}
		descriptor["methods"] = JSON(allowed)
		return JSON(descriptor), nil

	case "peers.list":
		var p pageRequest
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		records, err := b.Contacts(peer, p.Scope)
		if err != nil {
			return nil, err
		}
		r, err := page(records, p.Cursor, protocol.PageLimit(ctx))
		return JSON(r), err
	case "peers.publish":
		var p struct {
			Records []Record `json:"records"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		if len(p.Records) > protocol.MaxPage {
			return nil, Fail("resource_exhausted")
		}
		for _, r := range p.Records {
			if _, err := b.MergeContact(peer, r); err != nil {
				return nil, err
			}
		}
		return JSON(struct{}{}), nil
	default:
		return nil, &protocol.RPCError{Code: -32601, Message: "method_not_found"}
	}
}

// Connection returns an existing authenticated channel in either direction.
func (b *Bridge) Connection(peer string) *protocol.Conn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connectionLocked(peer)
}

func (b *Bridge) connectionLocked(peer string) *protocol.Conn {
	if b.closed || b.failed || b.state.Blocked[peer] {
		return nil
	}
	for c := range b.channels[peer] {
		select {
		case <-c.Done():
		default:
			return c
		}
	}
	return nil
}
