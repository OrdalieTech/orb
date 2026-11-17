package mitosis

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis/protocol"
)

type endpoint struct{ closed bool }

func (e *endpoint) Invoke(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (e *endpoint) Close() error { e.closed = true; return nil }
func newMitosis(t *testing.T) *Mitosis {
	t.Helper()
	b, err := Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestPairOnceCoversTheAgentButNotAgentCalls(t *testing.T) {
	b, phone := newMitosis(t), newMitosis(t)
	inv, err := b.Invite([]Grant{{Permissions: []string{"agent.list", "agent.prompt"}}})
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
	// One grant covers the agent: every conversation it runs, now or later.
	for i := 0; i < 20; i++ {
		r, token, err := b.Enroll("")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = b.Attach(r.ID, token, protocol.NewID(), &endpoint{}); err != nil {
			t.Fatal(err)
		}
		agent := phone.Principal()
		agent.Subject = Subject{Kind: "agent"}
		if !b.Allowed(phone.Principal(), r.ID, "agent.prompt") || b.Allowed(agent, r.ID, "agent.prompt") {
			t.Fatal("a controller grant missed a conversation, or gave agent access")
		}
	}
	if n := len(b.Catalog(phone.Principal())); n != 20 {
		t.Fatal(n)
	}
	_ = b.Block(phone.PeerID())
	if len(b.Catalog(phone.Principal())) != 0 {
		t.Fatal("block ignored")
	}
}

func TestRegistrationCredentialIsolationAndFencing(t *testing.T) {
	b := newMitosis(t)
	r, secret, err := b.Enroll("one")
	if err != nil {
		t.Fatal(err)
	}
	e := &endpoint{}
	gen, err := b.Attach(r.ID, secret, protocol.NewID(), e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Attach(r.ID, secret, protocol.NewID(), &endpoint{}); Code(err) != "identity_conflict" {
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
	if _, err = b.Attach(r.ID, "wrong", protocol.NewID(), e); Code(err) != "unauthorized" {
		t.Fatal(err)
	}
	_ = b.Close()
	if !e.closed {
		t.Fatal("connection not released")
	}
}

func TestAgentCallsRequireBothDirectionalGrants(t *testing.T) {
	source, destination := newMitosis(t), newMitosis(t)
	origin, credential, err := source.Enroll("origin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Attach(origin.ID, credential, protocol.NewID(), &endpoint{}); err != nil {
		t.Fatal(err)
	}
	target, credential, err := destination.Enroll("target")
	if err != nil {
		t.Fatal(err)
	}
	generation, err := destination.Attach(target.ID, credential, protocol.NewID(), &endpoint{})
	if err != nil {
		t.Fatal(err)
	}
	call := Call{InstanceID: target.ID, Service: protocol.Service, Method: "prompt", SessionID: "session", Expected: Expected{Generation: generation, Revision: "1"}, OperationID: protocol.NewID(), Args: JSON(map[string]string{"text": "hello"})}
	principal := Principal{PeerID: source.PeerID(), Subject: Subject{Kind: "agent"}}
	if _, err = source.Outbound(origin.ID, destination.PeerID(), call); err == nil {
		t.Fatal("missing source grant allowed")
	}
	if err = source.AddGrant(Grant{Principal: principal, Destination: destination.PeerID(), Permissions: []string{"agent.prompt"}}); err != nil {
		t.Fatal(err)
	}
	subject, err := source.Outbound(origin.ID, destination.PeerID(), call)
	if err != nil || subject != principal.Subject {
		t.Fatal(subject, err)
	}
	if _, err = destination.Call(context.Background(), principal, call); err == nil {
		t.Fatal("missing destination grant allowed")
	}
	if err = destination.AddGrant(Grant{Principal: principal, Permissions: []string{"agent.prompt"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = destination.Call(context.Background(), principal, call); err != nil {
		t.Fatal(err)
	}
	forged := principal
	forged.Subject.Kind = "controller"
	if _, err = destination.Call(context.Background(), forged, call); err == nil {
		t.Fatal("the source's controller inherited its agent's authority")
	}
	if _, err = source.Outbound(origin.ID, newMitosis(t).PeerID(), call); err == nil {
		t.Fatal("destination grant forwarded to third mitosis")
	}
}

func TestInvitationCannotMutateAuthorityAndCatalogCursorExpires(t *testing.T) {
	b, peer := newMitosis(t), newMitosis(t)
	inv, err := b.Invite([]Grant{{Permissions: []string{"agent.list"}}})
	if err != nil {
		t.Fatal(err)
	}
	inv.Grants[0].Permissions[0] = "agent.prompt"
	if _, err = b.Claim(peer.PeerID(), inv.ID, inv.Token); err != nil {
		t.Fatal(err)
	}
	if err = b.Approve(inv.ID, peer.PeerID()); err != nil {
		t.Fatal(err)
	}
	r, _, err := b.Enroll("test")
	if err != nil {
		t.Fatal(err)
	}
	if b.Allowed(peer.Principal(), r.ID, "agent.prompt") {
		t.Fatal("caller mutated persisted invitation")
	}
	items := make([]int, protocol.MaxPage+1)
	first, err := page(items, "")
	if err != nil || first.Cursor == "" {
		t.Fatal(first, err)
	}
	items[0] = 1
	if _, err = page(items, first.Cursor); Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
}

func TestCatalogObservationAndGrantRevocation(t *testing.T) {
	b, peer := newMitosis(t), newMitosis(t)
	if err := b.AddGrant(Grant{Principal: peer.Principal(), Permissions: []string{"agent.list"}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	raw, err := b.Handle(ctx, peer.PeerID(), "events.subscribe", JSON(struct{}{}))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Cursor string `json:"cursor"`
	}
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Handle(ctx, peer.PeerID(), "events.subscribe", JSON(map[string]string{"cursor": snapshot.Cursor})); err != nil {
		t.Fatal(err)
	}
	if _, _, err = b.Enroll("new"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Handle(ctx, peer.PeerID(), "events.subscribe", JSON(map[string]string{"cursor": snapshot.Cursor})); Code(err) != "cursor_expired" {
		t.Fatal(err)
	}
	if _, err = b.Admin(ctx, "revoke", JSON(map[string]string{"grant_id": b.state.Grants[0].ID})); err != nil {
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
	b, claimant, attacker := newMitosis(t), newMitosis(t), newMitosis(t)
	inv, err := b.Invite([]Grant{{Permissions: []string{"agent.list"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Claim(claimant.PeerID(), inv.ID, "wrong"); Code(err) != "unauthorized" {
		t.Fatal(err)
	}
	if _, err = b.Claim(claimant.PeerID(), inv.ID, inv.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Claim(attacker.PeerID(), inv.ID, inv.Token); Code(err) != "identity_conflict" {
		t.Fatal(err)
	}
	if err = b.Approve(inv.ID, attacker.PeerID()); Code(err) != "unauthorized" {
		t.Fatal(err)
	}
	if _, err = b.PairStatus(attacker.PeerID(), inv.ID); Code(err) != "not_found" {
		t.Fatal(err)
	}
	// Two devices presented the code: someone else saw it, so neither is ever approved.
	if err = b.Approve(inv.ID, claimant.PeerID()); Code(err) != "identity_conflict" {
		t.Fatal(err)
	}
	inv, err = b.Invite([]Grant{{Permissions: []string{"agent.list"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Claim(claimant.PeerID(), inv.ID, inv.Token); err != nil {
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

// Throwaway instances whose Orb has gone are retired; attached ones, named ones and
// never-attached ones stay. Named IDs go regardless.
func TestRetireRemovesGoneThrowawayInstances(t *testing.T) {
	b := newMitosis(t)
	enroll := func(alias string, attach, detach bool) Instance {
		t.Helper()
		r, token, err := b.Enroll(alias)
		if err != nil {
			t.Fatal(err)
		}
		if attach {
			generation, err := b.Attach(r.ID, token, protocol.NewID(), &endpoint{})
			if err != nil {
				t.Fatal(err)
			}
			if detach {
				b.Detach(r.ID, generation)
			}
		}
		return r
	}
	gone := enroll("instance-gone0001", true, true)
	live := enroll("instance-live0001", true, false)
	fresh := enroll("instance-fresh001", false, false)
	named := enroll("laptop", true, true)
	if removed, err := b.Retire(nil); err != nil || removed != 1 {
		t.Fatalf("pruned %d, %v", removed, err)
	}
	if _, ok := b.state.Instances[gone.ID]; ok {
		t.Fatal("gone throwaway instance kept")
	}
	for _, r := range []Instance{live, fresh, named} {
		if _, ok := b.state.Instances[r.ID]; !ok {
			t.Fatalf("%s pruned", r.Alias)
		}
	}
	if removed, err := b.Retire([]string{named.ID}); err != nil || removed != 1 {
		t.Fatalf("retired %d, %v", removed, err)
	}
}

// The owner calls its own Mitosis as a peer would and reaches everything there, the machine
// included, with no grant; a stranger reaches nothing, and the owner's agents nothing either.
func TestTheOwnerReachesItsOwnMachineWithoutGrants(t *testing.T) {
	b, stranger := newMitosis(t), newMitosis(t)
	instance, _, err := b.Enroll("phone")
	if err != nil {
		t.Fatal(err)
	}
	hosted := ""
	b.SetHost(func(_ context.Context, _ Principal, method string, _ json.RawMessage) (json.RawMessage, error) {
		hosted = method
		return JSON(struct{}{}), nil
	})
	raw, err := b.Handle(t.Context(), b.PeerID(), "instances.list", JSON(struct{}{}))
	if err != nil || !json.Valid(raw) || len(b.Catalog(b.Principal())) != 1 || !b.Allowed(b.Principal(), instance.ID, "agent.prompt") {
		t.Fatalf("owner catalog: %s %v", raw, err)
	}
	if _, err = b.Handle(t.Context(), b.PeerID(), "host.sessions", JSON(struct{}{})); err != nil || hosted != "host.sessions" {
		t.Fatalf("owner host call: %v", err)
	}
	if _, err = b.Handle(t.Context(), stranger.PeerID(), "instances.list", JSON(struct{}{})); Code(err) != "unauthorized" {
		t.Fatalf("stranger: %v", err)
	}
	agent := b.Principal()
	agent.Subject = Subject{Kind: "agent"}
	if b.Allowed(agent, instance.ID, "agent.prompt") {
		t.Fatal("the owner's authority passed to an agent")
	}
}
