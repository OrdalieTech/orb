package mitosis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/internal/document"
)

func TestSyncMergesAndCutsRemovedOrBlockedDevices(t *testing.T) {
	ctx := context.Background()
	owner, b, c := newMitosis(t), newMitosis(t), newMitosis(t)
	stores := map[*Mitosis]*document.Memory{}
	for x, auth := range map[*Mitosis]string{owner: `{"openai":{"type":"api_key","key":"o1"}}`, b: `{"anthropic":{"type":"api_key","key":"a1"}}`, c: ``} {
		stores[x] = &document.Memory{}
		_ = document.Replace(ctx, stores[x], []byte(auth))
		x.SetCredentials(stores[x])
	}
	admin := func(x *Mitosis, params string) {
		t.Helper()
		if _, err := x.Admin(ctx, "sync", json.RawMessage(params)); err != nil {
			t.Fatal(err)
		}
	}
	group := groupID(owner.PeerID())
	round := func() {
		for _, x := range []*Mitosis{b, c, owner, b, c} {
			admin(x, `{}`)
			for _, from := range []*Mitosis{owner, b, c} {
				for _, to := range []*Mitosis{owner, b, c} {
					records, _ := from.Contacts(to.PeerID(), group)
					for _, r := range records {
						_, _ = to.MergeContact(from.PeerID(), r)
					}
				}
			}
		}
	}
	key := func(x *Mitosis, provider string) string {
		raw, _ := stores[x].Read(ctx)
		var auth map[string]struct{ Key string }
		_ = json.Unmarshal(raw, &auth)
		return auth[provider].Key
	}
	sealedTo := func(x *Mitosis) bool {
		contact, _ := owner.state.Records[group+"/"+owner.PeerID()].Verify()
		var v vault
		_ = json.Unmarshal(contact.Extensions["vault"], &v)
		_, ok := v.Envelopes[x.PeerID()]
		return ok
	}
	if _, err := owner.Admin(ctx, "sync", json.RawMessage(`{"add":"`+b.PeerID()+`"}`)); Code(err) != "not_found" {
		t.Fatal("admitted a device it never paired with", err)
	}
	for _, x := range []*Mitosis{b, c} {
		_ = owner.SavePeer(x.PeerID(), "paired")
	}
	admin(owner, `{"add":"`+b.PeerID()+`"}`)
	admin(owner, `{"add":"`+c.PeerID()+`"}`)
	admin(b, `{"join":"`+owner.PeerID()+`"}`)
	admin(c, `{"join":"`+owner.PeerID()+`"}`)
	if _, err := b.Admin(ctx, "sync", json.RawMessage(`{"add":"`+c.PeerID()+`"}`)); err == nil {
		t.Fatal("a member changed the group")
	}
	round()
	for _, x := range []*Mitosis{owner, b, c} {
		if key(x, "openai") != "o1" || key(x, "anthropic") != "a1" {
			raw, _ := stores[x].Read(ctx)
			t.Fatalf("not merged: %s", raw)
		}
	}

	published := owner.state.Records[group+"/"+owner.PeerID()]
	admin(owner, `{}`)
	if owner.state.Records[group+"/"+owner.PeerID()] != published {
		t.Fatal("an unchanged round published again")
	}

	// A token refreshed on a member replaces the stale copies, the owner's included.
	_ = document.Replace(ctx, stores[b], []byte(`{"anthropic":{"type":"oauth","refresh":"r2","access":"a2","expires":2000},"openai":{"type":"api_key","key":"o1"}}`))
	_ = document.Replace(ctx, stores[owner], []byte(`{"anthropic":{"type":"oauth","refresh":"r1","access":"a1","expires":1000},"openai":{"type":"api_key","key":"o1"}}`))
	round()
	for _, x := range []*Mitosis{owner, b, c} {
		if raw, _ := stores[x].Read(ctx); !strings.Contains(string(raw), `"r2"`) {
			t.Fatalf("stale token kept: %s", raw)
		}
	}

	// A device added last catches up from another member, with the owner away.
	d := newMitosis(t)
	_ = owner.SavePeer(d.PeerID(), "paired")
	_ = c.SavePeer(d.PeerID(), "paired")
	stores[d] = &document.Memory{}
	d.SetCredentials(stores[d])
	admin(owner, `{"add":"`+d.PeerID()+`"}`)
	admin(d, `{"join":"`+owner.PeerID()+`"}`)
	if _, err := d.MergeContact(b.PeerID(), owner.state.Records[group+"/"+owner.PeerID()]); err != nil {
		t.Fatal(err)
	}
	admin(d, `{}`)
	if raw, _ := stores[d].Read(ctx); key(d, "openai") != "o1" || !strings.Contains(string(raw), `"r2"`) {
		t.Fatal("the new device did not catch up through a member")
	}

	_ = document.Replace(ctx, stores[owner], []byte(`{"openai":{"type":"api_key","key":"o2"}}`))
	admin(owner, `{"remove":"`+c.PeerID()+`"}`)
	round()
	if key(b, "openai") != "o2" || key(c, "openai") != "o1" || sealedTo(c) {
		t.Fatal("removal did not cut the removed device", key(b, "openai"), key(c, "openai"))
	}

	// The removed device leaves the owner's group and starts its own.
	admin(c, `{"remove":"`+owner.PeerID()+`"}`)
	admin(c, `{"add":"`+d.PeerID()+`"}`)
	if c.state.Scopes[c.state.Group][0] != c.PeerID() {
		t.Fatal("a device that left cannot own a group")
	}

	_ = document.Replace(ctx, stores[owner], []byte(`{"openai":{"type":"api_key","key":"o3"}}`))
	if err := owner.Block(b.PeerID()); err != nil {
		t.Fatal(err)
	}
	round()
	if key(b, "openai") != "o2" || sealedTo(b) {
		t.Fatal("a blocked device received", key(b, "openai"))
	}
}
