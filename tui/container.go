package tui

import (
	"reflect"
	"sort"
	"sync"
)

// Container renders child components vertically in insertion order.
type Container struct {
	mu       sync.RWMutex
	children []Component

	windowed         bool
	windowValid      bool
	windowWidth      int
	windowGeneration uint64
	windowChildLines [][]string
	windowChildCount []int
	windowTree       []int
	windowDirty      map[int]struct{}
	windowDirtyMin   int
	windowDirtyMax   int
	windowIndexes    map[Component]int
	windowDuplicates map[Component][]int
}

// NewWindowedContainer caches child layout so a viewport can render only its
// visible range. ChildChanged must be called after mutating an existing child.
func NewWindowedContainer() *Container {
	return &Container{windowed: true, windowDirtyMin: -1, windowDirtyMax: -1}
}

func componentsEqual(left, right Component) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftType, rightType := reflect.TypeOf(left), reflect.TypeOf(right)
	if leftType != rightType {
		return false
	}
	if leftType.Comparable() {
		return left == right
	}
	leftValue, rightValue := reflect.ValueOf(left), reflect.ValueOf(right)
	switch leftType.Kind() {
	case reflect.Slice:
		return leftValue.Pointer() == rightValue.Pointer() && leftValue.Len() == rightValue.Len() && leftValue.Cap() == rightValue.Cap()
	case reflect.Func, reflect.Map:
		return leftValue.Pointer() == rightValue.Pointer()
	default:
		return reflect.DeepEqual(left, right)
	}
}

func (container *Container) addWindowIndexLocked(component Component, index int) {
	if component != nil && !reflect.TypeOf(component).Comparable() {
		return
	}
	first, exists := container.windowIndexes[component]
	if !exists {
		container.windowIndexes[component] = index
		return
	}
	if container.windowDuplicates == nil {
		container.windowDuplicates = make(map[Component][]int)
	}
	duplicates := container.windowDuplicates[component]
	if len(duplicates) == 0 {
		duplicates = append(duplicates, first)
	}
	container.windowDuplicates[component] = append(duplicates, index)
}

func (container *Container) markWindowIndexDirtyLocked(index int) {
	if _, exists := container.windowDirty[index]; exists {
		return
	}
	container.windowDirty[index] = struct{}{}
	if container.windowDirtyMin < 0 || index < container.windowDirtyMin {
		container.windowDirtyMin = index
	}
	if container.windowDirtyMax < 0 || index > container.windowDirtyMax {
		container.windowDirtyMax = index
	}
}

func (container *Container) markWindowDirtyLocked(component Component) {
	if !container.windowed {
		return
	}
	var indices []int
	var single [1]int
	if component != nil && !reflect.TypeOf(component).Comparable() {
		for index, child := range container.children {
			if child != nil && !reflect.TypeOf(child).Comparable() {
				indices = append(indices, index)
			}
		}
	} else {
		if container.windowValid {
			if duplicates := container.windowDuplicates[component]; len(duplicates) > 0 {
				indices = duplicates
			} else if index, exists := container.windowIndexes[component]; exists {
				single[0] = index
				indices = single[:]
			}
		} else {
			for index, child := range container.children {
				if componentsEqual(child, component) {
					indices = append(indices, index)
				}
			}
		}
	}
	if len(indices) == 0 {
		return
	}
	container.windowGeneration++
	if !container.windowValid {
		return
	}
	for _, index := range indices {
		container.markWindowIndexDirtyLocked(index)
	}
}

func (container *Container) AddChild(component Component) {
	container.mu.Lock()
	defer container.mu.Unlock()
	index := len(container.children)
	container.children = append(container.children, component)
	if !container.windowed {
		return
	}
	container.windowGeneration++
	if !container.windowValid {
		return
	}
	container.windowChildLines = append(container.windowChildLines, nil)
	container.windowChildCount = append(container.windowChildCount, 0)
	container.windowTree = fenwickAppend(container.windowTree, 0)
	container.addWindowIndexLocked(component, index)
	container.markWindowIndexDirtyLocked(index)
}

func (container *Container) RemoveChild(component Component) {
	container.mu.Lock()
	defer container.mu.Unlock()
	for index, child := range container.children {
		if componentsEqual(child, component) {
			container.children = append(container.children[:index], container.children[index+1:]...)
			if container.windowed {
				container.windowValid = false
				container.windowGeneration++
			}
			return
		}
	}
}

func (container *Container) Clear() {
	container.mu.Lock()
	container.children = nil
	container.windowValid = false
	container.windowChildLines = nil
	container.windowChildCount = nil
	container.windowTree = nil
	container.windowDirty = nil
	container.windowDirtyMin = -1
	container.windowDirtyMax = -1
	container.windowIndexes = nil
	container.windowDuplicates = nil
	if container.windowed {
		container.windowGeneration++
	}
	container.mu.Unlock()
}

func (container *Container) childrenSnapshot() []Component {
	container.mu.RLock()
	defer container.mu.RUnlock()
	return append([]Component(nil), container.children...)
}

func (container *Container) Children() []Component {
	return container.childrenSnapshot()
}

// EndsWith safely exposes the suffix check needed by upstream components,
// whose TypeScript container children are directly observable.
func (container *Container) EndsWith(components ...Component) bool {
	container.mu.RLock()
	defer container.mu.RUnlock()
	if len(components) > len(container.children) {
		return false
	}
	offset := len(container.children) - len(components)
	for index, component := range components {
		if !componentsEqual(container.children[offset+index], component) {
			return false
		}
	}
	return true
}

func (container *Container) Invalidate() {
	container.mu.Lock()
	children := append([]Component(nil), container.children...)
	container.windowValid = false
	if container.windowed {
		container.windowGeneration++
	}
	container.mu.Unlock()
	for _, child := range children {
		invalidate(child)
	}
	container.mu.Lock()
	container.windowValid = false
	if container.windowed {
		container.windowGeneration++
	}
	container.mu.Unlock()
}

// ChildChanged invalidates the aggregate layout after an existing child mutates.
func (container *Container) ChildChanged(component Component) {
	container.mu.Lock()
	defer container.mu.Unlock()
	container.markWindowDirtyLocked(component)
}

func (container *Container) Render(width int) []string {
	children := container.childrenSnapshot()
	lines := make([]string, 0)
	for _, child := range children {
		lines = append(lines, child.Render(width)...)
	}
	return lines
}

type lineWindow interface {
	LineCount(width int) int
	RenderLines(width, start, end int) []string
}

func componentLineCount(component Component, width int) int {
	if window, ok := component.(lineWindow); ok {
		return window.LineCount(width)
	}
	return len(component.Render(width))
}

func componentLines(component Component, width, start, end int) []string {
	if start >= end {
		return nil
	}
	if window, ok := component.(lineWindow); ok {
		return window.RenderLines(width, start, end)
	}
	lines := component.Render(width)
	start, end = max(0, min(start, len(lines))), max(0, min(end, len(lines)))
	return lines[start:end]
}

type lineLayout struct {
	lines      []string
	window     lineWindow
	cached     *Container
	children   []lineLayout
	total      int
	rangeFresh bool
}

func buildLineLayout(component Component, width int) lineLayout {
	if container, ok := component.(*Container); ok {
		if container.windowed {
			rangeFresh := container.refreshWindow(width, -1, -1, false)
			container.mu.RLock()
			total := fenwickSum(container.windowTree, len(container.windowChildCount))
			container.mu.RUnlock()
			return lineLayout{cached: container, total: total, rangeFresh: rangeFresh}
		}
		children := container.childrenSnapshot()
		layout := lineLayout{children: make([]lineLayout, len(children))}
		for index, child := range children {
			layout.children[index] = buildLineLayout(child, width)
			layout.total += layout.children[index].total
		}
		return layout
	}
	if window, ok := component.(lineWindow); ok {
		return lineLayout{window: window, total: window.LineCount(width)}
	}
	lines := component.Render(width)
	return lineLayout{lines: lines, total: len(lines)}
}

func (layout *lineLayout) refreshRange(width, start, end int, allDirty bool) {
	start, end = max(0, min(start, layout.total)), max(0, min(end, layout.total))
	if start >= end && !allDirty {
		return
	}
	if layout.cached != nil {
		if !layout.rangeFresh {
			layout.cached.refreshWindow(width, start, end, allDirty)
			layout.cached.mu.RLock()
			layout.total = fenwickSum(layout.cached.windowTree, len(layout.cached.windowChildCount))
			layout.cached.mu.RUnlock()
			layout.rangeFresh = true
		}
		return
	}
	if layout.children == nil {
		return
	}
	offset := 0
	for index := range layout.children {
		child := &layout.children[index]
		next := offset + child.total
		if allDirty || (start < next && end > offset) {
			child.refreshRange(width, max(0, start-offset), min(child.total, end-offset), allDirty)
		}
		offset += child.total
	}
	layout.total = offset
}

func (layout lineLayout) appendRange(lines []string, width, start, end int) []string {
	start, end = max(0, min(start, layout.total)), max(0, min(end, layout.total))
	if start >= end {
		return lines
	}
	if layout.cached != nil {
		layout.cached.mu.RLock()
		lines = append(lines, layout.cached.renderCachedLinesLocked(start, end)...)
		layout.cached.mu.RUnlock()
		return lines
	}
	if layout.window != nil {
		return append(lines, layout.window.RenderLines(width, start, end)...)
	}
	if layout.children == nil {
		return append(lines, layout.lines[start:end]...)
	}
	offset := 0
	for _, child := range layout.children {
		next := offset + child.total
		if start < next && end > offset {
			lines = child.appendRange(lines, width, max(0, start-offset), min(child.total, end-offset))
		}
		if next >= end {
			break
		}
		offset = next
	}
	return lines
}

func fenwickBuild(counts []int) []int {
	tree := make([]int, len(counts)+1)
	for index, count := range counts {
		node := index + 1
		tree[node] += count
		if parent := node + node&-node; parent < len(tree) {
			tree[parent] += tree[node]
		}
	}
	return tree
}

func fenwickSum(tree []int, count int) int {
	total := 0
	for count > 0 {
		total += tree[count]
		count -= count & -count
	}
	return total
}

func fenwickAdd(tree []int, index, delta int) {
	for index++; index < len(tree); index += index & -index {
		tree[index] += delta
	}
}

func fenwickAppend(tree []int, value int) []int {
	index := len(tree)
	span := index & -index
	value += fenwickSum(tree, index-1) - fenwickSum(tree, index-span)
	return append(tree, value)
}

func fenwickFind(tree []int, target int) int {
	index, total := 0, 0
	bit := 1
	for bit<<1 < len(tree) {
		bit <<= 1
	}
	for bit > 0 {
		next := index + bit
		if next < len(tree) && total+tree[next] <= target {
			index = next
			total += tree[next]
		}
		bit >>= 1
	}
	return index
}

func (container *Container) refreshWindow(width, rangeStart, rangeEnd int, allDirty bool) bool {
	container.mu.Lock()
	if !container.windowed {
		container.mu.Unlock()
		return false
	}
	rebuild := !container.windowValid || container.windowWidth != width
	var indices []int
	if rebuild {
		indices = make([]int, len(container.children))
		for index := range indices {
			indices[index] = index
		}
	} else {
		total := fenwickSum(container.windowTree, len(container.windowChildCount))
		requestedEnd := rangeEnd
		if rangeStart >= 0 {
			rangeStart = max(0, min(rangeStart, total))
			rangeEnd = max(0, min(rangeEnd, total))
		}
		first, last := 0, -1
		if rangeStart >= 0 && rangeStart < rangeEnd {
			first = fenwickFind(container.windowTree, rangeStart)
			last = fenwickFind(container.windowTree, rangeEnd-1)
		} else if (allDirty || requestedEnd > total) && rangeEnd == total && len(container.windowChildCount) > 0 {
			first, last = len(container.windowChildCount)-1, len(container.windowChildCount)-1
		}
		for first > 0 && container.windowChildCount[first-1] == 0 {
			first--
		}
		if last >= 0 && rangeEnd == total {
			for last+1 < len(container.windowChildCount) && container.windowChildCount[last+1] == 0 {
				last++
			}
		}
		if allDirty || last >= container.windowDirtyMin && first <= container.windowDirtyMax {
			indices = make([]int, 0, len(container.windowDirty))
			for index := range container.windowDirty {
				if allDirty || index >= first && index <= last {
					indices = append(indices, index)
				}
			}
		}
		if len(indices) == 0 {
			container.mu.Unlock()
			return false
		}
		sort.Ints(indices)
	}
	generation := container.windowGeneration
	children := make([]Component, len(indices))
	for offset, index := range indices {
		children[offset] = container.children[index]
	}
	container.mu.Unlock()

	lines := make([][]string, len(children))
	for index, child := range children {
		lines[index] = child.Render(width)
	}

	container.mu.Lock()
	defer container.mu.Unlock()
	if generation != container.windowGeneration {
		return rebuild
	}
	if rebuild {
		container.windowValid = true
		container.windowWidth = width
		container.windowChildLines = lines
		container.windowChildCount = make([]int, len(lines))
		container.windowDirty = make(map[int]struct{})
		container.windowDirtyMin = -1
		container.windowDirtyMax = -1
		container.windowIndexes = make(map[Component]int, len(children))
		container.windowDuplicates = nil
		for index, child := range children {
			container.windowChildCount[index] = len(lines[index])
			container.addWindowIndexLocked(child, index)
		}
		container.windowTree = fenwickBuild(container.windowChildCount)
		return true
	}
	recomputeDirtyBounds := false
	for offset, index := range indices {
		count := len(lines[offset])
		delta := count - container.windowChildCount[index]
		container.windowChildLines[index] = lines[offset]
		container.windowChildCount[index] = count
		if delta != 0 {
			fenwickAdd(container.windowTree, index, delta)
		}
		if index == container.windowDirtyMin || index == container.windowDirtyMax {
			recomputeDirtyBounds = true
		}
		delete(container.windowDirty, index)
	}
	if len(container.windowDirty) == 0 {
		container.windowDirtyMin, container.windowDirtyMax = -1, -1
	} else if recomputeDirtyBounds {
		container.windowDirtyMin, container.windowDirtyMax = -1, -1
		for index := range container.windowDirty {
			if container.windowDirtyMin < 0 || index < container.windowDirtyMin {
				container.windowDirtyMin = index
			}
			if index > container.windowDirtyMax {
				container.windowDirtyMax = index
			}
		}
	}
	return false
}

// LineCount returns the rendered height without flattening cached child lines.
func (container *Container) LineCount(width int) int {
	if !container.windowed {
		total := 0
		for _, child := range container.childrenSnapshot() {
			total += componentLineCount(child, width)
		}
		return total
	}
	container.refreshWindow(width, -1, -1, true)
	container.mu.RLock()
	defer container.mu.RUnlock()
	return fenwickSum(container.windowTree, len(container.windowChildCount))
}

func (container *Container) renderCachedLinesLocked(start, end int) []string {
	total := fenwickSum(container.windowTree, len(container.windowChildCount))
	start, end = max(0, min(start, total)), max(0, min(end, total))
	if start >= end {
		return nil
	}
	lines := make([]string, 0, end-start)
	first := fenwickFind(container.windowTree, start)
	childStart := fenwickSum(container.windowTree, first)
	for index := first; index < len(container.windowChildLines) && childStart < end; index++ {
		from := max(0, start-childStart)
		to := min(len(container.windowChildLines[index]), end-childStart)
		lines = append(lines, container.windowChildLines[index][from:to]...)
		childStart += container.windowChildCount[index]
	}
	return lines
}

// RenderLines renders the half-open line range [start, end).
func (container *Container) RenderLines(width, start, end int) []string {
	if !container.windowed {
		lines := make([]string, 0, max(0, end-start))
		offset := 0
		for _, child := range container.childrenSnapshot() {
			count := componentLineCount(child, width)
			next := offset + count
			if start < next && end > offset {
				lines = append(lines, componentLines(child, width, max(0, start-offset), min(count, end-offset))...)
			}
			if next >= end {
				break
			}
			offset = next
		}
		return lines
	}

	rangeFresh := container.refreshWindow(width, -1, -1, false)
	if !rangeFresh {
		container.refreshWindow(width, start, end, false)
	}
	container.mu.RLock()
	defer container.mu.RUnlock()
	return container.renderCachedLinesLocked(start, end)
}
