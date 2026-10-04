package tui

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

type countedLines struct {
	lines   []string
	renders int
}

type invalidatingLines struct {
	container  *Container
	lines      []string
	renders    int
	invalidate bool
}

type sliceLines []string

func (component sliceLines) Render(int) []string { return component }

type funcLines func() []string

func (component funcLines) Render(int) []string { return component() }

type delayedInvalidateLines struct {
	mu                sync.Mutex
	invalidated       bool
	invalidateStarted chan struct{}
	allowInvalidate   chan struct{}
}

func (component *delayedInvalidateLines) Render(int) []string {
	component.mu.Lock()
	defer component.mu.Unlock()
	if component.invalidated {
		return []string{"fresh"}
	}
	return []string{"stale"}
}

func (component *delayedInvalidateLines) Invalidate() {
	close(component.invalidateStarted)
	<-component.allowInvalidate
	component.mu.Lock()
	component.invalidated = true
	component.mu.Unlock()
}

func (component *invalidatingLines) Render(int) []string {
	component.renders++
	if component.invalidate {
		component.invalidate = false
		component.container.ChildChanged(component)
	}
	return component.lines
}

func (component *countedLines) Render(int) []string {
	component.renders++
	return component.lines
}

func TestWindowedContainerRendersOnlyChangedTail(t *testing.T) {
	container := NewWindowedContainer()
	children := make([]*countedLines, 1_000)
	for index := range children {
		children[index] = &countedLines{lines: []string{fmt.Sprintf("line %d", index)}}
		container.AddChild(children[index])
	}

	if got := container.LineCount(80); got != len(children) {
		t.Fatalf("line count = %d, want %d", got, len(children))
	}
	if got := container.RenderLines(80, 997, 1_000); fmt.Sprint(got) != "[line 997 line 998 line 999]" {
		t.Fatalf("tail = %#v", got)
	}
	for index, child := range children {
		if child.renders != 1 {
			t.Fatalf("initial child %d renders = %d, want 1", index, child.renders)
		}
	}

	children[999].lines = []string{"changed", "extra"}
	container.ChildChanged(children[999])
	if got := container.RenderLines(80, 999, 1_001); fmt.Sprint(got) != "[changed extra]" {
		t.Fatalf("changed tail = %#v", got)
	}
	for index, child := range children[:999] {
		if child.renders != 1 {
			t.Fatalf("unchanged child %d rendered again: %d", index, child.renders)
		}
	}
	if children[999].renders != 2 {
		t.Fatalf("changed child renders = %d, want 2", children[999].renders)
	}

	appended := &countedLines{lines: []string{"appended"}}
	container.AddChild(appended)
	if got := container.RenderLines(80, 1_001, 1_002); fmt.Sprint(got) != "[appended]" {
		t.Fatalf("appended tail = %#v", got)
	}
	if appended.renders != 1 || children[999].renders != 2 {
		t.Fatalf("append rendered old tail=%d new=%d", children[999].renders, appended.renders)
	}
}

func TestWindowedContainerDefersConcurrentMutation(t *testing.T) {
	container := NewWindowedContainer()
	child := &invalidatingLines{container: container, lines: []string{"old"}}
	container.AddChild(child)
	_ = container.LineCount(80)

	child.lines = []string{"new", "extra"}
	child.invalidate = true
	container.ChildChanged(child)
	if got := container.LineCount(80); got != 1 || child.renders != 2 {
		t.Fatalf("concurrent mutation frame: lines=%d renders=%d, want old 1 and bounded 2", got, child.renders)
	}
	if got := container.LineCount(80); got != 2 || child.renders != 3 {
		t.Fatalf("retry frame: lines=%d renders=%d, want new 2 and 3", got, child.renders)
	}
}

func TestWindowedContainerAcceptsNonComparableComponents(t *testing.T) {
	container := NewWindowedContainer()
	component := sliceLines{"before"}
	container.AddChild(component)
	_ = container.LineCount(80)
	component[0] = "after"
	container.ChildChanged(component)
	if got := fmt.Sprint(container.RenderLines(80, 0, 1)); got != "[after]" {
		t.Fatalf("non-comparable component = %s", got)
	}
	if !container.EndsWith(component) {
		t.Fatal("non-comparable component suffix was not found")
	}
	container.RemoveChild(component)
	if got := container.LineCount(80); got != 0 {
		t.Fatalf("line count after non-comparable removal = %d", got)
	}

	first, second := sliceLines{"same"}, sliceLines{"same"}
	container.AddChild(first)
	container.AddChild(second)
	container.RemoveChild(second)
	children := container.Children()
	if len(children) != 1 || !componentsEqual(children[0], first) {
		t.Fatalf("distinct equal slices removed wrong child: %#v", children)
	}

	value := "before"
	dynamic := funcLines(func() []string { return []string{value} })
	container.Clear()
	container.AddChild(dynamic)
	_ = container.LineCount(80)
	value = "after"
	container.ChildChanged(dynamic)
	if got := fmt.Sprint(container.RenderLines(80, 0, 1)); got != "[after]" {
		t.Fatalf("function component = %s", got)
	}
	if !container.EndsWith(dynamic) {
		t.Fatal("function component suffix was not found")
	}
	container.RemoveChild(dynamic)
	if got := container.LineCount(80); got != 0 {
		t.Fatalf("line count after function removal = %d", got)
	}
}

func TestWindowedContainerFencesConcurrentChildInvalidation(t *testing.T) {
	container := NewWindowedContainer()
	child := &delayedInvalidateLines{
		invalidateStarted: make(chan struct{}),
		allowInvalidate:   make(chan struct{}),
	}
	container.AddChild(child)
	_ = container.LineCount(80)

	invalidated := make(chan struct{})
	go func() {
		container.Invalidate()
		close(invalidated)
	}()
	<-child.invalidateStarted
	if got := container.LineCount(80); got != 1 {
		t.Fatalf("concurrent rebuild line count = %d, want 1", got)
	}
	close(child.allowInvalidate)
	<-invalidated
	if got := fmt.Sprint(container.RenderLines(80, 0, 1)); got != "[fresh]" {
		t.Fatalf("render after concurrent invalidation = %s", got)
	}
}

func TestWindowedContainerConcurrentResizeAndMutation(t *testing.T) {
	container := NewWindowedContainer()
	child := NewText("initial", 0, 0, nil)
	container.AddChild(child)
	var workers sync.WaitGroup
	for worker := range 4 {
		workers.Go(func() {
			for step := range 200 {
				width := 1 + (step+worker*17)%80
				start := (step * 7) % 100
				if got := container.RenderLines(width, start, start+20); len(got) > 20 {
					t.Errorf("range returned %d rows", len(got))
				}
				container.LineCount(width)
			}
		})
	}
	workers.Go(func() {
		for step := range 200 {
			child.SetText(strings.Repeat("streaming text\n", step%30))
			container.ChildChanged(child)
			switch step % 4 {
			case 0:
				container.Clear()
				container.AddChild(child)
			case 1:
				container.RemoveChild(child)
				container.AddChild(child)
			case 2:
				container.Invalidate()
			}
		}
	})
	workers.Wait()
	child.SetText("restored\ncontent")
	container.ChildChanged(child)
	want := child.Render(40)
	if got := container.RenderLines(40, 0, 100); !slices.Equal(got, want) {
		t.Fatalf("settled range = %q, want %q", got, want)
	}
}
