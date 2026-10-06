package bridge

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/bridge/protocol"
)

func TestKeepaliveClosesAPeerThatStoppedAnswering(t *testing.T) {
	gone := make(chan struct{})
	defer close(gone)
	for _, answers := range []bool{true, false} {
		x, y := net.Pipe()
		server := protocol.NewConn(y, func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
			if !answers {
				<-gone // half-open: the peer is gone but nothing says so
			}
			return JSON(struct{}{}), nil
		})
		client := protocol.NewConn(x, nil)
		go Keepalive(t.Context(), client, 50*time.Millisecond)
		select {
		case <-client.Done():
			if answers {
				t.Fatal("a live peer was dropped")
			}
		case <-time.After(400 * time.Millisecond):
			if !answers {
				t.Fatal("a silent peer was kept")
			}
		}
		_ = client.Close()
		_ = server.Close()
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
