package modes

import (
	"testing"

	"github.com/OrdalieTech/orb/conformance/runner"
)

// Orb-owned snapshot updating for the modes-side render goldens (D35). The
// helpers rewrite frame-shaped fixture values from Orb's renderer under
// ORB_UPDATE_F12 while keeping each entry's stored shape: members the
// extraction wrote (lines vs head/tail summaries) are recomputed, absent
// members stay absent. Behavior traces are never rewritten.

// updateF12RawFrame rewrites one f12RawFrame-shaped fixture entry from got
// and reports true when the caller should skip its comparison.
func updateF12RawFrame(t testing.TB, snap *runner.Snapshot, got []string, base ...any) bool {
	if snap == nil {
		return false
	}
	t.Helper()
	snap.Set(len(got), append(append([]any{}, base...), "lineCount")...)
	snap.Set(f12RawLinesDigest(t, got), append(append([]any{}, base...), "sha256")...)
	updateF12FrameLines(snap, got, base...)
	return true
}

// updateF12VisibleChat rewrites one f12VisibleChat-shaped fixture entry; the
// digest matches the extraction's normalized-lines encoding.
func updateF12VisibleChat(t testing.TB, snap *runner.Snapshot, got []string, base ...any) bool {
	if snap == nil {
		return false
	}
	t.Helper()
	snap.Set(len(got), append(append([]any{}, base...), "lineCount")...)
	snap.Set(f12VisibleLinesDigest(t, got), append(append([]any{}, base...), "sha256")...)
	updateF12FrameLines(snap, got, base...)
	return true
}

func updateF12FrameLines(snap *runner.Snapshot, got []string, base ...any) {
	if snap.Has(append(append([]any{}, base...), "lines")...) {
		snap.Set(got, append(append([]any{}, base...), "lines")...)
	}
	if snap.Has(append(append([]any{}, base...), "head")...) {
		snap.Set(got[:min(20, len(got))], append(append([]any{}, base...), "head")...)
	}
	if snap.Has(append(append([]any{}, base...), "tail")...) {
		snap.Set(got[max(0, len(got)-20):], append(append([]any{}, base...), "tail")...)
	}
}
