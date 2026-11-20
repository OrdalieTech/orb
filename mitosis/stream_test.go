package mitosis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis/protocol"
)

type spyConn struct {
	net.Conn
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *spyConn) Read(p []byte) (int, error) {
	n, err := s.Conn.Read(p)
	if n > 0 {
		s.mu.Lock()
		s.buf.Write(p[:n])
		s.mu.Unlock()
	}
	return n, err
}

func (s *spyConn) Write(p []byte) (int, error) {
	if len(p) > 0 {
		s.mu.Lock()
		s.buf.Write(p)
		s.mu.Unlock()
	}
	return s.Conn.Write(p)
}

func (s *spyConn) saw(needle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Contains(s.buf.Bytes(), []byte(needle))
}

func setupGroup(t *testing.T, owner *Mitosis, members ...*Mitosis) string {
	t.Helper()
	ctx := t.Context()
	for _, x := range append([]*Mitosis{owner}, members...) {
		x.SetClass("credentials", aiauth.NewDocumentStore(&document.Memory{}))
	}
	for _, m := range members {
		_ = owner.SavePeer(m.PeerID(), "tc-owner")
		_ = m.SavePeer(owner.PeerID(), "ws://127.0.0.1:8789")
		a, b := net.Pipe()
		go func() { _, _, _ = owner.Connect(ctx, a, "", true) }()
		if _, _, err := m.Connect(ctx, b, owner.PeerID(), false); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Admin(ctx, "sync", JSON(map[string]string{"add": m.PeerID()})); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Admin(ctx, "sync", JSON(map[string]string{"join": owner.PeerID()})); err != nil {
			t.Fatal(err)
		}
	}
	group := groupID(owner.PeerID())
	all := append([]*Mitosis{owner}, members...)
	for range 3 {
		for _, to := range all {
			for _, from := range all {
				if from == to {
					continue
				}
				recs, _ := from.Contacts(to.PeerID(), group)
				for _, r := range recs {
					_, _ = to.MergeContact(from.PeerID(), r)
				}
			}
			_, _ = to.Sync(ctx, nil)
		}
	}
	return group
}

func TestStreamRelayEndToEndTLSAuthenticatesTabPeerID(t *testing.T) {
	ctx := t.Context()
	mac, fedora, tab := newMitosis(t), newMitosis(t), newMitosis(t)
	for _, x := range []*Mitosis{mac, fedora, tab} {
		t.Cleanup(func() { _ = x.Close() })
	}
	fedora.SetName("Fedora")
	group := setupGroup(t, mac, fedora, tab)

	// Publish fedora's tailcat locator in the group scope; tab has no direct way to dial tailcat.
	if _, err := fedora.Sync(ctx, []Locator{{Transport: "tailcat/1", Locator: "tc-fedora"}}); err != nil {
		t.Fatal(err)
	}
	recs, _ := fedora.Contacts(mac.PeerID(), group)
	for _, r := range recs {
		_, _ = mac.MergeContact(fedora.PeerID(), r)
		_, _ = tab.MergeContact(mac.PeerID(), r)
	}
	// Disconnect fedora's initial link to mac only to confirm mac dials fedora on demand for the relay.
	mac.Hangup(fedora.PeerID())

	// Attach an instance and Host handler on fedora recording the authenticated PeerID.
	inst, cred, err := fedora.Enroll("work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fedora.Attach(inst.ID, cred, protocol.NewID(), &watchEndpoint{session: "sess-42"}); err != nil {
		t.Fatal(err)
	}
	var hostCaller atomic.Value
	fedora.SetHost(func(_ context.Context, p Principal, method string, _ json.RawMessage) (json.RawMessage, error) {
		hostCaller.Store(p.PeerID)
		return JSON(map[string]string{"host": "Fedora", "method": method, "caller": p.PeerID}), nil
	})

	// Wire mac's raw dialer to pipe directly into fedora.Connect(..., server=true) through a spyConn.
	var dialCount atomic.Int32
	var spies []*spyConn
	var spyMu sync.Mutex
	macPeers := NewPeers(ctx, mac)
	macPeers.Dial = func(_ context.Context, peer, locator string) (net.Conn, error) {
		if peer != fedora.PeerID() || locator != "tc-fedora" {
			return nil, Fail("not_found")
		}
		dialCount.Add(1)
		a, b := net.Pipe()
		spy := &spyConn{Conn: a}
		spyMu.Lock()
		spies = append(spies, spy)
		spyMu.Unlock()
		go func() {
			_, _, _ = fedora.Connect(ctx, b, "", true)
		}()
		return spy, nil
	}

	// Tab's direct dialer refuses tailcat locators (just like the browser WebSocket dialer).
	tabPeers := NewPeers(ctx, tab)
	tabPeers.Dial = func(_ context.Context, _, locator string) (net.Conn, error) {
		if !strings.HasPrefix(locator, "ws://") && !strings.HasPrefix(locator, "wss://") {
			return nil, errors.New("browser only dials ws:// or wss://")
		}
		return nil, Fail("unavailable")
	}

	// Concurrent Remote calls from tab to fedora should deduplicate to one relayed dial.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	var descRaw, hostRaw json.RawMessage
	wg.Add(2)
	go func() {
		defer wg.Done()
		descRaw, errs[0] = tabPeers.Remote(ctx, fedora.PeerID(), "agent.describe", struct{}{})
	}()
	go func() {
		defer wg.Done()
		hostRaw, errs[1] = tabPeers.Remote(ctx, fedora.PeerID(), "host.sessions", struct{}{})
	}()
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("relayed calls failed: describe=%v host=%v", errs[0], errs[1])
	}
	if !strings.Contains(string(descRaw), `"sess-42"`) || !strings.Contains(string(descRaw), `"Fedora"`) {
		t.Fatalf("unexpected describe: %s", descRaw)
	}
	if got, _ := hostCaller.Load().(string); got != tab.PeerID() {
		t.Fatalf("fedora saw caller %q, want tab %q (mac=%q): %s", got, tab.PeerID(), mac.PeerID(), hostRaw)
	}
	if n := dialCount.Load(); n != 1 {
		t.Fatalf("concurrent dials opened %d tunnels, want 1", n)
	}

	// Send an agent.call prompt with a unique plaintext marker; verify the relay never saw plaintext.
	const secretPrompt = "secret-prompt-marker-e2e-tls"
	if _, err := tabPeers.Remote(ctx, fedora.PeerID(), "agent.call", AgentCall{
		SessionID: "sess-42", Method: "prompt", Args: JSON(map[string]string{"text": secretPrompt}),
	}); err != nil {
		t.Fatalf("relayed agent.call failed: %v", err)
	}
	spyMu.Lock()
	for _, s := range spies {
		if s.saw(secretPrompt) {
			t.Fatal("relay observed plaintext prompt bytes")
		}
	}
	spyMu.Unlock()

	// Single-hop: fedora's relayed connection is marked relayed and never offered as a relay candidate.
	if cands := tabPeers.relayCandidates(""); slices.Contains(cands, fedora.PeerID()) {
		t.Fatalf("relayed connection to fedora listed as relay candidate: %v", cands)
	}
}

func TestStreamRelayEnforcesMembershipLimitsAndRevocation(t *testing.T) {
	ctx := t.Context()
	mac, fedora, tab, otherTab := newMitosis(t), newMitosis(t), newMitosis(t), newMitosis(t)
	for _, x := range []*Mitosis{mac, fedora, tab, otherTab} {
		t.Cleanup(func() { _ = x.Close() })
	}
	group := setupGroup(t, mac, fedora, tab, otherTab)
	if _, err := fedora.Sync(ctx, []Locator{{Transport: "tailcat/1", Locator: "tc-fedora"}}); err != nil {
		t.Fatal(err)
	}
	recs, _ := fedora.Contacts(mac.PeerID(), group)
	for _, r := range recs {
		_, _ = mac.MergeContact(fedora.PeerID(), r)
	}
	// otherTab has no locator (like another browser tab).
	delete(mac.state.Peers, otherTab.PeerID())

	macPeers := NewPeers(ctx, mac)
	macPeers.Dial = func(_ context.Context, peer, locator string) (net.Conn, error) {
		if peer == fedora.PeerID() && locator == "tc-fedora" {
			a, b := net.Pipe()
			go func() { _, _, _ = fedora.Connect(ctx, b, "", true) }()
			return a, nil
		}
		return nil, Fail("unavailable")
	}

	outsider := newMitosis(t)
	t.Cleanup(func() { _ = outsider.Close() })
	if err := mac.AddGrant(FullGrant(outsider.PeerID())); err != nil {
		t.Fatal(err)
	}
	// Non-member caller (even with an explicit grant) cannot use stream.open.
	if _, err := mac.Handle(ctx, outsider.PeerID(), "stream.open", JSON(map[string]string{"peer_id": fedora.PeerID()})); Code(err) != "unauthorized" {
		t.Fatalf("non-member caller: %v", err)
	}
	// Non-member target rejected.
	if _, err := mac.Handle(ctx, tab.PeerID(), "stream.open", JSON(map[string]string{"peer_id": outsider.PeerID()})); Code(err) != "unauthorized" {
		t.Fatalf("non-member target: %v", err)
	}
	// Self or caller as target rejected.
	if _, err := mac.Handle(ctx, tab.PeerID(), "stream.open", JSON(map[string]string{"peer_id": mac.PeerID()})); Code(err) != "invalid_params" {
		t.Fatalf("self target: %v", err)
	}
	if _, err := mac.Handle(ctx, tab.PeerID(), "stream.open", JSON(map[string]string{"peer_id": tab.PeerID()})); Code(err) != "invalid_params" {
		t.Fatalf("caller target: %v", err)
	}
	// Locator-less group member (another browser tab) returns not_found fast.
	if _, err := mac.Handle(ctx, tab.PeerID(), "stream.open", JSON(map[string]string{"peer_id": otherTab.PeerID()})); Code(err) != "not_found" {
		t.Fatalf("locator-less target: %v", err)
	}

	// Enforce per-peer stream cap.
	openedIDs := make([]string, 0, streamMaxPerPeer)
	for range streamMaxPerPeer {
		raw, err := mac.Handle(ctx, tab.PeerID(), "stream.open", JSON(map[string]string{"peer_id": fedora.PeerID()}))
		if err != nil {
			t.Fatalf("stream.open within cap: %v", err)
		}
		var res struct {
			StreamID string `json:"stream_id"`
		}
		_ = json.Unmarshal(raw, &res)
		openedIDs = append(openedIDs, res.StreamID)
	}
	if _, err := mac.Handle(ctx, tab.PeerID(), "stream.open", JSON(map[string]string{"peer_id": fedora.PeerID()})); Code(err) != "resource_exhausted" {
		t.Fatalf("exceeding per-peer cap: %v", err)
	}
	for _, id := range openedIDs {
		_, _ = mac.Handle(ctx, tab.PeerID(), "stream.close", JSON(map[string]string{"stream_id": id}))
	}

	// Open a real relayed channel from tab to fedora, then remove tab on mac -> stream closes immediately.
	tabPeers := NewPeers(ctx, tab)
	relayedConn, err := tabPeers.Peer(ctx, fedora.PeerID(), "")
	if err != nil {
		t.Fatalf("relayed Peer: %v", err)
	}
	if _, err := mac.Admin(ctx, "sync", JSON(map[string]string{"remove": tab.PeerID()})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-relayedConn.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("relayed connection survived sync remove on relay")
	}
	mac.mu.Lock()
	left := len(mac.streams)
	mac.mu.Unlock()
	if left != 0 {
		t.Fatalf("relay leaked %d streams after removal", left)
	}
}

func TestStreamRelayInteropWithOlderRelayAndTarget(t *testing.T) {
	ctx := t.Context()
	// (1) Old relay (returns method_not_found on stream.open) is skipped in favor of a new relay.
	owner, oldRelay, newRelay, target, tab := newMitosis(t), newMitosis(t), newMitosis(t), newMitosis(t), newMitosis(t)
	for _, x := range []*Mitosis{owner, oldRelay, newRelay, target, tab} {
		t.Cleanup(func() { _ = x.Close() })
	}
	group := setupGroup(t, owner, oldRelay, newRelay, target, tab)
	target.SetName("Target")
	if _, err := target.Sync(ctx, []Locator{{Transport: "tailcat/1", Locator: "tc-target"}}); err != nil {
		t.Fatal(err)
	}
	recs, _ := target.Contacts(newRelay.PeerID(), group)
	for _, r := range recs {
		_, _ = newRelay.MergeContact(target.PeerID(), r)
	}
	// Disconnect tab from owner so tab only has direct channels to oldRelay (no stream.open) and newRelay.
	owner.Hangup(tab.PeerID())
	tab.Hangup(owner.PeerID())
	// Replace oldRelay's connection handler with a mock that simulates 0.25/0.26 (method_not_found on stream.open).
	oldA, oldB := net.Pipe()
	go func() {
		cfg, _ := oldRelay.TLSConfig("", true)
		_, _, _ = connectPeer(ctx, oldA, cfg, true, oldRelay.Hello(), func(ctx context.Context, peer, method string, params json.RawMessage) (json.RawMessage, error) {
			if strings.HasPrefix(method, "stream.") {
				return nil, &protocol.RPCError{Code: -32601, Message: "method_not_found"}
			}
			return oldRelay.Handle(ctx, peer, method, params)
		})
	}()
	if _, _, err := tab.Connect(ctx, oldB, oldRelay.PeerID(), false); err != nil {
		t.Fatal(err)
	}
	newA, newB := net.Pipe()
	go func() { _, _, _ = newRelay.Connect(ctx, newA, "", true) }()
	if _, _, err := tab.Connect(ctx, newB, newRelay.PeerID(), false); err != nil {
		t.Fatal(err)
	}
	newRelayPeers := NewPeers(ctx, newRelay)
	newRelayPeers.Dial = func(_ context.Context, peer, locator string) (net.Conn, error) {
		if peer == target.PeerID() && locator == "tc-target" {
			a, b := net.Pipe()
			go func() { _, _, _ = target.Connect(ctx, b, "", true) }()
			return a, nil
		}
		return nil, Fail("unavailable")
	}

	tabPeers := NewPeers(ctx, tab)
	// Force oldRelay to be tried first via lastRelay.
	tabPeers.mu.Lock()
	tabPeers.lastRelay = oldRelay.PeerID()
	tabPeers.mu.Unlock()
	raw, err := tabPeers.Remote(ctx, target.PeerID(), "mitosis.ping", struct{}{})
	if err != nil || !strings.Contains(string(raw), `"Target"`) {
		t.Fatalf("fallback past 0.25/0.26 relay failed: %s %v", raw, err)
	}

	// (2) Old target (no group-access rule, simulated by removing target's group state while keeping
	// its explicit grant check): target still authenticates tab's PeerID end to end and enforces
	// explicit grants.
	target.mu.Lock()
	target.state.Group = ""
	target.mu.Unlock()
	target.SetHost(func(_ context.Context, p Principal, method string, _ json.RawMessage) (json.RawMessage, error) {
		return JSON(map[string]string{"caller": p.PeerID}), nil
	})
	if _, err := tabPeers.Remote(ctx, target.PeerID(), "host.sessions", struct{}{}); Code(err) != "unauthorized" {
		t.Fatalf("old target allowed ungranted tab: %v", err)
	}
	if err := target.AddGrant(FullGrant(tab.PeerID())); err != nil {
		t.Fatal(err)
	}
	raw, err = tabPeers.Remote(ctx, target.PeerID(), "host.sessions", struct{}{})
	if err != nil || !strings.Contains(string(raw), tab.PeerID()) {
		t.Fatalf("old target with explicit grant failed: %s %v", raw, err)
	}
}

func TestReconcileSkipsRelayWhenDirectGroupMemberConnected(t *testing.T) {
	ctx := t.Context()
	mac, fedora, tab := newMitosis(t), newMitosis(t), newMitosis(t)
	for _, x := range []*Mitosis{mac, fedora, tab} {
		t.Cleanup(func() { _ = x.Close() })
	}
	group := setupGroup(t, mac, fedora, tab)
	if _, err := fedora.Sync(ctx, []Locator{{Transport: "tailcat/1", Locator: "tc-fedora"}}); err != nil {
		t.Fatal(err)
	}
	recs, _ := fedora.Contacts(mac.PeerID(), group)
	for _, r := range recs {
		_, _ = mac.MergeContact(fedora.PeerID(), r)
		_, _ = tab.MergeContact(mac.PeerID(), r)
	}
	var relayedDials atomic.Int32
	macPeers := NewPeers(ctx, mac)
	macPeers.Dial = func(_ context.Context, peer, locator string) (net.Conn, error) {
		relayedDials.Add(1)
		a, b := net.Pipe()
		go func() { _, _, _ = fedora.Connect(ctx, b, "", true) }()
		return a, nil
	}
	tabPeers := NewPeers(ctx, tab)
	tabPeers.Dial = func(_ context.Context, _, _ string) (net.Conn, error) {
		return nil, errors.New("browser cannot dial tailcat")
	}
	if !tabPeers.hasDirectGroupLink() {
		t.Fatal("tab missing direct group link to mac")
	}
	// Background reconciliation calls peer(..., allowRelay=!hasDirectGroupLink()) -> must not relay.
	if _, err := tabPeers.peer(ctx, fedora.PeerID(), "", !tabPeers.hasDirectGroupLink()); err == nil {
		t.Fatal("background reconcile opened relayed channel despite direct group link")
	}
	if relayedDials.Load() != 0 {
		t.Fatalf("expected 0 relayed dials during reconcile, got %d", relayedDials.Load())
	}
	// On-demand Peer/Remote DOES relay.
	if _, err := tabPeers.Peer(ctx, fedora.PeerID(), ""); err != nil {
		t.Fatalf("on-demand Peer failed to relay: %v", err)
	}
	if relayedDials.Load() != 1 {
		t.Fatalf("expected 1 relayed dial on demand, got %d", relayedDials.Load())
	}
}

func TestRelayRunsAfterDirectDialAndFirstRelayHang(t *testing.T) {
	ctx := t.Context()
	mac, fedora, tab, lab := newMitosis(t), newMitosis(t), newMitosis(t), newMitosis(t)
	for _, x := range []*Mitosis{mac, fedora, tab, lab} {
		t.Cleanup(func() { _ = x.Close() })
	}
	fedora.SetName("Fedora")
	group := setupGroup(t, mac, fedora, tab, lab)
	if _, err := fedora.Sync(ctx, []Locator{{Transport: "tailcat/1", Locator: "tc-fedora"}}); err != nil {
		t.Fatal(err)
	}
	recs, _ := fedora.Contacts(mac.PeerID(), group)
	for _, r := range recs {
		_, _ = mac.MergeContact(fedora.PeerID(), r)
		_, _ = lab.MergeContact(fedora.PeerID(), r)
		_, _ = tab.MergeContact(mac.PeerID(), r)
	}
	tab.Hangup(fedora.PeerID())
	fedora.Hangup(tab.PeerID())
	macPeers := NewPeers(ctx, mac)
	macPeers.Dial = func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { _, _, _ = fedora.Connect(ctx, b, "", true) }()
		return a, nil
	}
	// The saved locator never answers, and neither does fedora from lab, the relay tried first:
	// both must leave time for the relay through mac.
	hang := func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	NewPeers(ctx, lab).Dial = hang
	a, b := net.Pipe()
	go func() { _, _, _ = lab.Connect(ctx, a, "", true) }()
	if _, _, err := tab.Connect(ctx, b, lab.PeerID(), false); err != nil {
		t.Fatal(err)
	}
	tabPeers := NewPeers(ctx, tab)
	tabPeers.Dial = hang
	tabPeers.mu.Lock()
	tabPeers.lastRelay = lab.PeerID()
	tabPeers.mu.Unlock()
	raw, err := tabPeers.Remote(ctx, fedora.PeerID(), "mitosis.ping", struct{}{})
	if err != nil || !strings.Contains(string(raw), `"Fedora"`) {
		t.Fatalf("relay after a hanging direct dial: %s %v", raw, err)
	}
}
