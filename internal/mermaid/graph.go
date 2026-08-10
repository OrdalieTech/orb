package mermaid

// The shared diagram model. Flowchart, state, class and ER sources all parse
// into a graph; only sequence diagrams have their own model.

// Caps that keep layout bounded; exceeding one drops the diagram to fallback.
const (
	maxNodes      = 128
	maxEdges      = 512
	maxGroups     = 24
	maxGroupDepth = 6
	// Class members / ER attributes listed per box before eliding with `…`.
	maxMembers = 8
)

const (
	shapeRect    = "rect"
	shapeRound   = "round"
	shapeDiamond = "diamond"
)

// Decoration at one end of an edge.
const (
	headNone        = "none"
	headArrow       = "arrow"
	headCircle      = "circle"
	headCross       = "cross"
	headTriangle    = "triangle"
	headDiamondFill = "diamondFill"
	headDiamondOpen = "diamondOpen"
)

const (
	lineSolid  = "solid"
	lineDotted = "dotted"
	lineThick  = "thick"
)

const (
	dirDown  = "down"
	dirUp    = "up"
	dirRight = "right"
	dirLeft  = "left"
)

type node struct {
	label string
	shape string
}

// edge's label is "" when absent: every parser routes labels through nonEmpty,
// so an empty label string never reaches the model (upstream uses null).
type edge struct {
	from     int
	to       int
	label    string
	headTo   string
	headFrom string
	line     string
}

// group's parent is -1 at top level (upstream uses null).
type group struct {
	id     string
	label  string
	parent int
}

// classInfo is extra compartment content for class and ER boxes. An empty
// annotation is meaningful (`«»`), so presence is tracked separately.
type classInfo struct {
	annotation    string
	hasAnnotation bool
	attrs         []string
	methods       []string
}

// parseDir maps `LR`/`RL`/`BT` as written in a header or `direction`
// statement; anything else is down.
func parseDir(token string) string {
	switch asciiUpper(token) {
	case "LR":
		return dirRight
	case "RL":
		return dirLeft
	case "BT":
		return dirUp
	default:
		return dirDown
	}
}

type graph struct {
	nodes  []node
	edges  []edge
	index  map[string]int
	groups []group
	// Innermost subgraph each node was declared in, parallel to nodes; -1 none.
	nodeGroup []int
	curGroup  int
	// overCap is set when a cap was hit; the caller abandons the parse.
	overCap bool
	// warnings lists text the flowchart grammar could not read and silently
	// discarded. Flowchart parsing is deliberately lenient — a malformed
	// statement contributes whatever prefix parsed and the rest is dropped —
	// so without these the reader gets a clean diagram that is not what they
	// wrote.
	warnings []string
	dir      string
}

func newGraph(dir string) *graph {
	return &graph{index: map[string]int{}, curGroup: -1, dir: dir}
}

// nodeIndex returns the index of id, creating the node if new. A later
// declaration carrying a label overwrites the placeholder one an edge
// created. Returns -1 once maxNodes is reached, which aborts the parse.
func (g *graph) nodeIndex(id string, label string, hasLabel bool, shape string) int {
	if existing, ok := g.index[id]; ok {
		if hasLabel {
			g.nodes[existing].label = label
			g.nodes[existing].shape = shape
		}
		return existing
	}
	if len(g.nodes) >= maxNodes {
		g.overCap = true
		return -1
	}
	g.index[id] = len(g.nodes)
	if !hasLabel {
		label = id
	}
	g.nodes = append(g.nodes, node{label: label, shape: shape})
	g.nodeGroup = append(g.nodeGroup, g.curGroup)
	return len(g.nodes) - 1
}

// nodeLabel sets a node's label without disturbing its shape, creating it if
// new.
func (g *graph) nodeLabel(id, label string) int {
	if existing, ok := g.index[id]; ok {
		g.nodes[existing].label = label
		return existing
	}
	return g.nodeIndex(id, label, true, shapeRound)
}

// pushEdge appends an edge, or flags overCap when maxEdges is reached.
func (g *graph) pushEdge(e edge) bool {
	if len(g.edges) >= maxEdges {
		g.overCap = true
		return false
	}
	g.edges = append(g.edges, e)
	return true
}
