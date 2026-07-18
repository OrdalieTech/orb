package tui

// Container renders child components vertically in insertion order.
type Container struct {
	Children []Component
}

func (container *Container) AddChild(component Component) {
	container.Children = append(container.Children, component)
}

func (container *Container) RemoveChild(component Component) {
	for index, child := range container.Children {
		if child == component {
			container.Children = append(container.Children[:index], container.Children[index+1:]...)
			return
		}
	}
}

func (container *Container) Clear() { container.Children = nil }

func (container *Container) Invalidate() {
	for _, child := range container.Children {
		invalidate(child)
	}
}

func (container *Container) Render(width int) []string {
	lines := make([]string, 0)
	for _, child := range container.Children {
		lines = append(lines, child.Render(width)...)
	}
	return lines
}
