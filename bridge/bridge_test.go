package bridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
)

type memStore struct{ data []byte }

func (s *memStore) Load() ([]byte, error) { return s.data, nil }
func (s *memStore) Save(b []byte) error   { s.data = append([]byte(nil), b...); return nil }

type endpoint struct{ closed bool }

func (e *endpoint) Invoke(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (e *endpoint) Close() error { e.closed = true; return nil }
func newBridge(t *testing.T) *Bridge {
	t.Helper()
	b, err := Open(&memStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestPairOnceTwentyInstancesAndFixedGrants(t *testing.T) {
	b, phone := newBridge(t), newBridge(t)
	group := b.PersonalGroup()
	inv, err := b.Invite([]Grant{{GroupID: group, IncludeFuture: true, Permissions: []string{"instance.list", "instance.inspect"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Claim(phone.PeerID(), inv.ID, inv.Token); err != nil {
		t.Fatal(err)
	}
	if len(b.Catalog(phone.Principal())) != 0 {
		t.Fatal("unapproved catalog")
	}
	if err = b.Approve(inv.ID, phone.PeerID()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		r, token, err := b.Enroll("", group)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = b.Attach(r.ID, token, protocol.NewID(), &endpoint{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(b.Catalog(phone.Principal())); n != 20 {
		t.Fatal(n)
	}
	fixed := newBridge(t)
	if err = b.AddGrant(Grant{Principal: fixed.Principal(), GroupID: group, Permissions: []string{"instance.list"}}); err != nil {
		t.Fatal(err)
	}
	_, _, err = b.Enroll("last", group)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Catalog(fixed.Principal())) != 20 || len(b.Catalog(phone.Principal())) != 21 {
		t.Fatal("fixed grant widened")
	}
	_ = b.Block(phone.PeerID())
	if len(b.Catalog(phone.Principal())) != 0 {
		t.Fatal("block ignored")
	}
}

func TestFullAccessCoversCurrentAndFutureGroupsButNotAgentCalls(t *testing.T) {
	b, peer := newBridge(t), newBridge(t)
	grant := Grant{Principal: peer.Principal(), GroupID: "*", IncludeFuture: true, Permissions: []string{"instance.list", "instance.prompt"}}
	if err := b.AddGrant(grant); err != nil {
		t.Fatal(err)
	}
	group := protocol.NewID()
	if _, err := b.Admin(t.Context(), "group", connect.JSON(map[string]string{"group_id": group, "name": "later"})); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{b.PersonalGroup(), group} {
		instance, _, err := b.Enroll("test-"+g, g)
		if err != nil || !b.Allowed(peer.Principal(), instance.ID, "instance.prompt") {
			t.Fatalf("full access missing: %v", err)
		}
		agent := peer.Principal()
		agent.Subject = connect.Subject{Kind: "instance", InstanceID: protocol.NewID()}
		if b.Allowed(agent, instance.ID, "instance.prompt") {
			t.Fatal("controller trust granted agent access")
		}
	}
	if err := b.Block(peer.PeerID()); err != nil {
		t.Fatal(err)
	}
	if len(b.Catalog(peer.Principal())) != 0 {
		t.Fatal("blocked peer retained full access")
	}
}
func TestRegistrationCredentialIsolationAndFencing(t *testing.T) {
	b := newBridge(t)
	r, secret, err := b.Enroll("one", b.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	e := &endpoint{}
	gen, err := b.Attach(r.ID, secret, protocol.NewID(), e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Attach(r.ID, secret, protocol.NewID(), &endpoint{}); connect.Code(err) != "identity_conflict" {
		t.Fatal(err)
	}
	b.Detach(r.ID, gen)
	next, err := b.Attach(r.ID, secret, protocol.NewID(), &endpoint{})
	if err != nil || next == gen {
		t.Fatal(next, err)
	}
	b.Detach(r.ID, gen)
	if !b.Available(r.ID) {
		t.Fatal("stale detach fenced current")
	}
	if _, err = b.Attach(r.ID, "wrong", protocol.NewID(), e); connect.Code(err) != "unauthorized" {
		t.Fatal(err)
	}
	_ = b.Close()
	if !e.closed {
		t.Fatal("connection not released")
	}
}

func TestAgentCallsRequireBothDirectionalGrants(t *testing.T) {
	source, destination := newBridge(t), newBridge(t)
	origin, credential, err := source.Enroll("origin", source.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Attach(origin.ID, credential, protocol.NewID(), &endpoint{}); err != nil {
		t.Fatal(err)
	}
	target, credential, err := destination.Enroll("target", destination.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	generation, err := destination.Attach(target.ID, credential, protocol.NewID(), &endpoint{})
	if err != nil {
		t.Fatal(err)
	}
	call := connect.Call{InstanceID: target.ID, Service: protocol.Service, Method: "prompt", SessionID: "session", Expected: connect.Expected{Generation: generation, Revision: "1"}, OperationID: protocol.NewID(), Args: connect.JSON(map[string]string{"text": "hello"})}
	principal := connect.Principal{PeerID: source.PeerID(), Subject: connect.Subject{Kind: "instance", InstanceID: origin.ID}}
	if _, err = source.Outbound(origin.ID, destination.PeerID(), call); err == nil {
		t.Fatal("missing source grant allowed")
	}
	if err = source.AddGrant(Grant{Principal: principal, Destination: destination.PeerID(), Instances: []string{target.ID}, Permissions: []string{"instance.prompt"}}); err != nil {
		t.Fatal(err)
	}
	subject, err := source.Outbound(origin.ID, destination.PeerID(), call)
	if err != nil || subject != principal.Subject {
		t.Fatal(subject, err)
	}
	if _, err = destination.Call(context.Background(), principal, call); err == nil {
		t.Fatal("missing destination grant allowed")
	}
	if err = destination.AddGrant(Grant{Principal: principal, Instances: []string{target.ID}, Permissions: []string{"instance.prompt"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = destination.Call(context.Background(), principal, call); err != nil {
		t.Fatal(err)
	}
	forged := principal
	forged.Subject.InstanceID = protocol.NewID()
	if _, err = destination.Call(context.Background(), forged, call); err == nil {
		t.Fatal("another source instance inherited authority")
	}
	if _, err = source.Outbound(origin.ID, newBridge(t).PeerID(), call); err == nil {
		t.Fatal("destination grant forwarded to third bridge")
	}
}

func TestInvitationCannotMutateAuthorityAndCatalogCursorExpires(t *testing.T) {
	b, peer := newBridge(t), newBridge(t)
	inv, err := b.Invite([]Grant{{GroupID: b.PersonalGroup(), IncludeFuture: true, Permissions: []string{"instance.list"}}})
	if err != nil {
		t.Fatal(err)
	}
	inv.Grants[0].Permissions[0] = "instance.prompt"
	if _, err = b.Claim(peer.PeerID(), inv.ID, inv.Token); err != nil {
		t.Fatal(err)
	}
	if err = b.Approve(inv.ID, peer.PeerID()); err != nil {
		t.Fatal(err)
	}
	r, _, err := b.Enroll("test", b.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	if b.Allowed(peer.Principal(), r.ID, "instance.prompt") {
		t.Fatal("caller mutated persisted invitation")
	}
	items := make([]int, protocol.MaxPage+1)
	first, err := page(items, "")
	if err != nil || first.Cursor == "" {
		t.Fatal(first, err)
	}
	items[0] = 1
	if _, err = page(items, first.Cursor); connect.Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
}

func TestCatalogObservationAndGrantRevocation(t *testing.T) {
	b, peer := newBridge(t), newBridge(t)
	if err := b.AddGrant(Grant{Principal: peer.Principal(), GroupID: b.PersonalGroup(), IncludeFuture: true, Permissions: []string{"instance.list"}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	raw, err := b.Handle(ctx, peer.PeerID(), "events.subscribe", connect.JSON(struct{}{}))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Cursor string `json:"cursor"`
	}
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Handle(ctx, peer.PeerID(), "events.subscribe", connect.JSON(map[string]string{"cursor": snapshot.Cursor})); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Enroll("new", b.PersonalGroup()); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Handle(ctx, peer.PeerID(), "events.subscribe", connect.JSON(map[string]string{"cursor": snapshot.Cursor})); connect.Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
	if _, err = b.Admin(ctx, "revoke", connect.JSON(map[string]string{"grant_id": b.state.Grants[0].ID})); err != nil {
		t.Fatal(err)
	}
	if len(b.Catalog(peer.Principal())) != 0 {
		t.Fatal("revoked catalog visible")
	}
	reopened, err := Open(b.store, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Catalog(peer.Principal())) != 0 {
		t.Fatal("revocation not durable")
	}
}

func TestPairClaimRecoveryBindsApprovalToIdentity(t *testing.T) {
	b, claimant, attacker := newBridge(t), newBridge(t), newBridge(t)
	inv, err := b.Invite([]Grant{{GroupID: b.PersonalGroup(), Permissions: []string{"instance.list"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Claim(claimant.PeerID(), inv.ID, "wrong"); connect.Code(err) != "unauthorized" {
		t.Fatal(err)
	}
	if _, err = b.Claim(claimant.PeerID(), inv.ID, inv.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Claim(attacker.PeerID(), inv.ID, inv.Token); connect.Code(err) != "identity_conflict" {
		t.Fatal(err)
	}
	if err = b.Approve(inv.ID, attacker.PeerID()); connect.Code(err) != "unauthorized" {
		t.Fatal(err)
	}
	if _, err = b.PairStatus(attacker.PeerID(), inv.ID); connect.Code(err) != "not_found" {
		t.Fatal(err)
	}
	if err = b.Approve(inv.ID, claimant.PeerID()); err != nil {
		t.Fatal(err)
	}
	recovered, err := b.Claim(claimant.PeerID(), inv.ID, inv.Token)
	if err != nil || recovered.Status != "approved" {
		t.Fatal(recovered, err)
	}
}
