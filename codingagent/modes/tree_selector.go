package modes

import (
	"strings"

	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"
)

type treeGutter struct {
	position int
	show     bool
}

type treeVisualNode struct {
	node               *sessionstore.SessionTreeNode
	indent             int
	showConnector      bool
	isLast             bool
	gutters            []treeGutter
	isVirtualRootChild bool
}

func treeSelectItems(roots []*sessionstore.SessionTreeNode, leafID, filterMode string) []tui.SelectItem {
	byID := make(map[string]*sessionstore.SessionTreeNode)
	stack := appendReversedTreeNodes(nil, roots)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		byID[node.Entry.ID] = node
		stack = appendReversedTreeNodes(stack, node.Children)
	}

	active := make(map[string]bool)
	for id := leafID; id != ""; {
		active[id] = true
		node := byID[id]
		if node == nil || node.Entry.ParentID == nil {
			break
		}
		id = *node.Entry.ParentID
	}

	ordered := make([]*sessionstore.SessionTreeNode, 0, len(byID))
	stack = appendReversedTreeNodes(nil, prioritizeActiveTreeNodes(roots, active))
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		ordered = append(ordered, node)
		stack = appendReversedTreeNodes(stack, prioritizeActiveTreeNodes(node.Children, active))
	}

	visible := make(map[string]bool)
	for _, node := range ordered {
		visible[node.Entry.ID] = treeEntryVisible(node, node.Entry.ID == leafID, filterMode)
	}
	children := make(map[string][]*sessionstore.SessionTreeNode)
	for _, node := range ordered {
		if !visible[node.Entry.ID] {
			continue
		}
		parentID := nearestVisibleTreeParent(node, byID, visible)
		children[parentID] = append(children[parentID], node)
	}

	visibleRoots := children[""]
	multipleRoots := len(visibleRoots) > 1
	visuals := make([]treeVisualNode, 0, len(visible))
	visualStack := make([]treeVisualNode, 0, len(visible))
	for index := len(visibleRoots) - 1; index >= 0; index-- {
		visualStack = append(visualStack, treeVisualNode{
			node: visibleRoots[index], indent: boolInt(multipleRoots),
			showConnector: multipleRoots, isLast: index == len(visibleRoots)-1,
			isVirtualRootChild: multipleRoots,
		})
	}
	for len(visualStack) > 0 {
		item := visualStack[len(visualStack)-1]
		visualStack = visualStack[:len(visualStack)-1]
		visuals = append(visuals, item)

		nodeChildren := children[item.node.Entry.ID]
		branched := len(nodeChildren) > 1
		childIndent := item.indent
		if branched || (item.showConnector && item.indent > 0) {
			childIndent++
		}
		childGutters := item.gutters
		if item.showConnector && !item.isVirtualRootChild {
			displayIndent := item.indent
			if multipleRoots {
				displayIndent = max(0, displayIndent-1)
			}
			childGutters = append(append([]treeGutter(nil), item.gutters...), treeGutter{
				position: max(0, displayIndent-1),
				show:     !item.isLast,
			})
		}
		for index := len(nodeChildren) - 1; index >= 0; index-- {
			visualStack = append(visualStack, treeVisualNode{
				node: nodeChildren[index], indent: childIndent,
				showConnector: branched, isLast: index == len(nodeChildren)-1,
				gutters: childGutters,
			})
		}
	}

	items := make([]tui.SelectItem, 0, len(visuals))
	for _, item := range visuals {
		label := treeVisualPrefix(item, multipleRoots)
		if active[item.node.Entry.ID] {
			label += "• "
		}
		if item.node.Label != nil && *item.node.Label != "" {
			label += "[" + *item.node.Label + "] "
		}
		label += sessionEntryLabel(item.node.Entry)
		items = append(items, tui.SelectItem{Value: item.node.Entry.ID, Label: label})
	}
	return items
}

func nearestVisibleTreeParent(
	node *sessionstore.SessionTreeNode,
	byID map[string]*sessionstore.SessionTreeNode,
	visible map[string]bool,
) string {
	parent := node.Entry.ParentID
	for parent != nil {
		if visible[*parent] {
			return *parent
		}
		parentNode := byID[*parent]
		if parentNode == nil {
			break
		}
		parent = parentNode.Entry.ParentID
	}
	return ""
}

func prioritizeActiveTreeNodes(nodes []*sessionstore.SessionTreeNode, active map[string]bool) []*sessionstore.SessionTreeNode {
	result := make([]*sessionstore.SessionTreeNode, 0, len(nodes))
	for _, node := range nodes {
		if active[node.Entry.ID] {
			result = append(result, node)
		}
	}
	for _, node := range nodes {
		if !active[node.Entry.ID] {
			result = append(result, node)
		}
	}
	return result
}

func appendReversedTreeNodes(
	stack, nodes []*sessionstore.SessionTreeNode,
) []*sessionstore.SessionTreeNode {
	for index := len(nodes) - 1; index >= 0; index-- {
		stack = append(stack, nodes[index])
	}
	return stack
}

func treeVisualPrefix(node treeVisualNode, multipleRoots bool) string {
	displayIndent := node.indent
	if multipleRoots {
		displayIndent = max(0, displayIndent-1)
	}
	prefix := []rune(strings.Repeat(" ", displayIndent*3))
	connectorPosition := -1
	if node.showConnector && !node.isVirtualRootChild {
		connectorPosition = displayIndent - 1
	}
	for level := 0; level < displayIndent; level++ {
		offset := level * 3
		for _, gutter := range node.gutters {
			if gutter.position == level && gutter.show {
				prefix[offset] = '│'
			}
		}
		if level == connectorPosition {
			if node.isLast {
				prefix[offset] = '└'
			} else {
				prefix[offset] = '├'
			}
			prefix[offset+1] = '─'
		}
	}
	return string(prefix)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
