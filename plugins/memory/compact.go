package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Summarizer is the minimal LLM callback used by Compactor to produce <= 512-byte
// summaries for oversized entries or parent tree nodes.
type Summarizer func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

const compactSystemPrompt = `You compress timeline memory nodes into dense, factual summaries.
Hard limit: your output MUST be at most 512 UTF-8 bytes. Output ONLY the summary text, with no markdown fences, preamble, or commentary.

SCALE (example of ~512 bytes):
user asked to migrate auth middleware from session cookies to JWT; decided on RS256 with 15m access / 7d refresh tokens; updated internal/auth/jwt.go and middleware_test.go; fixed clock-skew flake in TestRefreshRotation; deferred Redis revocation list to follow-up PR #412.

Preserve concrete names, file paths, decisions, IDs, and outcomes. Drop filler.`

// Compactor runs non-blocking background compaction of the binary summary tree.
type Compactor struct {
	store     Store
	summarize Summarizer
	onWrite   func()

	wakeup chan struct{}

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewCompactor creates a Compactor backed by store. summarize may be nil, in
// which case only zero-LLM free nodes are materialized.
func NewCompactor(store Store, summarize Summarizer, onWrite func()) *Compactor {
	return &Compactor{
		store:     store,
		summarize: summarize,
		onWrite:   onWrite,
		wakeup:    make(chan struct{}, 1),
	}
}

// Start launches the background compaction loop. Calling Start more than once is a no-op.
func (c *Compactor) Start(ctx context.Context) {
	c.mu.Lock()
	if c.started || c.stopped {
		c.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.started = true
	c.cancel = cancel
	c.done = done
	c.mu.Unlock()

	go func() {
		defer close(done)
		for {
			select {
			case <-runCtx.Done():
				return
			case <-c.wakeup:
				_, _ = c.CompactOnce(runCtx, 16)
			}
		}
	}()
}

// Trigger requests a background compaction pass without blocking.
func (c *Compactor) Trigger() {
	select {
	case c.wakeup <- struct{}{}:
	default:
	}
}

// Stop halts the background compaction loop and waits for it to exit.
// Safe to call multiple times or even if Start was never called.
func (c *Compactor) Stop() {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true
	cancel := c.cancel
	done := c.done
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// CompactOnce materializes all free nodes (0 LLM calls) and then compacts up to
// maxNodes missing oversized leaf or parent nodes using the configured Summarizer.
func (c *Compactor) CompactOnce(ctx context.Context, maxNodes int) (int, error) {
	if maxNodes <= 0 {
		maxNodes = 16
	}
	written, err := MaterializeFreeNodes(ctx, c.store)
	if err != nil {
		return written, err
	}
	if c.summarize == nil {
		if written > 0 && c.onWrite != nil {
			c.onWrite()
		}
		return written, nil
	}

	entries, err := c.store.ListEntries(ctx, 0, -1)
	if err != nil {
		return written, err
	}
	if len(entries) == 0 {
		if written > 0 && c.onWrite != nil {
			c.onWrite()
		}
		return written, nil
	}
	existing, err := c.store.ListNodes(ctx)
	if err != nil {
		return written, err
	}
	nodes := make(map[NodeKey]Node, len(existing))
	for _, n := range existing {
		nodes[n.Key()] = n
	}

	entryByID := make(map[int]Entry, len(entries))
	maxID := -1
	for _, e := range entries {
		entryByID[e.ID] = e
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	total := maxID + 1

	llmWritten := 0

	// 1. Missing level-0 nodes for oversized entries.
	for _, e := range entries {
		if llmWritten >= maxNodes {
			break
		}
		if ctx.Err() != nil {
			break
		}
		k := NodeKey{Level: 0, Index: e.ID}
		if _, ok := nodes[k]; ok {
			continue
		}
		line := FormatEntryLine(e)
		if len(line) <= NodeBytes {
			continue
		}
		ctxLines := precedingContextLines(entries, nodes, e.ID, CompactContextLines)
		summary, err := c.summarizeWithLimit(ctx, ctxLines, line)
		if err != nil {
			return written + llmWritten, err
		}
		n := Node{Level: 0, Index: e.ID, Text: summary, TS: e.TS}
		if err := c.store.PutNode(ctx, n); err != nil {
			return written + llmWritten, err
		}
		nodes[k] = n
		llmWritten++
	}

	// 2. Missing parent nodes (level >= 1) whose two children both exist.
	for level := 1; (1<<level) <= total && llmWritten < maxNodes; level++ {
		span := 1 << level
		for idx := 0; (idx+1)*span <= total && llmWritten < maxNodes; idx++ {
			if ctx.Err() != nil {
				break
			}
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
			ts := childB.TS
			if ts.IsZero() {
				ts = childA.TS
			}
			if ts.IsZero() {
				ts = time.Now().UTC()
			}
			if len(combined) <= NodeBytes {
				n := Node{Level: level, Index: idx, Text: combined, TS: ts}
				if err := c.store.PutNode(ctx, n); err != nil {
					return written + llmWritten, err
				}
				nodes[k] = n
				written++
				continue
			}
			ctxLines := precedingContextLines(entries, nodes, k.Start(), CompactContextLines)
			summary, err := c.summarizeWithLimit(ctx, ctxLines, combined)
			if err != nil {
				return written + llmWritten, err
			}
			n := Node{Level: level, Index: idx, Text: summary, TS: ts}
			if err := c.store.PutNode(ctx, n); err != nil {
				return written + llmWritten, err
			}
			nodes[k] = n
			llmWritten++
		}
	}

	totalWritten := written + llmWritten
	if totalWritten > 0 && c.onWrite != nil {
		c.onWrite()
	}
	return totalWritten, nil
}

func precedingContextLines(entries []Entry, nodes map[NodeKey]Node, beforeID, maxLines int) []string {
	if beforeID <= 0 || maxLines <= 0 {
		return nil
	}
	start := beforeID - maxLines
	if start < 0 {
		start = 0
	}
	lines := make([]string, 0, beforeID-start)
	for _, e := range entries {
		if e.ID < start || e.ID >= beforeID {
			continue
		}
		if n, ok := nodes[NodeKey{Level: 0, Index: e.ID}]; ok {
			lines = append(lines, fmt.Sprintf("%d+1: %s", e.ID, n.Text))
		} else {
			lines = append(lines, fmt.Sprintf("%d+1: %s", e.ID, previewLine(FormatEntryLine(e))))
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines
}

func (c *Compactor) summarizeWithLimit(ctx context.Context, contextLines []string, target string) (string, error) {
	var basePrompt strings.Builder
	if len(contextLines) > 0 {
		basePrompt.WriteString("Preceding context (for reference only, do not summarize):\n")
		for _, l := range contextLines {
			basePrompt.WriteString(l)
			basePrompt.WriteByte('\n')
		}
		basePrompt.WriteByte('\n')
	}
	basePrompt.WriteString("Target to summarize (must be <= 512 UTF-8 bytes):\n")
	basePrompt.WriteString(target)

	prompt := basePrompt.String()
	var last string
	for try := 0; try < CompactMaxTries; try++ {
		res, err := c.summarize(ctx, compactSystemPrompt, prompt)
		if err != nil {
			return "", err
		}
		res = strings.TrimSpace(res)
		last = res
		if len(res) <= NodeBytes {
			return res, nil
		}
		prompt = fmt.Sprintf("%s\n\nPrevious attempt was %d UTF-8 bytes, which exceeds the %d-byte limit. Shorten it to at most %d UTF-8 bytes:\n%s",
			basePrompt.String(), len(res), NodeBytes, NodeBytes, res)
	}
	return strings.TrimSpace(truncateUTF8(last, NodeBytes)), nil
}
