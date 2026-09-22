package tui

import (
	"fmt"
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

type staleInvalidatingLines struct {
	container *Container
	stale     Component
	lines     []string
}

func (component *staleInvalidatingLines) Render(int) []string {
	component.container.ChildChanged(component.stale)
	return component.lines
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

func TestWindowedContainerReleasesOffscreenRenderCache(t *testing.T) {
	container := NewWindowedContainer()
	first, middle, last := NewText("first", 0, 0, nil), NewText("middle", 0, 0, nil), NewText("last", 0, 0, nil)
	container.AddChild(first)
	container.AddChild(middle)
	container.AddChild(last)
	ui := NewTUI(newFakeTerminal(80, 2))
	ui.SetViewport(container, NewText("input", 0, 0, nil))
	if frame := ui.renderViewport(80, 2); !strings.Contains(frame[0], "last") {
		t.Fatalf("tail viewport = %q", frame)
	}
	if container.windowChildLines[0] != nil || container.windowChildLines[1] != nil || first.cacheLines != nil || middle.cacheLines != nil {
		t.Fatal("offscreen rendered lines remain cached")
	}
	if got := container.RenderLines(79, 0, 1); len(got) != 1 || strings.TrimSpace(got[0]) != "first" {
		t.Fatalf("restored first line = %q", got)
	}
	if container.windowChildLines[2] != nil || last.cacheLines != nil {
		t.Fatal("previously visible tail remains cached after scrolling away")
	}
}

func TestWindowedLayoutRestoresEvictedSelectionLines(t *testing.T) {
	container := NewWindowedContainer()
	container.AddChild(NewText(strings.Repeat("selected\n", 9)+"selected", 0, 0, nil))
	container.AddChild(NewText("tail", 0, 0, nil))
	layout := buildLineLayout(container, 80)
	container.RenderLines(80, 10, 11)
	if container.windowChildLines[0] != nil {
		t.Fatal("expected first child to be evicted")
	}
	lines := layout.appendRange(nil, 80, 5, 7)
	if len(lines) != 2 || strings.TrimSpace(lines[0]) != "selected" || strings.TrimSpace(lines[1]) != "selected" {
		t.Fatalf("selection lines after eviction = %q", lines)
	}
}

func TestWindowedContainerReflowsOnWidthChange(t *testing.T) {
	container := NewWindowedContainer()
	child := &countedLines{lines: []string{"line"}}
	container.AddChild(child)
	_ = container.LineCount(80)
	_ = container.LineCount(80)
	_ = container.LineCount(40)
	if child.renders != 2 {
		t.Fatalf("renders after width change = %d, want 2", child.renders)
	}
}

func TestWindowedContainerRemovalDropsCachedTail(t *testing.T) {
	container := NewWindowedContainer()
	first := &countedLines{lines: []string{"first"}}
	last := &countedLines{lines: []string{"last"}}
	container.AddChild(first)
	container.AddChild(last)
	_ = container.LineCount(80)

	container.RemoveChild(last)
	if got := container.LineCount(80); got != 1 {
		t.Fatalf("line count after removal = %d, want 1", got)
	}
	if got := fmt.Sprint(container.RenderLines(80, 0, 2)); got != "[first]" {
		t.Fatalf("lines after removal = %s", got)
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

func TestWindowedContainerOlderMutationDoesNotRenderSuffix(t *testing.T) {
	container := NewWindowedContainer()
	children := make([]*countedLines, 1_000)
	for index := range children {
		children[index] = &countedLines{lines: []string{"line"}}
		container.AddChild(children[index])
	}
	_ = container.LineCount(80)

	children[10].lines = []string{"changed", "extra"}
	container.ChildChanged(children[10])
	if got := container.LineCount(80); got != 1_001 {
		t.Fatalf("line count after older mutation = %d, want 1001", got)
	}
	for index, child := range children {
		want := 1
		if index == 10 {
			want = 2
		}
		if child.renders != want {
			t.Fatalf("child %d renders = %d, want %d", index, child.renders, want)
		}
	}
}

func TestWindowedContainerRefreshesCoalescedChildrenAfterEmptyCache(t *testing.T) {
	container := NewWindowedContainer()
	if got := container.LineCount(80); got != 0 {
		t.Fatalf("initial line count = %d", got)
	}
	spacer := &countedLines{}
	text := &countedLines{lines: []string{"status"}}
	container.AddChild(spacer)
	container.AddChild(text)
	if got := fmt.Sprint(container.RenderLines(80, 0, 1)); got != "[status]" {
		t.Fatalf("coalesced children = %s", got)
	}
	if spacer.renders != 1 || text.renders != 1 {
		t.Fatalf("coalesced renders spacer=%d text=%d, want 1 each", spacer.renders, text.renders)
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

func TestWindowedContainerDuplicateDirty(t *testing.T) {
	container := NewWindowedContainer()
	shared := &countedLines{lines: []string{"shared"}}
	container.AddChild(shared)
	container.AddChild(&countedLines{lines: []string{"other"}})
	container.AddChild(shared)

	if got := container.LineCount(10); got != 3 {
		t.Fatalf("initial LineCount = %d, want 3", got)
	}

	shared.lines = []string{"changed", "changed too"}
	container.ChildChanged(shared)
	if got := container.LineCount(10); got != 5 {
		t.Fatalf("LineCount after duplicate dirty = %d, want 5", got)
	}
	want := []string{"changed", "changed too", "other", "changed", "changed too"}
	if got := container.RenderLines(10, 0, 5); !equalLines(got, want) {
		t.Fatalf("RenderLines = %q, want %q", got, want)
	}

	container.AddChild(shared)
	shared.lines = []string{"again"}
	container.ChildChanged(shared)
	if got := container.LineCount(10); got != 4 {
		t.Fatalf("LineCount after re-dirty = %d, want 4", got)
	}
	want = []string{"again", "other", "again", "again"}
	if got := container.RenderLines(10, 0, 4); !equalLines(got, want) {
		t.Fatalf("RenderLines after re-dirty = %q, want %q", got, want)
	}
}

func TestWindowedContainerIgnoresStaleChildChangesDuringRebuild(t *testing.T) {
	container := NewWindowedContainer()
	stale := &countedLines{lines: []string{"stale"}}
	container.AddChild(stale)
	_ = container.LineCount(80)
	container.RemoveChild(stale)
	container.AddChild(&staleInvalidatingLines{container: container, stale: stale, lines: []string{"fresh"}})
	if got := container.LineCount(80); got != 1 {
		t.Fatalf("line count after stale callback = %d, want 1", got)
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

func TestWindowedContainerRefillsAfterCascadingCollapse(t *testing.T) {
	container := NewWindowedContainer()
	var children []*countedLines
	for i := range 8 {
		child := &countedLines{lines: make([]string, 100)}
		for row := range child.lines {
			child.lines[row] = fmt.Sprintf("%d:%d", i, row)
		}
		children = append(children, child)
		container.AddChild(child)
	}
	container.RenderLines(59, 0, 10)
	for _, child := range children[:7] {
		child.lines = nil
		container.ChildChanged(child)
	}
	want := children[7].lines[72:90]
	if got := container.RenderLines(59, 72, 90); !equalLines(got, want) {
		t.Fatalf("refilled range = %q, want %q", got, want)
	}
}

func TestWindowedContainerInterruptedRefillPreservesRowOffsets(t *testing.T) {
	container := NewWindowedContainer()
	child := &invalidatingLines{container: container, lines: make([]string, 100)}
	container.AddChild(child)
	container.AddChild(&countedLines{lines: []string{"tail"}})
	container.RenderLines(59, 100, 101)
	child.invalidate = true
	child.lines[72] = "restored"
	if got := container.RenderLines(59, 72, 74); len(got) != 2 {
		t.Fatalf("interrupted refill lost its row positions: %q", got)
	}
	if got := container.RenderLines(59, 72, 74); len(got) != 2 || got[0] != "restored" {
		t.Fatalf("next frame did not restore evicted content: %q", got)
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
	if got := container.RenderLines(40, 0, 100); !equalLines(got, want) {
		t.Fatalf("settled range = %q, want %q", got, want)
	}
}
