package mermaid

import "strings"

// Render renders a Mermaid source block as Unicode box-drawing art.
//
// Supported: `graph`/`flowchart` (including `subgraph`), `stateDiagram`,
// `classDiagram`, `erDiagram` and `sequenceDiagram`.
//
// The diagram is laid out at whatever size it needs; Art.Width reports the
// columns that turned out to be. Deciding what to do when that exceeds the
// space at hand is the caller's.
//
// nil means there is no art to show: blank input, a syntax error, a diagram
// type this renderer does not draw, or one large enough that laying it out is
// refused.
//
// Rendering is best-effort. A flowchart keeps whatever parsed; the stricter
// grammars additionally get one retry without their final line, which is what
// keeps a streaming diagram on screen while its last statement is half-typed.
// Everything given up on is listed in Art.Warnings — advisory only, never a
// reason to withhold the art.
func Render(src string) *Art {
	src = stripControls(src)
	if jsTrim(src) == "" {
		return nil
	}
	drawn := attempt(src)
	if drawn == nil {
		return nil
	}
	plain, styled, width := drawn.canvas.toLines()
	return &Art{Plain: plain, Styled: styled, Width: width, Warnings: drawn.warnings}
}

type drawn struct {
	canvas   *canvas
	warnings []string
}

// attempt draws src, retrying once without its last line if the grammar
// rejects it.
//
// State, class, ER and sequence fail a whole diagram on one unreadable
// statement, and while a source is streaming its last line is usually still
// being typed — so without this a diagram alternates with the fallback all
// the way in. Only the final line is dropped, and doing so is always
// reported, so a finished document with a bad last line still says what it
// lost rather than quietly rendering short.
func attempt(src string) *drawn {
	if d := draw(src); d != nil {
		return d
	}

	body := jsTrimEnd(src)
	cut := strings.LastIndexByte(body, '\n')
	if cut == -1 {
		return nil
	}
	salvaged := draw(body[:cut])
	if salvaged == nil {
		return nil
	}

	dropped := jsTrim(body[cut+1:])
	return &drawn{
		canvas:   salvaged.canvas,
		warnings: append(salvaged.warnings, `dropped, unreadable final line: "`+dropped+`"`),
	}
}

// draw dispatches on the declared diagram type; nil means nothing was drawn.
func draw(src string) *drawn {
	plain := func(c *canvas) *drawn {
		if c == nil {
			return nil
		}
		return &drawn{canvas: c}
	}

	switch diagramKind(src) {
	case "flowchart":
		g := parseGraph(src)
		if g == nil {
			return nil
		}
		var c *canvas
		if len(g.groups) == 0 {
			c = layoutFlowchart(g)
		} else {
			c = layoutGrouped(g)
		}
		if c == nil {
			return nil
		}
		return &drawn{canvas: c, warnings: g.warnings}
	case "state":
		g := parseState(src)
		if g == nil {
			return nil
		}
		return plain(layoutFlowchart(g))
	case "class":
		g, infos := parseClass(src)
		if g == nil {
			return nil
		}
		return plain(layoutClass(g, infos))
	case "er":
		g, infos := parseEr(src)
		if g == nil {
			return nil
		}
		return plain(layoutClass(g, infos))
	case "sequence":
		seq := parseSequence(src)
		if seq == nil {
			return nil
		}
		return plain(layoutSequence(seq))
	default:
		return nil
	}
}
