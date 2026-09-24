package bridge

import (
	"testing"

	"github.com/OrdalieTech/orb/bridge/protocol"
)

func TestScopedThreeBridgeReconciliation(t *testing.T) {
	a, b, c := newBridge(t), newBridge(t), newBridge(t)
	scope := protocol.NewID()
	for _, x := range []*Bridge{a, b, c} {
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
	outsider := newBridge(t)
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
	a, c := newBridge(t), newBridge(t)
	scope := protocol.NewID()
	for _, b := range []*Bridge{a, c} {
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
