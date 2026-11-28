package memory

import (
	"context"
	"fmt"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	zoomPageBytes = 4096
	zoomSuffix    = " … [zoom]"
)

// Part is a node reference in a rendered View partition of [0, EntryCount).
type Part struct {
	Level int `json:"level"`
	Index int `json:"index"`
}

// Key returns the NodeKey corresponding to p.
func (p Part) Key() NodeKey { return NodeKey(p) }

// Start returns the first entry ID covered by p.
func (p Part) Start() int { return p.Index << p.Level }

// Span returns the number of entries covered by p (2^Level).
func (p Part) Span() int { return 1 << p.Level }

// End returns the exclusive upper bound entry ID covered by p.
func (p Part) End() int { return p.Start() + p.Span() }

// Name returns the canonical "id+n" reference string for p.
func (p Part) Name() string {
	return strconv.Itoa(p.Start()) + "+" + strconv.Itoa(p.Span())
}

// FormatEntryLine renders an Entry into a single compact line with normalized whitespace.
func FormatEntryLine(e Entry) string {
	kind := e.Kind
	if kind == "" {
		kind = KindNote
	}
	text := normalizeInlineWhitespace(e.Text)
	if text == "" {
		return string(kind) + ":"
	}
	return string(kind) + ": " + text
}

func normalizeInlineWhitespace(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func previewLine(line string) string {
	if len(line) <= NodeBytes {
		return line
	}
	limit := NodeBytes - len(zoomSuffix)
	if limit < 0 {
		return truncateUTF8(line, NodeBytes)
	}
	return strings.TrimRight(truncateUTF8(line, limit), " ") + zoomSuffix
}

// MaterializeFreeNodes creates level-0 and higher-level summary nodes whose
// verbatim text already fits within NodeBytes (512 B), requiring 0 LLM calls.
func MaterializeFreeNodes(ctx context.Context, store Store) (int, error) {
	entries, err := store.ListEntries(ctx, 0, -1)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	existing, err := store.ListNodes(ctx)
	if err != nil {
		return 0, err
	}
	nodes := make(map[NodeKey]Node, len(existing))
	for _, n := range existing {
		nodes[n.Key()] = n
	}

	created := 0
	maxID := -1
	for _, e := range entries {
		if e.ID > maxID {
			maxID = e.ID
		}
		k := NodeKey{Level: 0, Index: e.ID}
		if _, ok := nodes[k]; ok {
			continue
		}
		line := FormatEntryLine(e)
		if len(line) <= NodeBytes {
			n := Node{Level: 0, Index: e.ID, Text: line, TS: e.TS}
			if err := store.PutNode(ctx, n); err != nil {
				return created, err
			}
			nodes[k] = n
			created++
		}
	}

	total := maxID + 1
	for level := 1; (1 << level) <= total; level++ {
		span := 1 << level
		for idx := 0; (idx+1)*span <= total; idx++ {
			k := NodeKey{Level: level, Index: idx}
			if _, ok := nodes[k]; ok {
				continue
			}
			childA, okA := nodes[NodeKey{Level: level - 1, Index: 2 * idx}]
			childB, okB := nodes[NodeKey{Level: level - 1, Index: 2*idx + 1}]
			if !okA || !okB {
				continue
			}
			combined := childA.Text + "\n" + childB.Text
			if len(combined) <= NodeBytes {
				ts := childB.TS
				if ts.IsZero() {
					ts = childA.TS
				}
				n := Node{Level: level, Index: idx, Text: combined, TS: ts}
				if err := store.PutNode(ctx, n); err != nil {
					return created, err
				}
				nodes[k] = n
				created++
			}
		}
	}
	return created, nil
}

// View is a prompt-cache-stable rendered partition of the memory timeline.
type View struct {
	Parts      []Part
	EntryCount int
	HighBytes  int
	LowBytes   int
	Lines      []string
	Bytes      int
}

// Render returns the full newline-delimited view text.
func (v *View) Render() string {
	if v == nil || len(v.Lines) == 0 {
		return ""
	}
	return strings.Join(v.Lines, "\n")
}

func isValidPartition(parts []Part, count int) bool {
	if count == 0 {
		return len(parts) == 0
	}
	pos := 0
	for _, p := range parts {
		if p.Level < 0 || p.Index < 0 {
			return false
		}
		if p.Start() != pos {
			return false
		}
		pos = p.End()
	}
	return pos == count
}

// BuildView builds or incrementally extends a prompt-cache-stable memory View.
// When prev is valid and the extended view stays within highBytes, prev.Lines
// remains 100% byte-identical. Only when total bytes exceed highBytes does
// BuildView batch-merge sibling pairs down toward lowBytes (2/3 of highBytes)
// using OptChat's age-due priority.
func BuildView(ctx context.Context, store Store, prev *View, highBytes int) (*View, error) {
	high, low := NormalizeConfig(Config{ViewBytes: highBytes})
	total, err := store.EntryCount(ctx)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return &View{
			HighBytes: high,
			LowBytes:  low,
		}, nil
	}

	existingNodes, err := store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	nodes := make(map[NodeKey]Node, len(existingNodes))
	for _, n := range existingNodes {
		nodes[n.Key()] = n
	}

	entriesList, err := store.ListEntries(ctx, 0, -1)
	if err != nil {
		return nil, err
	}
	entries := make(map[int]Entry, len(entriesList))
	for _, e := range entriesList {
		entries[e.ID] = e
	}

	var parts []Part
	var lines []string
	preservePrefix := prev != nil && prev.EntryCount <= total && isValidPartition(prev.Parts, prev.EntryCount) && len(prev.Lines) == len(prev.Parts)

	renderPart := func(p Part) string {
		if n, ok := nodes[p.Key()]; ok {
			return p.Name() + ": " + n.Text
		}
		if p.Level == 0 {
			if e, ok := entries[p.Index]; ok {
				return p.Name() + ": " + previewLine(FormatEntryLine(e))
			}
		}
		return p.Name() + ": [uncompacted]"
	}

	partText := func(p Part) (string, time.Time, bool) {
		if n, ok := nodes[p.Key()]; ok {
			return n.Text, n.TS, true
		}
		if p.Level == 0 {
			if e, ok := entries[p.Index]; ok {
				line := FormatEntryLine(e)
				if len(line) <= NodeBytes {
					return line, e.TS, true
				}
			}
		}
		return "", time.Time{}, false
	}

	if preservePrefix {
		parts = make([]Part, len(prev.Parts), len(prev.Parts)+(total-prev.EntryCount))
		copy(parts, prev.Parts)
		lines = make([]string, len(prev.Lines), len(prev.Lines)+(total-prev.EntryCount))
		copy(lines, prev.Lines)
		for id := prev.EntryCount; id < total; id++ {
			p := Part{Level: 0, Index: id}
			parts = append(parts, p)
			lines = append(lines, renderPart(p))
		}
	} else {
		parts = make([]Part, total)
		lines = make([]string, total)
		for id := 0; id < total; id++ {
			p := Part{Level: 0, Index: id}
			parts[id] = p
			lines[id] = renderPart(p)
		}
	}

	totalBytes := renderedBytes(lines)

	if totalBytes > high {
		for totalBytes > low {
			bestK := -1
			bestDue := -1.0
			bestStart := 0
			var bestParent Node
			bestNeedsStore := false

			for k := 0; k+1 < len(parts); k++ {
				a, b := parts[k], parts[k+1]
				if a.Level != b.Level || a.Index%2 != 0 || b.Index != a.Index+1 {
					continue
				}
				parentKey := NodeKey{Level: a.Level + 1, Index: a.Index / 2}
				parentNode, ok := nodes[parentKey]
				needsStore := false
				if !ok {
					textA, tsA, okA := partText(a)
					textB, tsB, okB := partText(b)
					if !okA || !okB {
						continue
					}
					combined := textA + "\n" + textB
					if len(combined) > NodeBytes {
						continue
					}
					ts := tsB
					if ts.IsZero() {
						ts = tsA
					}
					parentNode = Node{
						Level: parentKey.Level,
						Index: parentKey.Index,
						Text:  combined,
						TS:    ts,
					}
					needsStore = true
				}

				start := a.Start()
				due := float64(total-start) / float64(int(1)<<(a.Level+2))
				if bestK == -1 || due > bestDue || (due == bestDue && start < bestStart) {
					bestK = k
					bestDue = due
					bestStart = start
					bestParent = parentNode
					bestNeedsStore = needsStore
				}
			}

			if bestK == -1 {
				break
			}
			if bestNeedsStore {
				nodes[bestParent.Key()] = bestParent
				_ = store.PutNode(ctx, bestParent)
			}
			mergedPart := Part{Level: bestParent.Level, Index: bestParent.Index}
			mergedLine := mergedPart.Name() + ": " + bestParent.Text

			oldLen := len(lines[bestK]) + 1 + len(lines[bestK+1])
			totalBytes = totalBytes - oldLen + len(mergedLine)

			parts[bestK] = mergedPart
			parts = append(parts[:bestK+1], parts[bestK+2:]...)
			lines[bestK] = mergedLine
			lines = append(lines[:bestK+1], lines[bestK+2:]...)
		}
	}

	return &View{
		Parts:      parts,
		EntryCount: total,
		HighBytes:  high,
		LowBytes:   low,
		Lines:      lines,
		Bytes:      totalBytes,
	}, nil
}

func renderedBytes(lines []string) int {
	if len(lines) == 0 {
		return 0
	}
	n := len(lines) - 1 // newlines
	for _, l := range lines {
		n += len(l)
	}
	return n
}

// Zoom inspects a single verbatim entry (n == 1, paged at 4096 bytes) or
// expands a power-of-2 summary node (n > 1, aligned id%n == 0) into its
// constituent halves up to depth (default 1, max 3).
func Zoom(ctx context.Context, store Store, id, n, depth, offset int) (string, error) {
	if id < 0 || n < 1 {
		return "", ErrInvalidRef
	}
	if n == 1 {
		e, err := store.GetEntry(ctx, id)
		if err != nil {
			return "", err
		}
		header := formatEntryHeader(e)
		full := header + "\n" + e.Text
		if offset < 0 {
			offset = 0
		}
		for offset < len(full) && !utf8.RuneStart(full[offset]) {
			offset++
		}
		if offset >= len(full) {
			return header + "\n[end of entry]", nil
		}
		rest := full[offset:]
		if len(rest) <= zoomPageBytes {
			return rest, nil
		}
		page := truncateUTF8(rest, zoomPageBytes)
		nextOffset := offset + len(page)
		remaining := len(full) - nextOffset
		return fmt.Sprintf("%s\n... [more: offset=%d, remaining=%d bytes]", page, nextOffset, remaining), nil
	}

	if n&(n-1) != 0 || id%n != 0 {
		return "", ErrInvalidRef
	}
	if depth <= 0 {
		depth = 1
	} else if depth > 3 {
		depth = 3
	}

	var out []string
	var expand func(cid, cn, d int) error
	expand = func(cid, cn, d int) error {
		if d <= 0 || cn == 1 {
			if cn == 1 {
				e, err := store.GetEntry(ctx, cid)
				if err != nil {
					return err
				}
				out = append(out, fmt.Sprintf("%d+1: %s", cid, FormatEntryLine(e)))
				return nil
			}
			level := bits.TrailingZeros(uint(cn))
			key := NodeKey{Level: level, Index: cid / cn}
			node, ok, err := store.GetNode(ctx, key)
			if err != nil {
				return err
			}
			if ok {
				out = append(out, fmt.Sprintf("%d+%d: %s", cid, cn, node.Text))
			} else {
				out = append(out, fmt.Sprintf("%d+%d: [uncompacted]", cid, cn))
			}
			return nil
		}
		half := cn / 2
		if err := expand(cid, half, d-1); err != nil {
			return err
		}
		return expand(cid+half, half, d-1)
	}

	if err := expand(id, n, depth); err != nil {
		return "", err
	}
	return strings.Join(out, "\n"), nil
}

func formatEntryHeader(e Entry) string {
	ts := e.TS.UTC().Format(time.RFC3339)
	if e.TS.IsZero() {
		ts = "unknown"
	}
	kind := e.Kind
	if kind == "" {
		kind = KindNote
	}
	return fmt.Sprintf("[entry #%d · %s · %s · peer=%s session=%s]", e.ID, kind, ts, e.Peer, e.Session)
}

// Search performs fast case-insensitive lexical search across both summary
// nodes and verbatim entries, returning deduplicated ranked hits.
func Search(ctx context.Context, store Store, query string, limit int) ([]SearchHit, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	} else if limit > 50 {
		limit = 50
	}
	tokens := strings.Fields(q)

	type scoredHit struct {
		hit   SearchHit
		score int
	}

	byRef := make(map[string]scoredHit)

	scoreText := func(text string) int {
		lower := strings.ToLower(text)
		score := 0
		if strings.Contains(lower, q) {
			score += 10
		}
		matched := 0
		for _, tok := range tokens {
			if strings.Contains(lower, tok) {
				matched++
				score += 2
			}
		}
		if matched == 0 {
			return 0
		}
		if matched == len(tokens) {
			score += 4
		}
		return score
	}

	entries, err := store.ListEntries(ctx, 0, -1)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		line := FormatEntryLine(e)
		sc := scoreText(line)
		if sc == 0 {
			continue
		}
		ref := fmt.Sprintf("%d+1", e.ID)
		byRef[ref] = scoredHit{
			hit: SearchHit{
				Ref:     ref,
				ID:      e.ID,
				Span:    1,
				Kind:    e.Kind,
				Snippet: previewLine(line),
				TS:      e.TS,
				Peer:    e.Peer,
				Session: e.Session,
			},
			score: sc,
		}
	}

	nodes, err := store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if n.Level == 0 {
			continue // covered by verbatim entries
		}
		sc := scoreText(n.Text)
		if sc == 0 {
			continue
		}
		k := n.Key()
		ref := k.Name()
		if prev, exists := byRef[ref]; !exists || sc > prev.score {
			byRef[ref] = scoredHit{
				hit: SearchHit{
					Ref:     ref,
					ID:      k.Start(),
					Span:    k.Span(),
					Snippet: n.Text,
					TS:      n.TS,
				},
				score: sc,
			}
		}
	}

	results := make([]scoredHit, 0, len(byRef))
	for _, sh := range byRef {
		results = append(results, sh)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		if !results[i].hit.TS.Equal(results[j].hit.TS) {
			return results[i].hit.TS.After(results[j].hit.TS)
		}
		return results[i].hit.ID > results[j].hit.ID
	})

	if len(results) > limit {
		results = results[:limit]
	}
	hits := make([]SearchHit, len(results))
	for i, r := range results {
		hits[i] = r.hit
	}
	return hits, nil
}
