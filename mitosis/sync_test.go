package mitosis

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
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
		envelopes, _, _ := split(owner.vaults[owner.pointers(group, owner.PeerID())["credentials"].Digest])
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
	// What either the owner or a member explicitly deletes is deleted everywhere.
	_ = memories[owner].Delete(ctx, "m1")
	_ = memories[c].Delete(ctx, "m2")
	round()
	for _, x := range []*Mitosis{owner, b, c} {
		if got := memorized(x); len(got) != 0 {
			t.Fatal("deletions not applied everywhere", got)
		}
	}
	// Re-add one item so the forged-vault check below has a non-empty memory vault.
	_, _ = memories[owner].Append(ctx, memory.Item{ID: "m2", Content: "restored"})
	round()

	// A vault is checked against its signed digest before anything opens it.
	v := owner.pointers(group, owner.PeerID())["memory"]
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
	if blob, err := c.fetch(ctx, v, b.PeerID(), true); err != nil || !bytes.Equal(blob, genuine) {
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

	// A token refreshed on a member replaces the stale copies, the owner's included, and reseals
	// the credentials alone: the memory vault every member holds stays as it is.
	memoryVault := owner.pointers(group, owner.PeerID())["memory"]
	_ = document.Replace(ctx, stores[owner], []byte(`{"anthropic":{"type":"oauth","refresh":"r1","access":"a1","expires":1000},"openai":{"type":"api_key","key":"o1"}}`))
	round()
	_ = document.Replace(ctx, stores[b], []byte(`{"anthropic":{"type":"oauth","refresh":"r2","access":"a2","expires":2000},"openai":{"type":"api_key","key":"o1"}}`))
	round()
	for _, x := range []*Mitosis{owner, b, c} {
		if raw, _ := stores[x].Read(ctx); !strings.Contains(string(raw), `"r2"`) {
			t.Fatalf("stale token kept: %s", raw)
		}
	}
	if memoryVault.Digest == "" || owner.pointers(group, owner.PeerID())["memory"] != memoryVault {
		t.Fatal("a credential change resealed the memory")
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

	// A device added last joins through a member and catches up from it, with the owner away.
	d := newMitosis(t)
	for _, x := range []*Mitosis{owner, b, c} {
		_ = x.SavePeer(d.PeerID(), "paired")
	}
	share(d, ``)
	admin(owner, `{"add":"`+d.PeerID()+`"}`)
	round(owner, b)
	link(d, b)
	admin(d, `{"join":"`+b.PeerID()+`"}`)
	if _, err := d.MergeContact(b.PeerID(), owner.state.Records[group+"/"+owner.PeerID()]); err != nil {
		t.Fatal(err)
	}
	admin(d, `{}`)
	if raw, _ := stores[d].Read(ctx); key(d, "openai") != "o1" || !strings.Contains(string(raw), `"r2"`) || !slices.Equal(memorized(d), []string{"m2"}) {
		t.Fatal("the new device did not catch up through a member")
	}

	// An owner vault that does not arrive holds back only its own class.
	_ = document.Replace(ctx, stores[owner], []byte(`{"openai":{"type":"api_key","key":"o2"}}`))
	_, _ = memories[owner].Append(ctx, memory.Item{ID: "m3", Content: "late"})
	admin(owner, `{}`)
	v = owner.pointers(group, owner.PeerID())["memory"]
	owner.mu.Lock()
	genuine = owner.vaults[v.Digest]
	owner.vaults[v.Digest] = append(slices.Clone(genuine[:len(genuine)-1]), genuine[len(genuine)-1]^1)
	owner.mu.Unlock()
	round(owner, b)
	if key(b, "openai") != "o2" || slices.Contains(memorized(b), "m3") {
		t.Fatal("a missing memory vault held back the credentials", key(b, "openai"))
	}
	owner.mu.Lock()
	owner.vaults[v.Digest] = genuine
	owner.mu.Unlock()
	round(b)
	if !slices.Contains(memorized(b), "m3") {
		t.Fatal("the memory did not follow once its vault arrived")
	}
	admin(owner, `{"remove":"`+c.PeerID()+`"}`)
	round()
	if key(b, "openai") != "o2" || key(c, "openai") != "o1" || sealedTo(c) {
		t.Fatal("removal did not cut the removed device", key(b, "openai"), key(c, "openai"))
	}
	// Nor can it fetch what is sealed after, from the owner or a member.
	v = owner.pointers(group, owner.PeerID())["credentials"]
	if _, err := c.fetch(ctx, v, owner.PeerID(), true); err == nil {
		t.Fatal("a removed device fetched a later vault")
	}

	// The removed device has left the group, and can start its own.
	admin(c, `{"add":"`+d.PeerID()+`"}`)
	if c.state.Group != groupID(c.PeerID()) || !slices.Equal(c.state.Owners, []string{c.PeerID()}) {
		t.Fatal("a removed device kept the group, or cannot own one", c.state.Group, c.state.Owners)
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

func TestSyncOwnersLiveWithoutEachOther(t *testing.T) {
	ctx := context.Background()
	a, b, c := newMitosis(t), newMitosis(t), newMitosis(t)
	stores := map[*Mitosis]*document.Memory{}
	all := []*Mitosis{a, b, c}
	for _, x := range all {
		stores[x] = &document.Memory{}
		x.SetClass("credentials", aiauth.NewDocumentStore(stores[x]))
		for _, y := range all {
			if x != y {
				_ = x.SavePeer(y.PeerID(), "paired")
			}
		}
	}
	for i, x := range all {
		for _, y := range all[i+1:] {
			near, far := net.Pipe()
			served := make(chan error, 1)
			go func() { _, _, err := y.Connect(ctx, far, "", true); served <- err }()
			if _, _, err := x.Connect(ctx, near, y.PeerID(), false); err != nil || <-served != nil {
				t.Fatal("link", err)
			}
		}
	}
	group := groupID(a.PeerID())
	admin := func(x *Mitosis, params string) error {
		_, err := x.Admin(ctx, "sync", json.RawMessage(params))
		return err
	}
	// round runs the devices given, each after taking the others' records.
	round := func(devices ...*Mitosis) {
		for range 3 {
			for _, to := range devices {
				for _, from := range devices {
					records, _ := from.Contacts(to.PeerID(), group)
					for _, r := range records {
						_, _ = to.MergeContact(from.PeerID(), r)
					}
				}
				_ = admin(to, `{}`)
			}
		}
	}
	key := func(x *Mitosis) string {
		raw, _ := stores[x].Read(ctx)
		var auth map[string]struct{ Key string }
		_ = json.Unmarshal(raw, &auth)
		return auth["openai"].Key
	}
	_ = admin(a, `{"add":"`+b.PeerID()+`"}`)
	_ = admin(a, `{"add":"`+c.PeerID()+`"}`)
	_ = admin(b, `{"join":"`+a.PeerID()+`"}`)
	_ = admin(c, `{"join":"`+a.PeerID()+`"}`)
	if err := admin(b, `{"promote":"`+c.PeerID()+`"}`); Code(err) != "unauthorized" {
		t.Fatal("a member promoted a device", err)
	}
	// With c away, a hands ownership to b; c, back, learns it from a alone.
	_ = admin(a, `{"promote":"`+b.PeerID()+`"}`)
	round(a, b)
	// b deletes what a's set holds, then demotes a: a's set is no member's hand-in, and the
	// deletion stands.
	_ = document.Replace(ctx, stores[a], []byte(`{"openai":{"type":"api_key","key":"k0"}}`))
	round(a, b)
	_ = document.Replace(ctx, stores[b], []byte(`{}`))
	_ = admin(b, `{"demote":"`+a.PeerID()+`"}`)
	round(b)
	if key(b) != "" {
		t.Fatal("a demoted owner's set came back as a hand-in")
	}
	round(a, b)
	round(a, c)
	if !slices.Equal(c.state.Owners, []string{b.PeerID()}) {
		t.Fatal("a member away through a handoff did not follow it", c.state.Owners)
	}
	_ = admin(b, `{"promote":"`+a.PeerID()+`"}`)
	round(all...)
	// With b away, a folds in c's hand-in, then deletes it; b, back, does not fold it in again
	// (c has run no round since, so its hand-in still holds it).
	take := func(to *Mitosis) { // to takes every device's records, then runs a round
		for _, from := range all {
			records, _ := from.Contacts(to.PeerID(), group)
			for _, r := range records {
				_, _ = to.MergeContact(from.PeerID(), r)
			}
		}
		_ = admin(to, `{}`)
	}
	_ = document.Replace(ctx, stores[c], []byte(`{"groq":{"type":"api_key","key":"h1"}}`))
	take(c)
	take(a)
	_ = document.Replace(ctx, stores[a], []byte(`{}`))
	take(a)
	take(b)
	take(b)
	if raw, _ := stores[b].Read(ctx); strings.Contains(string(raw), "h1") {
		t.Fatalf("an owner back folded in a hand-in another owner had consumed: %s", raw)
	}
	round(all...)

	// With a away, b changes the data and cuts c; a, back, follows both.
	_ = document.Replace(ctx, stores[b], []byte(`{"openai":{"type":"api_key","key":"k1"}}`))
	round(b, c)
	if key(c) != "k1" {
		t.Fatal("the second owner's change did not reach a member", key(c))
	}
	_ = admin(b, `{"remove":"`+c.PeerID()+`"}`)
	_ = document.Replace(ctx, stores[b], []byte(`{"openai":{"type":"api_key","key":"k2"}}`))
	round(b, c)
	round(a, b)
	if key(a) != "k2" || key(c) != "k1" || c.state.Group != "" || slices.Contains(a.state.Scopes[group], c.PeerID()) {
		t.Fatal("an owner's change did not hold", key(a), key(c), c.state.Group)
	}
	// a's own change, made after it took b's set, holds too, and the owners then reseal nothing.
	_ = document.Replace(ctx, stores[a], []byte(`{"openai":{"type":"api_key","key":"k3"}}`))
	round(a, b)
	published := [2]Record{a.state.Records[group+"/"+a.PeerID()], b.state.Records[group+"/"+b.PeerID()]}
	round(a, b)
	if key(b) != "k3" || published != [2]Record{a.state.Records[group+"/"+a.PeerID()], b.state.Records[group+"/"+b.PeerID()]} {
		t.Fatal("an owner's own change was lost, or the owners keep resealing", key(a), key(b))
	}
	// b adds an entry, then changes another, while a is off; a comes back restarted, holding no
	// vault, and runs a round before it sees b's record: it deletes nothing, and takes the entry
	// it missed a version of.
	_ = document.Replace(ctx, stores[b], []byte(`{"openai":{"type":"api_key","key":"k3"},"groq":{"type":"api_key","key":"g1"}}`))
	round(b)
	_ = document.Replace(ctx, stores[b], []byte(`{"openai":{"type":"api_key","key":"k4"},"groq":{"type":"api_key","key":"g1"}}`))
	round(b)
	a.mu.Lock()
	clear(a.sealed)
	clear(a.vaults)
	a.synced = ""
	a.mu.Unlock()
	_ = admin(a, `{}`)
	round(a, b)
	for _, x := range []*Mitosis{a, b} {
		if raw, _ := stores[x].Read(ctx); !strings.Contains(string(raw), "g1") {
			t.Fatalf("an owner back from a restart deleted what it never saw: %s", raw)
		}
	}
	// a's set is the group's; b restarts while a is off (nothing serves a's vault) and still
	// publishes its own change, from the set it already applied.
	_ = document.Replace(ctx, stores[a], []byte(`{"openai":{"type":"api_key","key":"k5"},"groq":{"type":"api_key","key":"g1"}}`))
	round(a, b)
	for _, x := range []*Mitosis{a, b} {
		x.mu.Lock()
		clear(x.sealed)
		clear(x.vaults)
		x.mu.Unlock()
	}
	_ = document.Replace(ctx, stores[b], []byte(`{"openai":{"type":"api_key","key":"k5"},"groq":{"type":"api_key","key":"g2"}}`))
	_ = admin(b, `{}`)
	b.mu.Lock()
	versions := b.state.Versions["credentials"]
	b.mu.Unlock()
	if b.pointers(group, b.PeerID())["credentials"].Digest == "" || versions["groq"].By != b.PeerID() {
		t.Fatal("an owner restarted while the other is off stopped publishing")
	}

	// a deletes x while b, unaware, adds y and edits it twice: once they meet, x stays deleted.
	_ = document.Replace(ctx, stores[a], []byte(`{"openai":{"type":"api_key","key":"x"}}`))
	round(a, b)
	_ = document.Replace(ctx, stores[a], []byte(`{}`))
	_ = admin(a, `{}`)
	for _, y := range []string{"y1", "y2"} {
		_ = document.Replace(ctx, stores[b], []byte(`{"openai":{"type":"api_key","key":"x"},"groq":{"type":"api_key","key":"`+y+`"}}`))
		_ = admin(b, `{}`)
	}
	round(a, b)
	for _, d := range []*Mitosis{a, b} {
		if raw, _ := stores[d].Read(ctx); strings.Contains(string(raw), `"x"`) || !strings.Contains(string(raw), "y2") {
			t.Fatalf("concurrent owners lost a deletion or a change: %s", raw)
		}
	}

	// Two owners removing each other at once leave one owner, whom both follow.
	_ = admin(a, `{"remove":"`+b.PeerID()+`"}`)
	_ = admin(b, `{"remove":"`+a.PeerID()+`"}`)
	round(a, b)
	in := slices.DeleteFunc([]*Mitosis{a, b}, func(x *Mitosis) bool { return x.state.Group == "" })
	if len(in) != 1 || !slices.Equal(in[0].state.Owners, []string{in[0].PeerID()}) {
		t.Fatal("concurrent removals did not leave exactly one owner", len(in))
	}
}

// TestSyncMixedClassPeersConvergeWithoutFlapping checks that a peer without the accounts class
// (such as a 0.25.0 device) and peers with it coexist in one group, converge on both credentials
// and accounts, and settle without re-publishing records on subsequent rounds.
func TestSyncMixedClassPeersConvergeWithoutFlapping(t *testing.T) {
	ctx := t.Context()
	openPeer := func(withAccounts bool) (*Mitosis, *document.Memory, *accounts.Store) {
		m, err := Open(&document.Memory{}, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Close() })
		credDoc := &document.Memory{}
		credStore := aiauth.NewDocumentStore(credDoc)
		m.SetClass("credentials", credStore)
		var acctStore *accounts.Store
		if withAccounts {
			acctStore = accounts.NewStoreWithDocument(&document.Memory{}, credStore)
			m.SetClass("accounts", acctStore)
		}
		return m, credDoc, acctStore
	}
	olderOwner, olderCred, _ := openPeer(false)
	newerOwner, newerCred, newerAccts := openPeer(true)
	tabMember, tabCred, tabAccts := openPeer(true)

	link := func(x, y *Mitosis) {
		_ = x.SavePeer(y.PeerID(), "tc1")
		_ = y.SavePeer(x.PeerID(), "tc1")
		a, b := net.Pipe()
		go func() { _, _, _ = x.Connect(ctx, a, "", true) }()
		if _, _, err := y.Connect(ctx, b, x.PeerID(), false); err != nil {
			t.Fatal(err)
		}
		for x.Connection(y.PeerID()) == nil || y.Connection(x.PeerID()) == nil {
			time.Sleep(time.Millisecond)
		}
	}
	link(olderOwner, newerOwner)
	link(newerOwner, tabMember)

	_ = document.Replace(ctx, olderCred, []byte(`{"anthropic":{"type":"api_key","key":"sk-old"}}`))
	_ = document.Replace(ctx, newerCred, []byte(`{"openai":{"type":"api_key","key":"sk-new"}}`))
	if _, err := newerAccts.Add(ctx, "opencode", "zen", aiauth.APIKeyCredential("sk-zen")); err != nil {
		t.Fatal(err)
	}

	admin := func(m *Mitosis, body string) {
		if _, err := m.SyncAdmin(ctx, json.RawMessage(body), nil); err != nil {
			t.Fatalf("sync %s: %v", body, err)
		}
	}
	admin(olderOwner, `{"add":"`+newerOwner.PeerID()+`"}`)
	admin(olderOwner, `{"promote":"`+newerOwner.PeerID()+`"}`)
	admin(newerOwner, `{"join":"`+olderOwner.PeerID()+`"}`)
	group := olderOwner.state.Group
	exchange := func(peers ...*Mitosis) {
		for _, a := range peers {
			for _, b := range peers {
				if a != b {
					if recs, err := a.Contacts(b.PeerID(), group); err == nil {
						for _, r := range recs {
							_, _ = b.MergeContact(a.PeerID(), r)
						}
					}
				}
			}
		}
	}
	round := func(peers ...*Mitosis) {
		for range 3 {
			exchange(peers...)
			for _, p := range peers {
				_, _ = p.Sync(ctx, nil)
			}
			exchange(peers...)
		}
	}
	round(olderOwner, newerOwner)
	admin(newerOwner, `{"add":"`+tabMember.PeerID()+`"}`)
	admin(tabMember, `{"join":"`+newerOwner.PeerID()+`"}`)
	round(olderOwner, newerOwner, tabMember)

	for name, doc := range map[string]*document.Memory{"olderOwner": olderCred, "newerOwner": newerCred, "tab": tabCred} {
		raw, _ := doc.Read(ctx)
		if !strings.Contains(string(raw), "sk-old") || !strings.Contains(string(raw), "sk-new") {
			t.Fatalf("%s credentials did not converge: %s", name, raw)
		}
	}
	rows, err := tabAccts.Accounts(ctx)
	if err != nil || !slices.ContainsFunc(rows, func(a accounts.Account) bool { return a.Provider == "opencode" && a.Name == "zen" && a.Active }) {
		t.Fatalf("tab accounts did not receive zen: %#v, %v", rows, err)
	}

	// Subsequent rounds publish no new records (no version or vault flapping).
	before := [3]Record{
		olderOwner.state.Records[group+"/"+olderOwner.PeerID()],
		newerOwner.state.Records[group+"/"+newerOwner.PeerID()],
		tabMember.state.Records[group+"/"+tabMember.PeerID()],
	}
	round(olderOwner, newerOwner, tabMember)
	after := [3]Record{
		olderOwner.state.Records[group+"/"+olderOwner.PeerID()],
		newerOwner.state.Records[group+"/"+newerOwner.PeerID()],
		tabMember.state.Records[group+"/"+tabMember.PeerID()],
	}
	if before != after {
		t.Fatalf("records flapped across idle rounds with mixed-class peers")
	}

	// A member removing itself (e.g. "Forget this tab") leaves locally and asks a connected owner
	// to drop it from the group along with its pairing, grants and group record so no debris remains.
	if err := newerOwner.AddGrant(FullGrant(tabMember.PeerID())); err != nil {
		t.Fatal(err)
	}
	admin(tabMember, `{"remove":"`+tabMember.PeerID()+`"}`)
	if tabMember.state.Group != "" {
		t.Fatalf("member still in group after removing itself: %q", tabMember.state.Group)
	}
	if slices.Contains(newerOwner.state.Scopes[group], tabMember.PeerID()) {
		t.Fatalf("connected owner still lists departed member %s", tabMember.PeerID())
	}
	if _, ok := newerOwner.state.Peers[tabMember.PeerID()]; ok {
		t.Fatalf("connected owner kept pairing for departed member %s", tabMember.PeerID())
	}
	if slices.ContainsFunc(newerOwner.state.Grants, func(g Grant) bool { return g.Principal.PeerID == tabMember.PeerID() }) {
		t.Fatalf("connected owner kept grant for departed member %s", tabMember.PeerID())
	}
	if _, ok := newerOwner.state.Records[group+"/"+tabMember.PeerID()]; ok {
		t.Fatalf("connected owner kept group record for departed member %s", tabMember.PeerID())
	}
	round(olderOwner, newerOwner)
	if slices.Contains(olderOwner.state.Scopes[group], tabMember.PeerID()) {
		t.Fatalf("other owner still lists departed member %s after sync", tabMember.PeerID())
	}
	if _, ok := olderOwner.state.Records[group+"/"+tabMember.PeerID()]; ok {
		t.Fatalf("other owner kept group record for departed member %s after adopt", tabMember.PeerID())
	}

	// Pre-existing ghost records for devices no longer in the group are dropped by prune (retire)
	// and refused if an older peer tries to re-inject them via MergeContact.
	ghost := newMitosis(t)
	_ = ghost.SetScope(group, []string{ghost.PeerID()})
	ghostRec, err := ghost.PublishContact(group, "ghost", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newerOwner.MergeContact(olderOwner.PeerID(), ghostRec); Code(err) != "unauthorized" {
		t.Fatalf("MergeContact accepted ghost record for non-member: %v", err)
	}
	newerOwner.mu.Lock()
	newerOwner.state.Records[group+"/"+ghost.PeerID()] = ghostRec
	newerOwner.mu.Unlock()
	raw, err := newerOwner.Admin(ctx, "retire", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(string(raw), `"records":1`) {
		t.Fatalf("retire (prune) did not drop ghost record: %s, %v", raw, err)
	}
	if _, ok := newerOwner.state.Records[group+"/"+ghost.PeerID()]; ok {
		t.Fatal("ghost record remained after prune")
	}
}

// TestNotifyDebouncesAndWakesSync proves Notify is a no-op outside a sync group and coalesces a
// burst of local changes into one Changes signal inside a group.
func TestNotifyDebouncesAndWakesSync(t *testing.T) {
	ctx := t.Context()
	m := newMitosis(t)
	drain := func() {
		for {
			select {
			case <-m.Changes():
			default:
				return
			}
		}
	}
	drain()
	m.Notify() // not in a group: should not wake
	select {
	case <-m.Changes():
		t.Fatal("Notify woke Changes outside a sync group")
	case <-time.After(350 * time.Millisecond):
	}
	other := newMitosis(t)
	_ = m.SavePeer(other.PeerID(), "tc1")
	m.SetClass("credentials", aiauth.NewDocumentStore(&document.Memory{}))
	if _, err := m.SyncAdmin(ctx, json.RawMessage(`{"add":"`+other.PeerID()+`"}`), nil); err != nil {
		t.Fatal(err)
	}
	drain()
	// Rapid burst coalesces into a single wake after ~250 ms.
	for range 5 {
		m.Notify()
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-m.Changes():
	case <-time.After(2 * time.Second):
		t.Fatal("Notify did not wake Changes")
	}
	select {
	case <-m.Changes():
		t.Fatal("Notify fired more than once for a coalesced burst")
	case <-time.After(350 * time.Millisecond):
	}
}

type failingClass struct {
	Class
	shareErr  error
	removeErr error
}

func (f failingClass) Shareable(ctx context.Context) (map[string]json.RawMessage, error) {
	if f.shareErr != nil {
		return nil, f.shareErr
	}
	return f.Class.Shareable(ctx)
}

func (f failingClass) Removed(ctx context.Context) (map[string]string, error) {
	if f.removeErr != nil {
		return nil, f.removeErr
	}
	if rc, ok := f.Class.(RemovalClass); ok {
		return rc.Removed(ctx)
	}
	return nil, nil
}

// TestSyncMemberRemovals covers all member-removal guarantees:
// (1) member forget removes on owner and stays gone after several rounds;
// (2) fresh member, offline member with older set, and wiped-store member remove nothing;
// (3) owner modifying an entry after member removal wins and propagates back to the member;
// (4) member logout removes credentials/accounts on owner and other members;
// (5) Shareable/Removed error stops the round without removing anything.
func TestSyncMemberRemovals(t *testing.T) {
	ctx := t.Context()
	type node struct {
		m        *Mitosis
		authDoc  *document.Memory
		authRem  *document.Memory
		auth     *aiauth.Store
		acctDoc  *document.Memory
		accts    *accounts.Store
		memDoc   *document.Memory
		memories filestore.Journal
	}
	makeNode := func() *node {
		n := &node{
			m:       newMitosis(t),
			authDoc: &document.Memory{},
			authRem: &document.Memory{},
			acctDoc: &document.Memory{},
			memDoc:  &document.Memory{},
		}
		n.auth = aiauth.NewDocumentStore(n.authDoc).WithRemoved(n.authRem)
		n.accts = accounts.NewStoreWithDocument(n.acctDoc, n.auth)
		n.memories = filestore.Journal{Document: n.memDoc}
		n.m.SetClass("credentials", n.auth)
		n.m.SetClass("accounts", n.accts)
		n.m.SetClass("memory", memory.Shared{Store: n.memories})
		return n
	}
	owner, mem1, mem2 := makeNode(), makeNode(), makeNode()
	link := func(a, b *Mitosis) {
		_ = a.SavePeer(b.PeerID(), "tc1")
		_ = b.SavePeer(a.PeerID(), "tc1")
		x, y := net.Pipe()
		go func() { _, _, _ = a.Connect(ctx, x, "", true) }()
		if _, _, err := b.Connect(ctx, y, a.PeerID(), false); err != nil {
			t.Fatal(err)
		}
		for a.Connection(b.PeerID()) == nil || b.Connection(a.PeerID()) == nil {
			time.Sleep(time.Millisecond)
		}
	}
	link(owner.m, mem1.m)
	link(owner.m, mem2.m)

	admin := func(n *node, body string) {
		if _, err := n.m.SyncAdmin(ctx, json.RawMessage(body), nil); err != nil {
			t.Fatalf("sync %s: %v", body, err)
		}
	}
	admin(owner, `{"add":"`+mem1.m.PeerID()+`"}`)
	admin(owner, `{"add":"`+mem2.m.PeerID()+`"}`)
	admin(mem1, `{"join":"`+owner.m.PeerID()+`"}`)
	admin(mem2, `{"join":"`+owner.m.PeerID()+`"}`)
	group := owner.m.state.Group

	round := func(peers ...*node) {
		for range 3 {
			for _, a := range peers {
				for _, b := range peers {
					if a != b {
						if recs, err := a.m.Contacts(b.m.PeerID(), group); err == nil {
							for _, r := range recs {
								_, _ = b.m.MergeContact(a.m.PeerID(), r)
							}
						}
					}
				}
			}
			for _, p := range peers {
				if _, err := p.m.Sync(ctx, nil); err != nil {
					t.Fatalf("sync failed: %v", err)
				}
			}
		}
	}
	memIDs := func(n *node) []string {
		items, err := n.memories.Query(ctx, memory.Filter{Limit: -1})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(items))
		for i, it := range items {
			ids[i] = it.ID
		}
		slices.Sort(ids)
		return ids
	}

	// Seed owner with memories, credentials, and a named account.
	_, _ = owner.memories.Append(ctx, memory.Item{ID: "keep", Content: "keep me"})
	_, _ = owner.memories.Append(ctx, memory.Item{ID: "drop", Content: "drop me"})
	_, _ = owner.memories.Append(ctx, memory.Item{ID: "race", Content: "v1"})
	_, _ = owner.auth.Modify(ctx, "openai", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return aiauth.APIKeyCredential("sk-default"), nil
	})
	lea, err := owner.accts.Add(ctx, "openai", "lea", aiauth.APIKeyCredential("sk-lea"))
	if err != nil {
		t.Fatal(err)
	}
	round(owner, mem1, mem2)
	if !slices.Equal(memIDs(mem1), []string{"drop", "keep", "race"}) {
		t.Fatalf("initial sync failed: %v", memIDs(mem1))
	}

	// (1) Member forget -> gone on owner and stays gone on all members after several rounds.
	if err := mem1.memories.Delete(ctx, "drop"); err != nil {
		t.Fatal(err)
	}
	round(owner, mem1, mem2)
	round(owner, mem1, mem2)
	for name, n := range map[string]*node{"owner": owner, "mem1": mem1, "mem2": mem2} {
		if !slices.Equal(memIDs(n), []string{"keep", "race"}) {
			t.Fatalf("%s memories after member forget = %v, want [keep race]", name, memIDs(n))
		}
		if rem, err := n.memories.Removed(ctx); err != nil || len(rem) != 0 {
			t.Fatalf("%s lingering tombstones after sync = %v, %v", name, rem, err)
		}
	}

	// (2) Fresh member, offline member with older set, and member whose store was wiped remove nothing.
	fresh := makeNode()
	link(owner.m, fresh.m)
	admin(owner, `{"add":"`+fresh.m.PeerID()+`"}`)
	admin(fresh, `{"join":"`+owner.m.PeerID()+`"}`)
	round(owner, fresh)
	if !slices.Equal(memIDs(fresh), []string{"keep", "race"}) {
		t.Fatalf("fresh member did not get group items: %v", memIDs(fresh))
	}
	// Wiped store on mem1 (data + tombstones gone with it) re-receives everything and removes nothing on owner.
	_ = document.Replace(ctx, mem1.memDoc, nil)
	_ = document.Replace(ctx, mem1.authDoc, nil)
	_ = document.Replace(ctx, mem1.authRem, nil)
	_ = document.Replace(ctx, mem1.acctDoc, nil)
	round(owner, mem1)
	if !slices.Equal(memIDs(owner), []string{"keep", "race"}) || !slices.Equal(memIDs(mem1), []string{"keep", "race"}) {
		t.Fatalf("wiped member caused removal: owner=%v mem1=%v", memIDs(owner), memIDs(mem1))
	}
	// Member back from offline with an older set removes nothing on owner.
	_, _ = owner.memories.Append(ctx, memory.Item{ID: "newer", Content: "added while mem2 offline"})
	round(owner)
	round(owner, mem2)
	if !slices.Equal(memIDs(owner), []string{"keep", "newer", "race"}) || !slices.Equal(memIDs(mem2), []string{"keep", "newer", "race"}) {
		t.Fatalf("offline member removed newer item: owner=%v mem2=%v", memIDs(owner), memIDs(mem2))
	}

	// (3) Owner modifies "race" locally after mem1 removes it -> owner keeps v2, mem1 gets v2.
	if err := mem1.memories.Delete(ctx, "race"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.memories.Append(ctx, memory.Item{ID: "race", Content: "v2"}); err != nil {
		t.Fatal(err)
	}
	round(owner, mem1)
	for name, n := range map[string]*node{"owner": owner, "mem1": mem1} {
		it, err := n.memories.Get(ctx, "race")
		if err != nil || it.Content != "v2" {
			t.Fatalf("%s race item = %+v, %v; want v2", name, it, err)
		}
	}

	// (4) Member logout of its only credential (leaving auth.json empty) + account removal
	// removes them on owner and other members, and never keeps the secret in tombstones.
	if err := mem1.auth.Delete(ctx, "openai"); err != nil {
		t.Fatal(err)
	}
	// Simulate a writer that leaves auth.json completely empty after logging out of the last provider.
	_ = document.Replace(ctx, mem1.authDoc, nil)
	if err := mem1.accts.Remove(ctx, "openai", lea.ID); err != nil {
		t.Fatal(err)
	}
	if remRaw, _ := mem1.authRem.Read(ctx); strings.Contains(string(remRaw), "sk-default") {
		t.Fatalf("auth tombstone leaked secret: %s", remRaw)
	}
	if acctRaw, _ := mem1.acctDoc.Read(ctx); strings.Contains(string(acctRaw), "sk-lea") {
		t.Fatalf("accounts tombstone leaked secret: %s", acctRaw)
	}
	round(owner, mem1, mem2)
	round(owner, mem1, mem2)
	for name, n := range map[string]*node{"owner": owner, "mem1": mem1, "mem2": mem2} {
		if c, err := n.auth.Read(ctx, "openai"); err != nil || c != nil {
			t.Fatalf("%s still holds logged-out openai credential: %#v, %v", name, c, err)
		}
		if rows, err := n.accts.Accounts(ctx); err != nil || len(rows) != 0 {
			t.Fatalf("%s still holds removed account lea: %#v, %v", name, rows, err)
		}
	}

	// (5) Shareable or Removed error stops the round and removes nothing anywhere.
	owner.m.SetClass("memory", failingClass{Class: memory.Shared{Store: owner.memories}, shareErr: Fail("unavailable")})
	if _, err := owner.m.Sync(ctx, nil); err == nil {
		t.Fatal("owner Sync succeeded despite Shareable error")
	}
	owner.m.SetClass("memory", memory.Shared{Store: owner.memories})
	mem1.m.SetClass("memory", failingClass{Class: memory.Shared{Store: mem1.memories}, removeErr: Fail("unavailable")})
	if _, err := mem1.m.Sync(ctx, nil); err == nil {
		t.Fatal("member Sync succeeded despite Removed error")
	}
	mem1.m.SetClass("memory", memory.Shared{Store: mem1.memories})
	round(owner, mem1, mem2)
	if !slices.Equal(memIDs(owner), []string{"keep", "newer", "race"}) || !slices.Equal(memIDs(mem1), []string{"keep", "newer", "race"}) {
		t.Fatalf("error round caused removals: owner=%v mem1=%v", memIDs(owner), memIDs(mem1))
	}

	// (6) A member forgets an entry it handed in, after the owner took it but before the owner's
	// new vault reaches it: the removal waits for that vault and the entry ends up nowhere.
	send := func(from, to *node) {
		recs, err := from.m.Contacts(to.m.PeerID(), group)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			_, _ = to.m.MergeContact(from.m.PeerID(), r)
		}
	}
	syncOnce := func(n *node) {
		if _, err := n.m.Sync(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mem1.memories.Append(ctx, memory.Item{ID: "brief", Content: "added then forgotten"}); err != nil {
		t.Fatal(err)
	}
	syncOnce(mem1)
	send(mem1, owner)
	syncOnce(owner)
	if _, err := owner.memories.Get(ctx, "brief"); err != nil {
		t.Fatalf("owner did not take the hand-in: %v", err)
	}
	if err := mem1.memories.Delete(ctx, "brief"); err != nil {
		t.Fatal(err)
	}
	syncOnce(mem1) // still on the owner's vault from before the hand-in
	round(owner, mem1, mem2)
	round(owner, mem1, mem2)
	for name, n := range map[string]*node{"owner": owner, "mem1": mem1, "mem2": mem2} {
		if slices.Contains(memIDs(n), "brief") {
			t.Fatalf("%s holds the forgotten entry: %v", name, memIDs(n))
		}
		if rem, err := n.memories.Removed(ctx); err != nil || len(rem) != 0 {
			t.Fatalf("%s lingering tombstones = %v, %v", name, rem, err)
		}
	}
}
