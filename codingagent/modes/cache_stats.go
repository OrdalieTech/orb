package modes

import (
	"github.com/OrdalieTech/pi-go/ai"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

// Prompt-cache accounting, mirroring upstream core/cache-stats.ts. The price
// source is the available-model list (cost is $/million tokens) instead of
// upstream's ModelRuntime lookup.

// cacheNoiseFloorTokens: per-turn misses at or below this are cache breakpoint
// granularity noise.
const cacheNoiseFloorTokens = 1024

// cacheRequest is the last request seen by a scan; everything in its prompt
// should be cached.
type cacheRequest struct {
	prompt    int64
	model     string
	timestamp int64
	// reported is sticky: some earlier request in this scan segment reported
	// cache activity. Distinguishes a total miss on a cache-read-only provider
	// from a provider that never reports caching at all.
	reported bool
}

// cacheMiss is a counted cache miss on a single assistant message.
type cacheMiss struct {
	tokens       int64
	cost         float64
	idle         int64
	modelChanged bool
}

// cacheWasteTotals accumulates re-billed prompt tokens across a session.
type cacheWasteTotals struct {
	missedTokens int64
	missedCost   float64
	missCount    int
}

// computeCacheMiss mirrors upstream detectMiss: the miss for one assistant
// message relative to the previous request, or nil when nothing is counted.
func computeCacheMiss(previous *cacheRequest, message *ai.AssistantMessage, models []ai.Model) *cacheMiss {
	prompt := message.Usage.Input + message.Usage.CacheRead + message.Usage.CacheWrite
	// A zero-cache turn only counts when cache activity was reported before:
	// on cache-read-only providers that is a total miss, while on providers
	// that never report caching it means nothing.
	if previous == nil || prompt <= 0 || message.Usage.CacheRead+message.Usage.CacheWrite == 0 && !previous.reported {
		return nil
	}
	missed := min(previous.prompt, prompt) - message.Usage.CacheRead
	if missed <= cacheNoiseFloorTokens {
		return nil
	}
	// Extra cost = missed tokens billed at the actual paid rate (input/cacheWrite,
	// incl. write premium) instead of the cache-read rate.
	paidTokens := message.Usage.Input + message.Usage.CacheWrite
	paidRate := 0.0
	if paidTokens > 0 {
		paidRate = (message.Usage.Cost.Input + message.Usage.Cost.CacheWrite) / float64(paidTokens)
	}
	readRate := 0.0
	if message.Usage.CacheRead > 0 {
		readRate = message.Usage.Cost.CacheRead / float64(message.Usage.CacheRead)
	} else {
		for _, model := range models {
			if model.Provider == message.Provider && model.ID == message.Model {
				readRate = model.Cost.CacheRead / 1_000_000
				break
			}
		}
	}
	return &cacheMiss{
		tokens:       missed,
		cost:         float64(missed) * max(0, paidRate-readRate),
		idle:         max(0, message.Timestamp-previous.timestamp),
		modelChanged: previous.model != string(message.Provider)+"/"+message.Model,
	}
}

// scanCacheEntries walks persisted entries in order, invoking visit with the
// previous-request state before folding each assistant message into it. The
// walk mirrors upstream cache-stats scan(): compaction and branch summaries
// reset the segment (the context legitimately changed), model switches do not.
// visit returning false stops the walk before the current message is folded.
func scanCacheEntries(entries []sessionstore.SessionEntry, visit func(previous *cacheRequest, entryID string, message *ai.AssistantMessage) bool) *cacheRequest {
	var previous *cacheRequest
	reported := false
	for _, entry := range entries {
		if entry.Type == "compaction" || entry.Type == "branch_summary" {
			previous = nil
			reported = false
			continue
		}
		if entry.Type != "message" {
			continue
		}
		decoded, err := ai.UnmarshalMessage(entry.Message)
		if err != nil {
			continue
		}
		assistant := asAssistantMessage(decoded)
		if assistant == nil {
			continue
		}
		if visit != nil && !visit(previous, entry.ID, assistant) {
			return previous
		}
		prompt := assistant.Usage.Input + assistant.Usage.CacheRead + assistant.Usage.CacheWrite
		if prompt > 0 {
			reported = reported || assistant.Usage.CacheRead+assistant.Usage.CacheWrite > 0
			previous = &cacheRequest{
				prompt:    prompt,
				model:     string(assistant.Provider) + "/" + assistant.Model,
				timestamp: assistant.Timestamp,
				reported:  reported,
			}
		}
	}
	return previous
}

// computeCacheWaste mirrors upstream computeCacheWaste: cumulative prompt
// tokens that should have been cache reads but were re-billed.
func computeCacheWaste(entries []sessionstore.SessionEntry, models []ai.Model) cacheWasteTotals {
	totals := cacheWasteTotals{}
	scanCacheEntries(entries, func(previous *cacheRequest, _ string, message *ai.AssistantMessage) bool {
		if miss := computeCacheMiss(previous, message, models); miss != nil {
			totals.missedTokens += miss.tokens
			totals.missedCost += miss.cost
			totals.missCount++
		}
		return true
	})
	return totals
}

// collectCacheMisses mirrors upstream collectCacheMisses; misses are keyed by
// session entry ID because decoded messages have no stable identity in Go.
func collectCacheMisses(entries []sessionstore.SessionEntry, models []ai.Model) map[string]*cacheMiss {
	misses := make(map[string]*cacheMiss)
	scanCacheEntries(entries, func(previous *cacheRequest, entryID string, message *ai.AssistantMessage) bool {
		if miss := computeCacheMiss(previous, message, models); miss != nil {
			misses[entryID] = miss
		}
		return true
	})
	return misses
}

// detectCacheMiss mirrors upstream detectCacheMiss for a just-completed or
// re-rendered assistant message. When target is already persisted the scan
// stops at its entry, so rebuilds resolve the same previous request the live
// path saw.
func (mode *InteractiveMode) detectCacheMiss(target *ai.AssistantMessage) *cacheMiss {
	var result *cacheMiss
	matched := false
	previous := scanCacheEntries(mode.session.Manager().GetEntries(), func(previous *cacheRequest, _ string, assistant *ai.AssistantMessage) bool {
		if assistant.Timestamp == target.Timestamp && assistant.Provider == target.Provider && assistant.Model == target.Model {
			matched = true
			result = computeCacheMiss(previous, target, mode.session.AvailableModels())
			return false
		}
		return true
	})
	if matched {
		return result
	}
	return computeCacheMiss(previous, target, mode.session.AvailableModels())
}
