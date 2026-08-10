package mermaid

// Sequence diagram layout.
//
// Participants get one column each, with lifelines running the full height
// and a box repeated at top and bottom. Column gaps are solved from the
// widest thing that has to fit between any two columns — a message label, a
// note, a self-message stub — then items stack down the canvas in source
// order.

import "sort"

// seqGap is the minimum columns between adjacent lifelines.
const seqGap = 5

// noteGeometry says where a note box sits, given the lifeline positions.
func noteGeometry(xs []int, anchor noteAnchor, textW int) (x, w int) {
	if anchor.kind == "over" {
		center := half(xs[anchor.from] + xs[anchor.to])
		w := max(xs[anchor.to]-xs[anchor.from]+5, textW+2*pad+2)
		return sat(center, half(w)), w
	}
	w = textW + 2*pad + 2
	if anchor.kind == "left" {
		return sat(xs[anchor.at], 2+w-1), w
	}
	return xs[anchor.at] + 2, w
}

func layoutSequence(seq *sequence) *canvas {
	n := len(seq.labels)
	labels := make([]string, n)
	boxW := make([]int, n)
	for i, l := range seq.labels {
		labels[i] = fitLabel(l, wrapWidth)
		boxW[i] = max(1, stringWidth(labels[i])) + 2*pad + 2
	}
	const boxH = 3

	gaps := make([]int, sat(n, 1))
	for i := range gaps {
		gaps[i] = max(seqGap, ceilDiv2(boxW[i])+ceilDiv2(boxW[i+1])+1)
	}

	// Each requirement is "columns l..r together need at least `need` cells".
	type req struct{ l, r, need int }
	var reqs []req
	for _, item := range seq.items {
		switch item.kind {
		case "message":
			tw := stringWidth(item.text)
			if item.from != item.to {
				reqs = append(reqs, req{min(item.from, item.to), max(item.from, item.to), max(tw+2, 4)})
			} else if item.from+1 < n {
				reqs = append(reqs, req{item.from, item.from + 1, 5 + tw + 2})
			}
		case "note":
			tw := stringWidth(item.text)
			a := item.anchor
			switch {
			case a.kind == "over" && a.from < a.to:
				reqs = append(reqs, req{a.from, a.to, sat(tw, 1)})
			case a.kind == "over":
				need := ceilDiv2(tw+4) + 2
				if a.from > 0 {
					reqs = append(reqs, req{a.from - 1, a.from, need})
				}
				if a.from+1 < n {
					reqs = append(reqs, req{a.from, a.from + 1, need})
				}
			case a.kind == "left" && a.at > 0:
				reqs = append(reqs, req{a.at - 1, a.at, tw + 7})
			case a.kind == "right" && a.at+1 < n:
				reqs = append(reqs, req{a.at, a.at + 1, tw + 7})
			}
		}
	}
	// Narrowest spans first, so a wide requirement absorbs what they already
	// gave. Stable, matching Array.prototype.sort.
	sort.SliceStable(reqs, func(i, j int) bool {
		return reqs[i].r-reqs[i].l < reqs[j].r-reqs[j].l
	})
	for _, rq := range reqs {
		cur := 0
		for i := rq.l; i < rq.r; i++ {
			cur += gaps[i]
		}
		if cur < rq.need {
			gaps[rq.r-1] += rq.need - cur
		}
	}

	xs := make([]int, n)
	xs[0] = half(boxW[0])
	for i := 1; i < n; i++ {
		xs[i] = xs[i-1] + gaps[i-1]
	}

	canvasW := xs[n-1] + ceilDiv2(boxW[n-1]) + 1
	for _, item := range seq.items {
		switch {
		case item.kind == "message" && item.from == item.to:
			canvasW = max(canvasW, xs[item.from]+5+stringWidth(item.text)+1)
		case item.kind == "note":
			gx, gw := noteGeometry(xs, item.anchor, stringWidth(item.text))
			canvasW = max(canvasW, gx+gw+1)
		case item.kind == "divider":
			canvasW = max(canvasW, stringWidth(item.text)+4)
		}
	}

	rows := make([]int, len(seq.items))
	y := boxH + 1
	for k, item := range seq.items {
		rows[k] = y
		y += rowHeight(item)
	}
	bottomTop := y
	canvasH := bottomTop + boxH

	if canvasW*canvasH > maxCanvasCells {
		return nil
	}

	c := newCanvas(canvasW, canvasH)

	for i := 0; i < n; i++ {
		for _, by := range [2]int{0, bottomTop} {
			drawBox(c, seqBox(sat(xs[i], half(boxW[i])), by, boxW[i], boxH), []string{labels[i]}, shapeRect)
		}
	}
	for k, item := range seq.items {
		if item.kind != "note" {
			continue
		}
		gx, gw := noteGeometry(xs, item.anchor, stringWidth(item.text))
		drawBox(c, seqBox(gx, rows[k], gw, 3), []string{item.text}, shapeRect)
	}

	for _, x := range xs {
		c.junction(x, boxH-1, bitD)
		c.segV(x, boxH, bottomTop-1)
		c.junction(x, bottomTop, bitU)
	}

	for k, item := range seq.items {
		r := rows[k]
		switch item.kind {
		case "message":
			drawMessage(c, item, xs, r)
		case "divider":
			drawDivider(c, item.text, r, canvasW)
		}
	}

	c.finalizeMask()
	return c
}

// ceilDiv2 is Math.ceil(n / 2) for non-negative n.
func ceilDiv2(n int) int { return (n + 1) / 2 }

func rowHeight(item seqItem) int {
	if item.kind == "note" {
		return 4
	}
	if item.kind == "divider" {
		return 2
	}
	if item.from == item.to {
		return 4
	}
	if item.text != "" {
		return 3
	}
	return 2
}

// seqBox is geometry for a box drawn by position and size; ranks are
// irrelevant here.
func seqBox(x, y, w, h int) placed {
	return placed{x: x, y: y, w: w, h: h, cx: x + half(w), cy: y + 1}
}

func drawMessage(c *canvas, item seqItem, xs []int, r int) {
	lineCh := "─"
	if item.dashed {
		lineCh = "╌"
	}

	if item.from == item.to {
		// A stub that leaves the lifeline and returns two rows down.
		x := xs[item.from]
		c.junction(x, r, bitR)
		c.set(x+1, r, lineCh, clsEdge)
		c.set(x+2, r, lineCh, clsEdge)
		c.set(x+3, r, "╮", clsEdge)
		c.set(x+3, r+1, "│", clsEdge)
		head := "◄"
		if item.head == headCross {
			head = "×"
		}
		c.set(x+1, r+2, head, clsEdge)
		c.set(x+2, r+2, lineCh, clsEdge)
		c.set(x+3, r+2, "╯", clsEdge)
		if item.text != "" {
			drawTextOverEdges(c, item.text, x+5, r+1, clsText)
		}
		return
	}

	x0 := xs[item.from]
	x1 := xs[item.to]
	rightward := x1 > x0
	// A labelled message writes its text on r and draws the arrow below it.
	arrowRow := r
	if item.text != "" {
		arrowRow = r + 1
	}
	lo := min(x0, x1)
	hi := max(x0, x1)

	if rightward {
		c.junction(x0, arrowRow, bitR)
	} else {
		c.junction(x0, arrowRow, bitL)
	}
	for x := lo + 1; x < hi; x++ {
		c.set(x, arrowRow, lineCh, clsEdge)
	}
	headCh := "◄"
	if rightward {
		headCh = "▶"
	}
	if item.head == headCross {
		headCh = "×"
	}
	if rightward {
		c.set(x1-1, arrowRow, headCh, clsEdge)
	} else {
		c.set(x1+1, arrowRow, headCh, clsEdge)
	}

	if item.text != "" {
		span := hi - lo - 1
		t := fitLabel(item.text, max(1, span))
		drawTextOverEdges(c, t, lo+1+half(sat(span, stringWidth(t))), r, clsText)
	}
}

// drawDivider draws a full-width rule labelling a `loop` / `alt` / `opt`
// block boundary.
func drawDivider(c *canvas, text string, r, canvasW int) {
	for x := 0; x < canvasW; x++ {
		c.set(x, r, "─", clsEdge)
	}
	drawTextOverEdges(c, " "+fitLabel(text, sat(canvasW, 4))+" ", 2, r, clsEdgeLabel)
}
