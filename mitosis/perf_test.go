package mitosis

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
)

type meteredConn struct {
	net.Conn
	reads, writes     atomic.Int64
	readByt, writeByt atomic.Int64
}

func (c *meteredConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	n, err := c.Conn.Read(p)
	c.readByt.Add(int64(n))
	return n, err
}

func (c *meteredConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	n, err := c.Conn.Write(p)
	c.writeByt.Add(int64(n))
	return n, err
}

func (c *meteredConn) reset() (rBytes, wBytes, rCalls, wCalls int64) {
	return c.readByt.Swap(0), c.writeByt.Swap(0), c.reads.Swap(0), c.writes.Swap(0)
}

func newBenchMitosis(tb testing.TB) *Mitosis {
	tb.Helper()
	b, err := Open(&document.Memory{}, true)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = b.Close() })
	return b
}

func BenchmarkDirectPeerTLSHandshake(b *testing.B) {
	server := newBenchMitosis(b)
	client := newBenchMitosis(b)
	ctx := context.Background()
	var totalBytes atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		a, c := net.Pipe()
		mc := &meteredConn{Conn: a}
		var wg sync.WaitGroup
		wg.Add(1)
		var serverConn *protocol.Conn
		go func() {
			defer wg.Done()
			sc, _, _ := server.Connect(ctx, c, "", true)
			serverConn = sc
		}()
		cc, _, err := client.Connect(ctx, mc, server.PeerID(), false)
		if err != nil {
			b.Fatal(err)
		}
		wg.Wait()
		r, w, _, _ := mc.reset()
		totalBytes.Add(r + w)
		_ = cc.Close()
		if serverConn != nil {
			_ = serverConn.Close()
		}
	}
	b.ReportMetric(float64(totalBytes.Load())/float64(b.N), "wire-B/handshake")
}

func BenchmarkDirectPeerRPC(b *testing.B) {
	server := newBenchMitosis(b)
	client := newBenchMitosis(b)
	ctx := context.Background()
	_ = server.SavePeer(client.PeerID(), "tc-client")
	_ = client.SavePeer(server.PeerID(), "tc-server")
	if _, err := server.Admin(ctx, "grant", JSON(FullGrant(client.PeerID()))); err != nil {
		b.Fatal(err)
	}
	a, c := net.Pipe()
	mc := &meteredConn{Conn: a}
	ready := make(chan error, 1)
	go func() {
		_, _, err := server.Connect(ctx, c, "", true)
		ready <- err
	}()
	if _, _, err := client.Connect(ctx, mc, server.PeerID(), false); err != nil {
		b.Fatal(err)
	}
	if err := <-ready; err != nil {
		b.Fatal(err)
	}
	mc.reset()

	peers := NewPeers(ctx, client)
	req := JSON(map[string]any{"peer_id": server.PeerID(), "method": "agent.describe", "params": struct{}{}})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := peers.Admin(ctx, "remote", req); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	r, w, _, wc := mc.reset()
	b.ReportMetric(float64(r+w)/float64(b.N), "wire-B/rpc")
	b.ReportMetric(float64(wc)/float64(b.N), "tls-writes/rpc")
}

func BenchmarkRelayedStreamRPC(b *testing.B) {
	ctx := context.Background()
	owner := newBenchMitosis(b)
	tab := newBenchMitosis(b)
	target := newBenchMitosis(b)
	for _, x := range []*Mitosis{owner, tab, target} {
		x.SetClass("credentials", aiauth.NewDocumentStore(&document.Memory{}))
	}

	// Link tab <-> owner (metered) and owner <-> target (via owner.setDial).
	_ = owner.SavePeer(tab.PeerID(), "tc-tab")
	_ = tab.SavePeer(owner.PeerID(), "ws://127.0.0.1:8789")
	_ = owner.SavePeer(target.PeerID(), "tc-target")
	_ = target.SavePeer(owner.PeerID(), "tc-owner")

	t2oA, t2oB := net.Pipe()
	mc := &meteredConn{Conn: t2oA}
	go func() { _, _, _ = owner.Connect(ctx, t2oB, "", true) }()
	if _, _, err := tab.Connect(ctx, mc, owner.PeerID(), false); err != nil {
		b.Fatal(err)
	}
	o2tA, o2tB := net.Pipe()
	go func() { _, _, _ = owner.Connect(ctx, o2tB, "", true) }()
	if _, _, err := target.Connect(ctx, o2tA, owner.PeerID(), false); err != nil {
		b.Fatal(err)
	}
	for _, m := range []*Mitosis{tab, target} {
		if _, err := owner.Admin(ctx, "sync", JSON(map[string]string{"add": m.PeerID()})); err != nil {
			b.Fatal(err)
		}
		if _, err := m.Admin(ctx, "sync", JSON(map[string]string{"join": owner.PeerID()})); err != nil {
			b.Fatal(err)
		}
	}
	group := groupID(owner.PeerID())
	all := []*Mitosis{owner, tab, target}
	for range 3 {
		for _, to := range all {
			for _, from := range all {
				if from == to {
					continue
				}
				recs, _ := from.Contacts(to.PeerID(), group)
				for _, rec := range recs {
					_, _ = to.MergeContact(from.PeerID(), rec)
				}
			}
			_, _ = to.Sync(ctx, nil)
		}
	}
	if _, err := target.Sync(ctx, []Locator{{Transport: "tailcat/1", Locator: "tc-target"}}); err != nil {
		b.Fatal(err)
	}
	recs, _ := target.Contacts(owner.PeerID(), group)
	for _, rec := range recs {
		_, _ = owner.MergeContact(target.PeerID(), rec)
		_, _ = tab.MergeContact(owner.PeerID(), rec)
	}
	owner.Hangup(target.PeerID())
	ownerPeers := NewPeers(ctx, owner)
	ownerPeers.Dial = func(_ context.Context, peer, _ string) (net.Conn, error) {
		if peer != target.PeerID() {
			return nil, Fail("not_found")
		}
		a, c := net.Pipe()
		go func() { _, _, _ = target.Connect(ctx, c, "", true) }()
		return a, nil
	}
	_ = ownerPeers

	// Warm the relayed channel from tab -> owner -> target.
	tabPeers := NewPeers(ctx, tab)
	tabPeers.Dial = func(context.Context, string, string) (net.Conn, error) {
		return nil, Fail("unavailable")
	}
	if _, err := tabPeers.Remote(ctx, target.PeerID(), "agent.describe", struct{}{}); err != nil {
		b.Fatal(err)
	}
	mc.reset()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := tabPeers.Remote(ctx, target.PeerID(), "agent.describe", struct{}{}); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	r, w, _, wc := mc.reset()
	b.ReportMetric(float64(r+w)/float64(b.N), "relay-wire-B/rpc")
	b.ReportMetric(float64(wc)/float64(b.N), "relay-writes/rpc")
}

func BenchmarkSyncVaultSealOpen(b *testing.B) {
	for _, peers := range []int{1, 5, 16} {
		recipients := make([]string, peers)
		var first *Mitosis
		for i := range peers {
			m := newBenchMitosis(b)
			if i == 0 {
				first = m
			}
			recipients[i] = m.PeerID()
		}
		for _, payloadSize := range []int{4 << 10, 128 << 10} {
			body := vaultBody{Entries: map[string]json.RawMessage{
				"entry": json.RawMessage(`"` + strings.Repeat("x", payloadSize) + `"`),
			}}
			name := fmt.Sprintf("peers=%d/size=%dKiB", peers, payloadSize>>10)
			b.Run("Seal/"+name, func(b *testing.B) {
				b.SetBytes(int64(payloadSize))
				b.ReportAllocs()
				b.ResetTimer()
				var blobLen int
				for range b.N {
					blob, err := seal(recipients, body)
					if err != nil {
						b.Fatal(err)
					}
					blobLen = len(blob)
				}
				b.ReportMetric(float64(blobLen), "vault-B")
			})
			blob, err := seal(recipients, body)
			if err != nil {
				b.Fatal(err)
			}
			b.Run("Open/"+name, func(b *testing.B) {
				b.SetBytes(int64(payloadSize))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if _, _, err := first.open(blob); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestNetworkSyncEfficiencyInitialIdleAndIncremental(t *testing.T) {
	ctx := context.Background()
	owner, b, c := newBenchMitosis(t), newBenchMitosis(t), newBenchMitosis(t)
	mems := map[*Mitosis]memory.Store{}
	for _, x := range []*Mitosis{owner, b, c} {
		store := &document.Memory{}
		_ = document.Replace(ctx, store, []byte(`{"openai":{"type":"api_key","key":"k"}}`))
		mem := filestore.Journal{Document: &document.Memory{}}
		mems[x] = mem
		x.SetClass("credentials", aiauth.NewDocumentStore(store))
		x.SetClass("memory", memory.Shared{Store: mem})
	}
	_, _ = mems[owner].Append(ctx, memory.Item{ID: "m1", Content: "Initial shared fact across all peers"})

	var conns []*meteredConn
	link := func(x, y *Mitosis) {
		t.Helper()
		near, far := net.Pipe()
		mc := &meteredConn{Conn: near}
		conns = append(conns, mc)
		done := make(chan error, 1)
		go func() { _, _, err := y.Connect(ctx, far, "", true); done <- err }()
		if _, _, err := x.Connect(ctx, mc, y.PeerID(), false); err != nil || <-done != nil {
			t.Fatal("link failed", err)
		}
	}
	link(owner, b)
	link(owner, c)
	link(b, c)
	for _, m := range []*Mitosis{b, c} {
		_ = owner.SavePeer(m.PeerID(), "tc-member")
		_ = m.SavePeer(owner.PeerID(), "tc-owner")
		if _, err := owner.Admin(ctx, "sync", JSON(map[string]string{"add": m.PeerID()})); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Admin(ctx, "sync", JSON(map[string]string{"join": owner.PeerID()})); err != nil {
			t.Fatal(err)
		}
	}
	group := groupID(owner.PeerID())
	sumBytes := func() (bytesTotal, writesTotal int64) {
		for _, mc := range conns {
			r, w, _, wc := mc.reset()
			bytesTotal += r + w
			writesTotal += wc
		}
		return bytesTotal, writesTotal
	}
	syncAll := func() (int64, int64) {
		sumBytes()
		for _, x := range []*Mitosis{b, c, owner, b, c} {
			if _, err := x.Admin(ctx, "sync", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			for _, from := range []*Mitosis{owner, b, c} {
				for _, to := range []*Mitosis{owner, b, c} {
					if from == to {
						continue
					}
					recs, _ := from.Contacts(to.PeerID(), group)
					for _, rec := range recs {
						_, _ = to.MergeContact(from.PeerID(), rec)
					}
				}
			}
		}
		return sumBytes()
	}

	initBytes, initWrites := syncAll()
	idleBytes1, idleWrites1 := syncAll()
	idleBytes2, idleWrites2 := syncAll()

	// Add 1 memory item on member b and measure incremental sync cost.
	_, _ = mems[b].Append(ctx, memory.Item{ID: "m2", Content: "One new incremental memory item"})
	deltaBytes, deltaWrites := syncAll()
	idleAfterDelta1, idleAfterWrites1 := syncAll()
	idleAfterDelta2, idleAfterWrites2 := syncAll()

	t.Logf("3-peer sync wire traffic:")
	t.Logf("  initial sync:        %6d B  (%d TLS writes)", initBytes, initWrites)
	t.Logf("  idle round 1:        %6d B  (%d TLS writes)", idleBytes1, idleWrites1)
	t.Logf("  idle round 2:        %6d B  (%d TLS writes)", idleBytes2, idleWrites2)
	t.Logf("  1-item delta sync:   %6d B  (%d TLS writes)", deltaBytes, deltaWrites)
	t.Logf("  post-delta round 1:  %6d B  (%d TLS writes)", idleAfterDelta1, idleAfterWrites1)
	t.Logf("  post-delta idle 2:   %6d B  (%d TLS writes)", idleAfterDelta2, idleAfterWrites2)

	if idleBytes2 != 0 {
		t.Errorf("steady-state idle sync transferred %d B (expected 0 B when digests match)", idleBytes2)
	}
	if idleAfterDelta2 != 0 {
		t.Errorf("post-delta steady-state idle sync transferred %d B (expected 0 B)", idleAfterDelta2)
	}
}
