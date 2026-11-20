package mitosis

import (
	"cmp"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"maps"
	"math/big"
	"slices"
	"time"

	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// A profile's sync group is a scope of devices, some of them its owners. Any owner admits,
// removes, promotes and demotes devices, each change a new version of the group's membership
// signed into its record, and merges and seals the group's data to every member; a member's own
// additions are sealed to the owners alone, as a hand-in. A device's signed record in the group
// scope carries only its vaults' digests, sizes and sequences, one vault per data class; a vault
// is fetched by digest (vault.get) from its publisher or any member holding it.
const (
	vaultSchema = "orb.mitosis-vault/3"
	// maxVault bounds a device's vaults together, which every member holds whole in memory, a
	// browser tab included, and an owner sends to each: megabytes of memory fit, files do not.
	maxVault = 64 << 20
	// vaultChunk is one vault.get answer, well under the 1 MiB frame once base64-encoded.
	vaultChunk = 512 << 10
	// envelope: the recipient's Ed25519 key, an ephemeral X25519 key, and the sealed bundle key.
	envelopeSize = 32 + 32 + 12 + 32 + 16
	// MaxPeers bounds the devices one Mitosis pairs with, holds channels to, or counts in a scope:
	// their locators and records stay well within the 1 MiB profile state.
	MaxPeers = 256
)

// pointer is what a record says of one of its publisher's vaults.
type pointer struct {
	Schema string `json:"schema"`
	Digest string `json:"digest"` // base64url SHA-256 of the vault
	Size   int    `json:"size"`
}

// doc is a sync group's membership as an owner signs it into its record. A version is valid when
// its publisher owns the version it replaces; the highest wins, and of two of one version the
// smaller digest, so two owners removing each other at once leave one of them.
type doc struct {
	Version uint64   `json:"version"`
	Owners  []string `json:"owners"`
	Members []string `json:"members"`
}

func (d doc) digest() string {
	sum := sha256.Sum256(JSON(d))
	return string(sum[:])
}

func (d doc) valid() bool {
	set := func(s []string) bool { return slices.IsSorted(s) && len(slices.Compact(slices.Clone(s))) == len(s) }
	if len(d.Owners) == 0 || len(d.Members) > MaxPeers || !set(d.Owners) || !set(d.Members) {
		return false
	}
	for _, p := range d.Members {
		if _, err := ParsePeerID(p); err != nil {
			return false
		}
	}
	return !slices.ContainsFunc(d.Owners, func(o string) bool { return !slices.Contains(d.Members, o) })
}

// vaultBody is what a vault seals: a class's entries and, from an owner, every entry's version,
// deletions included, and the hand-in it folded in per member.
type vaultBody struct {
	Entries  map[string]json.RawMessage `json:"entries"`
	Versions map[string]version         `json:"versions,omitempty"`
	Handins  map[string]string          `json:"handins,omitempty"`
}

// version is where an entry stands: a Lamport counter, the owner that set it, and its value's
// digest, empty once deleted. The later (V, By) wins, so every device merging the same vaults
// holds the same set, whatever order they come in.
type version struct {
	V  uint64 `json:"v"`
	By string `json:"by"`
	D  string `json:"d,omitempty"`
}

func (v version) after(w version) bool { return v.V > w.V || v.V == w.V && v.By > w.By }

// bundle is what a group shares: per data class, entries by name; each class is its own vault.
type bundle map[string]map[string]json.RawMessage

// A Class is one kind of profile data a sync group shares, as its host keeps it, by entry name:
// Shareable is what this device may share; MergeShared folds in what the group shared and returns
// the entries it changed. keepOwn (an owner folding hand-ins) takes only entries it lacks, or a
// fresher form of one by the class's own rule; otherwise it takes the group's entries over its
// own and drops removed, those the group stopped sharing.
type Class interface {
	Shareable(ctx context.Context) (map[string]json.RawMessage, error)
	MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error)
}

// SetClass shares a data class with the sync group under name; nil stops sharing it.
func (b *Mitosis) SetClass(name string, c Class) {
	b.mu.Lock()
	if c == nil {
		delete(b.classes, name)
	} else {
		b.classes[name] = c
	}
	b.mu.Unlock()
}

func groupID(founder string) string {
	sum := sha256.Sum256([]byte("orb-mitosis-group/" + founder))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

func (b *Mitosis) current() doc {
	return doc{b.state.GroupVersion, b.state.Owners, b.state.Scopes[b.state.Group]}
}

func (b *Mitosis) leave() {
	delete(b.state.Scopes, b.state.Group)
	b.state.Group, b.state.Owners, b.state.Prior, b.state.GroupVersion, b.state.Versions, b.state.Merged = "", nil, nil, 0, nil, nil
	clear(b.vaults)
	clear(b.sealed)
}

// change is an owner's membership change, signed in its next record: add, remove, promote or
// demote peer. The last one is kept until the group's membership shows it, and made once more
// when another owner's change of the same version won.
func (b *Mitosis) change(kind, peer string) error {
	cur, self := b.current(), b.PeerID()
	if !slices.Contains(cur.Owners, self) {
		return Fail("unauthorized")
	}
	d := doc{cur.Version + 1, slices.Clone(cur.Owners), slices.Clone(cur.Members)}
	switch kind {
	case "add":
		if b.state.Peers[peer] == "" && !slices.ContainsFunc(b.state.Grants, func(g Grant) bool { return g.Principal.PeerID == peer }) {
			return Fail("not_found") // a device this one never paired with is never reached
		}
		d.Members = append(d.Members, peer)
	case "promote":
		if !slices.Contains(d.Members, peer) {
			return Fail("not_found")
		}
		d.Owners = append(d.Owners, peer)
	case "remove", "demote":
		d.Owners = slices.DeleteFunc(d.Owners, func(p string) bool { return p == peer })
		if kind == "remove" {
			d.Members = slices.DeleteFunc(d.Members, func(p string) bool { return p == peer })
		}
	}
	slices.Sort(d.Owners)
	slices.Sort(d.Members)
	d.Owners, d.Members = slices.Compact(d.Owners), slices.Compact(d.Members)
	switch {
	case len(d.Members) > MaxPeers:
		return Fail("resource_exhausted")
	case len(d.Owners) == 0:
		return Fail("operation_conflict") // a group keeps an owner
	case !slices.Equal(d.Owners, cur.Owners) || !slices.Equal(d.Members, cur.Members):
		b.state.Prior, b.state.Owners, b.state.GroupVersion = cur.Owners, d.Owners, d.Version
		b.state.Scopes[b.state.Group] = d.Members
	}
	b.op = [2]string{kind, peer}
	return nil
}

// adopt takes the newest valid membership the group's owners published until none is newer,
// leaves the group when it leaves this device out, and makes this owner's last change again when
// another's won over it.
func (b *Mitosis) adopt() {
	self, adopted := b.PeerID(), false
	for changed := true; changed && b.state.Group != ""; {
		changed = false
		for _, p := range slices.Concat(b.state.Owners, b.state.Prior) {
			cur := b.current()
			var d doc
			c, err := b.state.Records[b.state.Group+"/"+p].Verify()
			if err != nil || json.Unmarshal(c.Extensions["group"], &d) != nil || !d.valid() {
				continue
			}
			if d.Version > cur.Version && slices.Contains(cur.Owners, p) {
				b.state.Prior = cur.Owners
			} else if d.Version != cur.Version || d.digest() >= cur.digest() || !slices.Contains(b.state.Prior, p) {
				continue
			}
			b.state.Owners, b.state.GroupVersion, b.state.Scopes[b.state.Group] = d.Owners, d.Version, d.Members
			changed, adopted = true, true
		}
	}
	cur, op := b.current(), b.op
	if b.state.Group != "" && !slices.Contains(cur.Members, self) {
		b.leave()
	}
	if adopted {
		_ = b.save()
	}
	if b.state.Group == "" {
		return
	}
	shown := map[string]bool{"add": slices.Contains(cur.Members, op[1]), "promote": slices.Contains(cur.Owners, op[1]),
		"remove": !slices.Contains(cur.Members, op[1]), "demote": !slices.Contains(cur.Owners, op[1])}
	if adopted && op[0] != "" {
		if !shown[op[0]] && b.change(op[0], op[1]) == nil {
			_ = b.save()
		}
		b.op = [2]string{}
	}
}

// SyncAdmin is the sync admin method: one membership change, then a round. join follows a paired
// device's group, and a device leaves its group by removing itself; add, remove, promote and
// demote are an owner's, and the first add creates the group. endpoints, when set, are where this
// device is reached; nil keeps the ones it published before.
func (b *Mitosis) SyncAdmin(ctx context.Context, params json.RawMessage, endpoints []Locator) (json.RawMessage, error) {
	var p struct {
		Join    string `json:"join,omitempty"`
		Add     string `json:"add,omitempty"`
		Remove  string `json:"remove,omitempty"`
		Promote string `json:"promote,omitempty"`
		Demote  string `json:"demote,omitempty"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	self := b.PeerID()
	kind, peer := "", cmp.Or(p.Join, p.Add, p.Remove, p.Promote, p.Demote)
	for k, v := range map[string]string{"add": p.Add, "remove": p.Remove, "promote": p.Promote, "demote": p.Demote} {
		if v != "" {
			kind = k
		}
	}
	if _, err := ParsePeerID(peer); peer != "" && err != nil {
		return nil, err
	}
	if peer == self && kind != "remove" {
		return nil, Fail("not_found")
	}
	var joined struct {
		Group  string   `json:"group_id"`
		Owners []string `json:"owners"`
	}
	if p.Join != "" {
		c := b.Connection(p.Join)
		if c == nil {
			return nil, Fail("unavailable")
		}
		if err := c.Call(ctx, "sync.group", struct{}{}, &joined); err != nil {
			return nil, err
		}
		if d := (doc{Owners: joined.Owners, Members: joined.Owners}); !d.valid() || len(joined.Group) != 22 {
			return nil, Fail("unauthorized")
		}
	}
	if peer == self && kind == "remove" {
		b.mu.Lock()
		owners := slices.Clone(b.state.Owners)
		b.mu.Unlock()
		b.tellLeave(ctx, owners)
	}
	b.round.Lock()
	defer b.round.Unlock()
	b.mu.Lock()
	var err error
	group := b.state.Group
	switch {
	case p.Join != "":
		if len(b.state.Scopes) >= 128 && group == "" {
			err = Fail("resource_exhausted")
			break
		}
		b.leave()
		b.state.Group, b.state.Owners = joined.Group, joined.Owners
		members := slices.Concat(joined.Owners, []string{p.Join, self})
		slices.Sort(members)
		b.state.Scopes[joined.Group] = slices.Compact(members)
	case peer == self:
		b.leave()
	case group == "" && kind != "add":
		err = Fail("not_found")
	case kind == "": // a round
	case group == "":
		if len(b.state.Scopes) >= 128 {
			err = Fail("resource_exhausted")
			break
		}
		b.state.Group, b.state.Owners, b.state.GroupVersion = groupID(self), []string{self}, 0
		b.state.Scopes[b.state.Group] = []string{self}
		fallthrough
	default:
		err = b.change(kind, peer)
	}
	if err == nil {
		err = b.save()
	}
	group = b.state.Group
	b.mu.Unlock()
	if err != nil || group == "" {
		return JSON(struct{}{}), err
	}
	out, err := b.sync(ctx, endpoints)
	if kind == "remove" {
		b.tellRemoved(peer, group)
	}
	return out, err
}

// tellLeave asks a connected owner to remove this device from the group's membership before it
// leaves locally, so forgetting a browser tab or leaving from a member does not leave a ghost
// member on the owners. Best effort: an offline device still leaves locally.
func (b *Mitosis) tellLeave(ctx context.Context, owners []string) {
	self := b.PeerID()
	for _, owner := range owners {
		if owner == self {
			continue
		}
		c := b.Connection(owner)
		if c == nil {
			continue
		}
		call, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := c.Call(call, "sync.leave", struct{}{}, nil)
		cancel()
		if err == nil {
			return
		}
	}
}

// tellRemoved hands a device this owner just left out its record, whose membership no longer
// counts it, over a channel already open; one offline is refused by the group from then on.
func (b *Mitosis) tellRemoved(peer, group string) {
	c := b.Connection(peer)
	b.mu.Lock()
	r, ok := b.state.Records[group+"/"+b.PeerID()]
	b.mu.Unlock()
	if c == nil || !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Call(ctx, "peers.publish", map[string]any{"records": []Record{r}}, nil)
}

// Sync runs one round of the group. Each owner seals what it holds of a class, every entry with
// its version, deletions included; every device merges the owners' vaults it opens entry by
// entry, the later version winning, so all hold the same set whatever order the vaults arrive
// in. A member takes that set over its own and hands the owners only what it lacks or holds
// otherwise; an owner folds in those hand-ins, keeping its own, and versions what it changed.
// It returns the group and, per class, the entries the round changed here.
func (b *Mitosis) Sync(ctx context.Context, endpoints []Locator) (json.RawMessage, error) {
	b.round.Lock()
	defer b.round.Unlock()
	return b.sync(ctx, endpoints)
}

func (b *Mitosis) sync(ctx context.Context, endpoints []Locator) (json.RawMessage, error) {
	b.mu.Lock()
	b.adopt()
	group, classes, self := b.state.Group, maps.Clone(b.classes), b.PeerID()
	members, owners := slices.Clone(b.state.Scopes[group]), slices.Clone(b.state.Owners)
	if group == "" || len(classes) == 0 {
		b.mu.Unlock()
		return nil, Fail("not_found")
	}
	owner := slices.Contains(owners, self)
	type sent struct {
		peer  string
		vault pointer
	}
	sets := map[string][]sent{}    // class -> the other owners' vaults
	handins := map[string][]sent{} // class -> members' hand-ins an owner has yet to open
	current := map[string]string{} // "<member> <class>" -> the hand-in it publishes now
	for _, peer := range members {
		for class, v := range b.pointers(group, peer) {
			current[peer+" "+class] = v.Digest
			switch {
			case classes[class] == nil || b.state.Blocked[peer] || peer == self:
			case slices.Contains(owners, peer):
				sets[class] = append(sets[class], sent{peer, v})
			case owner && b.state.Merged[peer+" "+class] != v.Digest:
				handins[class] = append(handins[class], sent{peer, v})
			}
		}
	}
	versions := map[string]map[string]version{}
	for class := range classes {
		versions[class] = maps.Clone(b.state.Versions[class])
		if versions[class] == nil {
			versions[class] = map[string]version{}
		}
	}
	b.mu.Unlock()

	opened := map[string][]byte{}
	merged := map[string]string{}
	stale := map[string]bool{}
	opens := map[string][]vaultBody{} // class -> other owners' vaults opened, and who each is sealed to
	readers := map[string][][]string{}
	out := bundle{}
	received := map[string][]string{}
	for name, class := range classes {
		vs, values, newer := versions[name], map[string]json.RawMessage{}, map[string]bool{}
		for _, s := range sets[name] {
			blob, err := b.fetch(ctx, s.vault, s.peer, true)
			var in vaultBody
			var to []string
			if err == nil {
				in, to, err = b.open(blob)
			}
			if err != nil {
				continue
			}
			opened[s.vault.Digest] = blob
			opens[name], readers[name] = append(opens[name], in), append(readers[name], append(to, s.peer))
			for entry, v := range in.Versions {
				if v.after(vs[entry]) {
					vs[entry], newer[entry] = v, true
				}
			}
			for member, digest := range in.Handins { // consumed already: none undoes a later change
				if digest == current[member+" "+name] {
					merged[member+" "+name] = digest
				}
			}
		}
		for _, in := range opens[name] {
			for entry, v := range in.Versions {
				if v == vs[entry] && v.D != "" {
					values[entry] = in.Entries[entry]
				}
			}
		}
		if !owner && len(sets[name]) > 0 && len(opens[name]) == 0 {
			// Until it opens an owner's vault of a class, a member leaves that class as it was:
			// its way holds back no credential.
			stale[name] = true
			continue
		}
		// An owner takes what is newer than what it holds; a member takes the whole set, so what
		// it changed of it comes back to the group's.
		incoming, removed := map[string]json.RawMessage{}, []string{}
		for entry, v := range vs {
			switch {
			case !owner && !newer[entry] && v.D != "" && values[entry] != nil:
				incoming[entry] = values[entry]
			case !newer[entry]:
			case v.D == "":
				removed = append(removed, entry)
			default:
				incoming[entry] = values[entry]
			}
		}
		changed, err := class.MergeShared(ctx, []map[string]json.RawMessage{incoming}, false, removed)
		if err != nil {
			return nil, err
		}
		var folded []map[string]json.RawMessage
		for _, s := range handins[name] {
			if merged[s.peer+" "+name] == s.vault.Digest {
				continue
			}
			blob, err := b.fetch(ctx, s.vault, s.peer, false)
			var in vaultBody
			if err == nil {
				in, _, err = b.open(blob)
			}
			if err == nil && in.Versions == nil { // a former owner's vault is no hand-in
				folded, merged[s.peer+" "+name] = append(folded, in.Entries), s.vault.Digest
			}
		}
		if len(folded) > 0 {
			c, err := class.MergeShared(ctx, folded, true, nil)
			if err != nil {
				return nil, err
			}
			changed = append(changed, c...)
		}
		own, err := class.Shareable(ctx)
		if err != nil {
			return nil, err
		}
		if owner { // what this owner holds otherwise than its versions say, it versions anew
			next := uint64(0)
			for _, v := range vs {
				next = max(next, v.V)
			}
			next++
			for entry, value := range own {
				if d := digests(value); vs[entry].D != d {
					vs[entry] = version{next, self, d}
				}
			}
			for entry, v := range vs {
				if _, kept := own[entry]; v.D != "" && !kept {
					vs[entry] = version{next, self, ""}
				}
			}
		} else {
			maps.DeleteFunc(own, func(entry string, value json.RawMessage) bool { return vs[entry].D == digests(value) })
		}
		out[name] = own
		if len(changed) > 0 {
			received[name] = slices.Compact(slices.Sorted(slices.Values(changed)))
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Group != group || !slices.Equal(b.state.Owners, owners) || !slices.Equal(b.state.Scopes[group], members) {
		return nil, Fail("not_found")
	}
	before := string(JSON([]any{b.state.Merged, b.state.Versions}))
	if b.state.Merged == nil {
		b.state.Merged = map[string]string{}
	}
	maps.Copy(b.state.Merged, merged)
	if b.state.Versions == nil {
		b.state.Versions = map[string]map[string]version{}
	}
	for name := range out {
		b.state.Versions[name] = versions[name]
	}
	recipients := owners
	if owner {
		recipients = members
	}
	recipients = slices.DeleteFunc(slices.Clone(recipients), func(p string) bool { return p == self || b.state.Blocked[p] })
	r, published := b.state.Records[group+"/"+self]
	if published && endpoints == nil {
		c, _ := r.Verify()
		endpoints = c.Endpoints
	}
	// Each class is sealed apart, and again only when what it seals changes: a refreshed token
	// is not a reason to send every member the whole memory.
	vaults := map[string]pointer{}
	total := 0
	b.handing = false
	for name, entries := range out {
		b.handing = b.handing || !owner && len(entries) > 0
		body := vaultBody{Entries: entries}
		if owner {
			body.Versions, body.Handins = versions[name], map[string]string{}
			for _, m := range members {
				if d := b.state.Merged[m+" "+name]; d != "" {
					body.Handins[m] = d
				}
			}
			same := func(in vaultBody, to []string) bool {
				return string(JSON([]any{in.Entries, in.Versions})) == string(JSON([]any{entries, body.Versions})) &&
					!slices.ContainsFunc(recipients, func(p string) bool { return !slices.Contains(to, p) })
			}
			said := false
			for i, in := range opens[name] {
				said = said || same(in, readers[name][i])
			}
			if said {
				continue // another owner's vault already says it, to everyone
			}
		}
		if len(recipients) == 0 {
			continue
		}
		sum := sha256.Sum256(JSON([]any{recipients, body}))
		last, ok := b.sealed[name]
		if !ok || last.sum != sum || b.vaults[last.vault.Digest] == nil {
			blob, err := seal(recipients, body)
			if err != nil {
				return nil, err
			}
			last = sealed{sum, pointer{vaultSchema, vaultDigest(blob), len(blob)}}
			b.sealed[name], b.vaults[last.vault.Digest] = last, blob
		}
		vaults[name] = last.vault
		if total += last.vault.Size; total > maxVault {
			return nil, Fail("resource_exhausted")
		}
	}
	for name := range stale {
		if last, ok := b.sealed[name]; ok {
			vaults[name] = last.vault // the hand-in stands until an owner's vault opens
		}
	}
	ext := map[string]json.RawMessage{}
	if len(vaults) > 0 {
		ext["vaults"] = JSON(vaults)
	}
	// Every device carries the membership it holds: one that was an owner still hands its last
	// change to a member that knows it alone as an owner.
	ext["group"] = JSON(b.current())
	sum := sha256.Sum256(JSON([]any{ext, endpoints, b.name}))
	if digest := string(sum[:]); !published || digest != b.synced {
		if _, err := b.publishContact(group, b.name, endpoints, false, ext); err != nil {
			return nil, err
		}
		b.synced = digest
	}
	if string(JSON([]any{b.state.Merged, b.state.Versions})) != before {
		if err := b.save(); err != nil {
			return nil, err
		}
	}
	// This device serves its own vaults and the owners' it opened; nothing else.
	keep := map[string]bool{}
	for _, v := range vaults {
		keep[v.Digest] = true
	}
	for digest, blob := range opened {
		keep[digest], b.vaults[digest] = true, blob
	}
	maps.DeleteFunc(b.vaults, func(digest string, _ []byte) bool { return !keep[digest] })
	maps.DeleteFunc(b.sealed, func(_ string, s sealed) bool { return !keep[s.vault.Digest] })
	return b.groupResult(group, owner || len(opened) > 0, received), nil
}

// groupResult is what a round reports: the group, its members, owners and names, whether this
// device holds the group's set (in_sync), and per class the entries the round changed here.
func (b *Mitosis) groupResult(group string, inSync bool, received map[string][]string) json.RawMessage {
	if received == nil {
		received = map[string][]string{}
	}
	names := map[string]string{}
	for _, p := range b.state.Scopes[group] {
		if r, ok := b.state.Records[group+"/"+p]; ok {
			if c, err := r.Verify(); err == nil {
				names[p] = c.Name // a browser tab has none: present still means it published here
			}
		}
	}
	return JSON(map[string]any{"group_id": group, "self": b.PeerID(), "owners": b.state.Owners, "members": b.state.Scopes[group], "names": names, "in_sync": inSync, "received": received})
}

// sealed is a class this device last sealed: a digest of what it sealed, and its vault.
type sealed struct {
	sum   [32]byte
	vault pointer
}

func digests(v json.RawMessage) string {
	sum := sha256.Sum256(v)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// pointers reads the vaults, by class, that peer's record in the group points at: none unless
// every one is well formed and together they stay within maxVault.
func (b *Mitosis) pointers(group, peer string) map[string]pointer {
	r, ok := b.state.Records[group+"/"+peer]
	if !ok {
		return nil
	}
	var vaults map[string]pointer
	c, err := r.Verify()
	if err != nil || c.Withdrawn || json.Unmarshal(c.Extensions["vaults"], &vaults) != nil || len(vaults) > 16 {
		return nil
	}
	total := 0
	for _, v := range vaults {
		if total += v.Size; v.Schema != vaultSchema || len(v.Digest) != 43 || v.Size < 2 || total > maxVault {
			return nil
		}
	}
	return vaults
}

func vaultDigest(blob []byte) string {
	sum := sha256.Sum256(blob)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// fetch returns the vault v points at, from this device or over a channel already open, in either
// direction, to its publisher first, then (for the owner's, relayed) to any other peer: a Worker object cannot dial, so it
// fetches over the streams its peers opened, and a device just added knows no member but the
// owner yet. Holders serve only the vault's recipients, and it is checked against the digest its
// publisher signed before anything opens it.
func (b *Mitosis) fetch(ctx context.Context, v pointer, publisher string, relayed bool) ([]byte, error) {
	b.mu.Lock()
	blob := b.vaults[v.Digest]
	holders := slices.Collect(maps.Keys(b.channels)) // map order spreads a new device's fetches
	b.mu.Unlock()
	if blob != nil {
		return blob, nil
	}
	if i := slices.Index(holders, publisher); i > 0 {
		holders[0], holders[i] = holders[i], holders[0]
	}
	if !relayed { // a member's hand-in lives on its publisher alone: no channel, no round spent asking
		holders = slices.DeleteFunc(holders, func(h string) bool { return h != publisher })
	}
	for _, holder := range holders {
		c := b.Connection(holder)
		if c == nil {
			continue
		}
		blob = nil
		for len(blob) < v.Size {
			var chunk struct {
				Size int    `json:"size"`
				Data []byte `json:"data"`
			}
			call, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := c.Call(call, "vault.get", map[string]any{"digest": v.Digest, "offset": len(blob)}, &chunk)
			cancel()
			if err != nil || chunk.Size != v.Size || len(chunk.Data) == 0 || len(blob)+len(chunk.Data) > v.Size {
				break
			}
			blob = append(blob, chunk.Data...)
		}
		if len(blob) == v.Size && vaultDigest(blob) == v.Digest {
			return blob, nil
		}
	}
	return nil, Fail("unavailable")
}

// serveVault answers vault.get: a vault this device holds, only to a current, unblocked member of
// its group that the vault is sealed to.
func (b *Mitosis) serveVault(peer string, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Digest string `json:"digest"`
		Offset int    `json:"offset"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	b.mu.Lock()
	blob := b.vaults[p.Digest]
	member := b.state.Group != "" && !b.state.Blocked[peer] && slices.Contains(b.state.Scopes[b.state.Group], peer)
	b.mu.Unlock()
	envelopes, _, err := split(blob)
	if !member || err != nil || envelopes[peer] == nil || p.Offset < 0 || p.Offset >= len(blob) {
		return nil, Fail("not_found")
	}
	return JSON(map[string]any{"size": len(blob), "data": blob[p.Offset:min(len(blob), p.Offset+vaultChunk)]}), nil
}

// seal encrypts a class's entries once under a fresh key, and that key to each recipient's X25519
// key. A vault is the recipient count (two bytes, big-endian), one envelope per recipient, then
// the sealed entries.
func seal(recipients []string, in vaultBody) ([]byte, error) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	out := binary.BigEndian.AppendUint16(nil, uint16(len(recipients)))
	for _, peer := range recipients {
		id, err := ParsePeerID(peer)
		if err != nil {
			return nil, err
		}
		to, err := sealingKey(peer)
		if err != nil {
			return nil, err
		}
		ephemeral, _ := ecdh.X25519().GenerateKey(rand.Reader)
		shared, err := ephemeral.ECDH(to)
		if err != nil {
			return nil, err
		}
		pub := ephemeral.PublicKey().Bytes()
		envelope, err := aead(envelopeKey(shared, pub, to.Bytes()), key, pub, true)
		if err != nil {
			return nil, err
		}
		out = slices.Concat(out, id, pub, envelope)
	}
	sealed, err := aead(key, JSON(in), nil, true)
	if err != nil {
		return nil, err
	}
	if out = append(out, sealed...); len(out) > maxVault {
		return nil, Fail("resource_exhausted")
	}
	return out, nil
}

// split reads a vault's envelopes, by recipient, and its sealed bundle.
func split(blob []byte) (map[string][]byte, []byte, error) {
	if len(blob) < 2 {
		return nil, nil, Fail("unauthorized")
	}
	n := int(binary.BigEndian.Uint16(blob))
	if len(blob) < 2+n*envelopeSize {
		return nil, nil, Fail("unauthorized")
	}
	envelopes := make(map[string][]byte, n)
	for i := range n {
		e := blob[2+i*envelopeSize:][:envelopeSize]
		envelopes[PeerID(ed25519.PublicKey(e[:32]))] = e[32:]
	}
	return envelopes, blob[2+n*envelopeSize:], nil
}

// open decrypts a vault sealed to this device and returns its entries and recipients.
func (b *Mitosis) open(blob []byte) (vaultBody, []string, error) {
	envelopes, sealed, err := split(blob)
	if err != nil {
		return vaultBody{}, nil, err
	}
	envelope := envelopes[b.PeerID()]
	if envelope == nil {
		return vaultBody{}, slices.Collect(maps.Keys(envelopes)), Fail("unauthorized")
	}
	sum := sha512.Sum512(b.state.Key[:ed25519.SeedSize])
	self, err := ecdh.X25519().NewPrivateKey(sum[:32])
	if err != nil {
		return vaultBody{}, nil, err
	}
	pub := envelope[:32]
	ephemeral, err := ecdh.X25519().NewPublicKey(pub)
	if err != nil {
		return vaultBody{}, nil, err
	}
	shared, err := self.ECDH(ephemeral)
	if err != nil {
		return vaultBody{}, nil, err
	}
	key, err := aead(envelopeKey(shared, pub, self.PublicKey().Bytes()), envelope[32:], pub, false)
	if err != nil {
		return vaultBody{}, nil, err
	}
	plain, err := aead(key, sealed, nil, false)
	var out vaultBody
	if err == nil {
		err = json.Unmarshal(plain, &out)
	}
	return out, slices.Collect(maps.Keys(envelopes)), err
}

// sealingKey is the Montgomery form of a peer's Ed25519 identity (RFC 7748 §4.1), the X25519 key
// whose private half is the clamped SHA-512 of its seed, so sealing to a peer needs only its PeerID.
func sealingKey(peer string) (*ecdh.PublicKey, error) {
	key, err := ParsePeerID(peer)
	if err != nil {
		return nil, err
	}
	le := slices.Clone(key)
	le[31] &= 0x7f
	slices.Reverse(le)
	p := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	y := new(big.Int).SetBytes(le)
	inverse := new(big.Int).ModInverse(new(big.Int).Mod(new(big.Int).Sub(big.NewInt(1), y), p), p)
	if inverse == nil {
		return nil, Fail("unauthorized")
	}
	u := new(big.Int).Mul(new(big.Int).Add(big.NewInt(1), y), inverse)
	out := u.Mod(u, p).FillBytes(make([]byte, 32))
	slices.Reverse(out)
	return ecdh.X25519().NewPublicKey(out)
}

func envelopeKey(shared, ephemeral, recipient []byte) []byte {
	sum := sha256.Sum256(slices.Concat([]byte("orb-mitosis-seal/1\n"), shared, ephemeral, recipient))
	return sum[:]
}

// aead is AES-256-GCM with a random nonce prefixed to the ciphertext.
func aead(key, data, ad []byte, encrypt bool) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	if encrypt {
		return gcm.Seal(nil, nil, data, ad), nil
	}
	return gcm.Open(nil, nil, data, ad)
}
