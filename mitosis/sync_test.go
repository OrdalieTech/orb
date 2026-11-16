package mitosis

import (
	"context"
	"encoding/json"
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

	_ = document.Replace(ctx, stores[owner], []byte(`{"openai":{"type":"api_key","key":"o2"}}`))
	admin(owner, `{"remove":"`+c.PeerID()+`"}`)
	round()
	if key(b, "openai") != "o2" || key(c, "openai") != "o1" || sealedTo(c) {
		t.Fatal("removal did not cut the removed device", key(b, "openai"), key(c, "openai"))
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
