package bridge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/internal/document"
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

func TestReconnectingPeerEvictsItsOldestChannel(t *testing.T) {
	server, client := newBridge(t), newBridge(t)
	peer := client.PeerID()
	var accepted []*protocol.Conn
	for range 5 {
		x, y := net.Pipe()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		done := make(chan *protocol.Conn, 1)
		go func() {
			c, _, err := server.Connect(ctx, y, "", true)
			if err != nil {
				t.Error(err)
			}
			done <- c
		}()
		if _, _, err := client.Connect(ctx, x, server.PeerID(), false); err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, <-done)
		cancel()
	}
	select {
	case <-accepted[0].Done():
	case <-time.After(time.Second):
		t.Fatal("the oldest channel survived a fifth connection")
	}
	if got := server.Connection(peer); got != accepted[4] {
		t.Fatal("calls did not go to the newest channel")
	}
}

func TestPeerConnectionStateDoesNotPersistAcrossRestart(t *testing.T) {
	store := &document.Memory{}
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

func TestHostCallsNeedAMachineWideLaunchGrant(t *testing.T) {
	b := newBridge(t)
	defer func() { _ = b.Close() }()
	peer := newBridge(t)
	defer func() { _ = peer.Close() }()
	called := ""
	b.SetHost(func(_ context.Context, p Principal, method string, _ json.RawMessage) (json.RawMessage, error) {
		called = p.PeerID + " " + method
		return JSON(struct{}{}), nil
	})
	controller := Principal{PeerID: peer.PeerID(), Subject: Subject{Kind: "controller"}}
	manage := Grant{Principal: controller, GroupID: "*", IncludeFuture: true, Permissions: []string{"instance.list", "instance.prompt"}}
	if err := b.AddGrant(manage); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Handle(t.Context(), peer.PeerID(), "host.sessions", JSON(struct{}{})); Code(err) != "unauthorized" || called != "" {
		t.Fatalf("host call without host.launch: %v %q", err, called)
	}
	if err := b.AddGrant(Grant{Principal: controller, GroupID: b.PersonalGroup(), IncludeFuture: true, Permissions: []string{"host.launch"}}); Code(err) != "unauthorized" {
		t.Fatalf("host.launch on one group accepted: %v", err)
	}
	manage.Permissions = append(manage.Permissions, "host.launch")
	if err := b.AddGrant(manage); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Handle(t.Context(), peer.PeerID(), "host.launch", JSON(struct{}{})); err != nil || called != peer.PeerID()+" host.launch" {
		t.Fatalf("granted host call: %v %q", err, called)
	}
}

// A machine's name reaches its owner and the peers it granted something, as their screens show
// it; a stranger learns nothing.
func TestAPeerLearnsTheNameOnlyOnceGranted(t *testing.T) {
	b := newBridge(t)
	defer func() { _ = b.Close() }()
	peer := newBridge(t)
	defer func() { _ = peer.Close() }()
	b.SetName("Studio Mac")
	name := func(caller *Bridge) string {
		raw, _ := b.Handle(t.Context(), caller.PeerID(), "bridge.ping", JSON(struct{}{}))
		var r struct{ Name string }
		_ = json.Unmarshal(raw, &r)
		return r.Name
	}
	if got := name(peer); got != "" {
		t.Fatalf("a stranger learned %q", got)
	}
	if got := name(b); got != "Studio Mac" {
		t.Fatalf("the owner learned %q", got)
	}
	if err := b.AddGrant(Grant{Principal: Principal{PeerID: peer.PeerID(), Subject: Subject{Kind: "controller"}}, GroupID: "*", IncludeFuture: true, Permissions: []string{"instance.list"}}); err != nil {
		t.Fatal(err)
	}
	if got := name(peer); got != "Studio Mac" {
		t.Fatalf("a granted peer learned %q", got)
	}
}
