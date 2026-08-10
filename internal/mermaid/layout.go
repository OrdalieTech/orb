package mermaid

// Graph layout: rank, order, place, route, draw.
//
// Follows the Sugiyama outline — assign ranks along the flow axis, reorder
// within ranks to cut crossings, then relax positions on the cross axis so
// chains stay straight. Edges between adjacent ranks share horizontal "bus"
// rows; everything else is routed around the diagram through vertical
// "lanes".
//
// `BT` and `RL` reuse the `TD`/`LR` layouts and flip the finished canvas, so
// text never ends up mirrored.

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// pad is the cells of padding between a box border and its text.
const pad = 1

// Minimum horizontal / vertical space between boxes.
const (
	gapX = 3
	gapY = 2
)

// maxCanvasCells refuses to allocate a canvas larger than this many cells.
const maxCanvasCells = 1 << 21

// sat is saturating subtraction; Rust's usize arithmetic never goes negative.
func sat(a, b int) int { return max(0, a-b) }

// half is Math.floor(n / 2), which differs from Go division for negative n.
func half(n int) int {
	if n < 0 {
		return (n - 1) / 2
	}
	return n / 2
}

// jsRound is Math.round: half-up, toward positive infinity.
func jsRound(x float64) float64 { return math.Floor(x + 0.5) }

type placed struct {
	x    int
	y    int
	w    int
	h    int
	cx   int
	cy   int
	rank int
}

// nodeSizes are per-node dimensions. lay* include room for self-edge loops
// and labels.
type nodeSizes struct {
	boxW       []int
	boxH       []int
	layW       []int
	layH       []int
	extraH     []int
	selfLabelW []int
}

// nodeExtra says what to draw inside a node box.
type nodeExtra struct {
	kind     string // "plain" | "frame" | "compartments"
	sub      *canvas
	sections [][]string
}

type routePlan struct {
	canvasW int
	canvasH int
	// bandEnd is the coordinate just past each rank's boxes, where its bus
	// rows begin.
	bandEnd []int
	// edgeBus is the bus track offset per edge.
	edgeBus []int
	// laneBase is the coordinate of the first lane track.
	laneBase int
	// edgeLane is the lane track offset per edge.
	edgeLane []int
}

// ------------------------------------------------------------------ ranking

// computeRanks is longest-path ranking over the graph's DAG.
//
// Back edges (those closing a cycle) are excluded by a DFS colouring pass, so
// `A --> B --> C --> A` still ranks 0, 1, 2 rather than diverging.
func computeRanks(g *graph) []int {
	n := len(g.nodes)
	children := make([][]int, n)
	indeg := make([]int, n)
	for _, e := range g.edges {
		if e.from != e.to {
			children[e.from] = append(children[e.from], e.to)
			indeg[e.to]++
		}
	}

	color := make([]uint8, n)
	dag := make([][]int, n)
	var order []int

	// Roots first so ranks grow from natural entry points, then any
	// leftovers.
	var starts []int
	for i := 0; i < n; i++ {
		if indeg[i] == 0 {
			starts = append(starts, i)
		}
	}
	for i := 0; i < n; i++ {
		starts = append(starts, i)
	}
	for _, start := range starts {
		if color[start] == 0 {
			dfsDag(start, children, color, dag, &order)
		}
	}

	rank := make([]int, n)
	for i := len(order) - 1; i >= 0; i-- {
		u := order[i]
		for _, v := range dag[u] {
			rank[v] = max(rank[v], rank[u]+1)
		}
	}
	return rank
}

// dfsDag is an iterative DFS recording postorder and skipping edges back into
// the stack.
func dfsDag(start int, children [][]int, color []uint8, dag [][]int, order *[]int) {
	type frame struct {
		u int
		i int
	}
	stack := []frame{{u: start}}
	color[start] = 1
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		u := f.u
		if f.i < len(children[u]) {
			v := children[u][f.i]
			f.i++
			if color[v] == 1 {
				continue // grey: a back edge, ignore it
			}
			dag[u] = append(dag[u], v)
			if color[v] == 0 {
				color[v] = 1
				stack = append(stack, frame{u: v})
			}
		} else {
			color[u] = 2
			*order = append(*order, u)
			stack = stack[:len(stack)-1]
		}
	}
}

// orderRanks reorders nodes within each rank to minimise edge crossings
// (barycenter sweeps): alternate down/up passes sort each rank by the mean
// position of its neighbours, keeping whichever ordering crossed least.
func orderRanks(byRank [][]int, edges []edge, ranks []int) {
	n := len(ranks)
	if len(byRank) < 2 || n < 3 {
		return
	}

	parents := make([][]int, n)
	children := make([][]int, n)
	for _, e := range edges {
		if e.from != e.to && ranks[e.to] > ranks[e.from] {
			parents[e.to] = append(parents[e.to], e.from)
			children[e.from] = append(children[e.from], e.to)
		}
	}

	pos := make([]int, n)
	reindex := func(row []int) {
		for i, v := range row {
			pos[v] = i
		}
	}
	for _, row := range byRank {
		reindex(row)
	}

	copyRows := func(rows [][]int) [][]int {
		out := make([][]int, len(rows))
		for i, row := range rows {
			out[i] = append([]int(nil), row...)
		}
		return out
	}

	best := copyRows(byRank)
	bestCrossings := countCrossings(edges, ranks, pos)
	if bestCrossings == 0 {
		return
	}

	for it := 0; it < 8; it++ {
		// Alternate sweeping down (sort by parents) and up (sort by
		// children).
		var rows [][]int
		var neigh [][]int
		if it%2 == 0 {
			rows = byRank[1:]
			neigh = parents
		} else {
			for i := len(byRank) - 2; i >= 0; i-- {
				rows = append(rows, byRank[i])
			}
			neigh = children
		}
		for _, row := range rows {
			sortByBarycenter(row, neigh, pos)
			reindex(row)
		}
		crossings := countCrossings(edges, ranks, pos)
		if crossings < bestCrossings {
			bestCrossings = crossings
			best = copyRows(byRank)
		}
		if bestCrossings == 0 {
			break
		}
	}

	for i := range byRank {
		copy(byRank[i], best[i])
	}
}

func sortByBarycenter(row []int, neigh [][]int, pos []int) {
	type keyed struct {
		key float64
		v   int
	}
	items := make([]keyed, len(row))
	for i, v := range row {
		key := float64(pos[v])
		if len(neigh[v]) > 0 {
			s := 0.0
			for _, u := range neigh[v] {
				s += float64(pos[u])
			}
			key = s / float64(len(neigh[v]))
		}
		items[i] = keyed{key: key, v: v}
	}
	// Stable, matching Array.prototype.sort.
	sort.SliceStable(items, func(i, j int) bool { return items[i].key < items[j].key })
	for i, it := range items {
		row[i] = it.v
	}
}

func countCrossings(edges []edge, ranks []int, pos []int) int {
	type adj struct{ r, pf, pt int }
	var adjacent []adj
	for _, e := range edges {
		if e.from != e.to && ranks[e.to] == ranks[e.from]+1 {
			adjacent = append(adjacent, adj{ranks[e.from], pos[e.from], pos[e.to]})
		}
	}
	crossings := 0
	for i := 0; i < len(adjacent); i++ {
		a := adjacent[i]
		for j := i + 1; j < len(adjacent); j++ {
			b := adjacent[j]
			if a.r == b.r && ((a.pf < b.pf && a.pt > b.pt) || (a.pf > b.pf && a.pt < b.pt)) {
				crossings++
			}
		}
	}
	return crossings
}

// assignPositions assigns a cross-axis centre to every node so nodes line up
// under their neighbours: each node drifts toward the average of its
// neighbours while ranks keep their order and boxes keep sep between them.
func assignPositions(byRank [][]int, size []int, sep int, edges []edge, ranks []int) []int {
	n := len(size)
	parents := make([][]int, n)
	children := make([][]int, n)
	for _, e := range edges {
		if e.from != e.to && ranks[e.to] > ranks[e.from] {
			parents[e.to] = append(parents[e.to], e.from)
			children[e.from] = append(children[e.from], e.to)
		}
	}

	pos := make([]float64, n)
	for _, row := range byRank {
		x := 0.0
		for _, v := range row {
			h := float64(size[v]) / 2
			x += h
			pos[v] = x
			x += h + float64(sep)
		}
	}

	for it := 0; it < 10; it++ {
		var rows [][]int
		var neigh [][]int
		if it%2 == 0 {
			rows = byRank
			neigh = parents
		} else {
			for i := len(byRank) - 1; i >= 0; i-- {
				rows = append(rows, byRank[i])
			}
			neigh = children
		}
		for _, row := range rows {
			relaxRank(row, neigh, pos, size, sep)
		}
	}

	minLeft := math.Inf(1)
	for v := 0; v < n; v++ {
		minLeft = math.Min(minLeft, pos[v]-float64(size[v])/2)
	}
	if math.IsInf(minLeft, 0) {
		minLeft = 0
	}
	out := make([]int, n)
	for v := 0; v < n; v++ {
		out[v] = max(0, int(jsRound(pos[v]-minLeft)))
	}
	return out
}

func relaxRank(nodes []int, neigh [][]int, pos []float64, size []int, sep int) {
	n := len(nodes)
	if n == 0 {
		return
	}

	desired := make([]float64, n)
	for i, v := range nodes {
		if len(neigh[v]) == 0 {
			desired[i] = pos[v]
		} else {
			s := 0.0
			for _, u := range neigh[v] {
				s += pos[u]
			}
			desired[i] = s / float64(len(neigh[v]))
		}
	}
	halfOf := func(i int) float64 { return float64(size[nodes[i]]) / 2 }

	// Sweep right then left, then take the midpoint: this centres a node
	// between the tightest packing that respects order from either side.
	left := make([]float64, n)
	for i := 0; i < n; i++ {
		if i == 0 {
			left[i] = desired[i]
		} else {
			left[i] = math.Max(desired[i], left[i-1]+halfOf(i-1)+float64(sep)+halfOf(i))
		}
	}
	right := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		if i == n-1 {
			right[i] = desired[i]
		} else {
			right[i] = math.Min(desired[i], right[i+1]-halfOf(i+1)-float64(sep)-halfOf(i))
		}
	}
	for i := 0; i < n; i++ {
		pos[nodes[i]] = (left[i] + right[i]) / 2
	}
	for i := 1; i < n; i++ {
		minP := pos[nodes[i-1]] + halfOf(i-1) + float64(sep) + halfOf(i)
		if pos[nodes[i]] < minP {
			pos[nodes[i]] = minP
		}
	}
}

// ------------------------------------------------------------------- tracks

// span5 is a span competing for a track: [start, end, from, to, edgeIndex].
type span5 [5]int

// assignTracks packs spans into as few parallel tracks as possible.
//
// Two spans share a track when they are two cells apart, or when they share
// an endpoint — edges fanning out of one node deliberately reuse a single row
// so a merge draws one arrowhead rather than a stack of them.
func assignTracks(spans []span5) (assigned [][2]int, count int) {
	sorted := append([]span5(nil), spans...)
	sort.Slice(sorted, func(i, j int) bool {
		for k := 0; k < 5; k++ {
			if sorted[i][k] != sorted[j][k] {
				return sorted[i][k] < sorted[j][k]
			}
		}
		return false
	})
	var tracks [][][4]int
	for _, sp := range sorted {
		s, e, f, t, idx := sp[0], sp[1], sp[2], sp[3], sp[4]
		slot := -1
		for ti, members := range tracks {
			fits := true
			for _, m := range members {
				s2, e2, f2, t2 := m[0], m[1], m[2], m[3]
				if e2+2 > s && e+2 > s2 && f2 != f && t2 != t {
					fits = false
					break
				}
			}
			if fits {
				slot = ti
				break
			}
		}
		if slot == -1 {
			tracks = append(tracks, nil)
			slot = len(tracks) - 1
		}
		tracks[slot] = append(tracks[slot], [4]int{s, e, f, t})
		assigned = append(assigned, [2]int{idx, slot})
	}
	return assigned, len(tracks)
}

// busSpans lists edges from rank r to r + 1 that must jog sideways, so need a
// bus row.
func busSpans(g *graph, ranks, centers []int, r int, exact bool) []span5 {
	var out []span5
	for i, e := range g.edges {
		jogs := centers[e.from] != centers[e.to]
		if !exact {
			d := centers[e.from] - centers[e.to]
			jogs = d > 1 || d < -1
		}
		if e.from != e.to && ranks[e.from] == r && ranks[e.to] == r+1 && jogs {
			out = append(out, span5{
				min(centers[e.from], centers[e.to]),
				max(centers[e.from], centers[e.to]),
				e.from,
				e.to,
				i,
			})
		}
	}
	return out
}

// laneSpans lists edges skipping a rank or running backwards; these go around
// in a lane.
func laneSpans(g *graph, ranks []int, placedNodes []placed, vertical bool) []span5 {
	var out []span5
	for i, e := range g.edges {
		if e.from == e.to || ranks[e.to] == ranks[e.from]+1 {
			continue
		}
		pf := placedNodes[e.from]
		pt := placedNodes[e.to]
		var a, b int
		if vertical {
			a, b = min(pf.cy, pt.cy), max(pf.cy, pt.cy)
		} else {
			a, b = min(pf.cx, pt.cx), max(pf.cx, pt.cx)
		}
		out = append(out, span5{a, b, e.from, e.to, i})
	}
	return out
}

// ----------------------------------------------------------------- placement

func placeTd(ranks []int, maxRank int, byRank [][]int, sizes nodeSizes, g *graph, placedNodes []placed) routePlan {
	centers := assignPositions(byRank, sizes.layW, gapX, g.edges, ranks)

	edgeBus := make([]int, len(g.edges))
	busTracks := make([]int, maxRank+1)
	for r := 0; r < maxRank; r++ {
		spans := busSpans(g, ranks, centers, r, false)
		if len(spans) == 0 {
			continue
		}
		assigned, count := assignTracks(spans)
		for _, a := range assigned {
			edgeBus[a[0]] = a[1]
		}
		busTracks[r] = count
	}

	rankH := make([]int, len(byRank))
	for r, row := range byRank {
		if len(row) == 0 {
			rankH[r] = 3
			continue
		}
		h := 0
		for _, i := range row {
			h = max(h, sizes.boxH[i]+sizes.extraH[i])
		}
		rankH[r] = h
	}
	rankY := make([]int, maxRank+1)
	for r := 1; r <= maxRank; r++ {
		rankY[r] = rankY[r-1] + rankH[r-1] + max(gapY, busTracks[r-1]+1)
	}
	canvasH := rankY[maxRank] + rankH[maxRank]
	bandEnd := make([]int, maxRank+1)
	for r := 0; r <= maxRank; r++ {
		bandEnd[r] = rankY[r] + rankH[r]
	}

	diagramW := 1
	for r, row := range byRank {
		for _, idx := range row {
			w := sizes.boxW[idx]
			h := sizes.boxH[idx]
			cx := centers[idx]
			x := sat(cx, half(w))
			y := rankY[r] + half(rankH[r]-h-sizes.extraH[idx])
			placedNodes[idx] = placed{x: x, y: y, w: w, h: h, cx: cx, cy: y + half(h), rank: r}
			diagramW = max(diagramW, x+w)
			if sizes.extraH[idx] > 0 && sizes.selfLabelW[idx] > 0 {
				diagramW = max(diagramW, x+w+2+sizes.selfLabelW[idx])
			}
		}
	}

	contentW := diagramW
	for _, e := range g.edges {
		if e.from == e.to || e.label == "" {
			continue
		}
		lw := min(stringWidth(e.label), maxLabel)
		if ranks[e.to] == ranks[e.from]+1 {
			contentW = max(contentW, placedNodes[e.to].cx+2+lw)
		} else {
			contentW = max(contentW, diagramW+lw+1)
		}
	}

	edgeLane := make([]int, len(g.edges))
	lanes := laneSpans(g, ranks, placedNodes, true)
	canvasW := contentW
	laneBase := 0
	if len(lanes) > 0 {
		assigned, count := assignTracks(lanes)
		for _, a := range assigned {
			edgeLane[a[0]] = a[1]
		}
		canvasW = contentW + 1 + count
		laneBase = contentW + 1
	}

	return routePlan{canvasW: canvasW, canvasH: canvasH, bandEnd: bandEnd, edgeBus: edgeBus, laneBase: laneBase, edgeLane: edgeLane}
}

func placeLr(ranks []int, maxRank int, byRank [][]int, sizes nodeSizes, g *graph, placedNodes []placed) routePlan {
	colW := make([]int, len(byRank))
	for r, row := range byRank {
		w := 0
		for _, i := range row {
			w = max(w, sizes.boxW[i])
		}
		colW[r] = w
	}

	// Left-to-right edge labels sit in the gap between columns, so the gap
	// has to be wide enough for the widest of them.
	maxLabelW := 0
	for _, e := range g.edges {
		if (e.from == e.to || ranks[e.to] == ranks[e.from]+1) && e.label != "" {
			maxLabelW = max(maxLabelW, min(stringWidth(e.label), maxLabel))
		}
	}
	baseGap := max(gapX+1, maxLabelW+3)

	centers := assignPositions(byRank, sizes.layH, 1, g.edges, ranks)

	edgeBus := make([]int, len(g.edges))
	busTracks := make([]int, maxRank+1)
	for r := 0; r < maxRank; r++ {
		spans := busSpans(g, ranks, centers, r, true)
		if len(spans) == 0 {
			continue
		}
		assigned, count := assignTracks(spans)
		for _, a := range assigned {
			edgeBus[a[0]] = a[1]
		}
		busTracks[r] = count
	}

	rankX := make([]int, maxRank+1)
	for r := 1; r <= maxRank; r++ {
		rankX[r] = rankX[r-1] + colW[r-1] + max(baseGap, busTracks[r-1]+1)
	}
	selfTail := 0
	for _, i := range byRank[maxRank] {
		if sizes.extraH[i] > 0 && sizes.selfLabelW[i] > 0 {
			selfTail = max(selfTail, 2+sizes.selfLabelW[i])
		}
	}
	canvasW := rankX[maxRank] + colW[maxRank] + selfTail
	bandEnd := make([]int, maxRank+1)
	for r := 0; r <= maxRank; r++ {
		bandEnd[r] = rankX[r] + colW[r]
	}

	diagramH := 1
	for r, row := range byRank {
		x := rankX[r]
		for _, idx := range row {
			w := sizes.boxW[idx]
			h := sizes.boxH[idx]
			cy := centers[idx]
			y := sat(cy, half(h+sizes.extraH[idx]))
			placedNodes[idx] = placed{x: x, y: y, w: w, h: h, cx: x + half(w), cy: y + half(h), rank: r}
			diagramH = max(diagramH, y+h+sizes.extraH[idx])
		}
	}

	edgeLane := make([]int, len(g.edges))
	lanes := laneSpans(g, ranks, placedNodes, false)
	canvasH := diagramH
	laneBase := 0
	if len(lanes) > 0 {
		assigned, count := assignTracks(lanes)
		for _, a := range assigned {
			edgeLane[a[0]] = a[1]
		}
		canvasH = diagramH + 1 + count
		laneBase = diagramH + 1
	}

	return routePlan{canvasW: canvasW, canvasH: canvasH, bandEnd: bandEnd, edgeBus: edgeBus, laneBase: laneBase, edgeLane: edgeLane}
}

// -------------------------------------------------------------------- canvas

// layoutCanvas ranks, places, draws and routes a graph onto a fresh canvas.
// nil means the diagram is empty or over the cell cap.
func layoutCanvas(g *graph, extras []nodeExtra) *canvas {
	n := len(g.nodes)
	if n == 0 {
		return nil
	}

	ranks := computeRanks(g)
	maxRank := 0
	for _, r := range ranks {
		maxRank = max(maxRank, r)
	}

	byRank := make([][]int, maxRank+1)
	for idx, r := range ranks {
		byRank[r] = append(byRank[r], idx)
	}
	orderRanks(byRank, g.edges, ranks)

	wrapped := make([][]string, n)
	for i, nd := range g.nodes {
		wrapped[i] = wrapLabel(nd.label, wrapWidth, maxLines)
	}
	widest := func(lines []string) int {
		w := 1
		for _, l := range lines {
			w = max(w, stringWidth(l))
		}
		return w
	}

	boxW := make([]int, n)
	boxH := make([]int, n)
	for i, extra := range extras {
		switch extra.kind {
		case "frame":
			boxW[i] = max(extra.sub.w+2, stringWidth(fitLabel(g.nodes[i].label, wrapWidth))+4)
			boxH[i] = extra.sub.h + 2
		case "compartments":
			var flat []string
			for _, sec := range extra.sections {
				flat = append(flat, sec...)
			}
			boxW[i] = widest(flat) + 2*pad + 2
			filled := 0
			rows := 0
			for _, sec := range extra.sections {
				if len(sec) > 0 {
					filled++
				}
				rows += len(sec)
			}
			boxH[i] = rows + sat(filled, 1) + 2
		default:
			boxW[i] = widest(wrapped[i]) + 2*pad + 2
			boxH[i] = len(wrapped[i]) + 2
		}
	}

	// A self-edge needs two rows below its box, and room beside it for a
	// label.
	extraH := make([]int, n)
	selfLabelW := make([]int, n)
	for _, e := range g.edges {
		if e.from != e.to {
			continue
		}
		extraH[e.from] = 2
		if e.label != "" {
			selfLabelW[e.from] = max(selfLabelW[e.from], min(stringWidth(e.label), maxLabel))
		}
	}
	for i := 0; i < n; i++ {
		if extraH[i] > 0 {
			boxW[i] = max(boxW[i], 7)
		}
	}

	layW := make([]int, n)
	layH := make([]int, n)
	for i := 0; i < n; i++ {
		layW[i] = boxW[i]
		if selfLabelW[i] > 0 {
			layW[i] += 2 * (selfLabelW[i] + 3)
		}
		layH[i] = boxH[i] + extraH[i]
	}
	sizes := nodeSizes{boxW: boxW, boxH: boxH, layW: layW, layH: layH, extraH: extraH, selfLabelW: selfLabelW}

	placedNodes := make([]placed, n)

	vertical := g.dir == dirDown || g.dir == dirUp
	var plan routePlan
	if vertical {
		plan = placeTd(ranks, maxRank, byRank, sizes, g, placedNodes)
	} else {
		plan = placeLr(ranks, maxRank, byRank, sizes, g, placedNodes)
	}

	if plan.canvasW*plan.canvasH > maxCanvasCells {
		return nil
	}

	c := newCanvas(plan.canvasW, plan.canvasH)
	for idx := 0; idx < n; idx++ {
		extra := extras[idx]
		switch extra.kind {
		case "frame":
			drawFrame(c, placedNodes[idx], g.nodes[idx].label, extra.sub)
		case "compartments":
			drawClassBox(c, placedNodes[idx], extra.sections)
		default:
			drawBox(c, placedNodes[idx], wrapped[idx], g.nodes[idx].shape)
		}
	}

	for i, e := range g.edges {
		switch e.line {
		case lineDotted:
			c.curStyle = styDot
		case lineThick:
			c.curStyle = styThick
		default:
			c.curStyle = stySolid
		}
		if e.from == e.to {
			routeSelf(c, placedNodes[e.from], e)
			continue
		}
		from := placedNodes[e.from]
		to := placedNodes[e.to]
		adjacent := to.rank == from.rank+1
		bus := plan.bandEnd[from.rank] + plan.edgeBus[i]
		lane := plan.laneBase + plan.edgeLane[i]
		if vertical {
			if adjacent {
				routeForward(c, from, to, e, bus)
			} else {
				routeBack(c, from, to, e, lane)
			}
		} else if adjacent {
			routeForwardLr(c, from, to, e, bus)
		} else {
			routeBackLr(c, from, to, e, lane)
		}
	}

	c.finalizeMask()
	return c
}

// orient applies the direction flip a finished canvas needs for `BT` / `RL`.
func orient(c *canvas, g *graph) *canvas {
	switch g.dir {
	case dirUp:
		c.flipVertical()
	case dirLeft:
		c.flipHorizontal()
	}
	return c
}

// layoutFlowchart lays out flowchart and state diagrams: plain boxes, no
// extra content.
func layoutFlowchart(g *graph) *canvas {
	extras := make([]nodeExtra, len(g.nodes))
	for i := range extras {
		extras[i] = nodeExtra{kind: "plain"}
	}
	c := layoutCanvas(g, extras)
	if c == nil {
		return nil
	}
	return orient(c, g)
}

// layoutClass lays out class and ER diagrams: boxes divided into
// title / attribute / method rows.
func layoutClass(g *graph, infos []classInfo) *canvas {
	extras := make([]nodeExtra, len(g.nodes))
	for i, nd := range g.nodes {
		var title []string
		if infos[i].hasAnnotation {
			title = append(title, "«"+infos[i].annotation+"»")
		}
		title = append(title, displayGenerics(nd.label))
		extras[i] = nodeExtra{kind: "compartments", sections: [][]string{title, infos[i].attrs, infos[i].methods}}
	}
	c := layoutCanvas(g, extras)
	if c == nil {
		return nil
	}
	return orient(c, g)
}

// -------------------------------------------------------------------- groups

func nodeKey(i int) string  { return "n" + strconv.Itoa(i) }
func groupKey(i int) string { return "g" + strconv.Itoa(i) }

type scopedEdge struct {
	fromKey string
	toKey   string
	edge    int
}

// layoutGrouped lays out a flowchart that uses `subgraph`.
//
// Each subgraph becomes a framed box holding its own independently laid-out
// canvas. An edge is drawn in the innermost scope containing both endpoints;
// one crossing a subgraph boundary attaches to the frame instead of the node.
// The top-level scope is -1 (upstream null).
func layoutGrouped(g *graph) *canvas {
	// A node whose id matches a subgraph id stands in for that subgraph.
	proxy := map[int]int{}
	for gi, gr := range g.groups {
		if ni, ok := g.index[gr.id]; ok {
			proxy[ni] = gi
		}
	}

	groupChain := func(gi int) []int {
		var chain []int
		for cur := gi; cur != -1; cur = g.groups[cur].parent {
			chain = append(chain, cur)
		}
		for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
			chain[i], chain[j] = chain[j], chain[i]
		}
		return chain
	}
	endpoint := func(n int) (key string, chain []int) {
		if gi, ok := proxy[n]; ok {
			return groupKey(gi), groupChain(g.groups[gi].parent)
		}
		return nodeKey(n), groupChain(g.nodeGroup[n])
	}

	// Edges bucketed by the scope that draws them.
	scopeEdges := map[int][]scopedEdge{}
	referenced := make([]bool, len(g.groups))
	for ei, e := range g.edges {
		fKey, fChain := endpoint(e.from)
		tKey, tChain := endpoint(e.to)
		k := 0
		for k < len(fChain) && k < len(tChain) && fChain[k] == tChain[k] {
			k++
		}
		scope := -1
		if k > 0 {
			scope = fChain[k-1]
		}
		if len(fChain) > k {
			fKey = groupKey(fChain[k])
		}
		if len(tChain) > k {
			tKey = groupKey(tChain[k])
		}
		for _, key := range [2]string{fKey, tKey} {
			if strings.HasPrefix(key, "g") {
				gi, _ := strconv.Atoi(key[1:])
				referenced[gi] = true
			}
		}
		scopeEdges[scope] = append(scopeEdges[scope], scopedEdge{fromKey: fKey, toKey: tKey, edge: ei})
	}

	directNodes := map[int][]int{}
	for ni, gi := range g.nodeGroup {
		if _, ok := proxy[ni]; ok {
			continue
		}
		directNodes[gi] = append(directNodes[gi], ni)
	}

	// Drop empty subgraphs, but keep any that an edge attaches to.
	keep := make([]bool, len(g.groups))
	for gi := len(g.groups) - 1; gi >= 0; gi-- {
		hasNodes := len(directNodes[gi]) > 0
		hasChildren := false
		for c, gr := range g.groups {
			if gr.parent == gi && keep[c] {
				hasChildren = true
				break
			}
		}
		keep[gi] = hasNodes || hasChildren || referenced[gi]
	}

	c := buildScope(g, -1, scopeEdges, directNodes, keep)
	if c == nil {
		return nil
	}
	return orient(c, g)
}

func buildScope(g *graph, scope int, scopeEdges map[int][]scopedEdge, directNodes map[int][]int, keep []bool) *canvas {
	var items []string
	for _, ni := range directNodes[scope] {
		items = append(items, nodeKey(ni))
	}
	for gi, gr := range g.groups {
		if gr.parent == scope && keep[gi] {
			items = append(items, groupKey(gi))
		}
	}

	if len(items) == 0 {
		return newCanvas(1, 1)
	}

	indexOf := map[string]int{}
	var nodes []node
	var extras []nodeExtra
	for _, item := range items {
		indexOf[item] = len(nodes)
		i, _ := strconv.Atoi(item[1:])
		if strings.HasPrefix(item, "n") {
			nodes = append(nodes, node{label: g.nodes[i].label, shape: g.nodes[i].shape})
			extras = append(extras, nodeExtra{kind: "plain"})
		} else {
			sub := buildScope(g, i, scopeEdges, directNodes, keep)
			if sub == nil {
				return nil
			}
			nodes = append(nodes, node{label: g.groups[i].label, shape: shapeRect})
			extras = append(extras, nodeExtra{kind: "frame", sub: sub})
		}
	}

	var edges []edge
	for _, se := range scopeEdges[scope] {
		fi, okF := indexOf[se.fromKey]
		ti, okT := indexOf[se.toKey]
		if !okF || !okT {
			continue
		}
		e := g.edges[se.edge]
		edges = append(edges, edge{
			from: fi, to: ti,
			label: e.label, headTo: e.headTo, headFrom: e.headFrom, line: e.line,
		})
	}

	// Layout only reads nodes/edges/dir, so a bare graph carrying those is
	// enough.
	synth := newGraph(g.dir)
	synth.nodes = nodes
	synth.edges = edges
	return layoutCanvas(synth, extras)
}

// ------------------------------------------------------------------- drawing

func drawBox(c *canvas, p placed, lines []string, shape string) {
	x, y, w, h := p.x, p.y, p.w, p.h
	right := x + w - 1
	bottom := y + h - 1

	rounded := shape == shapeRound || shape == shapeDiamond
	if rounded {
		c.set(x, y, "╭", clsBorder)
		c.set(right, y, "╮", clsBorder)
		c.set(x, bottom, "╰", clsBorder)
		c.set(right, bottom, "╯", clsBorder)
	} else {
		c.set(x, y, "┌", clsBorder)
		c.set(right, y, "┐", clsBorder)
		c.set(x, bottom, "└", clsBorder)
		c.set(right, bottom, "┘", clsBorder)
	}

	// The perimeter is drawn as bits so edges can tee into it, but it is the
	// box outline, so it claims border rather than edge.
	for cx := x + 1; cx < right; cx++ {
		c.addBits(cx, y, bitL|bitR, clsBorder)
		c.addBits(cx, bottom, bitL|bitR, clsBorder)
	}
	for cy := y + 1; cy < bottom; cy++ {
		c.addBits(x, cy, bitU|bitD, clsBorder)
		c.addBits(right, cy, bitU|bitD, clsBorder)
	}

	for cy := y; cy <= bottom; cy++ {
		for cx := x; cx <= right; cx++ {
			if cx < c.w && cy < c.h {
				c.occupied[c.idx(cx, cy)] = 1
			}
		}
	}

	inner := max(1, sat(w, 2*pad+2))
	for li, line := range lines {
		text := fitLabel(line, inner)
		textX := x + 1 + pad + half(sat(inner, stringWidth(text)))
		drawText(c, text, textX, y+1+li, clsText)
	}
}

// drawClassBox draws a class or ER box: sections separated by horizontal
// rules, title centred.
func drawClassBox(c *canvas, p placed, sections [][]string) {
	drawBox(c, p, nil, shapeRect)
	inner := max(1, sat(p.w, 2*pad+2))
	row := p.y + 1
	first := true
	for si, section := range sections {
		if len(section) == 0 {
			continue
		}
		if !first {
			c.set(p.x, row, "├", clsBorder)
			for x := p.x + 1; x < p.x+p.w-1; x++ {
				c.set(x, row, "─", clsBorder)
			}
			c.set(p.x+p.w-1, row, "┤", clsBorder)
			row++
		}
		first = false
		for _, line := range section {
			text := fitLabel(line, inner)
			tx := p.x + 1 + pad
			if si == 0 {
				tx = p.x + 1 + pad + half(sat(inner, stringWidth(text)))
			}
			drawTextOverEdges(c, text, tx, row, clsText)
			row++
		}
	}
}

// drawFrame draws a subgraph frame: a titled box with a finished sub-canvas
// centred inside.
func drawFrame(c *canvas, p placed, title string, sub *canvas) {
	drawBox(c, p, nil, shapeRect)
	t := fitLabel(title, sat(p.w, 4))
	drawTextOverEdges(c, " "+t+" ", p.x+1, p.y, clsText)
	c.blit(sub, p.x+1+half(p.w-2-sub.w), p.y+1+half(p.h-2-sub.h))
}

// ------------------------------------------------------------------- routing

func headGlyph(head, arrow string) string {
	switch head {
	case headCircle:
		return "o"
	case headCross:
		return "×"
	case headDiamondFill:
		return "◆"
	case headDiamondOpen:
		return "◇"
	case headTriangle:
		switch arrow {
		case "▼":
			return "▽"
		case "▲":
			return "△"
		case "◄":
			return "◁"
		case "▶":
			return "▷"
		}
		return arrow
	default:
		return arrow
	}
}

// routeForward draws adjacent ranks, top-down: drop, jog along the bus row,
// drop into the head.
func routeForward(c *canvas, from, to placed, e edge, bus int) {
	tx := to.cx
	// A jog of one column reads as a kink; snap straight instead.
	bx := from.cx
	if d := from.cx - tx; d >= -1 && d <= 1 {
		bx = tx
	}
	by := from.y + from.h - 1
	headRow := to.y - 1

	c.junction(bx, by, bitD)
	c.segV(bx, by, bus)
	if bx == tx {
		c.segV(bx, bus, headRow)
	} else {
		c.segH(bus, bx, tx)
		c.segV(tx, bus, headRow)
	}

	if e.headTo == headNone {
		c.addBits(tx, headRow, bitU, clsEdge)
	} else {
		c.set(tx, headRow, headGlyph(e.headTo, "▼"), clsEdge)
	}
	if e.headFrom != headNone {
		c.set(bx, by, headGlyph(e.headFrom, "▲"), clsEdge)
	}

	if e.label != "" {
		placeLabel(c, e.label, headRow, tx+1)
	}
}

// routeSelf draws a self-edge: a stub loop hanging below the box.
func routeSelf(c *canvas, p placed, e edge) {
	bottom := p.y + p.h - 1
	exitX := p.cx + 1
	retX := p.x + p.w - 2
	if retX <= exitX || bottom+2 >= c.h {
		return
	}

	v, h, bl, br := "│", "─", "╰", "╯"
	switch e.line {
	case lineDotted:
		v, h, bl, br = "╎", "╌", "╰", "╯"
	case lineThick:
		v, h, bl, br = "┃", "━", "┗", "┛"
	}

	c.junction(exitX, bottom, bitD)
	c.set(exitX, bottom+1, v, clsEdge)
	c.set(exitX, bottom+2, bl, clsEdge)
	for x := exitX + 1; x < retX; x++ {
		c.set(x, bottom+2, h, clsEdge)
	}
	c.set(retX, bottom+2, br, clsEdge)
	c.set(retX, bottom+1, headGlyph(e.headTo, "▲"), clsEdge)
	if e.label != "" {
		placeLabel(c, e.label, bottom+1, p.x+p.w+1)
	}
}

// routeBack draws a skip or back edge, top-down: out the right side, up a
// lane, back in.
func routeBack(c *canvas, from, to placed, e edge, laneX int) {
	sx := from.x + from.w - 1
	sy := from.cy
	tx := to.x + to.w - 1
	tyc := to.cy

	c.junction(sx, sy, bitR)
	c.segH(sy, sx, laneX)
	c.segV(laneX, sy, tyc)
	c.segH(tyc, tx+1, laneX)

	if e.headTo == headNone {
		c.addBits(tx+1, tyc, bitR, clsEdge)
	} else {
		c.set(tx+1, tyc, headGlyph(e.headTo, "◄"), clsEdge)
	}
	if e.headFrom != headNone {
		c.set(sx, sy, headGlyph(e.headFrom, "◄"), clsEdge)
	}

	if e.label != "" {
		placeLabel(c, e.label, sat(tyc, 1), sat(laneX, stringWidth(e.label)+1))
	}
}

// routeForwardLr draws adjacent ranks, left-to-right: out the right side, jog
// on the bus column.
func routeForwardLr(c *canvas, from, to placed, e edge, bus int) {
	rx := from.x + from.w - 1
	ry := from.cy
	ly := to.cy
	headCol := to.x - 1

	c.junction(rx, ry, bitR)
	c.segH(ry, rx, bus)
	if ry == ly {
		c.segH(ry, bus, headCol)
	} else {
		c.segV(bus, ry, ly)
		c.segH(ly, bus, headCol)
	}

	if e.headTo == headNone {
		c.addBits(headCol, ly, bitR, clsEdge)
	} else {
		c.set(headCol, ly, headGlyph(e.headTo, "▶"), clsEdge)
	}
	if e.headFrom != headNone {
		c.set(rx, ry, headGlyph(e.headFrom, "◄"), clsEdge)
	}

	if e.label != "" {
		placeLabel(c, e.label, sat(ly, 1), bus+1)
	}
}

// routeBackLr draws a skip or back edge, left-to-right: down out the bottom,
// along a lane, back up.
func routeBackLr(c *canvas, from, to placed, e edge, laneY int) {
	sx := from.cx
	sy := from.y + from.h - 1
	tx := to.cx
	ty := to.y + to.h - 1

	c.junction(sx, sy, bitD)
	c.segV(sx, sy, laneY)
	c.segH(laneY, sx, tx)
	c.segV(tx, laneY, ty+1)

	if e.headTo == headNone {
		c.addBits(tx, ty+1, bitD, clsEdge)
	} else {
		c.set(tx, ty+1, headGlyph(e.headTo, "▲"), clsEdge)
	}
	if e.headFrom != headNone {
		c.set(sx, sy, headGlyph(e.headFrom, "▲"), clsEdge)
	}

	if e.label != "" {
		placeLabel(c, e.label, sat(laneY, 1), half(sx+tx))
	}
}

// placeLabel writes an edge label, stopping at the first cell already
// occupied.
func placeLabel(c *canvas, label string, row, startX int) {
	if row >= c.h {
		return
	}
	text := fitLabel(label, maxLabel)
	x := startX
	for _, mc := range measured(text) {
		if mc.width == 0 {
			continue
		}
		if x+mc.width > c.w {
			break
		}
		blocked := false
		for k := 0; k < mc.width; k++ {
			i := c.idx(x+k, row)
			if c.ch[i] != " " || c.mask[i] != 0 || c.occupied[i] != 0 {
				blocked = true
			}
		}
		if blocked {
			break
		}
		c.set(x, row, mc.cluster, clsEdgeLabel)
		for k := 1; k < mc.width; k++ {
			c.set(x+k, row, cont, clsEdgeLabel)
		}
		x += mc.width
	}
}
