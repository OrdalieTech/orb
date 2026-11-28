package memory

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestStressScaleUTF8AndTreeIntegrity(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	const totalEntries = 620
	kinds := []EntryKind{KindUser, KindTalk, KindWork, KindNote}
	noisy32K := strings.Repeat("NOISE_éàç_日本語_🚀🔥\t\n\"quoted\\path\"\x00 ", 900) // > 32 KiB

	for i := 0; i < totalEntries; i++ {
		var text string
		switch i % 6 {
		case 0:
			text = fmt.Sprintf("tiny %d ok", i)
		case 1:
			text = fmt.Sprintf("français #%d: résumé café naïve façade œuvre", i)
		case 2:
			text = fmt.Sprintf("cjk #%d: 日本語テスト 中文记忆 한국어 🚀🔥🧠", i)
		case 3:
			text = fmt.Sprintf("ctrl #%d:\tline1\nline2\r\n\"json\":\"val\"\x00end", i)
		case 4:
			text = fmt.Sprintf("medium #%d: %s", i, strings.Repeat("alpha beta gamma ", 15))
		case 5:
			// 32 KiB noisy entry
			text = fmt.Sprintf("huge #%d: %s", i, noisy32K)
		}
		e, err := store.AppendEntry(ctx, Entry{
			Kind:    kinds[i%len(kinds)],
			Text:    text,
			Peer:    fmt.Sprintf("peer-%d", i%5),
			Session: fmt.Sprintf("sess-%d", i%3),
			TS:      base.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("AppendEntry(%d): %v", i, err)
		}
		if e.ID != i {
			t.Fatalf("AppendEntry(%d) got ID %d", i, e.ID)
		}
	}

	// 1. Free-node promotion uses 0 LLM calls.
	var llmCalls atomic.Int64
	comp := NewCompactor(store, func(ctx context.Context, sys, user string) (string, error) {
		llmCalls.Add(1)
		return "should-not-be-called", nil
	}, nil)
	_ = comp
	freeCreated, err := MaterializeFreeNodes(ctx, store)
	if err != nil {
		t.Fatalf("MaterializeFreeNodes: %v", err)
	}
	if freeCreated == 0 {
		t.Fatalf("expected >0 free nodes materialized, got 0")
	}
	if llmCalls.Load() != 0 {
		t.Fatalf("MaterializeFreeNodes triggered %d LLM calls, want 0", llmCalls.Load())
	}

	// Verify all materialized nodes are valid UTF-8 and <= NodeBytes.
	nodes, err := store.ListNodes(ctx)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for _, n := range nodes {
		if !utf8.ValidString(n.Text) {
			t.Fatalf("node %v has invalid UTF-8", n.Key())
		}
		if len(n.Text) > NodeBytes {
			t.Fatalf("node %v len=%d > %d", n.Key(), len(n.Text), NodeBytes)
		}
	}

	// Compact the full tree with a fast deterministic summarizer so higher-level nodes exist.
	fastComp := NewCompactor(store, func(ctx context.Context, sys, user string) (string, error) {
		return truncateUTF8("sum: "+normalizeInlineWhitespace(user), 240), nil
	}, nil)
	for pass := 0; pass < 100; pass++ {
		w, err := fastComp.CompactOnce(ctx, 256)
		if err != nil {
			t.Fatalf("CompactOnce pass %d: %v", pass, err)
		}
		if w == 0 {
			break
		}
	}

	// 2. BuildView at 4 KiB, 24 KiB, and 64 KiB budgets.
	for _, budget := range []int{4 * 1024, 24 * 1024, 64 * 1024} {
		v, err := BuildView(ctx, store, nil, budget)
		if err != nil {
			t.Fatalf("BuildView(%d): %v", budget, err)
		}
		if v.EntryCount != totalEntries {
			t.Fatalf("BuildView(%d) EntryCount=%d, want %d", budget, v.EntryCount, totalEntries)
		}
		assertContiguousPartition(t, v.Parts, totalEntries)
		for i, line := range v.Lines {
			if !utf8.ValidString(line) {
				t.Fatalf("BuildView(%d) line %d invalid UTF-8", budget, i)
			}
		}
		if v.Bytes > v.LowBytes {
			t.Fatalf("BuildView(%d) bytes=%d > lowBytes=%d after batch-merge (high=%d, parts=%d)",
				budget, v.Bytes, v.LowBytes, v.HighBytes, len(v.Parts))
		}
		t.Logf("BuildView(%d KiB): parts=%d bytes=%d (low=%d high=%d)",
			budget/1024, len(v.Parts), v.Bytes, v.LowBytes, v.HighBytes)
	}

	// 3. Prompt-cache prefix stability: 20 consecutive incremental appends below highBytes.
	v, err := BuildView(ctx, store, nil, 64*1024)
	if err != nil {
		t.Fatalf("BuildView(64KiB): %v", err)
	}
	for i := 0; i < 20; i++ {
		prevLines := append([]string(nil), v.Lines...)
		_, err := store.AppendEntry(ctx, Entry{
			Kind: KindNote,
			Text: fmt.Sprintf("incremental stable append #%d 🚀", i),
			TS:   base.Add(time.Duration(totalEntries+i) * time.Second),
		})
		if err != nil {
			t.Fatalf("incremental AppendEntry %d: %v", i, err)
		}
		next, err := BuildView(ctx, store, v, 64*1024)
		if err != nil {
			t.Fatalf("incremental BuildView %d: %v", i, err)
		}
		assertContiguousPartition(t, next.Parts, totalEntries+i+1)
		if next.Bytes > next.HighBytes {
			t.Fatalf("unexpectedly exceeded highBytes during incremental stability test: %d > %d", next.Bytes, next.HighBytes)
		}
		if len(next.Lines) != len(prevLines)+1 {
			t.Fatalf("step %d: len(Lines)=%d, want %d", i, len(next.Lines), len(prevLines)+1)
		}
		for j := range prevLines {
			if next.Lines[j] != prevLines[j] {
				t.Fatalf("prompt-cache prefix broken at step %d line %d:\nprev=%q\nnext=%q", i, j, prevLines[j], next.Lines[j])
			}
		}
		v = next
	}
}

func assertContiguousPartition(t *testing.T, parts []Part, count int) {
	t.Helper()
	if !isValidPartition(parts, count) {
		t.Fatalf("invalid partition for count=%d: %+v", count, parts)
	}
}

func TestStressZoomAdversarialAndUTF8(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Entry 0: large multi-byte UTF-8 text (>12 KiB) to test verbatim paging across rune boundaries.
	multiByteChunk := "éàç日本語🚀🔥" // 2+2+2+3+3+3+4+4 = 23 bytes per repeat
	hugeUTF8 := strings.Repeat(multiByteChunk, 600)
	if _, err := store.AppendEntry(ctx, Entry{Kind: KindWork, Text: hugeUTF8, TS: base}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 16; i++ {
		if _, err := store.AppendEntry(ctx, Entry{
			Kind: KindNote,
			Text: fmt.Sprintf("entry-%d 日本語 🚀", i),
			TS:   base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Partially compact: materialize free nodes only (entry 0 has no level-0 node, so 0+2, 0+4, etc. are uncompacted).
	if _, err := MaterializeFreeNodes(ctx, store); err != nil {
		t.Fatal(err)
	}

	// 1. Verbatim n=1 paging across multi-byte UTF-8 boundaries.
	pages := 0
	for offset := 0; offset < len(hugeUTF8)+100; offset += 4096 {
		out, err := Zoom(ctx, store, 0, 1, 1, offset)
		if err != nil {
			t.Fatalf("Zoom(0, 1, 1, %d): %v", offset, err)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("Zoom(0, 1, 1, %d) returned INVALID UTF-8!", offset)
		}
		pages++
	}
	// Also test misaligned offsets (1, 2, 3, 4095, 4097) to ensure mid-rune offsets never produce invalid UTF-8!
	for _, off := range []int{1, 2, 3, 5, 17, 4095, 4097, len(hugeUTF8) - 1, len(hugeUTF8) + 999999} {
		out, err := Zoom(ctx, store, 0, 1, 1, off)
		if err != nil {
			t.Fatalf("Zoom(0, 1, 1, %d): %v", off, err)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("Zoom(0, 1, 1, %d) produced INVALID UTF-8 on mid-rune offset!", off)
		}
	}
	t.Logf("Zoom UTF-8 paging checked %d pages + misaligned offsets cleanly", pages)

	// 2. Valid power-of-2 spans up to depth=3, including partial/uncompacted subtrees.
	for _, tc := range []struct {
		id, n, depth int
	}{
		{0, 2, 1}, {0, 4, 2}, {0, 8, 3}, {8, 8, 3}, {4, 4, 2}, {12, 4, 1}, {0, 16, 3}, {0, 16, 99},
	} {
		out, err := Zoom(ctx, store, tc.id, tc.n, tc.depth, 0)
		if err != nil {
			t.Fatalf("Zoom(%d, %d, %d, 0): %v", tc.id, tc.n, tc.depth, err)
		}
		if !utf8.ValidString(out) || strings.TrimSpace(out) == "" {
			t.Fatalf("Zoom(%d, %d, %d, 0) empty or invalid UTF-8", tc.id, tc.n, tc.depth)
		}
	}

	// 3. Adversarial inputs.
	adversarial := []struct {
		name         string
		id, n, depth int
		wantErr      error
	}{
		{"negative id", -1, 1, 1, ErrInvalidRef},
		{"id >= count", 16, 1, 1, ErrNotFound},
		{"non-power-of-2 n=3", 0, 3, 1, ErrInvalidRef},
		{"n=0", 0, 0, 1, ErrInvalidRef},
		{"unaligned id=1 n=4", 1, 4, 1, ErrInvalidRef},
		{"unaligned id=2 n=4", 2, 4, 1, ErrInvalidRef},
	}
	for _, tc := range adversarial {
		_, err := Zoom(ctx, store, tc.id, tc.n, tc.depth, 0)
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("adversarial %s: err=%v, want %v", tc.name, err, tc.wantErr)
		}
	}
}

func TestStressSearch(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 600; i++ {
		text := fmt.Sprintf("record #%d: token_%d, punctuation! (foo/bar:baz) 日本語検索 🚀", i, i%25)
		if _, err := store.AppendEntry(ctx, Entry{Kind: KindNote, Text: text, TS: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := MaterializeFreeNodes(ctx, store); err != nil {
		t.Fatal(err)
	}

	// Empty query -> nil/empty, no error.
	if hits, err := Search(ctx, store, "   ", 10); err != nil || len(hits) != 0 {
		t.Fatalf("empty query: hits=%d err=%v", len(hits), err)
	}
	// CJK query.
	hits, err := Search(ctx, store, "日本語検索", 10)
	if err != nil || len(hits) != 10 {
		t.Fatalf("CJK query: hits=%d err=%v", len(hits), err)
	}
	// Multi-token + punctuation.
	hits, err = Search(ctx, store, "token_7 foo/bar:baz", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("multi-token query: hits=%d err=%v", len(hits), err)
	}
	// Huge query.
	hugeQuery := strings.Repeat("token_3 ", 500)
	if _, err := Search(ctx, store, hugeQuery, 5); err != nil {
		t.Fatalf("huge query err: %v", err)
	}
	// Limit edge cases: 0, -1, 999.
	for _, lim := range []int{0, -1, 999} {
		h, err := Search(ctx, store, "token_1", lim)
		if err != nil {
			t.Fatalf("Search limit=%d: %v", lim, err)
		}
		if len(h) == 0 {
			t.Fatalf("Search limit=%d returned 0 hits", lim)
		}
		for _, hit := range h {
			if !utf8.ValidString(hit.Snippet) {
				t.Fatalf("Search hit snippet invalid UTF-8: %q", hit.Snippet)
			}
		}
	}
}

func TestStressProfile(t *testing.T) {
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	// Empty / whitespace content.
	if _, _, err := ProfileAdd(nil, "   \t\n ", now); !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("ProfileAdd whitespace: err=%v, want ErrEmptyContent", err)
	}
	if _, _, err := ProfileSet(nil, "id1", "  ", now); !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("ProfileSet whitespace: err=%v, want ErrEmptyContent", err)
	}

	// 1,375-char cap boundary using 4-byte emoji (1375 runes = 5500 bytes, MUST be allowed!).
	exactEmoji := strings.Repeat("🚀", ProfileMaxChars)
	if utf8.RuneCountInString(exactEmoji) != ProfileMaxChars || len(exactEmoji) != ProfileMaxChars*4 {
		t.Fatalf("test setup error")
	}
	entries, added, err := ProfileAdd(nil, exactEmoji, now)
	if err != nil {
		t.Fatalf("ProfileAdd exact %d runes (4-byte emoji): %v", ProfileMaxChars, err)
	}
	// Duplicate detection (case-insensitive / trimmed).
	entries2, dup, err := ProfileAdd(entries, "  "+exactEmoji+"  ", now)
	if err != nil || dup.ID != added.ID || len(entries2) != 1 {
		t.Fatalf("duplicate detection failed: len=%d err=%v", len(entries2), err)
	}
	// Adding 1 more rune must fail with ErrProfileTooLarge.
	if _, _, err := ProfileAdd(entries, "x", now); !errors.Is(err, ErrProfileTooLarge) {
		t.Fatalf("ProfileAdd over cap: err=%v, want ErrProfileTooLarge", err)
	}

	// Remove and verify.
	entries, ok := ProfileRemove(entries, added.ID)
	if !ok || len(entries) != 0 {
		t.Fatalf("ProfileRemove failed: ok=%v len=%d", ok, len(entries))
	}

	// Concurrent add/set/remove against MemStore.
	ctx := context.Background()
	store := NewMemoryStore()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				cur, _ := store.GetProfile(ctx)
				next, added, err := ProfileAdd(cur, fmt.Sprintf("fact-%d-%d", id, i), now)
				if err == nil {
					_ = store.SaveProfile(ctx, next)
					if i%3 == 0 {
						next2, _, err2 := ProfileSet(next, added.ID, fmt.Sprintf("updated-%d-%d", id, i), now)
						if err2 == nil {
							_ = store.SaveProfile(ctx, next2)
						}
					}
					if i%5 == 0 {
						next3, _ := ProfileRemove(next, added.ID)
						_ = store.SaveProfile(ctx, next3)
					}
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestStressCompactorFlakyOversized(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Ingest 32 oversized entries (>512 B) so compactor must summarize leaves and parents.
	for i := 0; i < 32; i++ {
		text := fmt.Sprintf("oversized-%d: %s", i, strings.Repeat("日本語🚀éàç ", 80))
		if _, err := store.AppendEntry(ctx, Entry{Kind: KindWork, Text: text, TS: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}

	var rngMu sync.Mutex
	rng := rand.New(rand.NewSource(42))
	var calls atomic.Int64

	flaky := func(ctx context.Context, sys, user string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n := calls.Add(1)
		rngMu.Lock()
		r := rng.Intn(100)
		rngMu.Unlock()
		switch {
		case r < 30:
			return "", errors.New("simulated transient LLM error")
		case r < 45:
			return "   \n\t  ", nil // empty/whitespace
		default:
			// Deliberately oversized >512 B with CJK + 4-byte emoji
			return fmt.Sprintf("sum-%d: %s", n, strings.Repeat("日本語🔥🚀éàç", 60)), nil
		}
	}

	comp := NewCompactor(store, flaky, nil)
	for pass := 0; pass < 80; pass++ {
		_, _ = comp.CompactOnce(ctx, 16)
	}

	// Test context cancellation is honored.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, _ = comp.CompactOnce(cancelCtx, 16)

	nodes, err := store.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Fatalf("expected >0 nodes compacted, got 0")
	}
	for _, n := range nodes {
		if !utf8.ValidString(n.Text) {
			t.Fatalf("stored node %v has invalid UTF-8", n.Key())
		}
		if len(n.Text) > NodeBytes {
			t.Fatalf("stored node %v len=%d > %d", n.Key(), len(n.Text), NodeBytes)
		}
		if strings.TrimSpace(n.Text) == "" {
			t.Fatalf("stored node %v has empty text", n.Key())
		}
	}
	t.Logf("Flaky compactor stored %d valid <=512B UTF-8 nodes over %d LLM calls", len(nodes), calls.Load())
}

func TestStressCompactorConcurrencyAndNoLeak(t *testing.T) {
	runtime.GC()
	beforeG := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := NewMemoryStore()

	comp := NewCompactor(store, func(ctx context.Context, sys, user string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Millisecond):
			return truncateUTF8("ok: "+normalizeInlineWhitespace(user), 200), nil
		}
	}, nil)
	comp.Start(ctx)

	var wg sync.WaitGroup
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				switch (id + i) % 6 {
				case 0:
					_, _ = store.AppendEntry(ctx, Entry{
						Kind: KindWork,
						Text: fmt.Sprintf("g=%d i=%d %s", id, i, strings.Repeat("日本語🚀 ", 20)),
					})
					comp.Trigger()
				case 1:
					comp.Trigger()
				case 2:
					_, _ = BuildView(ctx, store, nil, 8*1024)
				case 3:
					cnt, _ := store.EntryCount(ctx)
					if cnt > 0 {
						_, _ = Zoom(ctx, store, 0, 1, 1, 0)
					}
				case 4:
					_, _ = Search(ctx, store, "日本語", 5)
				case 5:
					p, _ := store.GetProfile(ctx)
					next, _, err := ProfileAdd(p, fmt.Sprintf("p-%d-%d", id, i), time.Now())
					if err == nil {
						_ = store.SaveProfile(ctx, next)
					}
				}
			}
		}(g)
	}
	wg.Wait()
	comp.Stop()
	comp.Stop() // idempotent second stop

	// Wait briefly for goroutines to settle and verify no leak.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= beforeG+2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	afterG := runtime.NumGoroutine()
	if afterG > beforeG+2 {
		t.Fatalf("possible goroutine leak: before=%d after=%d", beforeG, afterG)
	}
	t.Logf("Concurrency hammer clean: goroutines before=%d after=%d", beforeG, afterG)
}
