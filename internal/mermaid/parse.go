package mermaid

// Source text to diagram model.
//
// Every parseX returns nil when the source is not that kind of diagram, or
// when it exceeds a cap — Render tries each in turn and gives up when they
// all decline.

import (
	"strconv"
	"strings"
)

// ---------------------------------------------------------------- statements

func flushStatement(cur string, out []string) []string {
	if trimmed := jsTrim(cur); trimmed != "" {
		out = append(out, trimmed)
	}
	return out
}

// splitStatements splits one source line into statements on `;`, stopping at
// a `%%` comment. Quoted spans are opaque, so a label may contain `;` and `%%`.
func splitStatements(line string, out []string) []string {
	chars := []rune(line)
	var cur strings.Builder
	inQuotes := false
	for i := 0; i < len(chars); i++ {
		c := chars[i]
		switch {
		case inQuotes:
			if c == '"' {
				inQuotes = false
			}
			cur.WriteRune(c)
		case c == '"':
			inQuotes = true
			cur.WriteRune(c)
		case c == '%' && i+1 < len(chars) && chars[i+1] == '%':
			return flushStatement(cur.String(), out)
		case c == ';':
			out = flushStatement(cur.String(), out)
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}
	return flushStatement(cur.String(), out)
}

// statementsOf lists all statements in a source block, in order.
func statementsOf(src string) []string {
	var out []string
	for _, line := range srcLines(src) {
		out = splitStatements(line, out)
	}
	return out
}

func firstWord(s string) string {
	if fields := jsFields(s); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// splitOnce splits on the first occurrence of sep, Rust's split_once.
func splitOnce(s, sep string) (string, string, bool) {
	if i := strings.Index(s, sep); i != -1 {
		return s[:i], s[i+len(sep):], true
	}
	return "", "", false
}

// headerKind is the diagram kind from the header statement, lowercased.
func headerKind(statements []string) string {
	if len(statements) == 0 {
		return ""
	}
	return asciiLower(firstWord(statements[0]))
}

// diagramKind is the kind of diagram src declares
// ("flowchart" | "state" | "class" | "er" | "sequence"), or "" if its header
// names no type this renderer draws. Reads the header only — it says nothing
// about whether the body parses. Each branch mirrors the header test in the
// matching parseX, so the two always agree on what they recognise.
func diagramKind(src string) string {
	kind := headerKind(statementsOf(src))
	switch {
	case kind == "graph" || kind == "flowchart":
		return "flowchart"
	case strings.HasPrefix(kind, "statediagram"):
		return "state"
	case strings.HasPrefix(kind, "classdiagram"):
		return "class"
	case kind == "erdiagram":
		return "er"
	case kind == "sequencediagram":
		return "sequence"
	}
	return ""
}

// ----------------------------------------------------------------- flowchart

func parseGraph(src string) *graph {
	statements := statementsOf(src)
	kind := headerKind(statements)
	if kind != "graph" && kind != "flowchart" {
		return nil
	}

	headerWords := jsFields(statements[0])
	dir := "TB"
	if len(headerWords) > 1 {
		dir = headerWords[1]
	}
	g := newGraph(parseDir(dir))
	var stack []int

	for _, st := range statements[1:] {
		switch asciiLower(firstWord(st)) {
		case "subgraph":
			if len(g.groups) >= maxGroups || len(stack) >= maxGroupDepth {
				return nil
			}
			id, label := parseSubgraphDecl(jsTrim(st[len("subgraph"):]))
			parent := -1
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			g.groups = append(g.groups, group{id: id, label: label, parent: parent})
			stack = append(stack, len(g.groups)-1)
			g.curGroup = stack[len(stack)-1]
			continue
		case "end":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			g.curGroup = -1
			if len(stack) > 0 {
				g.curGroup = stack[len(stack)-1]
			}
			continue
		case "classdef", "class", "style", "linkstyle", "click", "direction":
			continue
		}
		parseStatement(st, g)
		if g.overCap {
			return nil
		}
	}

	if len(g.nodes) == 0 {
		return nil
	}
	return g
}

// parseSubgraphDecl reads `subgraph id[Title]`, `subgraph "Title"`, or a bare
// title.
func parseSubgraphDecl(rest string) (id, label string) {
	if strings.HasPrefix(rest, `"`) {
		if close := strings.Index(rest[1:], `"`); close != -1 {
			quoted := rest[1 : 1+close]
			return quoted, decodeHtmlEntities(quoted)
		}
	}
	if open := strings.Index(rest, "["); open != -1 {
		id := jsTrim(rest[:open])
		label := cleanLabel(jsTrim(strings.TrimRight(rest[open+1:], "]")))
		if id != "" && label != "" {
			return id, label
		}
	}
	return rest, rest
}

// parseStatement reads a chain of `node link node link node ...`, each link
// fanning out over `&`.
//
// Parses as far as it can and keeps the prefix, matching upstream and
// mermaid.js. Whatever it could not read is recorded in g.warnings rather
// than failing the diagram — see the note on that field.
func parseStatement(st string, g *graph) {
	chars := []rune(st)
	i := 0

	prev, i, ok := parseNodeGroup(chars, i, g)
	if !ok {
		g.warnings = append(g.warnings, `dropped, does not start with a node: "`+st+`"`)
		return
	}

	for {
		i = skipSpaces(chars, i)
		if i >= len(chars) {
			break
		}
		link, ok := parseLink(chars, i)
		if !ok {
			g.warnings = append(g.warnings, `dropped, expected a link: "`+string(chars[i:])+`"`)
			break
		}
		i = skipSpaces(chars, link.next)
		target, next, ok := parseNodeGroup(chars, i, g)
		if !ok {
			g.warnings = append(g.warnings, `dropped, link has no target: "`+st+`"`)
			break
		}
		i = next
		for _, f := range prev {
			for _, t := range target {
				// `A <-- B` reads right-to-left: swap the endpoints so the
				// arrow that was written on the left becomes a normal forward
				// head.
				reversed := link.left == headArrow && link.right != headArrow
				e := edge{
					from: f, to: t,
					label: link.label, headTo: link.right, headFrom: link.left,
					line: link.line,
				}
				if reversed {
					e.from, e.to = t, f
					e.headTo, e.headFrom = headArrow, link.right
				}
				if !g.pushEdge(e) {
					return
				}
			}
		}
		prev = target
	}
}

// parseNodeGroup reads one or more nodes joined by `&`, which fan out into a
// cross product.
func parseNodeGroup(chars []rune, start int, g *graph) (nodes []int, next int, ok bool) {
	first, i, ok := parseNode(chars, start, g)
	if !ok {
		return nil, 0, false
	}
	nodes = []int{first}
	for {
		j := skipSpaces(chars, i)
		if j >= len(chars) || chars[j] != '&' {
			break
		}
		idx, after, ok := parseNode(chars, j+1, g)
		if !ok {
			return nil, 0, false
		}
		nodes = append(nodes, idx)
		i = after
	}
	return nodes, i, true
}

func skipSpaces(chars []rune, i int) int {
	for i < len(chars) && (chars[i] == ' ' || chars[i] == '\t') {
		i++
	}
	return i
}

func parseNode(chars []rune, start int, g *graph) (index, next int, ok bool) {
	i := skipSpaces(chars, start)
	idStart := i
	for i < len(chars) && isIdChar(chars[i]) {
		i++
	}
	if i == idStart {
		return 0, 0, false
	}
	id := string(chars[idStart:i])

	sh := readShapeAt(chars, i)
	if sh.unclosed != "" {
		g.warnings = append(g.warnings, `node "`+id+"\": label is missing its closing `"+sh.unclosed+"`")
	}
	index = g.nodeIndex(id, sh.label, sh.hasLabel, sh.shape)
	if index == -1 {
		return 0, 0, false
	}
	return index, sh.after, true
}

// shaped is what a shape bracket yielded. unclosed is the closing token that
// was expected but never found ("" when closed properly or no bracket).
type shaped struct {
	shape    string
	label    string
	hasLabel bool
	after    int
	unclosed string
}

func runeAt(chars []rune, i int) rune {
	if i < len(chars) {
		return chars[i]
	}
	return 0
}

// readShapeAt dispatches on the bracket following an id to pick shape and
// closing token.
func readShapeAt(chars []rune, i int) shaped {
	c := runeAt(chars, i)
	n := runeAt(chars, i+1)
	switch c {
	case '[':
		if n == '[' {
			return readShape(chars, i+2, "]]", shapeRect)
		}
		if n == '(' {
			return readShape(chars, i+2, ")]", shapeRound)
		}
		return readShape(chars, i+1, "]", shapeRect)
	case '(':
		if n == '(' {
			return readShape(chars, i+2, "))", shapeRound)
		}
		if n == '[' {
			return readShape(chars, i+2, "])", shapeRound)
		}
		return readShape(chars, i+1, ")", shapeRound)
	case '{':
		if n == '{' {
			return readShape(chars, i+2, "}}", shapeDiamond)
		}
		return readShape(chars, i+1, "}", shapeDiamond)
	case '>':
		return readShape(chars, i+1, "]", shapeRect)
	}
	return shaped{shape: shapeRect, after: i}
}

// readShape reads label text up to closer.
//
// Quoting is decided by the first non-space character: inside a quoted label
// the closer is ignored until the quote closes, so `A["a] b"]` is one node.
// An unquoted label ends at the first closer, so `A[5" pipe]` keeps its quote.
func readShape(chars []rune, start int, closer, shape string) shaped {
	j := start
	for j < len(chars) && (chars[j] == ' ' || chars[j] == '\t') {
		j++
	}
	quoted := runeAt(chars, j) == '"'

	closerRunes := []rune(closer)
	i := start
	var text strings.Builder
	inQuotes := false
	for i < len(chars) {
		c := chars[i]
		if quoted && c == '"' {
			inQuotes = !inQuotes
			text.WriteRune(c)
			i++
			continue
		}
		if !inQuotes && i+len(closerRunes) <= len(chars) && string(chars[i:i+len(closerRunes)]) == closer {
			return shaped{shape: shape, label: cleanLabel(text.String()), hasLabel: true, after: i + len(closerRunes)}
		}
		text.WriteRune(c)
		i++
	}
	// Ran off the end still looking for the closer: everything after the
	// opening bracket became label text, so any link operator in it was
	// swallowed.
	return shaped{shape: shape, label: cleanLabel(text.String()), hasLabel: true, after: len(chars), unclosed: closer}
}

func isLinkChar(c rune) bool {
	return c == '-' || c == '.' || c == '=' || c == '<' || c == '>'
}

type link struct {
	left  string
	right string
	line  string
	label string
	next  int
}

// parseLink reads a link operator and its label.
//
// Labels come in two forms: `-->|text|` and the inline `-- text -->`, the
// latter only when the first operator carried no head.
func parseLink(chars []rune, start int) (link, bool) {
	i := skipSpaces(chars, start)
	left := headNone
	// A leading `o`/`x` decorates the tail, but only directly before an
	// operator.
	if c := runeAt(chars, i); c == 'o' || c == 'x' {
		if n := runeAt(chars, i+1); n == '-' || n == '.' || n == '=' {
			left = headCross
			if c == 'o' {
				left = headCircle
			}
			i++
		}
	}

	opStart := i
	for i < len(chars) && isLinkChar(chars[i]) {
		i++
	}
	if i == opStart {
		return link{}, false
	}
	op1 := string(chars[opStart:i])
	if left == headNone && strings.HasPrefix(op1, "<") {
		left = headArrow
	}

	line := lineKind(op1)
	right := headNone
	if strings.Contains(op1, ">") {
		right = headArrow
	}
	if right == headNone {
		if head, next, ok := trailingHead(chars, i); ok {
			right = head
			i = next
		}
	}

	if runeAt(chars, i) == '|' {
		i++
		lStart := i
		for i < len(chars) && chars[i] != '|' {
			i++
		}
		label := cleanLabel(string(chars[lStart:i]))
		if i < len(chars) && chars[i] == '|' {
			i++
		}
		return link{left: left, right: right, line: line, label: label, next: i}, true
	}

	if right == headNone {
		textStart := skipSpaces(chars, i)
		j := textStart
		for j < len(chars) && !isLinkChar(chars[j]) {
			j++
		}
		if j < len(chars) && j > textStart && chars[j] != '<' {
			text := string(chars[textStart:j])
			op2Start := j
			for j < len(chars) && isLinkChar(chars[j]) {
				j++
			}
			op2 := string(chars[op2Start:j])
			if strings.Contains(op2, ">") {
				right = headArrow
			} else if head, next, ok := trailingHead(chars, j); ok {
				right = head
				j = next
			}
			if line == lineSolid {
				line = lineKind(op2)
			}
			return link{left: left, right: right, line: line, label: cleanLabel(text), next: j}, true
		}
	}

	return link{left: left, right: right, line: line, next: i}, true
}

func lineKind(op string) string {
	if strings.Contains(op, "=") {
		return lineThick
	}
	if strings.Contains(op, ".") {
		return lineDotted
	}
	return lineSolid
}

// trailingHead reads a trailing `o`/`x` head, only when followed by a
// statement boundary.
func trailingHead(chars []rune, i int) (head string, next int, ok bool) {
	switch runeAt(chars, i) {
	case 'o':
		head = headCircle
	case 'x':
		head = headCross
	default:
		return "", 0, false
	}
	if i+1 < len(chars) {
		switch chars[i+1] {
		case ' ', '\t', '|', '&', ';':
		default:
			return "", 0, false
		}
	}
	return head, i + 1, true
}

// --------------------------------------------------------------------- state

func parseState(src string) *graph {
	statements := statementsOf(src)
	kind := headerKind(statements)
	if !strings.HasPrefix(kind, "statediagram") {
		return nil
	}

	g := newGraph(dirDown)
	inNote := false

	for _, st := range statements[1:] {
		if inNote {
			if asciiLower(st) == "end note" {
				inNote = false
			}
			continue
		}
		first := asciiLower(firstWord(st))
		switch {
		case first == "direction":
			dir := ""
			if fields := jsFields(st); len(fields) > 1 {
				dir = fields[1]
			}
			g.dir = parseDir(dir)
		case first == "note":
			// A single-line `note ... : text` needs no terminator.
			if !strings.Contains(st, ":") {
				inNote = true
			}
		case first == "state":
			if !parseStateDecl(st, g) {
				return nil
			}
		case first == "classdef" || first == "class" || first == "hide" ||
			first == "scale" || first == "}" || first == "--":
			// Styling and composite-state punctuation carry no layout meaning.
		case strings.Contains(st, "-->"):
			if !parseTransition(st, g) {
				return nil
			}
		default:
			if !parseStateDesc(st, g) {
				return nil
			}
		}
		if g.overCap {
			return nil
		}
	}

	if len(g.nodes) == 0 {
		return nil
	}
	return g
}

// parseStateDecl reads `state "Label" as id`, `state id <<choice>>`, or
// `state id {`.
func parseStateDecl(st string, g *graph) bool {
	rest := jsTrim(strings.TrimSuffix(jsTrim(st[len("state"):]), "{"))
	if rest == "" {
		return true
	}

	if strings.HasPrefix(rest, `"`) {
		close := strings.Index(rest[1:], `"`)
		if close == -1 {
			return false
		}
		label := rest[1 : 1+close]
		after := jsTrim(rest[1+close+1:])
		id := label
		if strings.HasPrefix(after, "as") {
			id = jsTrim(after[2:])
		}
		return g.nodeLabel(id, decodeHtmlEntities(label)) != -1
	}

	shape := shapeRound
	id := rest
	stereotyped := false
	if pos := strings.Index(rest, "<<"); pos != -1 {
		stereo := jsTrim(strings.TrimSuffix(rest[pos+2:], ">>"))
		if stereo == "choice" {
			shape = shapeDiamond
		}
		id = jsTrim(rest[:pos])
		stereotyped = true
	}
	if id == "" || hasJSSpace(id) {
		return false
	}
	return g.nodeIndex(id, id, stereotyped, shape) != -1
}

// parseTransition reads `A --> B: label`, including chains `A --> B --> C`.
func parseTransition(st string, g *graph) bool {
	rest := st
	prev := -1

	for {
		lhs, rhs, found := splitOnce(rest, "-->")
		if !found {
			break
		}

		fromID := jsTrim(strings.TrimRight(jsTrimEnd(lhs), "-"))
		var from int
		if prev != -1 {
			// Mid-chain: the source is the previous target, so nothing may
			// precede.
			if fromID != "" {
				return false
			}
			from = prev
		} else {
			if fromID == "" {
				return false
			}
			from = stateEndpoint(g, fromID, true)
			if from == -1 {
				return false
			}
		}

		nextArrow := strings.Index(rhs, "-->")
		toPartRaw, tail := rhs, ""
		if nextArrow != -1 {
			toPartRaw, tail = rhs[:nextArrow], rhs[nextArrow:]
		}

		toPart := toPartRaw
		label := ""
		if before, after, ok := splitOnce(toPartRaw, ":"); ok {
			toPart = before
			label = decodeHtmlEntities(jsTrim(after))
		}

		toID := jsTrim(strings.TrimRight(jsTrimEnd(strings.TrimLeft(jsTrimStart(toPart), ">")), "-"))
		if toID == "" {
			return false
		}
		to := stateEndpoint(g, toID, false)
		if to == -1 {
			return false
		}

		if !g.pushEdge(edge{from: from, to: to, label: label, headTo: headArrow, headFrom: headNone, line: lineSolid}) {
			return true
		}
		prev = to
		rest = tail
	}
	return true
}

// stateEndpoint: `[*]` is start or end depending on which side of the arrow
// it sits.
func stateEndpoint(g *graph, id string, isSource bool) int {
	if id == "[*]" {
		key := "[*]end"
		if isSource {
			key = "[*]start"
		}
		return g.nodeIndex(key, "●", true, shapeRound)
	}
	return g.nodeIndex(id, "", false, shapeRound)
}

// parseStateDesc reads `id: description`, or a bare state name.
func parseStateDesc(st string, g *graph) bool {
	if before, after, ok := splitOnce(st, ":"); ok {
		id := jsTrim(before)
		desc := jsTrim(after)
		if id == "" || hasJSSpace(id) || desc == "" {
			return false
		}
		return g.nodeLabel(id, decodeHtmlEntities(desc)) != -1
	}
	if hasJSSpace(st) {
		return false
	}
	return g.nodeIndex(st, "", false, shapeRound) != -1
}

// --------------------------------------------------------------------- class

type classOp struct {
	op       string
	headFrom string
	headTo   string
	line     string
}

// classOps are relation operators, longest-first so `--|>` wins over `--`.
var classOps = []classOp{
	{"<|--", headTriangle, headNone, lineSolid},
	{"--|>", headNone, headTriangle, lineSolid},
	{"<|..", headTriangle, headNone, lineDotted},
	{"..|>", headNone, headTriangle, lineDotted},
	{"*--", headDiamondFill, headNone, lineSolid},
	{"--*", headNone, headDiamondFill, lineSolid},
	{"o--", headDiamondOpen, headNone, lineSolid},
	{"--o", headNone, headDiamondOpen, lineSolid},
	{"<--", headArrow, headNone, lineSolid},
	{"-->", headNone, headArrow, lineSolid},
	{"<..", headArrow, headNone, lineDotted},
	{"..>", headNone, headArrow, lineDotted},
	{"--", headNone, headNone, lineSolid},
	{"..", headNone, headNone, lineDotted},
}

const maxClassOp = 4

func parseClass(src string) (*graph, []classInfo) {
	statements := statementsOf(src)
	kind := headerKind(statements)
	if !strings.HasPrefix(kind, "classdiagram") {
		return nil, nil
	}

	g := newGraph(dirDown)
	var infos []classInfo
	sync := func() {
		for len(infos) < len(g.nodes) {
			infos = append(infos, classInfo{})
		}
	}
	// declare a class, keeping infos aligned with g.nodes.
	declare := func(name string) int {
		idx := g.nodeIndex(name, "", false, shapeRect)
		sync()
		return idx
	}
	curClass := -1

	for _, st := range statements[1:] {
		if curClass != -1 {
			if st == "}" {
				curClass = -1
			} else {
				pushMember(&infos[curClass], st)
			}
			continue
		}

		first := asciiLower(firstWord(st))
		if first == "direction" {
			dir := ""
			if fields := jsFields(st); len(fields) > 1 {
				dir = fields[1]
			}
			g.dir = parseDir(dir)
			continue
		}
		switch first {
		case "note", "callback", "click", "link", "style", "cssclass", "classdef", "namespace", "}":
			continue
		}
		if first == "class" {
			rest := jsTrim(st[len("class"):])
			open := strings.HasSuffix(rest, "{")
			name := rest
			if open {
				name = jsTrim(rest[:len(rest)-1])
			}
			if name == "" || hasJSSpace(name) {
				return nil, nil
			}
			idx := declare(name)
			if idx == -1 {
				return nil, nil
			}
			if open {
				curClass = idx
			}
			continue
		}

		if strings.HasPrefix(st, "<<") {
			annotation, rest, ok := splitOnce(st[2:], ">>")
			if !ok {
				return nil, nil
			}
			name := jsTrim(rest)
			if name == "" || hasJSSpace(name) {
				return nil, nil
			}
			idx := declare(name)
			if idx == -1 {
				return nil, nil
			}
			infos[idx].annotation = jsTrim(annotation)
			infos[idx].hasAnnotation = true
			continue
		}

		if rel, ok := parseClassRelation(st); ok {
			f := declare(rel.from)
			if f == -1 {
				return nil, nil
			}
			t := declare(rel.to)
			if t == -1 {
				return nil, nil
			}
			if len(g.edges) >= maxEdges {
				return nil, nil
			}
			g.edges = append(g.edges, edge{
				from: f, to: t,
				label: rel.label, headTo: rel.headTo, headFrom: rel.headFrom,
				line: rel.line,
			})
			continue
		}

		if before, after, ok := splitOnce(st, ":"); ok {
			id := jsTrim(before)
			text := jsTrim(after)
			if id == "" || hasJSSpace(id) || text == "" {
				return nil, nil
			}
			idx := declare(id)
			if idx == -1 {
				return nil, nil
			}
			pushMember(&infos[idx], text)
			continue
		}
		return nil, nil
	}

	if len(g.nodes) == 0 {
		return nil, nil
	}
	sync()
	return g, infos
}

// pushMember adds a member to the attribute or method compartment, eliding
// past the cap.
func pushMember(info *classInfo, raw string) {
	if strings.HasPrefix(raw, "<<") {
		if annotation, _, ok := splitOnce(raw[2:], ">>"); ok {
			info.annotation = jsTrim(annotation)
			info.hasAnnotation = true
		}
		return
	}
	member := decodeHtmlEntities(displayGenerics(jsTrim(raw)))
	list := &info.attrs
	if strings.Contains(member, "(") {
		list = &info.methods
	}
	if len(*list) < maxMembers {
		*list = append(*list, member)
	} else if len(*list) == maxMembers {
		*list = append(*list, "…")
	}
}

type classRelation struct {
	from     string
	to       string
	headFrom string
	headTo   string
	line     string
	label    string
}

func parseClassRelation(st string) (classRelation, bool) {
	chars := []rune(st)
	foundPos := -1
	var found classOp

search:
	for pos := 0; pos < len(chars); pos++ {
		tail := string(chars[pos:min(pos+maxClassOp, len(chars))])
		for _, op := range classOps {
			if !strings.HasPrefix(tail, op.op) {
				continue
			}
			// `o` is also an identifier character: skip a match glued to a
			// name.
			if strings.HasPrefix(op.op, "o") && pos > 0 && isIdChar(chars[pos-1]) {
				continue
			}
			if strings.HasSuffix(op.op, "o") {
				if after := runeAt(chars, pos+len(op.op)); after != 0 && isIdChar(after) {
					continue
				}
			}
			foundPos = pos
			found = op
			break search
		}
	}
	if foundPos == -1 {
		return classRelation{}, false
	}

	lhsRaw := jsTrim(string(chars[:foundPos]))
	rhsRaw := jsTrim(string(chars[foundPos+len(found.op):]))

	lhs, cardFrom := stripCardinalitySuffix(lhsRaw)
	rhs, cardTo := stripCardinalityPrefix(rhsRaw)

	toID := rhs
	relLabel := ""
	if before, after, ok := splitOnce(rhs, ":"); ok {
		toID = before
		relLabel = decodeHtmlEntities(jsTrim(after))
	}
	toID = jsTrim(toID)

	if lhs == "" || toID == "" || hasJSSpace(lhs) || hasJSSpace(toID) {
		return classRelation{}, false
	}

	label := joinNonEmpty(cardFrom, relLabel, cardTo)
	return classRelation{
		from: lhs, to: toID,
		headFrom: found.headFrom, headTo: found.headTo, line: found.line,
		label: label,
	}, true
}

func joinNonEmpty(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}

// stripCardinalitySuffix reads `Class "1"` — a quoted cardinality trailing
// the left-hand name.
func stripCardinalitySuffix(s string) (string, string) {
	t := jsTrimEnd(s)
	if strings.HasSuffix(t, `"`) {
		rest := t[:len(t)-1]
		if q := strings.LastIndex(rest, `"`); q != -1 {
			return jsTrimEnd(rest[:q]), rest[q+1:]
		}
	}
	return t, ""
}

// stripCardinalityPrefix reads `"0..*" Class` — a quoted cardinality leading
// the right-hand name.
func stripCardinalityPrefix(s string) (string, string) {
	t := jsTrimStart(s)
	if strings.HasPrefix(t, `"`) {
		rest := t[1:]
		if q := strings.Index(rest, `"`); q != -1 {
			return jsTrimStart(rest[q+1:]), rest[:q]
		}
	}
	return t, ""
}

// displayGenerics: Mermaid writes generics as `List~T~`; show them as
// `List<T>`.
func displayGenerics(s string) string {
	var out strings.Builder
	open := false
	for _, c := range s {
		if c == '~' {
			if open {
				out.WriteByte('>')
			} else {
				out.WriteByte('<')
			}
			open = !open
		} else {
			out.WriteRune(c)
		}
	}
	return out.String()
}

// ------------------------------------------------------------------------ ER

func parseEr(src string) (*graph, []classInfo) {
	statements := statementsOf(src)
	if headerKind(statements) != "erdiagram" {
		return nil, nil
	}

	g := newGraph(dirDown)
	var infos []classInfo
	curEntity := -1

	for _, st := range statements[1:] {
		if curEntity != -1 {
			if st == "}" {
				curEntity = -1
			} else {
				pushErAttribute(&infos[curEntity], st)
			}
			continue
		}

		if rel, label, ok := splitErRelationship(st); ok {
			tokens := jsFields(rel)
			if len(tokens) != 3 {
				return nil, nil
			}
			op, ok := parseErOp(tokens[1])
			if !ok {
				return nil, nil
			}
			f := erEntity(g, &infos, tokens[0])
			if f == -1 {
				return nil, nil
			}
			t := erEntity(g, &infos, tokens[2])
			if t == -1 {
				return nil, nil
			}
			if len(g.edges) >= maxEdges {
				return nil, nil
			}
			relLabel := ""
			if label.set {
				relLabel = cleanLabel(label.text)
			}
			g.edges = append(g.edges, edge{
				from: f, to: t,
				label:  joinNonEmpty(op.cardL, relLabel, op.cardR),
				headTo: headNone, headFrom: headNone, line: op.line,
			})
			continue
		}

		open := strings.HasSuffix(st, "{")
		decl := st
		if open {
			decl = jsTrim(st[:len(st)-1])
		}
		if decl == "" || len(jsFields(decl)) != 1 {
			return nil, nil
		}
		idx := erEntity(g, &infos, decl)
		if idx == -1 {
			return nil, nil
		}
		if open {
			curEntity = idx
		}
	}

	if len(g.nodes) == 0 {
		return nil, nil
	}
	for len(infos) < len(g.nodes) {
		infos = append(infos, classInfo{})
	}
	return g, infos
}

func erEntity(g *graph, infos *[]classInfo, token string) int {
	open := strings.Index(token, "[")
	var idx int
	if open != -1 {
		id := token[:open]
		label := cleanLabel(strings.TrimRight(token[open+1:], "]"))
		if id == "" || label == "" {
			return -1
		}
		idx = g.nodeLabel(id, label)
	} else {
		idx = g.nodeIndex(token, "", false, shapeRect)
	}
	if idx == -1 {
		return -1
	}
	for len(*infos) < len(g.nodes) {
		*infos = append(*infos, classInfo{})
	}
	return idx
}

// optText distinguishes an absent relationship label from an empty one.
type optText struct {
	text string
	set  bool
}

func splitErRelationship(st string) (string, optText, bool) {
	rel := st
	label := optText{}
	if before, after, ok := splitOnce(st, ":"); ok {
		rel = before
		label = optText{text: jsTrim(after), set: true}
	}
	for _, t := range jsFields(rel) {
		if _, ok := parseErOp(t); ok {
			return rel, label, true
		}
	}
	return "", optText{}, false
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

type erOp struct {
	cardL string
	cardR string
	line  string
}

// parseErOp reads a crow's-foot operator: two cardinality glyphs around `--`
// or `..`.
func parseErOp(tok string) (erOp, bool) {
	if len(tok) != 6 || !isASCII(tok) {
		return erOp{}, false
	}
	var line string
	switch tok[2:4] {
	case "--":
		line = lineSolid
	case "..":
		line = lineDotted
	default:
		return erOp{}, false
	}
	cardL, okL := erCard(tok[0:2])
	cardR, okR := erCard(tok[4:6])
	if !okL || !okR {
		return erOp{}, false
	}
	return erOp{cardL: cardL, cardR: cardR, line: line}, true
}

func erCard(tok string) (string, bool) {
	switch tok {
	case "|o", "o|":
		return "0..1", true
	case "||":
		return "1", true
	case "}o", "o{":
		return "0..*", true
	case "}|", "|{":
		return "1..*", true
	}
	return "", false
}

// pushErAttribute: ER attributes are `type name`; a trailing quoted comment
// is dropped.
func pushErAttribute(info *classInfo, raw string) {
	var parts []string
	for _, tok := range jsFields(raw) {
		if strings.HasPrefix(tok, `"`) {
			break
		}
		parts = append(parts, tok)
	}
	if len(parts) == 0 {
		return
	}
	line := decodeHtmlEntities(strings.Join(parts, " "))
	if len(info.attrs) < maxMembers {
		info.attrs = append(info.attrs, line)
	} else if len(info.attrs) == maxMembers {
		info.attrs = append(info.attrs, "…")
	}
}

// ------------------------------------------------------------------ sequence

type seqOp struct {
	op     string
	dashed bool
	head   string // "arrow" | "cross"
}

// seqOps are message operators, longest-first so `-->>` wins over `-->`.
var seqOps = []seqOp{
	{"-->>", true, headArrow},
	{"->>", false, headArrow},
	{"--x", true, headCross},
	{"-x", false, headCross},
	{"--)", true, headArrow},
	{"-)", false, headArrow},
	{"-->", true, headArrow},
	{"->", false, headArrow},
}

const maxSeqOp = 4

type noteAnchor struct {
	kind string // "over" | "left" | "right"
	from int    // over
	to   int    // over
	at   int    // left / right
}

// seqItem's text is "" for an absent message text (upstream null): message
// texts route through nonEmpty, and autonumber keeps them non-empty.
type seqItem struct {
	kind   string // "message" | "note" | "divider"
	from   int
	to     int
	text   string
	dashed bool
	head   string
	anchor noteAnchor
}

type sequence struct {
	labels []string
	index  map[string]int
	items  []seqItem
}

func (s *sequence) participant(id string, label string, hasLabel bool) int {
	if existing, ok := s.index[id]; ok {
		if hasLabel {
			s.labels[existing] = label
		}
		return existing
	}
	if len(s.labels) >= maxNodes {
		return -1
	}
	s.index[id] = len(s.labels)
	if !hasLabel {
		label = id
	}
	s.labels = append(s.labels, label)
	return len(s.labels) - 1
}

func parseSequence(src string) *sequence {
	statements := statementsOf(src)
	if headerKind(statements) != "sequencediagram" {
		return nil
	}

	seq := &sequence{index: map[string]int{}}
	autonumber := false
	msgCount := 0
	// One entry per open block; true when it draws a divider on `end`.
	var blocks []bool

	for _, st := range statements[1:] {
		first := firstWord(st)
		lower := asciiLower(first)

		if lower == "participant" || lower == "actor" {
			rest := jsTrim(st[len(first):])
			if rest == "" {
				return nil
			}
			id, label, hasLabel := rest, "", false
			if before, after, ok := splitOnce(rest, " as "); ok {
				id, label, hasLabel = jsTrim(before), cleanLabel(after), true
			}
			if seq.participant(id, label, hasLabel) == -1 {
				return nil
			}
			continue
		}
		if lower == "autonumber" {
			autonumber = true
			continue
		}
		switch lower {
		case "activate", "deactivate", "create", "destroy", "title",
			"acctitle", "accdescr", "links", "link", "properties":
			continue
		}
		if lower == "note" {
			text, anchor, ok := parseNoteAnchor(jsTrim(st[len(first):]), seq)
			if !ok {
				return nil
			}
			if len(seq.items) >= maxEdges {
				return nil
			}
			seq.items = append(seq.items, seqItem{kind: "note", anchor: anchor, text: text})
			continue
		}
		switch lower {
		case "loop", "alt", "opt", "par", "critical", "break", "else", "and", "option":
			if lower == "else" || lower == "and" || lower == "option" {
				// A continuation only divides a block that opened one.
				if len(blocks) == 0 || !blocks[len(blocks)-1] {
					continue
				}
			} else {
				blocks = append(blocks, true)
			}
			if len(seq.items) >= maxEdges {
				return nil
			}
			seq.items = append(seq.items, seqItem{kind: "divider", text: decodeHtmlEntities(st)})
			continue
		case "rect", "box":
			blocks = append(blocks, false)
			continue
		case "end":
			divides := false
			if len(blocks) > 0 {
				divides = blocks[len(blocks)-1]
				blocks = blocks[:len(blocks)-1]
			}
			if divides {
				if len(seq.items) >= maxEdges {
					return nil
				}
				seq.items = append(seq.items, seqItem{kind: "divider", text: "end"})
			}
			continue
		}

		msg, ok := parseSeqMessage(st, seq)
		if !ok {
			return nil
		}
		text := msg.text
		if autonumber {
			msgCount++
			if text == "" {
				text = strconv.Itoa(msgCount) + "."
			} else {
				text = strconv.Itoa(msgCount) + ". " + text
			}
		}
		if len(seq.items) >= maxEdges {
			return nil
		}
		seq.items = append(seq.items, seqItem{
			kind: "message",
			from: msg.from, to: msg.to,
			text: text, dashed: msg.dashed, head: msg.head,
		})
	}

	if len(seq.labels) == 0 {
		return nil
	}
	return seq
}

func parseNoteAnchor(rest string, seq *sequence) (string, noteAnchor, bool) {
	lower := asciiLower(rest)
	var kind string
	var idsAndText string
	switch {
	case strings.HasPrefix(lower, "over "):
		kind = "over"
		idsAndText = rest[len("over "):]
	case strings.HasPrefix(lower, "left of "):
		kind = "left"
		idsAndText = rest[len("left of "):]
	case strings.HasPrefix(lower, "right of "):
		kind = "right"
		idsAndText = rest[len("right of "):]
	default:
		return "", noteAnchor{}, false
	}

	ids, textRaw, ok := splitOnce(idsAndText, ":")
	if !ok {
		return "", noteAnchor{}, false
	}
	text := decodeHtmlEntities(jsTrim(textRaw))
	var parts []string
	for _, s := range strings.Split(ids, ",") {
		if trimmed := jsTrim(s); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return "", noteAnchor{}, false
	}
	a := seq.participant(parts[0], "", false)
	if a == -1 {
		return "", noteAnchor{}, false
	}

	if kind != "over" {
		return text, noteAnchor{kind: kind, at: a}, true
	}
	b := a
	if len(parts) > 1 {
		second := seq.participant(parts[1], "", false)
		if second == -1 {
			return "", noteAnchor{}, false
		}
		b = second
	}
	return text, noteAnchor{kind: "over", from: min(a, b), to: max(a, b)}, true
}

type seqMessage struct {
	from   int
	to     int
	text   string
	dashed bool
	head   string
}

func parseSeqMessage(st string, seq *sequence) (seqMessage, bool) {
	chars := []rune(st)
	foundPos := -1
	var found seqOp
search:
	for pos := 0; pos < len(chars); pos++ {
		tail := string(chars[pos:min(pos+maxSeqOp, len(chars))])
		for _, op := range seqOps {
			if strings.HasPrefix(tail, op.op) {
				foundPos = pos
				found = op
				break search
			}
		}
	}
	if foundPos == -1 {
		return seqMessage{}, false
	}

	fromID := jsTrim(string(chars[:foundPos]))
	if fromID == "" {
		return seqMessage{}, false
	}
	// `+`/`-` activate and deactivate the target; they carry no layout
	// meaning.
	rest := strings.TrimLeft(jsTrimStart(string(chars[foundPos+len(found.op):])), "+-")

	toID := rest
	text := ""
	if before, after, ok := splitOnce(rest, ":"); ok {
		toID = before
		text = decodeHtmlEntities(jsTrim(after))
	}
	toID = jsTrim(toID)
	if toID == "" {
		return seqMessage{}, false
	}

	from := seq.participant(fromID, "", false)
	if from == -1 {
		return seqMessage{}, false
	}
	to := seq.participant(toID, "", false)
	if to == -1 {
		return seqMessage{}, false
	}
	return seqMessage{from: from, to: to, text: text, dashed: found.dashed, head: found.head}, true
}
