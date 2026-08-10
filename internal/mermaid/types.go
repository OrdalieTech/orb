package mermaid

// Semantic classes of a run of cells. The renderer never knows about colour;
// consumers map these to their own theme.
//
//	border     box outlines, subgraph frames, compartment rules
//	text       node / participant / compartment labels
//	edge       connector lines and arrowheads
//	edgeLabel  text sitting on an edge
//	title      the `mermaid: <kind>` header of a source box
//	none       blank filler
const (
	clsBorder    = "border"
	clsText      = "text"
	clsEdge      = "edge"
	clsEdgeLabel = "edgeLabel"
	clsTitle     = "title"
	clsNone      = "none"
)

// Span is a run of adjacent cells sharing one semantic class.
type Span struct {
	Cls  string
	Text string
}

// Art is a rendered diagram. Plain[i] and Styled[i] describe the same row:
// Plain is right-trimmed for display width and copy/paste, Styled keeps the
// run structure needed to colour it.
//
// Width is the display columns the widest row needs — the number to compare
// against the space you have. It cannot be recovered from Plain, whose rows
// are strings of code points, not columns.
//
// Warnings lists source the flowchart grammar could not read and dropped.
// Non-empty means the art is real but incomplete. Only flowcharts warn; the
// other grammars refuse the whole diagram instead, and Render returns nil.
// They are advisory: do not gate rendering on them.
type Art struct {
	Plain    []string
	Styled   [][]Span
	Width    int
	Warnings []string
}
