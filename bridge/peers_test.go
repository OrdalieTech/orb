package bridge

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/OrdalieTech/orb/bridge/protocol"
)

func TestKeepaliveClosesAPeerThatStoppedAnswering(t *testing.T) {
	for _, answers := range []bool{true, false} {
		// A bubble's clock: a loaded machine cannot make a live peer miss its ping.
		synctest.Test(t, func(t *testing.T) {
			gone := make(chan struct{})
			x, y := net.Pipe()
			server := protocol.NewConn(y, func(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
				if method != "bridge.ping" {
					// Peers serve bridge.ping alone: anything else breaks the wire contract.
					t.Errorf("keepalive called %q", method)
				}
				if !answers {
					<-gone // half-open: the peer is gone but nothing says so
				}
				return JSON(struct{}{}), nil
			})
			client := protocol.NewConn(x, nil)
			go Keepalive(t.Context(), client, time.Second)
			select {
			case <-client.Done():
				if answers {
					t.Error("a live peer was dropped")
				}
			case <-time.After(time.Minute):
				if !answers {
					t.Error("a silent peer was kept")
				}
			}
			close(gone)
			_ = client.Close()
			_ = server.Close()
		})
	}
}

func TestAnInviterBecomesAKnownPeerOnlyOnceTrusted(t *testing.T) {
	b := newBridge(t)
	defer func() { _ = b.Close() }()
	inviter, err := b.Invite(nil) // any valid PeerID stands in for the inviter
	if err != nil {
		t.Fatal(err)
	}
	peer := inviter.PeerID
	s := NewPeers(t.Context(), b)
	s.joining[peer] = "tailcat-locator"
	if locator, _ := b.PeerLocator(peer); locator != "" {
		t.Fatalf("known before trust: %q", locator)
	}
	if _, err = s.Admin(t.Context(), "grant", JSON(ConversationGrant(peer))); err != nil {
		t.Fatal(err)
	}
	if locator, _ := b.PeerLocator(peer); locator != "tailcat-locator" || len(s.joining) != 0 {
		t.Fatalf("after trust: locator %q, joining %v", locator, s.joining)
	}
}
