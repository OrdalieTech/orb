//go:build !wasm

package websocket

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	ws "github.com/coder/websocket"
)

type memory struct{ data []byte }

func (m *memory) Load() ([]byte, error) { return m.data, nil }
func (m *memory) Save(b []byte) error   { m.data = append([]byte(nil), b...); return nil }

func TestClientPairingAuthorizationAndReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	b, err := bridge.Open(&memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	handler, err := Handler(ctx, b, []string{"https://orb.example"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	address := "ws" + strings.TrimPrefix(server.URL, "http")
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer := bridge.PeerID(pub)
	dial := func(pin string) (*protocol.Conn, error) {
		stream, err := Dial(ctx, address)
		if err != nil {
			return nil, err
		}
		return bridge.ConnectClient(ctx, stream, key, pin)
	}
	if c, err := dial(peer); err == nil {
		_ = c.Close()
		t.Fatal("wrong server identity accepted")
	}
	c, err := dial(b.PeerID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err = c.Call(ctx, "instances.list", struct{}{}, nil); err == nil {
		t.Fatal("unpaired client has access")
	}
	inv, err := b.Invite([]bridge.Grant{{GroupID: b.PersonalGroup(), IncludeFuture: true, Permissions: []string{"instance.list", "instance.inspect"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Call(ctx, "pair.claim", map[string]string{"invitation_id": inv.ID, "token": inv.Token}, nil); err != nil {
		t.Fatal(err)
	}
	if err = c.Call(ctx, "instances.list", struct{}{}, nil); err == nil {
		t.Fatal("unapproved client has access")
	}
	if err = b.Approve(inv.ID, peer); err != nil {
		t.Fatal(err)
	}
	if err = c.Call(ctx, "instances.list", struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	if err = c.Call(ctx, "status", struct{}{}, nil); err == nil {
		t.Fatal("remote admin exposed")
	}
	// A controller grant must not authorize an agent subject on the same key.
	instance, token, err := b.Enroll("test", b.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	endpoint := bridge.NewLocal(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return bridge.JSON(struct{}{}), nil
	})
	if _, err = b.Attach(instance.ID, token, protocol.NewID(), endpoint); err != nil {
		t.Fatal(err)
	}
	call := bridge.Call{InstanceID: instance.ID, Service: protocol.Service, Method: "inspect", Args: bridge.JSON(struct{}{})}
	if err = c.Call(ctx, "instances.call", call, nil); err != nil {
		t.Fatal(err)
	}
	if err = c.Call(ctx, "instances.call", struct {
		bridge.Call
		Subject bridge.Subject `json:"subject"`
	}{call, bridge.Subject{Kind: "instance", InstanceID: protocol.NewID()}}, nil); err == nil {
		t.Fatal("controller grant leaked to agent")
	}
	// No runtime or listener exists on the client side.
	incoming := b.Connection(peer)
	if incoming == nil {
		t.Fatal("connection not registered")
	}
	if err = incoming.Call(ctx, "instances.list", struct{}{}, nil); err == nil {
		t.Fatal("client accepts inbound calls")
	}
	_ = c.Close()
	c, err = dial(b.PeerID())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Call(ctx, "instances.list", struct{}{}, nil); err != nil {
		t.Fatal("pairing did not survive reconnect", err)
	}
	if !b.Available(instance.ID) {
		t.Fatal("disconnect disposed remote runtime")
	}
	if err = b.Block(peer); err != nil {
		t.Fatal(err)
	}
	if err = c.Call(ctx, "instances.list", struct{}{}, nil); err == nil {
		t.Fatal("blocked peer retained access")
	}
}

func TestOriginsAndURLs(t *testing.T) {
	for _, address := range []string{"http://localhost", "ws://remote.example", "wss://user:pass@example.com", "wss://example.com/?token=secret"} {
		if ValidateURL(address) == nil {
			t.Errorf("accepted %s", address)
		}
	}
	for _, address := range []string{"ws://127.0.0.1:1234", "ws://[::1]:1234", "wss://example.com/bridge"} {
		if err := ValidateURL(address); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateOrigins([]string{"*"}); err == nil {
		t.Fatal("wildcard origin accepted")
	}
	b, err := bridge.Open(&memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	h, err := Handler(t.Context(), b, []string{"https://orb.example"})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(h)
	defer s.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, res, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http"), &ws.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}}})
	if err == nil || res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatal("foreign origin accepted", err)
	}
}
