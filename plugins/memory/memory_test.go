package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNodeKeyAndParseRef(t *testing.T) {
	k := NodeKey{Level: 3, Index: 2}
	if k.Start() != 16 || k.Span() != 8 || k.End() != 24 || k.Name() != "16+8" {
		t.Fatalf("unexpected NodeKey metrics: start=%d span=%d end=%d name=%q", k.Start(), k.Span(), k.End(), k.Name())
	}

	p := Part{Level: 2, Index: 1}
	if p.Start() != 4 || p.Span() != 4 || p.End() != 8 || p.Name() != "4+4" {
		t.Fatalf("unexpected Part metrics: %+v", p)
	}

	tests := []struct {
		ref    string
		id, n  int
		wantOK bool
	}{
		{"0+1", 0, 1, true},
		{"16+8", 16, 8, true},
		{" 4+2 ", 4, 2, true},
		{"", 0, 0, false},
		{"12", 0, 0, false},
		{"-1+2", 0, 0, false},
		{"0+0", 0, 0, false},
		{"abc+2", 0, 0, false},
		{"2+xyz", 0, 0, false},
	}
	for _, tc := range tests {
		id, n, ok := ParseRef(tc.ref)
		if ok != tc.wantOK || id != tc.id || n != tc.n {
			t.Errorf("ParseRef(%q) = (%d, %d, %v), want (%d, %d, %v)", tc.ref, id, n, ok, tc.id, tc.n, tc.wantOK)
		}
	}

	if h, l := NormalizeConfig(Config{}); h != DefaultViewBytes || l != DefaultViewLowBytes {
		t.Fatalf("NormalizeConfig(default) = (%d, %d), want (%d, %d)", h, l, DefaultViewBytes, DefaultViewLowBytes)
	}
	if h, l := NormalizeConfig(Config{ViewBytes: 1024}); h != MinViewBytes || l != MinViewBytes*2/3 {
		t.Fatalf("NormalizeConfig(min) = (%d, %d)", h, l)
	}
	if h, l := NormalizeConfig(Config{ViewBytes: 999999}); h != MaxViewBytes || l != MaxViewBytes*2/3 {
		t.Fatalf("NormalizeConfig(max) = (%d, %d)", h, l)
	}
}

func TestMaterializeFreeNodesZeroLLM(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	// 4 short entries: all 4 fit at level 0, pairs fit at level 1, and all 4 fit at level 2!
	for i := 0; i < 4; i++ {
		_, err := store.AppendEntry(ctx, Entry{
			Kind: KindUser,
			Text: fmt.Sprintf("short msg %d", i),
			TS:   now.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	created, err := MaterializeFreeNodes(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	// 4 level-0 + 2 level-1 + 1 level-2 = 7 free nodes with 0 LLM calls.
	if created != 7 {
		t.Fatalf("created = %d, want 7", created)
	}

	root, ok, err := store.GetNode(ctx, NodeKey{Level: 2, Index: 0})
	if err != nil || !ok {
		t.Fatalf("expected level-2 free node, ok=%v err=%v", ok, err)
	}
	if !strings.Contains(root.Text, "short msg 0") || !strings.Contains(root.Text, "short msg 3") {
		t.Fatalf("unexpected root text: %q", root.Text)
	}

	// Second call is idempotent (0 new nodes).
	created2, err := MaterializeFreeNodes(ctx, store)
	if err != nil || created2 != 0 {
		t.Fatalf("second MaterializeFreeNodes = (%d, %v), want (0, nil)", created2, err)
	}
}

func TestBuildViewPrefixStabilityAndBatchMerge(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	// Use 4 KiB budget (high=4096, low=2730).
	// Each entry ~240 bytes -> 16 entries ~3950 bytes (under 4096).
	payload := strings.Repeat("a", 230)
	for i := 0; i < 12; i++ {
		_, err := store.AppendEntry(ctx, Entry{
			Kind: KindTalk,
			Text: fmt.Sprintf("entry-%02d %s", i, payload),
			TS:   now.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Pre-populate level-1 summary nodes so batch-merge can compress pairs cleanly.
	for i := 0; i < 6; i++ {
		if err := store.PutNode(ctx, Node{
			Level: 1,
			Index: i,
			Text:  fmt.Sprintf("summary of pair %d", i),
			TS:    now.Add(time.Duration(2*i+1) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	v1, err := BuildView(ctx, store, nil, MinViewBytes)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Bytes > MinViewBytes {
		t.Fatalf("v1.Bytes = %d, expected <= %d", v1.Bytes, MinViewBytes)
	}
	if len(v1.Parts) != 12 {
		t.Fatalf("len(v1.Parts) = %d, want 12", len(v1.Parts))
	}

	// Append 2 more entries (still under 4096 B): prefix must be 100% byte-identical!
	for i := 12; i < 14; i++ {
		_, err := store.AppendEntry(ctx, Entry{
			Kind: KindTalk,
			Text: fmt.Sprintf("entry-%02d %s", i, payload),
			TS:   now.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	v2, err := BuildView(ctx, store, v1, MinViewBytes)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Bytes > MinViewBytes {
		t.Fatalf("v2.Bytes = %d, expected <= %d", v2.Bytes, MinViewBytes)
	}
	for i := range v1.Lines {
		if v2.Lines[i] != v1.Lines[i] {
			t.Fatalf("prefix line %d changed before high-water mark: %q != %q", i, v2.Lines[i], v1.Lines[i])
		}
	}

	// Append 4 more entries to exceed 4096 B -> triggers batch-merge down to <= low (2730 B).
	for i := 14; i < 18; i++ {
		_, err := store.AppendEntry(ctx, Entry{
			Kind: KindTalk,
			Text: fmt.Sprintf("entry-%02d %s", i, payload),
			TS:   now.Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 6; i < 9; i++ {
		_ = store.PutNode(ctx, Node{
			Level: 1,
			Index: i,
			Text:  fmt.Sprintf("summary of pair %d", i),
			TS:    now.Add(time.Duration(2*i+1) * time.Minute),
		})
	}

	v3, err := BuildView(ctx, store, v2, MinViewBytes)
	if err != nil {
		t.Fatal(err)
	}
	if v3.Bytes > v3.LowBytes {
		t.Fatalf("after batch-merge, v3.Bytes = %d, want <= LowBytes (%d)", v3.Bytes, v3.LowBytes)
	}
	// Oldest pair (0+2) must have been merged first by age-due priority.
	if v3.Parts[0].Name() != "0+2" {
		t.Fatalf("expected oldest part to be merged to 0+2, got %s", v3.Parts[0].Name())
	}
}

func TestZoomSearchDateAndProfile(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	// Entry 0: large verbatim entry (> 4096 bytes) to test paging.
	longText := strings.Repeat("alpha-beta-gamma ", 350) // ~5950 bytes
	_, _ = store.AppendEntry(ctx, Entry{Kind: KindWork, Text: longText, Peer: "alice", Session: "s1", TS: now})
	_, _ = store.AppendEntry(ctx, Entry{Kind: KindUser, Text: "deploy postgres vault", Peer: "bob", Session: "s1", TS: now.Add(time.Minute)})
	_, _ = store.AppendEntry(ctx, Entry{Kind: KindTalk, Text: "vault migration complete", Peer: "orb", Session: "s1", TS: now.Add(2 * time.Minute)})
	_, _ = store.AppendEntry(ctx, Entry{Kind: KindNote, Text: "remember mitosis sync", Peer: "orb", Session: "s1", TS: now.Add(3 * time.Minute)})

	_ = store.PutNode(ctx, Node{Level: 1, Index: 0, Text: "pair 0-1 summary", TS: now.Add(time.Minute)})
	_ = store.PutNode(ctx, Node{Level: 1, Index: 1, Text: "pair 2-3 summary", TS: now.Add(3 * time.Minute)})

	// Zoom n=1 page 1
	page1, err := Zoom(ctx, store, 0, 1, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page1, "[entry #0 · work ·") || !strings.Contains(page1, "[more: offset=") {
		t.Fatalf("unexpected page1: %s", page1[:min(200, len(page1))])
	}

	// Zoom n=1 page 2
	page2, err := Zoom(ctx, store, 0, 1, 1, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page2, "[more: offset=") {
		t.Fatalf("expected page2 to be final page, got: %s", page2[max(0, len(page2)-100):])
	}

	// Zoom n=4 depth=1 -> two halves 0+2 and 2+2
	z4, err := Zoom(ctx, store, 0, 4, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(z4, "0+2: pair 0-1 summary") || !strings.Contains(z4, "2+2: pair 2-3 summary") {
		t.Fatalf("unexpected z4: %s", z4)
	}

	// Zoom invalid alignment
	if _, err := Zoom(ctx, store, 1, 4, 1, 0); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("expected ErrInvalidRef for unaligned zoom, got %v", err)
	}

	// Date
	dateStr, err := Date(ctx, store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dateStr, "2026-04-01T12:01:00Z") || !strings.Contains(dateStr, "peer=bob") {
		t.Fatalf("unexpected Date output: %q", dateStr)
	}

	// Search
	hits, err := Search(ctx, store, "postgres vault", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].ID != 1 {
		t.Fatalf("expected entry 1 as top search hit, got %+v", hits)
	}

	// Profile helpers: add, dedup, set, remove, 1375-char cap
	var prof []ProfileEntry
	var added ProfileEntry
	prof, added, err = ProfileAdd(prof, "Prefers concise Go code", now)
	if err != nil || len(prof) != 1 {
		t.Fatalf("ProfileAdd failed: len=%d err=%v", len(prof), err)
	}
	// Duplicate (case-insensitive) is a no-op
	prof, _, err = ProfileAdd(prof, "prefers concise go code", now)
	if err != nil || len(prof) != 1 {
		t.Fatalf("expected dedup, got len=%d err=%v", len(prof), err)
	}
	// Set updates existing
	prof, _, err = ProfileSet(prof, added.ID, "Prefers idiomatic Go", now)
	if err != nil || prof[0].Content != "Prefers idiomatic Go" {
		t.Fatalf("ProfileSet failed: %+v err=%v", prof, err)
	}
	// Cap at ProfileMaxChars (1375)
	if _, _, err := ProfileAdd(prof, strings.Repeat("x", ProfileMaxChars), now); !errors.Is(err, ErrProfileTooLarge) {
		t.Fatalf("expected ErrProfileTooLarge, got %v", err)
	}
	// Remove
	var removed bool
	prof, removed = ProfileRemove(prof, added.ID)
	if !removed || len(prof) != 0 {
		t.Fatalf("ProfileRemove failed: removed=%v len=%d", removed, len(prof))
	}
}

func TestCompactorRetriesTruncationContextAndLifecycle(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC)

	// Add 20 short preceding entries + 1 oversized entry (ID 20) to verify <= 15 context lines.
	for i := 0; i < 20; i++ {
		_, _ = store.AppendEntry(ctx, Entry{Kind: KindUser, Text: fmt.Sprintf("ctx line %d", i), TS: now.Add(time.Duration(i) * time.Second)})
	}
	_, _ = store.AppendEntry(ctx, Entry{Kind: KindWork, Text: strings.Repeat("oversized-work-log ", 40), TS: now.Add(time.Minute)})

	// 1. Nil summarizer: materializes free nodes only, non-blocking.
	nilComp := NewCompactor(store, nil, nil)
	nFree, err := nilComp.CompactOnce(ctx, 16)
	if err != nil || nFree == 0 {
		t.Fatalf("nil summarizer CompactOnce = (%d, %v), want >0 free nodes", nFree, err)
	}
	if _, ok, _ := store.GetNode(ctx, NodeKey{Level: 0, Index: 20}); ok {
		t.Fatalf("oversized entry 20 should not have a free node")
	}

	// 2. Mock summarizer: exceeds 512 B twice, then fits on 3rd try.
	var calls int
	var maxContextSeen int
	comp := NewCompactor(store, func(ctx context.Context, sys, user string) (string, error) {
		calls++
		// Count preceding context lines in user prompt.
		if idx := strings.Index(user, "Target to summarize"); idx > 0 {
			ctxPart := user[:idx]
			lineCount := 0
			for _, l := range strings.Split(ctxPart, "\n") {
				if strings.Contains(l, "+1: ") {
					lineCount++
				}
			}
			if lineCount > maxContextSeen {
				maxContextSeen = lineCount
			}
		}
		if calls <= 2 {
			return strings.Repeat("too-long ", 70), nil // > 512 bytes
		}
		return "concise summary of oversized work log", nil
	}, nil)

	nComp, err := comp.CompactOnce(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if nComp != 1 || calls != 3 {
		t.Fatalf("CompactOnce = %d, calls = %d, want (1, 3)", nComp, calls)
	}
	if maxContextSeen > CompactContextLines {
		t.Fatalf("sent %d context lines, max allowed is %d", maxContextSeen, CompactContextLines)
	}
	node20, ok, _ := store.GetNode(ctx, NodeKey{Level: 0, Index: 20})
	if !ok || node20.Text != "concise summary of oversized work log" {
		t.Fatalf("unexpected node20: ok=%v text=%q", ok, node20.Text)
	}

	// 3. Exceeds CompactMaxTries (5) -> clean UTF-8 truncation with multi-byte runes.
	_, _ = store.AppendEntry(ctx, Entry{Kind: KindWork, Text: strings.Repeat("another-big-entry ", 40), TS: now.Add(2 * time.Minute)})
	var truncCalls int
	compTrunc := NewCompactor(store, func(ctx context.Context, sys, user string) (string, error) {
		truncCalls++
		return strings.Repeat("é", 300), nil // 600 UTF-8 bytes
	}, nil)
	if _, err := compTrunc.CompactOnce(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if truncCalls != CompactMaxTries {
		t.Fatalf("truncCalls = %d, want %d", truncCalls, CompactMaxTries)
	}
	node21, ok, _ := store.GetNode(ctx, NodeKey{Level: 0, Index: 21})
	if !ok || len(node21.Text) > NodeBytes || !utf8.ValidString(node21.Text) {
		t.Fatalf("invalid truncated node21: ok=%v len=%d validUTF8=%v", ok, len(node21.Text), utf8.ValidString(node21.Text))
	}

	// 4. Background Start/Trigger/Stop under -race.
	var writes atomic.Int32
	bgStore := NewMemoryStore()
	_, _ = bgStore.AppendEntry(ctx, Entry{Kind: KindUser, Text: "hello bg", TS: now})
	doneCh := make(chan struct{}, 1)
	bg := NewCompactor(bgStore, nil, func() {
		writes.Add(1)
		select {
		case doneCh <- struct{}{}:
		default:
		}
	})
	bg.Start(ctx)
	bg.Trigger()
	bg.Trigger() // non-blocking even if already queued
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background compactor")
	}
	bg.Stop()
	bg.Stop() // idempotent Stop
	if writes.Load() < 1 {
		t.Fatalf("expected onWrite to be called")
	}
}

type countingStore struct {
	*MemoryStore
	writes atomic.Int32
}

func (c *countingStore) AppendEntry(ctx context.Context, e Entry) (Entry, error) {
	c.writes.Add(1)
	return c.MemoryStore.AppendEntry(ctx, e)
}
func (c *countingStore) PutEntry(ctx context.Context, e Entry) error {
	c.writes.Add(1)
	return c.MemoryStore.PutEntry(ctx, e)
}
func (c *countingStore) PutNode(ctx context.Context, n Node) error {
	c.writes.Add(1)
	return c.MemoryStore.PutNode(ctx, n)
}
func (c *countingStore) SaveProfile(ctx context.Context, entries []ProfileEntry) error {
	c.writes.Add(1)
	return c.MemoryStore.SaveProfile(ctx, entries)
}

func TestMitosisSharedSyncIdempotencyAndConvergence(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC)

	ownerStore := &countingStore{MemoryStore: NewMemoryStore()}
	memberStore := &countingStore{MemoryStore: NewMemoryStore()}
	owner := Shared{Store: ownerStore}
	member := Shared{Store: memberStore}

	// Strict no-op fast path: empty incoming + empty removed -> 0 writes.
	changed, err := owner.MergeShared(ctx, nil, true, nil)
	if err != nil || len(changed) != 0 || ownerStore.writes.Load() != 0 {
		t.Fatalf("empty merge should be strict no-op: changed=%v writes=%d err=%v", changed, ownerStore.writes.Load(), err)
	}

	// Owner has entry 0 + profile p1.
	_, _ = ownerStore.AppendEntry(ctx, Entry{Kind: KindUser, Text: "owner msg 0", Peer: "owner", TS: now})
	pOwner, p1, _ := ProfileAdd(nil, "owner fact", now)
	_ = ownerStore.SaveProfile(ctx, pOwner)

	// Member independently wrote a conflicting local entry 0 + profile p2.
	_, _ = memberStore.AppendEntry(ctx, Entry{Kind: KindUser, Text: "member msg 0", Peer: "member", TS: now.Add(time.Second)})
	pMember, _, _ := ProfileAdd(nil, "member fact", now.Add(time.Second))
	_ = memberStore.SaveProfile(ctx, pMember)

	// 1. Owner folds member hand-in (keepOwn=true): member's conflicting ID 0 is reassigned to ID 1.
	memberSnap, err := member.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownerChanged, err := owner.MergeShared(ctx, []map[string]json.RawMessage{memberSnap}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerChanged) != 2 { // e/1 + p/<member>
		t.Fatalf("ownerChanged = %v, want 2 changes", ownerChanged)
	}
	cnt, _ := ownerStore.EntryCount(ctx)
	if cnt != 2 {
		t.Fatalf("owner EntryCount = %d, want 2", cnt)
	}

	// 2. Member adopts owner's canonical state (keepOwn=false).
	ownerSnap, err := owner.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	memberChanged, err := member.MergeShared(ctx, []map[string]json.RawMessage{ownerSnap}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberChanged) == 0 {
		t.Fatal("expected member to adopt owner state")
	}

	// 3. Repeated merge when converged -> 0 writes, 0 changes!
	ownerStore.writes.Store(0)
	memberStore.writes.Store(0)
	ownerSnap2, _ := owner.Shareable(ctx)
	memberSnap2, _ := member.Shareable(ctx)

	c1, err := owner.MergeShared(ctx, []map[string]json.RawMessage{memberSnap2}, true, nil)
	if err != nil || len(c1) != 0 || ownerStore.writes.Load() != 0 {
		t.Fatalf("converged owner merge had mutations: changed=%v writes=%d err=%v", c1, ownerStore.writes.Load(), err)
	}
	c2, err := member.MergeShared(ctx, []map[string]json.RawMessage{ownerSnap2}, false, nil)
	if err != nil || len(c2) != 0 || memberStore.writes.Load() != 0 {
		t.Fatalf("converged member merge had mutations: changed=%v writes=%d err=%v", c2, memberStore.writes.Load(), err)
	}

	// 4. Profile deletion tombstone propagation.
	curProf, _ := ownerStore.GetProfile(ctx)
	nextProf, _ := ProfileRemove(curProf, p1.ID)
	_ = ownerStore.SaveProfile(ctx, nextProf)

	rems, err := owner.Removed(ctx)
	if err != nil || len(rems) != 1 {
		t.Fatalf("expected 1 tombstone on owner, got %v err=%v", rems, err)
	}
	remKeys := make([]string, 0, len(rems))
	for k := range rems {
		remKeys = append(remKeys, k)
	}
	c3, err := member.MergeShared(ctx, nil, false, remKeys)
	if err != nil || len(c3) != 1 {
		t.Fatalf("expected member to remove tombstoned profile, changed=%v err=%v", c3, err)
	}
	memberProfAfter, _ := memberStore.GetProfile(ctx)
	if len(memberProfAfter) != 1 {
		t.Fatalf("expected 1 profile entry remaining on member, got %d", len(memberProfAfter))
	}
}
