package tailcat

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/plugins/bridge"
)

func TestRejectNonTailcatAndEmbeddedRelay(t *testing.T) {
	for _, s := range []string{"example.com", "https://example.com", "tc!", ""} {
		if _, err := parse(s); err == nil {
			t.Fatal(s)
		}
	}
}

func TestLiveNativePath(t *testing.T) {
	locator := os.Getenv("ORB_BRIDGE_LIVE_LOCATOR")
	if locator == "" {
		t.Skip("native live fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	n, err := Open(nil, func(json.RawMessage) error { return nil }, "https://tailcat.dev/derpmap.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = n.Close() }()
	peer := os.Getenv("ORB_BRIDGE_LIVE_PEER")
	stream, err := n.Dial(ctx, peer, locator)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bridge.Open(&liveStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	c, id, err := b.Connect(ctx, stream, peer, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if id != peer {
		t.Fatal("pin mismatch")
	}
	// The unknown identity can negotiate, but cannot read any catalog.
	if err = c.Call(ctx, "instances.list", struct{}{}, nil); connect.Code(err) != "unauthorized" {
		t.Fatal(err)
	}
	client := n.clients[peer]
	relay, err := client.Ping(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("relay handshake: %s", relay.Latency)
	direct := false
	for range 10 {
		ping, err := client.DiscoPing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ping.Endpoint != "" {
			direct = true
			t.Log("direct path verified")
			break
		}
		t.Logf("relay region: %d", ping.DERPRegionID)
		time.Sleep(200 * time.Millisecond)
	}
	if os.Getenv("TS_DEBUG_ALWAYS_USE_DERP") == "true" {
		if direct {
			t.Fatal("forced relay unexpectedly direct")
		}
	} else if !direct {
		t.Fatal("native direct path not established")
	}
}

type liveStore struct{ data []byte }

func (s *liveStore) Load() ([]byte, error) { return s.data, nil }
func (s *liveStore) Save(b []byte) error   { s.data = append([]byte(nil), b...); return nil }
