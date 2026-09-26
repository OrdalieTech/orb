package tui

// undoStack stores state snapshots. Callers push already-detached clones
// (upstream clones on push via structuredClone).
type undoStack[S any] struct {
	stack []S
}

// undoDepth bounds the history: each snapshot holds the buffer's line list,
// so typing a long text keystroke by keystroke would otherwise grow without end.
const undoDepth = 1000

func (stack *undoStack[S]) push(state S) {
	if len(stack.stack) >= undoDepth {
		stack.stack = append(stack.stack[:0], stack.stack[len(stack.stack)-undoDepth+1:]...)
	}
	stack.stack = append(stack.stack, state)
}

func (stack *undoStack[S]) pop() (S, bool) {
	if len(stack.stack) == 0 {
		var zero S
		return zero, false
	}
	last := stack.stack[len(stack.stack)-1]
	stack.stack = stack.stack[:len(stack.stack)-1]
	return last, true
}

func (stack *undoStack[S]) clear() { stack.stack = nil }
