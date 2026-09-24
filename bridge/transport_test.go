package bridge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/bridge/protocol"
)

func TestPinnedTLSAndWrongIdentity(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		a, b := newBridge(t), newBridge(t)
		serverConfig, err := b.TLSConfig("", true)
		if err != nil {
			t.Fatal(err)
		}
		pin := b.PeerID()
		if wrong {
			pin = a.PeerID()
		}
		clientConfig, err := a.TLSConfig(pin, false)
		if err != nil {
			t.Fatal(err)
		}
		x, y := net.Pipe()
		client, server := tls.Client(x, clientConfig), tls.Server(y, serverConfig)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		done := make(chan error, 1)
		go func() { done <- server.HandshakeContext(ctx) }()
		err = client.HandshakeContext(ctx)
		if wrong && err == nil {
			t.Fatal("wrong pin accepted")
		}
		if !wrong && err != nil {
			t.Fatal(err)
		}
		_ = client.Close()
		_ = server.Close()
		cancel()
		<-done
	}
}

func TestPeerConnectionStateDoesNotPersistAcrossRestart(t *testing.T) {
	store := &memStore{}
	server, err := Open(store, true)
	if err != nil {
		t.Fatal(err)
	}
	client := newBridge(t)
	defer func() { _ = client.Close() }()
	peer := client.PeerID()
	if err := server.SavePeer(peer, "saved-locator"); err != nil {
		t.Fatal(err)
	}
	state := func(b *Bridge, want string) {
		t.Helper()
		raw, err := b.Admin(t.Context(), "status", JSON(struct{}{}))
		if err != nil {
			t.Fatal(err)
		}
		var status struct {
			Peers  []string          `json:"peers"`
			States map[string]string `json:"peer_states"`
		}
		if err := json.Unmarshal(raw, &status); err != nil {
			t.Fatal(err)
		}
		if len(status.Peers) != 1 || status.Peers[0] != peer || status.States[peer] != want {
			t.Fatalf("status=%s, want saved peer %s", raw, want)
		}
	}
	connectTo := func(b *Bridge) *protocol.Conn {
		t.Helper()
		x, y := net.Pipe()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		serverErr := make(chan error, 1)
		go func() { _, _, err := b.Connect(ctx, y, "", true); serverErr <- err }()
		c, _, err := client.Connect(ctx, x, b.PeerID(), false)
		if err != nil {
			t.Fatal(err)
		}
		if err := <-serverErr; err != nil {
			t.Fatal(err)
		}
		return c
	}
	state(server, "disconnected")
	c := connectTo(server)
	state(server, "connected")
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("peer connection survived stop")
	}
	if err := c.Call(t.Context(), "bridge.ping", struct{}{}, nil); err == nil {
		t.Fatal("closed bridge accepted a call")
	}
	server, err = Open(store, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	state(server, "disconnected")
	c = connectTo(server)
	state(server, "connected")
	if err := server.Block(peer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("blocked peer stayed connected")
	}
	state(server, "blocked")
}
