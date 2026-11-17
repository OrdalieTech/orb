package mitosis

import (
	"net"
	"testing"

	"github.com/OrdalieTech/orb/mitosis/protocol"
)

func TestScopedThreeMitosisReconciliation(t *testing.T) {
	a, b, c := newMitosis(t), newMitosis(t), newMitosis(t)
	scope := protocol.NewID()
	for _, x := range []*Mitosis{a, b, c} {
		if err := x.SetScope(scope, []string{a.PeerID(), b.PeerID(), c.PeerID()}); err != nil {
			t.Fatal(err)
		}
	}
	record, err := c.PublishContact(scope, "C", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.MergeContact(c.PeerID(), record); err != nil {
		t.Fatal(err)
	}
	if _, err = a.MergeContact(b.PeerID(), record); err != nil {
		t.Fatal(err)
	}
	outsider := newMitosis(t)
	if _, err = outsider.MergeContact(b.PeerID(), record); Code(err) != "unauthorized" {
		t.Fatal(err)
	}
	withdrawn, err := c.PublishContact(scope, "C", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.MergeContact(b.PeerID(), withdrawn); err != nil {
		t.Fatal(err)
	}
	if changed, err := a.MergeContact(b.PeerID(), record); err != nil || changed {
		t.Fatal("resurrection", err)
	}
	if len(a.Catalog(c.Principal())) != 0 {
		t.Fatal("discovery granted execution")
	}
}

func TestWithdrawalFloorSurvivesRestartAndRejectsEquivocation(t *testing.T) {
	a, c := newMitosis(t), newMitosis(t)
	scope := protocol.NewID()
	for _, b := range []*Mitosis{a, c} {
		if err := b.SetScope(scope, []string{a.PeerID(), c.PeerID()}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := c.PublishContact(scope, "C", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.MergeContact(c.PeerID(), first); err != nil {
		t.Fatal(err)
	}
	// Two independently signed publications of the same revision are a conflict.
	delete(c.state.Records, scope+"/"+c.PeerID())
	conflict, err := c.PublishContact(scope, "different", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.MergeContact(c.PeerID(), conflict); Code(err) != "identity_conflict" {
		t.Fatal(err)
	}
	withdrawn, err := c.PublishContact(scope, "C", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.MergeContact(c.PeerID(), withdrawn); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(a.store, false)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := restarted.MergeContact(c.PeerID(), first); err != nil || changed {
		t.Fatal(changed, err)
	}
	records, err := restarted.Contacts(c.PeerID(), scope)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	contact, err := records[0].Verify()
	if err != nil || !contact.Withdrawn {
		t.Fatal(contact, err)
	}
}

func TestAnExchangeCarriesOnlyWhatEachSideLacks(t *testing.T) {
	a, b, c := newMitosis(t), newMitosis(t), newMitosis(t)
	scope := protocol.NewID()
	for _, x := range []*Mitosis{a, b} {
		if err := x.SetScope(scope, []string{a.PeerID(), b.PeerID(), c.PeerID()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.SetScope(scope, []string{c.PeerID()}); err != nil {
		t.Fatal(err)
	}
	old, _ := c.PublishContact(scope, "C", nil, false)
	fresh, _ := c.PublishContact(scope, "C", nil, false)
	own, _ := b.PublishContact(scope, "B", nil, false)
	for _, m := range []struct {
		to *Mitosis
		r  Record
	}{{a, fresh}, {b, old}} {
		if _, err := m.to.MergeContact(c.PeerID(), m.r); err != nil {
			t.Fatal(err)
		}
	}
	x, y := net.Pipe()
	go func() { _, _, _ = b.Connect(t.Context(), y, "", true) }()
	conn, _, err := a.Connect(t.Context(), x, b.PeerID(), false)
	if err != nil {
		t.Fatal(err)
	}
	have, _ := a.Versions(b.PeerID(), scope)
	if err = NewPeers(t.Context(), a).exchange(t.Context(), conn, scope, b.PeerID(), have); err != nil {
		t.Fatal(err)
	}
	held, _ := b.contacts(a.PeerID(), scope)
	got, _ := a.contacts(b.PeerID(), scope)
	if held[c.PeerID()] != fresh || got[b.PeerID()] != own {
		t.Fatal("an exchange left a side behind")
	}
	have, _ = a.Versions(b.PeerID(), scope)
	if newer, behind, err := b.Newer(a.PeerID(), scope, have); err != nil || len(newer)+len(behind) != 0 {
		t.Fatal("converged sides still differ", newer, behind, err)
	}
}
