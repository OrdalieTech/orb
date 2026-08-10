package mermaid

import "github.com/rivo/uniseg"

// Display width, measured in grapheme clusters.
//
// A cluster is the unit both of measuring and of painting, so a box is always
// sized for exactly what gets drawn into it. Clustering comes from uniseg
// (UAX #29, upstream's Intl.Segmenter), which already handles ZWJ sequences,
// skin-tone modifiers, variation selectors, keycaps, flags and Hangul.
// Per-code-point widths come from the generated table in width_data.go.

const vs16 = 0xfe0f

func isRegionalIndicator(cp rune) bool { return cp >= 0x1f1e6 && cp <= 0x1f1ff }

// codePointWidth is the width of one code point; the table covers the whole
// code point space.
func codePointWidth(cp rune) int {
	lo, hi := 0, len(widths)-1
	for lo <= hi {
		mid := (lo + hi) >> 1
		run := widths[mid]
		switch {
		case cp < run[0]:
			hi = mid - 1
		case cp > run[1]:
			lo = mid + 1
		default:
			return int(run[2])
		}
	}
	return 1
}

// clusterWidth is the columns occupied by one grapheme cluster.
//
// The widest code point wins, so a base plus its combining marks measures as
// the base. Two adjustments: a variation selector requesting emoji
// presentation forces two columns, as does a regional indicator pair (a flag).
//
// Zero is a real answer — a soft hyphen or zero-width space occupies nothing,
// and callers skip painting such a cluster rather than reserving a cell.
func clusterWidth(cluster string) int {
	w := 0
	hasVS16 := false
	regional := 0
	for _, cp := range cluster {
		if cp == vs16 {
			hasVS16 = true
		}
		if isRegionalIndicator(cp) {
			regional++
		}
		if cw := codePointWidth(cp); cw > w {
			w = cw
		}
	}
	if hasVS16 || regional >= 2 {
		return 2
	}
	return w
}

type measuredCluster struct {
	cluster string
	width   int
}

// measured lists grapheme clusters paired with their display width, so no
// loop can split a cluster.
func measured(s string) []measuredCluster {
	var out []measuredCluster
	graphemes := uniseg.NewGraphemes(s)
	for graphemes.Next() {
		cluster := graphemes.Str()
		out = append(out, measuredCluster{cluster, clusterWidth(cluster)})
	}
	return out
}

// stringWidth is the display columns of a string.
func stringWidth(s string) int {
	w := 0
	graphemes := uniseg.NewGraphemes(s)
	for graphemes.Next() {
		w += clusterWidth(graphemes.Str())
	}
	return w
}
