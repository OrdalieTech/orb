package tui

import "sync"

// Container renders child components vertically in insertion order.
type Container struct {
	mu       sync.RWMutex
	children []Component
}

func (container *Container) AddChild(component Component) {
	container.mu.Lock()
	defer container.mu.Unlock()
	container.children = append(container.children, component)
}

func (container *Container) RemoveChild(component Component) {
	container.mu.Lock()
	defer container.mu.Unlock()
	for index, child := range container.children {
		if child == component {
			container.children = append(container.children[:index], container.children[index+1:]...)
			return
		}
	}
}

func (container *Container) Clear() {
	container.mu.Lock()
	container.children = nil
	container.mu.Unlock()
}

func (container *Container) childrenSnapshot() []Component {
	container.mu.RLock()
	defer container.mu.RUnlock()
	return append([]Component(nil), container.children...)
}

func (container *Container) Invalidate() {
	children := container.childrenSnapshot()
	for _, child := range children {
		invalidate(child)
	}
}

func (container *Container) Render(width int) []string {
	children := container.childrenSnapshot()
	lines := make([]string, 0)
	for _, child := range children {
		lines = append(lines, child.Render(width)...)
	}
	return lines
}
