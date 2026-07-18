package tui

import (
	"strings"
	"sync"
)

type inputState struct {
	value  string
	cursor int
}

// Input is a single-line text input with horizontal scrolling. The cursor is
// a rune index into the value.
type Input struct {
	mu      sync.Mutex
	value   string
	cursor  int
	focused bool
	pending []func()

	pasteBuffer string
	isInPaste   bool

	killRing   killRing
	lastAction string // "kill" | "yank" | "type-word" | ""

	undoStack undoStack[inputState]

	OnSubmit func(string)
	OnEscape func()
}

func NewInput() *Input { return &Input{} }

func (input *Input) SetFocused(focused bool) {
	input.mu.Lock()
	input.focused = focused
	input.mu.Unlock()
}

func (input *Input) GetValue() string {
	input.mu.Lock()
	defer input.mu.Unlock()
	return input.value
}

// GetCursor returns the upstream-compatible UTF-16 column offset.
func (input *Input) GetCursor() int {
	input.mu.Lock()
	defer input.mu.Unlock()
	return utf16Length(runeSlice(input.value, 0, input.cursor))
}

func (input *Input) SetValue(value string) {
	input.mu.Lock()
	input.value = value
	input.cursor = min(input.cursor, runeLen(value))
	input.mu.Unlock()
}

func (input *Input) Invalidate() {}

func (input *Input) HandleInput(event KeyEvent) {
	input.mu.Lock()
	input.handleData(event.Raw)
	pending := input.pending
	input.pending = nil
	input.mu.Unlock()
	for _, callback := range pending {
		callback()
	}
}

func (input *Input) handleData(data string) {
	// Bracketed paste buffering: \x1b[200~ ... \x1b[201~
	if strings.Contains(data, "\x1b[200~") {
		input.isInPaste = true
		input.pasteBuffer = ""
		data = strings.Replace(data, "\x1b[200~", "", 1)
	}
	if input.isInPaste {
		input.pasteBuffer += data
		if endIndex := strings.Index(input.pasteBuffer, "\x1b[201~"); endIndex != -1 {
			pasteContent := input.pasteBuffer[:endIndex]
			input.handlePaste(pasteContent)
			input.isInPaste = false
			remaining := input.pasteBuffer[endIndex+6:]
			input.pasteBuffer = ""
			if remaining != "" {
				input.handleData(remaining)
			}
		}
		return
	}

	kb := GetKeybindings()
	switch {
	case kb.Matches(data, "tui.select.cancel"):
		if input.OnEscape != nil {
			input.pending = append(input.pending, input.OnEscape)
		}
		return
	case kb.Matches(data, "tui.editor.undo"):
		input.undo()
		return
	case kb.Matches(data, "tui.input.submit") || data == "\n":
		if input.OnSubmit != nil {
			callback, value := input.OnSubmit, input.value
			input.pending = append(input.pending, func() { callback(value) })
		}
		return
	case kb.Matches(data, "tui.editor.deleteCharBackward"):
		input.handleBackspace()
		return
	case kb.Matches(data, "tui.editor.deleteCharForward"):
		input.handleForwardDelete()
		return
	case kb.Matches(data, "tui.editor.deleteWordBackward"):
		input.deleteWordBackwards()
		return
	case kb.Matches(data, "tui.editor.deleteWordForward"):
		input.deleteWordForward()
		return
	case kb.Matches(data, "tui.editor.deleteToLineStart"):
		input.deleteToLineStart()
		return
	case kb.Matches(data, "tui.editor.deleteToLineEnd"):
		input.deleteToLineEnd()
		return
	case kb.Matches(data, "tui.editor.yank"):
		input.yank()
		return
	case kb.Matches(data, "tui.editor.yankPop"):
		input.yankPop()
		return
	case kb.Matches(data, "tui.editor.cursorLeft"):
		input.lastAction = ""
		if input.cursor > 0 {
			graphemes := graphemeSegments(runeSlice(input.value, 0, input.cursor))
			input.cursor -= lastGraphemeLength(graphemes)
		}
		return
	case kb.Matches(data, "tui.editor.cursorRight"):
		input.lastAction = ""
		if input.cursor < runeLen(input.value) {
			graphemes := graphemeSegments(runeSliceFrom(input.value, input.cursor))
			input.cursor += firstGraphemeLength(graphemes)
		}
		return
	case kb.Matches(data, "tui.editor.cursorLineStart"):
		input.lastAction = ""
		input.cursor = 0
		return
	case kb.Matches(data, "tui.editor.cursorLineEnd"):
		input.lastAction = ""
		input.cursor = runeLen(input.value)
		return
	case kb.Matches(data, "tui.editor.cursorWordLeft"):
		input.moveWordBackwards()
		return
	case kb.Matches(data, "tui.editor.cursorWordRight"):
		input.moveWordForwards()
		return
	}

	// Kitty CSI-u printable characters arrive before the control-char check
	// because the sequences themselves contain ESC.
	if printable := DecodeKittyPrintable(data); printable != "" {
		input.insertCharacter(printable)
		return
	}

	for _, r := range data {
		if r < 32 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return
		}
	}
	input.insertCharacter(data)
}

func lastGraphemeLength(graphemes []segment) int {
	if len(graphemes) == 0 {
		return 1
	}
	return runeLen(graphemes[len(graphemes)-1].text)
}

func firstGraphemeLength(graphemes []segment) int {
	if len(graphemes) == 0 {
		return 1
	}
	return runeLen(graphemes[0].text)
}

func (input *Input) insertCharacter(char string) {
	if isWhitespaceChar(char) || input.lastAction != "type-word" {
		input.pushUndo()
	}
	input.lastAction = "type-word"
	input.value = runeSlice(input.value, 0, input.cursor) + char + runeSliceFrom(input.value, input.cursor)
	input.cursor += runeLen(char)
}

func (input *Input) handleBackspace() {
	input.lastAction = ""
	if input.cursor > 0 {
		input.pushUndo()
		graphemes := graphemeSegments(runeSlice(input.value, 0, input.cursor))
		graphemeLength := lastGraphemeLength(graphemes)
		input.value = runeSlice(input.value, 0, input.cursor-graphemeLength) + runeSliceFrom(input.value, input.cursor)
		input.cursor -= graphemeLength
	}
}

func (input *Input) handleForwardDelete() {
	input.lastAction = ""
	if input.cursor < runeLen(input.value) {
		input.pushUndo()
		graphemes := graphemeSegments(runeSliceFrom(input.value, input.cursor))
		graphemeLength := firstGraphemeLength(graphemes)
		input.value = runeSlice(input.value, 0, input.cursor) + runeSliceFrom(input.value, input.cursor+graphemeLength)
	}
}

func (input *Input) deleteToLineStart() {
	if input.cursor == 0 {
		return
	}
	input.pushUndo()
	deleted := runeSlice(input.value, 0, input.cursor)
	input.killRing.push(deleted, true, input.lastAction == "kill")
	input.lastAction = "kill"
	input.value = runeSliceFrom(input.value, input.cursor)
	input.cursor = 0
}

func (input *Input) deleteToLineEnd() {
	if input.cursor >= runeLen(input.value) {
		return
	}
	input.pushUndo()
	deleted := runeSliceFrom(input.value, input.cursor)
	input.killRing.push(deleted, false, input.lastAction == "kill")
	input.lastAction = "kill"
	input.value = runeSlice(input.value, 0, input.cursor)
}

func (input *Input) deleteWordBackwards() {
	if input.cursor == 0 {
		return
	}
	wasKill := input.lastAction == "kill"
	input.pushUndo()

	oldCursor := input.cursor
	input.moveWordBackwards()
	deleteFrom := input.cursor
	input.cursor = oldCursor

	deleted := runeSlice(input.value, deleteFrom, input.cursor)
	input.killRing.push(deleted, true, wasKill)
	input.lastAction = "kill"

	input.value = runeSlice(input.value, 0, deleteFrom) + runeSliceFrom(input.value, input.cursor)
	input.cursor = deleteFrom
}

func (input *Input) deleteWordForward() {
	if input.cursor >= runeLen(input.value) {
		return
	}
	wasKill := input.lastAction == "kill"
	input.pushUndo()

	oldCursor := input.cursor
	input.moveWordForwards()
	deleteTo := input.cursor
	input.cursor = oldCursor

	deleted := runeSlice(input.value, input.cursor, deleteTo)
	input.killRing.push(deleted, false, wasKill)
	input.lastAction = "kill"

	input.value = runeSlice(input.value, 0, input.cursor) + runeSliceFrom(input.value, deleteTo)
}

func (input *Input) yank() {
	text := input.killRing.peek()
	if text == "" {
		return
	}
	input.pushUndo()
	input.value = runeSlice(input.value, 0, input.cursor) + text + runeSliceFrom(input.value, input.cursor)
	input.cursor += runeLen(text)
	input.lastAction = "yank"
}

func (input *Input) yankPop() {
	if input.lastAction != "yank" || input.killRing.length() <= 1 {
		return
	}
	input.pushUndo()

	previous := input.killRing.peek()
	input.value = runeSlice(input.value, 0, input.cursor-runeLen(previous)) + runeSliceFrom(input.value, input.cursor)
	input.cursor -= runeLen(previous)

	input.killRing.rotate()
	text := input.killRing.peek()
	input.value = runeSlice(input.value, 0, input.cursor) + text + runeSliceFrom(input.value, input.cursor)
	input.cursor += runeLen(text)
	input.lastAction = "yank"
}

func (input *Input) pushUndo() {
	input.undoStack.push(inputState{value: input.value, cursor: input.cursor})
}

func (input *Input) undo() {
	snapshot, ok := input.undoStack.pop()
	if !ok {
		return
	}
	input.value = snapshot.value
	input.cursor = snapshot.cursor
	input.lastAction = ""
}

func (input *Input) moveWordBackwards() {
	if input.cursor == 0 {
		return
	}
	input.lastAction = ""
	input.cursor = findWordBackward(input.value, input.cursor, nil)
}

func (input *Input) moveWordForwards() {
	if input.cursor >= runeLen(input.value) {
		return
	}
	input.lastAction = ""
	input.cursor = findWordForward(input.value, input.cursor, nil)
}

func (input *Input) handlePaste(pastedText string) {
	input.lastAction = ""
	input.pushUndo()
	cleaned := strings.NewReplacer("\r\n", "", "\r", "", "\n", "", "\t", "    ").Replace(pastedText)
	input.value = runeSlice(input.value, 0, input.cursor) + cleaned + runeSliceFrom(input.value, input.cursor)
	input.cursor += runeLen(cleaned)
}

func (input *Input) Render(width int) []string {
	input.mu.Lock()
	defer input.mu.Unlock()

	prompt := "> "
	availableWidth := width - len(prompt)
	if availableWidth <= 0 {
		return []string{prompt}
	}

	visibleText := ""
	cursorDisplay := input.cursor
	totalWidth := VisibleWidth(input.value)
	valueLength := runeLen(input.value)

	if totalWidth < availableWidth {
		visibleText = input.value
	} else {
		// Horizontal scrolling; reserve one column when the cursor sits at
		// the end of the value.
		scrollWidth := availableWidth
		if input.cursor == valueLength {
			scrollWidth = availableWidth - 1
		}
		cursorCol := VisibleWidth(runeSlice(input.value, 0, input.cursor))

		if scrollWidth > 0 {
			halfWidth := scrollWidth / 2
			startCol := 0
			switch {
			case cursorCol < halfWidth:
				startCol = 0
			case cursorCol > totalWidth-halfWidth:
				startCol = max(0, totalWidth-scrollWidth)
			default:
				startCol = max(0, cursorCol-halfWidth)
			}
			visibleText = SliceByColumn(input.value, startCol, scrollWidth, true)
			beforeCursor := SliceByColumn(input.value, startCol, max(0, cursorCol-startCol), true)
			cursorDisplay = runeLen(beforeCursor)
		} else {
			visibleText = ""
			cursorDisplay = 0
		}
	}

	graphemes := graphemeSegments(runeSliceFrom(visibleText, cursorDisplay))
	atCursor := " "
	if len(graphemes) > 0 {
		atCursor = graphemes[0].text
	}
	beforeCursor := runeSlice(visibleText, 0, cursorDisplay)
	afterCursor := runeSliceFrom(visibleText, cursorDisplay+runeLen(atCursor))

	marker := ""
	if input.focused {
		marker = CursorMarker
	}

	cursorChar := "\x1b[7m" + atCursor + "\x1b[27m"
	textWithCursor := beforeCursor + marker + cursorChar + afterCursor

	visualLength := VisibleWidth(textWithCursor)
	padding := strings.Repeat(" ", max(0, availableWidth-visualLength))
	return []string{prompt + textWithCursor + padding}
}
