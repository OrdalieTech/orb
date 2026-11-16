package mitosis

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"testing"

	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
)

func TestSyncMergesAndCutsRemovedOrBlockedDevices(t *testing.T) {
	ctx := context.Background()
	owner, b, c := newMitosis(t), newMitosis(t), newMitosis(t)
	stores := map[*Mitosis]*document.Memory{}
	memories := map[*Mitosis]memory.Store{}
	share := func(x *Mitosis, auth string) {
		stores[x], memories[x] = &document.Memory{}, filestore.Journal{Document: &document.Memory{}}
		_ = document.Replace(ctx, stores[x], []byte(auth))
		x.SetClass("credentials", aiauth.NewDocumentStore(stores[x]))
		x.SetClass("memory", memory.Shared{Store: memories[x]})
	}
	share(owner, `{"openai":{"type":"api_key","key":"o1"}}`)
	share(b, `{"anthropic":{"type":"api_key","key":"a1"}}`)
	share(c, ``)
	// Vaults travel over the channels devices hold, in either direction.
	link := func(x, y *Mitosis) {
		t.Helper()
		near, far := net.Pipe()
		served := make(chan error, 1)
		go func() { _, _, err := y.Connect(ctx, far, "", true); served <- err }()
		if _, _, err := x.Connect(ctx, near, y.PeerID(), false); err != nil || <-served != nil {
			t.Fatal("link", err)
		}
	}
	link(owner, b)
	link(owner, c)
	link(b, c)
	admin := func(x *Mitosis, params string) {
		t.Helper()
		if _, err := x.Admin(ctx, "sync", json.RawMessage(params)); err != nil {
			t.Fatal(err)
		}
	}
	group := groupID(owner.PeerID())
	round := func(devices ...*Mitosis) {
		if devices == nil {
			devices = []*Mitosis{b, c, owner, b, c}
		}
		for _, x := range devices {
			_, _ = x.Admin(ctx, "sync", json.RawMessage(`{}`)) // a removed or blocked device's round fails
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
		v, _, _ := owner.pointer(group, owner.PeerID())
		envelopes, _, _ := split(owner.vaults[v.Digest])
		return envelopes[x.PeerID()] != nil
	}
	memorized := func(x *Mitosis) []string {
		items, _ := memories[x].Query(ctx, memory.Filter{Limit: -1})
		var out []string
		for _, item := range items {
			out = append(out, item.ID)
		}
		slices.Sort(out)
		return out
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
	// Memory merges by item, and the vault outgrows one answer: it arrives in chunks.
	_, _ = memories[owner].Append(ctx, memory.Item{ID: "m1", Content: strings.Repeat("o", vaultChunk)})
	_, _ = memories[b].Append(ctx, memory.Item{ID: "m2", Content: "from b"})
	round()
	for _, x := range []*Mitosis{owner, b, c} {
		if key(x, "openai") != "o1" || key(x, "anthropic") != "a1" {
			raw, _ := stores[x].Read(ctx)
			t.Fatalf("not merged: %s", raw)
		}
		if got := memorized(x); !slices.Equal(got, []string{"m1", "m2"}) {
			t.Fatal("memory not merged", got)
		}
	}
	// What the owner deletes is deleted everywhere; what a member deletes comes back.
	_ = memories[owner].Delete(ctx, "m1")
	_ = memories[c].Delete(ctx, "m2")
	round()
	for _, x := range []*Mitosis{owner, b, c} {
		if got := memorized(x); !slices.Equal(got, []string{"m2"}) {
			t.Fatal("owner deletion not applied", got)
		}
	}

	// A vault is checked against its signed digest before anything opens it.
	v, _, _ := owner.pointer(group, owner.PeerID())
	b.mu.Lock()
	genuine := b.vaults[v.Digest]
	forged := slices.Clone(genuine)
	forged[len(forged)-1] ^= 1
	b.vaults[v.Digest] = forged
	b.mu.Unlock()
	c.mu.Lock()
	delete(c.vaults, v.Digest)
	c.mu.Unlock()
	// b, asked first, serves a forged vault; the owner then serves the genuine one.
	if blob, err := c.fetch(ctx, v, b.PeerID()); err != nil || !bytes.Equal(blob, genuine) {
		t.Fatal("a forged vault was accepted", err)
	}
	b.mu.Lock()
	b.vaults[v.Digest] = genuine
	b.mu.Unlock()

	published := owner.state.Records[group+"/"+owner.PeerID()]
	admin(owner, `{}`)
	if owner.state.Records[group+"/"+owner.PeerID()] != published {
		t.Fatal("an unchanged round published again")
	}

	// A token refreshed on a member replaces the stale copies, the owner's included.
	_ = document.Replace(ctx, stores[owner], []byte(`{"anthropic":{"type":"oauth","refresh":"r1","access":"a1","expires":1000},"openai":{"type":"api_key","key":"o1"}}`))
	round()
	_ = document.Replace(ctx, stores[b], []byte(`{"anthropic":{"type":"oauth","refresh":"r2","access":"a2","expires":2000},"openai":{"type":"api_key","key":"o1"}}`))
	round()
	for _, x := range []*Mitosis{owner, b, c} {
		if raw, _ := stores[x].Read(ctx); !strings.Contains(string(raw), `"r2"`) {
			t.Fatalf("stale token kept: %s", raw)
		}
	}

	// A command key never leaves its device, and what the owner drops is dropped everywhere.
	_ = document.Replace(ctx, stores[b], []byte(`{"evil":{"type":"api_key","key":"!touch /tmp/x"},"anthropic":{"type":"oauth","refresh":"r2","access":"a2","expires":2000},"openai":{"type":"api_key","key":"o1"}}`))
	_ = document.Replace(ctx, stores[c], []byte(`{"groq":{"type":"api_key","key":"g1"}}`))
	round(b, c, owner, b)
	if raw, _ := stores[owner].Read(ctx); strings.Contains(string(raw), "evil") || !strings.Contains(string(raw), "groq") {
		t.Fatalf("a command key was shared, or a member's entry was not: %s", raw)
	}
	// c stops running rounds; its stale hand-in must not bring back what the owner drops.
	_ = document.Replace(ctx, stores[owner], []byte(`{"openai":{"type":"api_key","key":"o1"},"anthropic":{"type":"oauth","refresh":"r2","access":"a2","expires":2000}}`))
	round(b, owner, b)
	for _, x := range []*Mitosis{owner, b} {
		if raw, _ := stores[x].Read(ctx); strings.Contains(string(raw), "groq") {
			t.Fatalf("an entry the owner removed was kept: %s", raw)
		}
	}

	// A device added last catches up from another member, with the owner away.
	d := newMitosis(t)
	_ = owner.SavePeer(d.PeerID(), "paired")
	_ = c.SavePeer(d.PeerID(), "paired")
	share(d, ``)
	admin(owner, `{"add":"`+d.PeerID()+`"}`)
	round(owner, b)
	admin(d, `{"join":"`+owner.PeerID()+`"}`)
	link(d, b)
	if _, err := d.MergeContact(b.PeerID(), owner.state.Records[group+"/"+owner.PeerID()]); err != nil {
		t.Fatal(err)
	}
	admin(d, `{}`)
	if raw, _ := stores[d].Read(ctx); key(d, "openai") != "o1" || !strings.Contains(string(raw), `"r2"`) || !slices.Equal(memorized(d), []string{"m2"}) {
		t.Fatal("the new device did not catch up through a member")
	}

	_ = document.Replace(ctx, stores[owner], []byte(`{"openai":{"type":"api_key","key":"o2"}}`))
	admin(owner, `{"remove":"`+c.PeerID()+`"}`)
	round()
	if key(b, "openai") != "o2" || key(c, "openai") != "o1" || sealedTo(c) {
		t.Fatal("removal did not cut the removed device", key(b, "openai"), key(c, "openai"))
	}
	// Nor can it fetch what is sealed after, from the owner or a member.
	v, _, _ = owner.pointer(group, owner.PeerID())
	if _, err := c.fetch(ctx, v, owner.PeerID()); err == nil {
		t.Fatal("a removed device fetched a later vault")
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
