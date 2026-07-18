package tui

import "strings"

type boxCache struct {
	childLines []string
	width      int
	bgSample   string
	lines      []string
}

// Box applies horizontal and vertical padding and an optional background to
// child components.
type Box struct {
	Children []Component
	paddingX int
	paddingY int
	bg       StyleFunc
	cache    *boxCache
}

func NewBox(paddingX, paddingY int, background StyleFunc) *Box {
	return &Box{paddingX: paddingX, paddingY: paddingY, bg: background}
}

func (box *Box) AddChild(component Component) {
	box.Children = append(box.Children, component)
	box.cache = nil
}

func (box *Box) RemoveChild(component Component) {
	for index, child := range box.Children {
		if child == component {
			box.Children = append(box.Children[:index], box.Children[index+1:]...)
			box.cache = nil
			return
		}
	}
}

func (box *Box) Clear() { box.Children, box.cache = nil, nil }

func (box *Box) SetBackground(background StyleFunc) { box.bg = background }

func (box *Box) Invalidate() {
	box.cache = nil
	for _, child := range box.Children {
		invalidate(child)
	}
}

func (box *Box) Render(width int) []string {
	if len(box.Children) == 0 {
		return nil
	}
	contentWidth := max(1, width-box.paddingX*2)
	left := strings.Repeat(" ", max(0, box.paddingX))
	childLines := make([]string, 0)
	for _, child := range box.Children {
		for _, line := range child.Render(contentWidth) {
			childLines = append(childLines, left+line)
		}
	}
	if len(childLines) == 0 {
		return nil
	}
	sample := ""
	if box.bg != nil {
		sample = box.bg("test")
	}
	if box.cache != nil && box.cache.width == width && box.cache.bgSample == sample && equalLines(box.cache.childLines, childLines) {
		return box.cache.lines
	}
	lines := make([]string, 0, len(childLines)+box.paddingY*2)
	for range max(0, box.paddingY) {
		lines = append(lines, box.applyBackground("", width))
	}
	for _, line := range childLines {
		lines = append(lines, box.applyBackground(line, width))
	}
	for range max(0, box.paddingY) {
		lines = append(lines, box.applyBackground("", width))
	}
	box.cache = &boxCache{childLines: childLines, width: width, bgSample: sample, lines: lines}
	return lines
}

func (box *Box) applyBackground(line string, width int) string {
	line += strings.Repeat(" ", max(0, width-VisibleWidth(line)))
	if box.bg != nil {
		return ApplyBackgroundToLine(line, width, box.bg)
	}
	return line
}

func equalLines(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
