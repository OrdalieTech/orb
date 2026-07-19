package tui

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Paste markers like "[paste #1 +123 lines]" or "[paste #2 1234 chars]".
var (
	pasteMarkerRegex  = regexp.MustCompile(`\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]`)
	pasteMarkerSingle = regexp.MustCompile(`^\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]$`)
)

// isPasteMarker reports whether a segment was merged by segmentWithMarkers.
func isPasteMarker(value string) bool {
	return len(value) >= 10 && pasteMarkerSingle.MatchString(value)
}

// segmentWithMarkers merges base segments falling inside paste markers into
// single atomic segments so cursor movement, deletion, and word-wrap treat
// markers as units. Only markers whose ID exists in validIDs are merged.
func segmentWithMarkers(text string, base func(string) []segment, validIDs map[int]bool) []segment {
	if len(validIDs) == 0 || !strings.Contains(text, "[paste #") {
		return base(text)
	}
	type span struct{ start, end int }
	var markers []span
	for _, match := range pasteMarkerRegex.FindAllStringSubmatchIndex(text, -1) {
		id, _ := strconv.Atoi(text[match[2]:match[3]])
		if !validIDs[id] {
			continue
		}
		start := utf8.RuneCountInString(text[:match[0]])
		markers = append(markers, span{start: start, end: start + utf8.RuneCountInString(text[match[0]:match[1]])})
	}
	if len(markers) == 0 {
		return base(text)
	}

	baseSegments := base(text)
	result := make([]segment, 0, len(baseSegments))
	markerIdx := 0
	for _, seg := range baseSegments {
		for markerIdx < len(markers) && markers[markerIdx].end <= seg.index {
			markerIdx++
		}
		if markerIdx < len(markers) {
			marker := markers[markerIdx]
			if seg.index >= marker.start && seg.index < marker.end {
				if seg.index == marker.start {
					result = append(result, segment{text: runeSlice(text, marker.start, marker.end), index: marker.start, wordLike: true})
				}
				continue
			}
		}
		result = append(result, seg)
	}
	return result
}

// textChunk is one word-wrapped piece of a line; indices are rune offsets
// into the original line.
type textChunk struct {
	text       string
	startIndex int
	endIndex   int
}

// TextChunk is one upstream-compatible UTF-16-indexed wrapped segment.
type TextChunk struct {
	Text       string
	StartIndex int
	EndIndex   int
}

func isCJKBreakGrapheme(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool {
		low, high := 0, len(cjkBreakRanges)
		for low < high {
			middle := low + (high-low)/2
			candidate := cjkBreakRanges[middle]
			switch {
			case r < candidate[0]:
				high = middle
			case r > candidate[1]:
				low = middle + 1
			default:
				return true
			}
		}
		return false
	})
}

// Ranges match upstream's union of the Han, Hiragana, Katakana, Hangul, and
// Bopomofo Script_Extensions Unicode properties at the pinned Node runtime.
var cjkBreakRanges = [][2]rune{
	{0x00B7, 0x00B7}, {0x02C7, 0x02C7}, {0x02C9, 0x02CB}, {0x02D9, 0x02D9},
	{0x02EA, 0x02EB}, {0x0305, 0x0305}, {0x0323, 0x0323}, {0x1100, 0x11FF},
	{0x2E80, 0x2E99}, {0x2E9B, 0x2EF3}, {0x2F00, 0x2FD5}, {0x2FF0, 0x2FFF},
	{0x3001, 0x3003}, {0x3005, 0x3011}, {0x3013, 0x301F}, {0x3021, 0x3035},
	{0x3037, 0x303F}, {0x3041, 0x3096}, {0x3099, 0x30FF}, {0x3105, 0x312F},
	{0x3131, 0x318E}, {0x3190, 0x31E5}, {0x31EF, 0x321E}, {0x3220, 0x3247},
	{0x3260, 0x327E}, {0x3280, 0x32B0}, {0x32C0, 0x32CB}, {0x32D0, 0x3370},
	{0x337B, 0x337F}, {0x33E0, 0x33FE}, {0x3400, 0x4DBF}, {0x4E00, 0x9FFF},
	{0xA700, 0xA707}, {0xA960, 0xA97C}, {0xAC00, 0xD7A3}, {0xD7B0, 0xD7C6},
	{0xD7CB, 0xD7FB}, {0xF900, 0xFA6D}, {0xFA70, 0xFAD9}, {0xFE45, 0xFE46},
	{0xFF61, 0xFFBE}, {0xFFC2, 0xFFC7}, {0xFFCA, 0xFFCF}, {0xFFD2, 0xFFD7},
	{0xFFDA, 0xFFDC}, {0x16FE2, 0x16FE3}, {0x16FF0, 0x16FF6}, {0x1AFF0, 0x1AFF3},
	{0x1AFF5, 0x1AFFB}, {0x1AFFD, 0x1AFFE}, {0x1B000, 0x1B122}, {0x1B132, 0x1B132},
	{0x1B150, 0x1B152}, {0x1B155, 0x1B155}, {0x1B164, 0x1B167}, {0x1D360, 0x1D371},
	{0x1F200, 0x1F200}, {0x1F250, 0x1F251}, {0x20000, 0x2A6DF}, {0x2A700, 0x2B81D},
	{0x2B820, 0x2CEAD}, {0x2CEB0, 0x2EBE0}, {0x2EBF0, 0x2EE5D}, {0x2F800, 0x2FA1D},
	{0x30000, 0x3134A}, {0x31350, 0x33479},
}

// wordWrapLine splits a line into word-wrapped chunks, wrapping at word
// boundaries when possible and at character level for oversized words.
func wordWrapLine(line string, maxWidth int, preSegmented []segment) []textChunk {
	if line == "" || maxWidth <= 0 {
		return []textChunk{{}}
	}
	if VisibleWidth(line) <= maxWidth {
		return []textChunk{{text: line, startIndex: 0, endIndex: runeLen(line)}}
	}

	segments := preSegmented
	if segments == nil {
		segments = graphemeSegments(line)
	}

	var chunks []textChunk
	currentWidth := 0
	chunkStart := 0

	// Wrap opportunity: the rune position after the last whitespace before a
	// non-whitespace grapheme, i.e. where a line break is allowed.
	wrapOppIndex := -1
	wrapOppWidth := 0

	for i := 0; i < len(segments); i++ {
		seg := segments[i]
		grapheme := seg.text
		gWidth := VisibleWidth(grapheme)
		charIndex := seg.index
		isWs := !isPasteMarker(grapheme) && isWhitespaceChar(grapheme)

		if currentWidth+gWidth > maxWidth {
			if wrapOppIndex >= 0 && currentWidth-wrapOppWidth+gWidth <= maxWidth {
				chunks = append(chunks, textChunk{text: runeSlice(line, chunkStart, wrapOppIndex), startIndex: chunkStart, endIndex: wrapOppIndex})
				chunkStart = wrapOppIndex
				currentWidth -= wrapOppWidth
			} else if chunkStart < charIndex {
				chunks = append(chunks, textChunk{text: runeSlice(line, chunkStart, charIndex), startIndex: chunkStart, endIndex: charIndex})
				chunkStart = charIndex
				currentWidth = 0
			}
			wrapOppIndex = -1
		}

		if gWidth > maxWidth {
			// Single atomic segment wider than maxWidth (e.g. paste marker in
			// a narrow terminal): re-wrap at grapheme granularity; the split
			// is purely visual.
			subChunks := wordWrapLine(grapheme, maxWidth, nil)
			for j := 0; j < len(subChunks)-1; j++ {
				sc := subChunks[j]
				chunks = append(chunks, textChunk{text: sc.text, startIndex: charIndex + sc.startIndex, endIndex: charIndex + sc.endIndex})
			}
			last := subChunks[len(subChunks)-1]
			chunkStart = charIndex + last.startIndex
			currentWidth = VisibleWidth(last.text)
			wrapOppIndex = -1
			continue
		}

		currentWidth += gWidth

		var next *segment
		if i+1 < len(segments) {
			next = &segments[i+1]
		}
		if isWs && next != nil && (isPasteMarker(next.text) || !isWhitespaceChar(next.text)) {
			wrapOppIndex = next.index
			wrapOppWidth = currentWidth
		} else if !isWs && next != nil && !isWhitespaceChar(next.text) {
			isCJK := !isPasteMarker(grapheme) && isCJKBreakGrapheme(grapheme)
			nextIsCJK := !isPasteMarker(next.text) && isCJKBreakGrapheme(next.text)
			if isCJK || nextIsCJK {
				wrapOppIndex = next.index
				wrapOppWidth = currentWidth
			}
		}
	}

	chunks = append(chunks, textChunk{text: runeSliceFrom(line, chunkStart), startIndex: chunkStart, endIndex: runeLen(line)})
	return chunks
}

// WordWrapLine exposes upstream's default word-wrapping helper.
func WordWrapLine(line string, maxWidth int) []TextChunk {
	chunks := wordWrapLine(line, maxWidth, nil)
	result := make([]TextChunk, len(chunks))
	for index, chunk := range chunks {
		result[index] = TextChunk{
			Text:       chunk.text,
			StartIndex: utf16Length(runeSlice(line, 0, chunk.startIndex)),
			EndIndex:   utf16Length(runeSlice(line, 0, chunk.endIndex)),
		}
	}
	return result
}

type editorState struct {
	lines      []string
	cursorLine int
	cursorCol  int // rune index
}

func (state editorState) clone() editorState {
	return editorState{lines: append([]string(nil), state.lines...), cursorLine: state.cursorLine, cursorCol: state.cursorCol}
}

type layoutLine struct {
	text      string
	hasCursor bool
	cursorPos int
}

type visualLine struct {
	logicalLine int
	startCol    int
	length      int
}

type EditorTheme struct {
	BorderColor StyleFunc
	SelectList  SelectListTheme
}

var slashCommandSelectListLayout = SelectListLayoutOptions{MinPrimaryColumnWidth: 12, MaxPrimaryColumnWidth: 32}

const attachmentAutocompleteDebounce = 20 * time.Millisecond

var defaultAutocompleteTriggerCharacters = []string{"@", "#"}

func matchesAutocompleteTrigger(text string, triggerCharacters []string) bool {
	runes := []rune(text)
	start := 0
	for index, r := range runes {
		if isWhitespaceRune(r) {
			start = index + 1
		}
	}
	return start < len(runes) && containsString(triggerCharacters, string(runes[start]))
}

func matchesAutocompleteDebounce(text string, triggerCharacters []string) bool {
	runes := []rune(text)
	starts := []int{0}
	for index, r := range runes {
		if r == ' ' || r == '\t' {
			starts = append(starts, index+1)
		}
	}
	for index := len(starts) - 1; index >= 0; index-- {
		token := runes[starts[index]:]
		if len(token) == 0 {
			continue
		}
		if token[0] == '@' {
			remainder := token[1:]
			if len(remainder) > 0 && remainder[0] == '"' {
				if !strings.ContainsRune(string(remainder[1:]), '"') {
					return true
				}
			}
			if !strings.ContainsFunc(string(remainder), isWhitespaceRune) {
				return true
			}
			continue
		}
		if containsString(triggerCharacters, string(token[0])) && token[0] != '@' &&
			!strings.ContainsFunc(string(token[1:]), isWhitespaceRune) {
			return true
		}
	}
	return false
}

const (
	segmentModeWord     = "word"
	segmentModeGrapheme = "grapheme"
)

// Editor is the multi-line text editor: undo, kill ring, word navigation,
// paste collapse, prompt history, autocomplete.
type Editor struct {
	mu      sync.Mutex
	state   editorState
	focused bool
	ui      *TUI
	theme   EditorTheme

	paddingX     int
	lastWidth    int
	scrollOffset int

	borderColor StyleFunc

	autocompleteProvider          AutocompleteProvider
	autocompleteTriggerCharacters []string
	autocompleteList              *SelectList
	autocompleteState             string // "", "regular", "force"
	autocompletePrefix            string
	autocompleteMaxVisible        int
	autocompleteCancel            context.CancelFunc
	autocompleteDebounceTimer     *time.Timer
	autocompleteStartToken        int
	autocompleteRequestID         int
	autocompleteBusy              int
	autocompleteIdle              *sync.Cond
	autocompleteRunMu             sync.Mutex

	pastes       map[int]string
	pasteCounter int
	pasteBuffer  string
	isInPaste    bool

	history      []string
	historyIndex int // -1 = not browsing
	historyDraft *editorState

	killRing   killRing
	lastAction string // "kill" | "yank" | "type-word" | ""

	jumpMode string // "", "forward", "backward"

	preferredVisualCol   int // -1 = unset (sticky column)
	snappedFromCursorCol int // -1 = unset (pre-snap position on atomic segments)

	undoStack undoStack[editorState]

	pending []func()

	OnSubmit      func(string)
	OnChange      func(string)
	DisableSubmit bool

	// InputInterceptor is called before default key handling. Return true to
	// consume the event and suppress further processing.
	InputInterceptor func(KeyEvent) bool
}

func NewEditor(ui *TUI, theme EditorTheme) *Editor {
	borderColor := theme.BorderColor
	if borderColor == nil {
		borderColor = func(value string) string { return value }
	}
	editor := &Editor{
		state:                         editorState{lines: []string{""}},
		ui:                            ui,
		theme:                         theme,
		lastWidth:                     80,
		borderColor:                   borderColor,
		autocompleteTriggerCharacters: append([]string(nil), defaultAutocompleteTriggerCharacters...),
		autocompleteMaxVisible:        5,
		pastes:                        map[int]string{},
		historyIndex:                  -1,
		preferredVisualCol:            -1,
		snappedFromCursorCol:          -1,
	}
	editor.autocompleteIdle = sync.NewCond(&editor.mu)
	return editor
}

func (editor *Editor) SetFocused(focused bool) {
	editor.mu.Lock()
	editor.focused = focused
	editor.mu.Unlock()
}

func (editor *Editor) SetBorderColor(color StyleFunc) {
	editor.mu.Lock()
	if color == nil {
		color = func(value string) string { return value }
	}
	editor.borderColor = color
	editor.mu.Unlock()
}

func (editor *Editor) GetBorderColor() StyleFunc {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return editor.borderColor
}

// validPasteIDs is the set of currently valid paste IDs for marker-aware
// segmentation.
func (editor *Editor) validPasteIDs() map[int]bool {
	ids := make(map[int]bool, len(editor.pastes))
	for id := range editor.pastes {
		ids[id] = true
	}
	return ids
}

func (editor *Editor) segment(text, mode string) []segment {
	base := graphemeSegments
	if mode == segmentModeWord {
		base = wordSegments
	}
	return segmentWithMarkers(text, base, editor.validPasteIDs())
}

func (editor *Editor) GetPaddingX() int {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return editor.paddingX
}

func (editor *Editor) SetPaddingX(padding int) {
	editor.mu.Lock()
	padding = max(0, padding)
	changed := editor.paddingX != padding
	editor.paddingX = padding
	editor.mu.Unlock()
	if changed {
		editor.ui.RequestRender()
	}
}

func (editor *Editor) GetAutocompleteMaxVisible() int {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return editor.autocompleteMaxVisible
}

func (editor *Editor) SetAutocompleteMaxVisible(maxVisible int) {
	editor.mu.Lock()
	maxVisible = max(3, min(20, maxVisible))
	changed := editor.autocompleteMaxVisible != maxVisible
	editor.autocompleteMaxVisible = maxVisible
	editor.mu.Unlock()
	if changed {
		editor.ui.RequestRender()
	}
}

func (editor *Editor) SetAutocompleteProvider(provider AutocompleteProvider) {
	extra := []string(nil)
	if triggers, ok := provider.(TriggerCharacterProvider); ok {
		extra = triggers.TriggerCharacters()
	}
	editor.mu.Lock()
	defer editor.mu.Unlock()
	editor.cancelAutocomplete()
	editor.autocompleteProvider = provider
	editor.setAutocompleteTriggerCharacters(extra)
}

// AddToHistory records a prompt for up/down navigation, skipping empties and
// consecutive duplicates, keeping at most 100 entries.
func (editor *Editor) AddToHistory(text string) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	trimmed := trimWhitespace(text)
	if trimmed == "" {
		return
	}
	if len(editor.history) > 0 && editor.history[0] == trimmed {
		return
	}
	editor.history = append([]string{trimmed}, editor.history...)
	if len(editor.history) > 100 {
		editor.history = editor.history[:100]
	}
}

func (editor *Editor) line(index int) string {
	if index < 0 || index >= len(editor.state.lines) {
		return ""
	}
	return editor.state.lines[index]
}

func (editor *Editor) currentLine() string { return editor.line(editor.state.cursorLine) }

func (editor *Editor) isEditorEmpty() bool {
	return len(editor.state.lines) == 1 && editor.state.lines[0] == ""
}

func (editor *Editor) isOnFirstVisualLine() bool {
	visualLines := editor.buildVisualLineMap(editor.lastWidth)
	return editor.findCurrentVisualLine(visualLines) == 0
}

func (editor *Editor) isOnLastVisualLine() bool {
	visualLines := editor.buildVisualLineMap(editor.lastWidth)
	return editor.findCurrentVisualLine(visualLines) == len(visualLines)-1
}

func (editor *Editor) navigateHistory(direction int) {
	editor.lastAction = ""
	if len(editor.history) == 0 {
		return
	}
	newIndex := editor.historyIndex - direction // Up(-1) increases, Down(1) decreases
	if newIndex < -1 || newIndex >= len(editor.history) {
		return
	}
	if editor.historyIndex == -1 && newIndex >= 0 {
		editor.pushUndoSnapshot()
		draft := editor.state.clone()
		editor.historyDraft = &draft
	}
	editor.historyIndex = newIndex
	if editor.historyIndex == -1 {
		draft := editor.historyDraft
		editor.historyDraft = nil
		if draft != nil {
			editor.state = *draft
			editor.preferredVisualCol = -1
			editor.snappedFromCursorCol = -1
			editor.scrollOffset = 0
			editor.emitChange()
		} else {
			editor.setTextInternal("", "end")
		}
	} else {
		placement := "end"
		if direction == -1 {
			placement = "start"
		}
		editor.setTextInternal(editor.history[editor.historyIndex], placement)
	}
}

func (editor *Editor) exitHistoryBrowsing() {
	editor.historyIndex = -1
	editor.historyDraft = nil
}

// setTextInternal sets text without resetting history state (used by
// navigateHistory).
func (editor *Editor) setTextInternal(text, cursorPlacement string) {
	lines := strings.Split(text, "\n")
	editor.state.lines = lines
	if cursorPlacement == "start" {
		editor.state.cursorLine = 0
		editor.setCursorCol(0)
	} else {
		editor.state.cursorLine = len(editor.state.lines) - 1
		editor.setCursorCol(runeLen(editor.currentLine()))
	}
	editor.scrollOffset = 0
	editor.emitChange()
}

func (editor *Editor) Invalidate() {}

func (editor *Editor) Render(width int) []string {
	editor.mu.Lock()
	defer editor.mu.Unlock()

	maxPadding := max(0, (width-1)/2)
	paddingX := min(editor.paddingX, maxPadding)
	contentWidth := max(1, width-paddingX*2)

	// Layout width: with padding the cursor can overflow into it, without
	// padding we reserve one column for the cursor.
	layoutWidth := contentWidth
	if paddingX == 0 {
		layoutWidth = max(1, contentWidth-1)
	}
	editor.lastWidth = layoutWidth

	horizontal := editor.borderColor("─")
	layoutLines := editor.layoutText(layoutWidth)

	terminalRows := editor.ui.Terminal().Rows()
	maxVisibleLines := max(5, terminalRows*3/10)

	cursorLineIndex := 0
	for index, line := range layoutLines {
		if line.hasCursor {
			cursorLineIndex = index
			break
		}
	}
	if cursorLineIndex < editor.scrollOffset {
		editor.scrollOffset = cursorLineIndex
	} else if cursorLineIndex >= editor.scrollOffset+maxVisibleLines {
		editor.scrollOffset = cursorLineIndex - maxVisibleLines + 1
	}
	maxScrollOffset := max(0, len(layoutLines)-maxVisibleLines)
	editor.scrollOffset = max(0, min(editor.scrollOffset, maxScrollOffset))

	visibleLines := layoutLines[editor.scrollOffset:min(editor.scrollOffset+maxVisibleLines, len(layoutLines))]

	result := make([]string, 0, len(visibleLines)+2)
	leftPadding := strings.Repeat(" ", paddingX)
	rightPadding := leftPadding

	if editor.scrollOffset > 0 {
		indicator := fmt.Sprintf("─── ↑ %d more ", editor.scrollOffset)
		remaining := width - VisibleWidth(indicator)
		if remaining >= 0 {
			result = append(result, editor.borderColor(indicator+strings.Repeat("─", remaining)))
		} else {
			result = append(result, editor.borderColor(TruncateToWidth(indicator, width, "...", false)))
		}
	} else {
		result = append(result, strings.Repeat(horizontal, width))
	}

	// Emit the hardware cursor marker when focused so TUI can position the
	// cursor for IME candidate windows even while autocomplete is visible.
	emitCursorMarker := editor.focused

	for _, line := range visibleLines {
		displayText := line.text
		lineVisibleWidth := VisibleWidth(line.text)
		cursorInPadding := false

		if line.hasCursor {
			before := runeSlice(displayText, 0, line.cursorPos)
			after := runeSliceFrom(displayText, line.cursorPos)
			marker := ""
			if emitCursorMarker {
				marker = CursorMarker
			}
			if after != "" {
				afterGraphemes := editor.segment(after, segmentModeGrapheme)
				firstGrapheme := ""
				if len(afterGraphemes) > 0 {
					firstGrapheme = afterGraphemes[0].text
				}
				restAfter := runeSliceFrom(after, runeLen(firstGrapheme))
				displayText = before + marker + "\x1b[7m" + firstGrapheme + "\x1b[0m" + restAfter
			} else {
				displayText = before + marker + "\x1b[7m \x1b[0m"
				lineVisibleWidth++
				if lineVisibleWidth > contentWidth && paddingX > 0 {
					cursorInPadding = true
				}
			}
		}

		padding := strings.Repeat(" ", max(0, contentWidth-lineVisibleWidth))
		lineRightPadding := rightPadding
		if cursorInPadding {
			lineRightPadding = rightPadding[1:]
		}
		result = append(result, leftPadding+displayText+padding+lineRightPadding)
	}

	linesBelow := len(layoutLines) - (editor.scrollOffset + len(visibleLines))
	if linesBelow > 0 {
		indicator := fmt.Sprintf("─── ↓ %d more ", linesBelow)
		remaining := width - VisibleWidth(indicator)
		result = append(result, editor.borderColor(indicator+strings.Repeat("─", max(0, remaining))))
	} else {
		result = append(result, strings.Repeat(horizontal, width))
	}

	if editor.autocompleteState != "" && editor.autocompleteList != nil {
		for _, line := range editor.autocompleteList.Render(contentWidth) {
			linePadding := strings.Repeat(" ", max(0, contentWidth-VisibleWidth(line)))
			result = append(result, leftPadding+line+linePadding+rightPadding)
		}
	}

	return result
}

func (editor *Editor) HandleInput(event KeyEvent) {
	if editor.InputInterceptor != nil && editor.InputInterceptor(event) {
		return
	}
	editor.mu.Lock()
	editor.handleData(event.Raw)
	pending := editor.pending
	editor.pending = nil
	editor.mu.Unlock()
	for _, callback := range pending {
		callback()
	}
}

func (editor *Editor) handleData(data string) {
	kb := GetKeybindings()

	// Character jump mode: awaiting the character to jump to.
	if editor.jumpMode != "" {
		if kb.Matches(data, "tui.editor.jumpForward") || kb.Matches(data, "tui.editor.jumpBackward") {
			editor.jumpMode = ""
			return
		}
		printable := DecodePrintableKey(data)
		if printable == "" {
			if r, _ := utf8.DecodeRuneInString(data); data != "" && r >= 32 {
				printable = data
			}
		}
		if printable != "" {
			direction := editor.jumpMode
			editor.jumpMode = ""
			editor.jumpToChar(printable, direction)
			return
		}
		// Control character: cancel and fall through to normal handling.
		editor.jumpMode = ""
	}

	// Bracketed paste mode.
	if strings.Contains(data, "\x1b[200~") {
		editor.isInPaste = true
		editor.pasteBuffer = ""
		data = strings.Replace(data, "\x1b[200~", "", 1)
	}
	if editor.isInPaste {
		editor.pasteBuffer += data
		if endIndex := strings.Index(editor.pasteBuffer, "\x1b[201~"); endIndex != -1 {
			pasteContent := editor.pasteBuffer[:endIndex]
			if pasteContent != "" {
				editor.handlePaste(pasteContent)
			}
			editor.isInPaste = false
			remaining := editor.pasteBuffer[endIndex+6:]
			editor.pasteBuffer = ""
			if remaining != "" {
				editor.handleData(remaining)
			}
		}
		return
	}

	// Ctrl+C: let the parent handle (exit/clear).
	if kb.Matches(data, "tui.input.copy") {
		return
	}

	if kb.Matches(data, "tui.editor.undo") {
		editor.undo()
		return
	}

	// Autocomplete mode.
	if editor.autocompleteState != "" && editor.autocompleteList != nil {
		if kb.Matches(data, "tui.select.cancel") {
			editor.cancelAutocomplete()
			return
		}
		if kb.Matches(data, "tui.select.up") || kb.Matches(data, "tui.select.down") {
			editor.autocompleteList.HandleInput(keyEventFor(data))
			return
		}
		if kb.Matches(data, "tui.input.tab") {
			if item, ok := editor.autocompleteList.GetSelectedItem(); ok && editor.autocompleteProvider != nil {
				editor.pushUndoSnapshot()
				editor.lastAction = ""
				editor.applyCompletionResult(item)
				editor.cancelAutocomplete()
				editor.emitChange()
			}
			return
		}
		if kb.Matches(data, "tui.select.confirm") {
			if item, ok := editor.autocompleteList.GetSelectedItem(); ok && editor.autocompleteProvider != nil {
				editor.pushUndoSnapshot()
				editor.lastAction = ""
				editor.applyCompletionResult(item)
				if strings.HasPrefix(editor.autocompletePrefix, "/") {
					editor.cancelAutocomplete()
					// Fall through to submit.
				} else {
					editor.cancelAutocomplete()
					editor.emitChange()
					return
				}
			}
		}
	}

	// Tab: trigger completion.
	if kb.Matches(data, "tui.input.tab") && editor.autocompleteState == "" {
		editor.handleTabCompletion()
		return
	}

	// Deletion actions.
	if kb.Matches(data, "tui.editor.deleteToLineEnd") {
		editor.deleteToEndOfLine()
		return
	}
	if kb.Matches(data, "tui.editor.deleteToLineStart") {
		editor.deleteToStartOfLine()
		return
	}
	if kb.Matches(data, "tui.editor.deleteWordBackward") {
		editor.deleteWordBackwards()
		return
	}
	if kb.Matches(data, "tui.editor.deleteWordForward") {
		editor.deleteWordForward()
		return
	}
	if kb.Matches(data, "tui.editor.deleteCharBackward") || MatchesKey(data, "shift+backspace") {
		editor.handleBackspace()
		return
	}
	if kb.Matches(data, "tui.editor.deleteCharForward") || MatchesKey(data, "shift+delete") {
		editor.handleForwardDelete()
		return
	}

	// Kill ring actions.
	if kb.Matches(data, "tui.editor.yank") {
		editor.yank()
		return
	}
	if kb.Matches(data, "tui.editor.yankPop") {
		editor.yankPop()
		return
	}

	// Cursor movement actions.
	if kb.Matches(data, "tui.editor.cursorLineStart") {
		editor.moveToLineStart()
		return
	}
	if kb.Matches(data, "tui.editor.cursorLineEnd") {
		editor.moveToLineEnd()
		return
	}
	if kb.Matches(data, "tui.editor.cursorWordLeft") {
		editor.moveWordBackwards()
		return
	}
	if kb.Matches(data, "tui.editor.cursorWordRight") {
		editor.moveWordForwards()
		return
	}

	// New line.
	if kb.Matches(data, "tui.input.newLine") ||
		(len(data) > 1 && data[0] == '\n') ||
		data == "\x1b\r" ||
		data == "\x1b[13;2~" ||
		(len(data) > 1 && strings.Contains(data, "\x1b") && strings.Contains(data, "\r")) ||
		data == "\n" {
		if editor.shouldSubmitOnBackslashEnter(data, kb) {
			editor.handleBackspace()
			editor.submitValue()
			return
		}
		editor.addNewLine()
		return
	}

	// Submit (Enter).
	if kb.Matches(data, "tui.input.submit") {
		if editor.DisableSubmit {
			return
		}
		// Workaround for terminals without Shift+Enter support: a backslash
		// before the cursor turns Enter into a newline.
		currentLine := []rune(editor.currentLine())
		if editor.state.cursorCol > 0 && editor.state.cursorCol <= len(currentLine) && currentLine[editor.state.cursorCol-1] == '\\' {
			editor.handleBackspace()
			editor.addNewLine()
			return
		}
		editor.submitValue()
		return
	}

	// Arrow key navigation (with history support).
	if kb.Matches(data, "tui.editor.cursorUp") {
		if editor.isOnFirstVisualLine() && (editor.isEditorEmpty() || editor.historyIndex > -1 || editor.state.cursorCol == 0) {
			editor.navigateHistory(-1)
		} else if editor.isOnFirstVisualLine() {
			editor.moveToLineStart()
		} else {
			editor.moveCursor(-1, 0)
		}
		return
	}
	if kb.Matches(data, "tui.editor.cursorDown") {
		if editor.historyIndex > -1 && editor.isOnLastVisualLine() {
			editor.navigateHistory(1)
		} else if editor.isOnLastVisualLine() {
			editor.moveToLineEnd()
		} else {
			editor.moveCursor(1, 0)
		}
		return
	}
	if kb.Matches(data, "tui.editor.cursorRight") {
		editor.moveCursor(0, 1)
		return
	}
	if kb.Matches(data, "tui.editor.cursorLeft") {
		editor.moveCursor(0, -1)
		return
	}

	if kb.Matches(data, "tui.editor.pageUp") {
		editor.pageScroll(-1)
		return
	}
	if kb.Matches(data, "tui.editor.pageDown") {
		editor.pageScroll(1)
		return
	}

	if kb.Matches(data, "tui.editor.jumpForward") {
		editor.jumpMode = "forward"
		return
	}
	if kb.Matches(data, "tui.editor.jumpBackward") {
		editor.jumpMode = "backward"
		return
	}

	if MatchesKey(data, "shift+space") {
		editor.insertCharacter(" ", false)
		return
	}

	if printable := DecodePrintableKey(data); printable != "" {
		editor.insertCharacter(printable, false)
		return
	}

	if r, _ := utf8.DecodeRuneInString(data); data != "" && r >= 32 {
		editor.insertCharacter(data, false)
	}
}

func (editor *Editor) applyCompletionResult(item SelectItem) {
	provider := editor.autocompleteProvider
	lines := append([]string(nil), editor.state.lines...)
	cursorLine, cursorCol := editor.state.cursorLine, editor.state.cursorCol
	prefix := editor.autocompletePrefix
	editor.mu.Unlock()
	result := provider.ApplyCompletion(
		lines,
		cursorLine,
		cursorCol,
		AutocompleteItem(item),
		prefix,
	)
	editor.mu.Lock()
	editor.state.lines = result.Lines
	editor.state.cursorLine = result.CursorLine
	editor.setCursorCol(result.CursorCol)
}

func (editor *Editor) layoutText(contentWidth int) []layoutLine {
	if len(editor.state.lines) == 0 || (len(editor.state.lines) == 1 && editor.state.lines[0] == "") {
		return []layoutLine{{text: "", hasCursor: true, cursorPos: 0}}
	}

	var layoutLines []layoutLine
	for i := range editor.state.lines {
		line := editor.line(i)
		isCurrentLine := i == editor.state.cursorLine

		if VisibleWidth(line) <= contentWidth {
			entry := layoutLine{text: line, hasCursor: isCurrentLine}
			if isCurrentLine {
				entry.cursorPos = editor.state.cursorCol
			}
			layoutLines = append(layoutLines, entry)
			continue
		}

		chunks := wordWrapLine(line, contentWidth, editor.segment(line, segmentModeGrapheme))
		for chunkIndex, chunk := range chunks {
			cursorPos := editor.state.cursorCol
			isLastChunk := chunkIndex == len(chunks)-1

			hasCursorInChunk := false
			adjustedCursorPos := 0
			if isCurrentLine {
				if isLastChunk {
					hasCursorInChunk = cursorPos >= chunk.startIndex
					adjustedCursorPos = cursorPos - chunk.startIndex
				} else {
					hasCursorInChunk = cursorPos >= chunk.startIndex && cursorPos < chunk.endIndex
					if hasCursorInChunk {
						adjustedCursorPos = cursorPos - chunk.startIndex
						// Clamp for cursors sitting in trimmed whitespace.
						if adjustedCursorPos > runeLen(chunk.text) {
							adjustedCursorPos = runeLen(chunk.text)
						}
					}
				}
			}
			if hasCursorInChunk {
				layoutLines = append(layoutLines, layoutLine{text: chunk.text, hasCursor: true, cursorPos: adjustedCursorPos})
			} else {
				layoutLines = append(layoutLines, layoutLine{text: chunk.text})
			}
		}
	}
	return layoutLines
}

func (editor *Editor) GetText() string {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return editor.getTextLocked()
}

func (editor *Editor) getTextLocked() string { return strings.Join(editor.state.lines, "\n") }

func (editor *Editor) expandPasteMarkers(text string) string {
	for pasteID := 1; pasteID <= editor.pasteCounter; pasteID++ {
		pasteContent, ok := editor.pastes[pasteID]
		if !ok {
			continue
		}
		markerRegex := regexp.MustCompile(fmt.Sprintf(`\[paste #%d( (\+\d+ lines|\d+ chars))?\]`, pasteID))
		text = markerRegex.ReplaceAllStringFunc(text, func(string) string { return pasteContent })
	}
	return text
}

// GetExpandedText returns the text with paste markers expanded to their
// actual content (e.g. for an external editor).
func (editor *Editor) GetExpandedText() string {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return editor.expandPasteMarkers(editor.getTextLocked())
}

func (editor *Editor) GetLines() []string {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return append([]string(nil), editor.state.lines...)
}

// GetCursor returns the upstream-compatible UTF-16 column offset.
func (editor *Editor) GetCursor() (line, col int) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	line = editor.state.cursorLine
	col = utf16Length(runeSlice(editor.currentLine(), 0, editor.state.cursorCol))
	return line, col
}

func (editor *Editor) SetText(text string) {
	editor.mu.Lock()
	editor.cancelAutocomplete()
	editor.lastAction = ""
	editor.exitHistoryBrowsing()
	editor.pastes = map[int]string{}
	editor.pasteCounter = 0
	normalized := normalizeEditorText(text)
	if editor.getTextLocked() != normalized {
		editor.pushUndoSnapshot()
	}
	editor.setTextInternal(normalized, "end")
	pending := editor.pending
	editor.pending = nil
	editor.mu.Unlock()
	for _, callback := range pending {
		callback()
	}
}

// InsertTextAtCursor inserts text at the cursor as one undoable unit.
func (editor *Editor) InsertTextAtCursor(text string) {
	if text == "" {
		return
	}
	editor.mu.Lock()
	editor.cancelAutocomplete()
	editor.pushUndoSnapshot()
	editor.lastAction = ""
	editor.exitHistoryBrowsing()
	editor.insertTextAtCursorInternal(text)
	pending := editor.pending
	editor.pending = nil
	editor.mu.Unlock()
	for _, callback := range pending {
		callback()
	}
}

// normalizeEditorText normalizes line endings and expands tabs to 4 spaces.
func normalizeEditorText(text string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n", "\t", "    ").Replace(text)
}

func (editor *Editor) insertTextAtCursorInternal(text string) {
	if text == "" {
		return
	}
	normalized := normalizeEditorText(text)
	insertedLines := strings.Split(normalized, "\n")

	currentLine := editor.currentLine()
	beforeCursor := runeSlice(currentLine, 0, editor.state.cursorCol)
	afterCursor := runeSliceFrom(currentLine, editor.state.cursorCol)

	if len(insertedLines) == 1 {
		editor.state.lines[editor.state.cursorLine] = beforeCursor + normalized + afterCursor
		editor.setCursorCol(editor.state.cursorCol + runeLen(normalized))
	} else {
		newLines := make([]string, 0, len(editor.state.lines)+len(insertedLines))
		newLines = append(newLines, editor.state.lines[:editor.state.cursorLine]...)
		newLines = append(newLines, beforeCursor+insertedLines[0])
		newLines = append(newLines, insertedLines[1:len(insertedLines)-1]...)
		newLines = append(newLines, insertedLines[len(insertedLines)-1]+afterCursor)
		newLines = append(newLines, editor.state.lines[editor.state.cursorLine+1:]...)
		editor.state.lines = newLines
		editor.state.cursorLine += len(insertedLines) - 1
		editor.setCursorCol(runeLen(insertedLines[len(insertedLines)-1]))
	}
	editor.emitChange()
}

func (editor *Editor) insertCharacter(char string, skipUndoCoalescing bool) {
	editor.exitHistoryBrowsing()

	// Undo coalescing (fish-style): consecutive word chars coalesce; each
	// space is separately undoable and captures state before itself.
	if !skipUndoCoalescing {
		if isWhitespaceChar(char) || editor.lastAction != "type-word" {
			editor.pushUndoSnapshot()
		}
		editor.lastAction = "type-word"
	}

	line := editor.currentLine()
	before := runeSlice(line, 0, editor.state.cursorCol)
	after := runeSliceFrom(line, editor.state.cursorCol)
	editor.state.lines[editor.state.cursorLine] = before + char + after
	editor.setCursorCol(editor.state.cursorCol + runeLen(char))

	editor.emitChange()

	if editor.autocompleteState != "" {
		editor.updateAutocomplete()
		return
	}
	switch {
	case char == "/" && editor.isAtStartOfMessage():
		editor.tryTriggerAutocomplete()
	case containsString(editor.autocompleteTriggerCharacters, char):
		currentLine := editor.currentLine()
		textBeforeCursor := []rune(runeSlice(currentLine, 0, editor.state.cursorCol))
		var charBeforeSymbol rune
		if len(textBeforeCursor) >= 2 {
			charBeforeSymbol = textBeforeCursor[len(textBeforeCursor)-2]
		}
		if len(textBeforeCursor) == 1 || charBeforeSymbol == ' ' || charBeforeSymbol == '\t' {
			editor.tryTriggerAutocomplete()
		}
	case isSlashWordChar(char):
		currentLine := editor.currentLine()
		textBeforeCursor := runeSlice(currentLine, 0, editor.state.cursorCol)
		if editor.isInSlashCommandContext(textBeforeCursor) {
			editor.tryTriggerAutocomplete()
		} else if matchesAutocompleteTrigger(textBeforeCursor, editor.autocompleteTriggerCharacters) {
			editor.tryTriggerAutocomplete()
		}
	}
}

func isSlashWordChar(char string) bool {
	runes := []rune(char)
	if len(runes) != 1 {
		return false
	}
	r := runes[0]
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

var csiUCtrlInPaste = regexp.MustCompile(`\x1b\[(\d+);5u`)

func (editor *Editor) handlePaste(pastedText string) {
	editor.cancelAutocomplete()
	editor.exitHistoryBrowsing()
	editor.lastAction = ""
	editor.pushUndoSnapshot()

	// Some terminals re-encode control bytes inside bracketed paste as CSI-u
	// Ctrl+<letter> sequences; decode them back so newlines survive.
	decodedText := csiUCtrlInPaste.ReplaceAllStringFunc(pastedText, func(match string) string {
		sub := csiUCtrlInPaste.FindStringSubmatch(match)
		code, _ := strconv.Atoi(sub[1])
		if code >= 97 && code <= 122 {
			return string(rune(code - 96))
		}
		if code >= 65 && code <= 90 {
			return string(rune(code - 64))
		}
		return match
	})

	cleanText := normalizeEditorText(decodedText)

	var filtered strings.Builder
	for _, r := range cleanText {
		if r == '\n' || r >= 32 {
			filtered.WriteRune(r)
		}
	}
	filteredText := filtered.String()

	// When pasting a file path after a word character, prepend a space.
	if len(filteredText) > 0 && strings.ContainsRune("/~.", rune(filteredText[0])) {
		currentLine := []rune(editor.currentLine())
		if editor.state.cursorCol > 0 && editor.state.cursorCol <= len(currentLine) {
			before := currentLine[editor.state.cursorCol-1]
			if (before >= 'a' && before <= 'z') || (before >= 'A' && before <= 'Z') || (before >= '0' && before <= '9') || before == '_' {
				filteredText = " " + filteredText
			}
		}
	}

	pastedLines := strings.Split(filteredText, "\n")
	totalChars := utf16Length(filteredText)
	if len(pastedLines) > 10 || totalChars > 1000 {
		editor.pasteCounter++
		pasteID := editor.pasteCounter
		editor.pastes[pasteID] = filteredText
		marker := fmt.Sprintf("[paste #%d %d chars]", pasteID, totalChars)
		if len(pastedLines) > 10 {
			marker = fmt.Sprintf("[paste #%d +%d lines]", pasteID, len(pastedLines))
		}
		editor.insertTextAtCursorInternal(marker)
		return
	}

	editor.insertTextAtCursorInternal(filteredText)
}

// utf16Length mirrors JS String.prototype.length for paste-marker labels.
func utf16Length(value string) int {
	length := 0
	for _, r := range value {
		if r > 0xFFFF {
			length += 2
		} else {
			length++
		}
	}
	return length
}

func (editor *Editor) addNewLine() {
	editor.cancelAutocomplete()
	editor.exitHistoryBrowsing()
	editor.lastAction = ""
	editor.pushUndoSnapshot()

	currentLine := editor.currentLine()
	before := runeSlice(currentLine, 0, editor.state.cursorCol)
	after := runeSliceFrom(currentLine, editor.state.cursorCol)

	editor.state.lines[editor.state.cursorLine] = before
	editor.state.lines = append(editor.state.lines[:editor.state.cursorLine+1],
		append([]string{after}, editor.state.lines[editor.state.cursorLine+1:]...)...)

	editor.state.cursorLine++
	editor.setCursorCol(0)
	editor.emitChange()
}

func (editor *Editor) shouldSubmitOnBackslashEnter(data string, kb *KeybindingsManager) bool {
	if editor.DisableSubmit {
		return false
	}
	if !MatchesKey(data, "enter") {
		return false
	}
	submitKeys := kb.Keys("tui.input.submit")
	hasShiftEnter := false
	for _, key := range submitKeys {
		if key == "shift+enter" || key == "shift+return" {
			hasShiftEnter = true
			break
		}
	}
	if !hasShiftEnter {
		return false
	}
	currentLine := []rune(editor.currentLine())
	return editor.state.cursorCol > 0 && editor.state.cursorCol <= len(currentLine) && currentLine[editor.state.cursorCol-1] == '\\'
}

func (editor *Editor) submitValue() {
	editor.cancelAutocomplete()
	result := trimWhitespace(editor.expandPasteMarkers(editor.getTextLocked()))

	editor.state = editorState{lines: []string{""}}
	editor.pastes = map[int]string{}
	editor.pasteCounter = 0
	editor.exitHistoryBrowsing()
	editor.scrollOffset = 0
	editor.undoStack.clear()
	editor.lastAction = ""

	if editor.OnChange != nil {
		callback := editor.OnChange
		editor.pending = append(editor.pending, func() { callback("") })
	}
	if editor.OnSubmit != nil {
		callback := editor.OnSubmit
		editor.pending = append(editor.pending, func() { callback(result) })
	}
}

func (editor *Editor) handleBackspace() {
	editor.exitHistoryBrowsing()
	editor.lastAction = ""

	if editor.state.cursorCol > 0 {
		editor.pushUndoSnapshot()

		line := editor.currentLine()
		beforeCursor := runeSlice(line, 0, editor.state.cursorCol)
		graphemes := editor.segment(beforeCursor, segmentModeGrapheme)
		lastGrapheme := ""
		if len(graphemes) > 0 {
			lastGrapheme = graphemes[len(graphemes)-1].text
		}
		graphemeLength := 1
		if lastGrapheme != "" {
			graphemeLength = runeLen(lastGrapheme)
		}

		if match := pasteMarkerSingle.FindStringSubmatch(lastGrapheme); match != nil {
			targetID, _ := strconv.Atoi(match[1])
			delete(editor.pastes, targetID)
			editor.pasteCounter--

			// Renumber markers with IDs greater than the removed one.
			for index, mapLine := range editor.state.lines {
				editor.state.lines[index] = pasteMarkerRegex.ReplaceAllStringFunc(mapLine, func(fullMatch string) string {
					sub := pasteMarkerRegex.FindStringSubmatchIndex(fullMatch)
					id, _ := strconv.Atoi(fullMatch[sub[2]:sub[3]])
					if id <= targetID {
						return fullMatch
					}
					// Unmatched optional suffix stringifies as "undefined"
					// upstream; the quirk is preserved.
					suffix := "undefined"
					if sub[4] >= 0 {
						suffix = fullMatch[sub[4]:sub[5]]
					}
					newText := fmt.Sprintf("[paste #%d%s]", id-1, suffix)
					if content, ok := editor.pastes[id]; ok {
						editor.pastes[id-1] = content
					} else {
						editor.pastes[id-1] = newText
					}
					delete(editor.pastes, id)
					return newText
				})
			}
		}

		line = editor.currentLine()
		before := runeSlice(line, 0, editor.state.cursorCol-graphemeLength)
		after := runeSliceFrom(line, editor.state.cursorCol)
		editor.state.lines[editor.state.cursorLine] = before + after
		editor.setCursorCol(editor.state.cursorCol - graphemeLength)
	} else if editor.state.cursorLine > 0 {
		editor.pushUndoSnapshot()
		currentLine := editor.currentLine()
		previousLine := editor.line(editor.state.cursorLine - 1)
		editor.state.lines[editor.state.cursorLine-1] = previousLine + currentLine
		editor.state.lines = append(editor.state.lines[:editor.state.cursorLine], editor.state.lines[editor.state.cursorLine+1:]...)
		editor.state.cursorLine--
		editor.setCursorCol(runeLen(previousLine))
	}

	editor.emitChange()

	if editor.autocompleteState != "" {
		editor.updateAutocomplete()
	} else {
		textBeforeCursor := runeSlice(editor.currentLine(), 0, editor.state.cursorCol)
		if editor.isInSlashCommandContext(textBeforeCursor) {
			editor.tryTriggerAutocomplete()
		} else if matchesAutocompleteTrigger(textBeforeCursor, editor.autocompleteTriggerCharacters) {
			editor.tryTriggerAutocomplete()
		}
	}
}

// setCursorCol also clears the sticky column; use for all non-vertical moves.
func (editor *Editor) setCursorCol(col int) {
	editor.state.cursorCol = col
	editor.preferredVisualCol = -1
	editor.snappedFromCursorCol = -1
}

func (editor *Editor) moveToVisualLine(visualLines []visualLine, currentVisualLine, targetVisualLine int) {
	if currentVisualLine < 0 || currentVisualLine >= len(visualLines) || targetVisualLine < 0 || targetVisualLine >= len(visualLines) {
		return
	}
	currentVL := visualLines[currentVisualLine]
	targetVL := visualLines[targetVisualLine]

	// A snapped cursor resolves its pre-snap position against the visual
	// line it belongs to, staying correct across resizes.
	var currentVisualCol int
	if editor.snappedFromCursorCol >= 0 {
		vlIndex := findVisualLineAt(visualLines, currentVL.logicalLine, editor.snappedFromCursorCol)
		currentVisualCol = editor.snappedFromCursorCol - visualLines[vlIndex].startCol
	} else {
		cursorCol := utf16Length(runeSlice(editor.currentLine(), 0, editor.state.cursorCol))
		currentVisualCol = cursorCol - currentVL.startCol
	}

	isLastSourceSegment := currentVisualLine == len(visualLines)-1 || visualLines[currentVisualLine+1].logicalLine != currentVL.logicalLine
	sourceMaxVisualCol := currentVL.length
	if !isLastSourceSegment {
		sourceMaxVisualCol = max(0, currentVL.length-1)
	}
	isLastTargetSegment := targetVisualLine == len(visualLines)-1 || visualLines[targetVisualLine+1].logicalLine != targetVL.logicalLine
	targetMaxVisualCol := targetVL.length
	if !isLastTargetSegment {
		targetMaxVisualCol = max(0, targetVL.length-1)
	}

	moveToVisualCol := editor.computeVerticalMoveColumn(currentVisualCol, sourceMaxVisualCol, targetMaxVisualCol)

	editor.state.cursorLine = targetVL.logicalLine
	targetCol := targetVL.startCol + moveToVisualCol
	logicalLine := editor.line(targetVL.logicalLine)
	targetCol = min(targetCol, utf16Length(logicalLine))
	editor.state.cursorCol = runeIndexFromUTF16(logicalLine, targetCol)

	// Snap to atomic segment boundaries (e.g. paste markers) so the cursor
	// never lands inside a multi-rune unit.
	for _, seg := range editor.segment(logicalLine, segmentModeGrapheme) {
		segmentStart := utf16Length(runeSlice(logicalLine, 0, seg.index))
		segmentLength := utf16Length(seg.text)
		if segmentStart > targetCol {
			break
		}
		if segmentLength <= 1 {
			continue
		}
		if targetCol < segmentStart+segmentLength {
			isContinuation := segmentStart < targetVL.startCol
			isMovingDown := targetVisualLine > currentVisualLine

			if isContinuation && isMovingDown {
				// Already visited this segment on the way down: skip its
				// remaining continuation lines.
				segEnd := segmentStart + segmentLength
				next := targetVisualLine + 1
				for next < len(visualLines) && visualLines[next].logicalLine == targetVL.logicalLine && visualLines[next].startCol < segEnd {
					next++
				}
				if next < len(visualLines) {
					editor.moveToVisualLine(visualLines, currentVisualLine, next)
					return
				}
			}

			editor.snappedFromCursorCol = targetCol
			editor.state.cursorCol = seg.index
			return
		}
	}
	editor.snappedFromCursorCol = -1
}

// computeVerticalMoveColumn implements the sticky-column decision table from
// upstream (see editor.ts for the truth table).
func (editor *Editor) computeVerticalMoveColumn(currentVisualCol, sourceMaxVisualCol, targetMaxVisualCol int) int {
	hasPreferred := editor.preferredVisualCol >= 0
	cursorInMiddle := currentVisualCol < sourceMaxVisualCol
	targetTooShort := targetMaxVisualCol < currentVisualCol

	if !hasPreferred || cursorInMiddle {
		if targetTooShort {
			editor.preferredVisualCol = currentVisualCol
			return targetMaxVisualCol
		}
		editor.preferredVisualCol = -1
		return currentVisualCol
	}

	if targetTooShort || targetMaxVisualCol < editor.preferredVisualCol {
		return targetMaxVisualCol
	}

	result := editor.preferredVisualCol
	editor.preferredVisualCol = -1
	return result
}

func (editor *Editor) moveToLineStart() {
	editor.lastAction = ""
	editor.setCursorCol(0)
}

func (editor *Editor) moveToLineEnd() {
	editor.lastAction = ""
	editor.setCursorCol(runeLen(editor.currentLine()))
}

func (editor *Editor) deleteToStartOfLine() {
	editor.exitHistoryBrowsing()
	currentLine := editor.currentLine()

	if editor.state.cursorCol > 0 {
		editor.pushUndoSnapshot()
		deletedText := runeSlice(currentLine, 0, editor.state.cursorCol)
		editor.killRing.push(deletedText, true, editor.lastAction == "kill")
		editor.lastAction = "kill"
		editor.state.lines[editor.state.cursorLine] = runeSliceFrom(currentLine, editor.state.cursorCol)
		editor.setCursorCol(0)
	} else if editor.state.cursorLine > 0 {
		editor.pushUndoSnapshot()
		editor.killRing.push("\n", true, editor.lastAction == "kill")
		editor.lastAction = "kill"
		previousLine := editor.line(editor.state.cursorLine - 1)
		editor.state.lines[editor.state.cursorLine-1] = previousLine + currentLine
		editor.state.lines = append(editor.state.lines[:editor.state.cursorLine], editor.state.lines[editor.state.cursorLine+1:]...)
		editor.state.cursorLine--
		editor.setCursorCol(runeLen(previousLine))
	}
	editor.emitChange()
}

func (editor *Editor) deleteToEndOfLine() {
	editor.exitHistoryBrowsing()
	currentLine := editor.currentLine()

	if editor.state.cursorCol < runeLen(currentLine) {
		editor.pushUndoSnapshot()
		deletedText := runeSliceFrom(currentLine, editor.state.cursorCol)
		editor.killRing.push(deletedText, false, editor.lastAction == "kill")
		editor.lastAction = "kill"
		editor.state.lines[editor.state.cursorLine] = runeSlice(currentLine, 0, editor.state.cursorCol)
	} else if editor.state.cursorLine < len(editor.state.lines)-1 {
		editor.pushUndoSnapshot()
		editor.killRing.push("\n", false, editor.lastAction == "kill")
		editor.lastAction = "kill"
		nextLine := editor.line(editor.state.cursorLine + 1)
		editor.state.lines[editor.state.cursorLine] = currentLine + nextLine
		editor.state.lines = append(editor.state.lines[:editor.state.cursorLine+1], editor.state.lines[editor.state.cursorLine+2:]...)
	}
	editor.emitChange()
}

func (editor *Editor) deleteWordBackwards() {
	editor.exitHistoryBrowsing()
	currentLine := editor.currentLine()

	if editor.state.cursorCol == 0 {
		if editor.state.cursorLine > 0 {
			editor.pushUndoSnapshot()
			editor.killRing.push("\n", true, editor.lastAction == "kill")
			editor.lastAction = "kill"
			previousLine := editor.line(editor.state.cursorLine - 1)
			editor.state.lines[editor.state.cursorLine-1] = previousLine + currentLine
			editor.state.lines = append(editor.state.lines[:editor.state.cursorLine], editor.state.lines[editor.state.cursorLine+1:]...)
			editor.state.cursorLine--
			editor.setCursorCol(runeLen(previousLine))
		}
	} else {
		editor.pushUndoSnapshot()
		wasKill := editor.lastAction == "kill"

		oldCursorCol := editor.state.cursorCol
		editor.moveWordBackwards()
		deleteFrom := editor.state.cursorCol
		editor.setCursorCol(oldCursorCol)

		deletedText := runeSlice(currentLine, deleteFrom, editor.state.cursorCol)
		editor.killRing.push(deletedText, true, wasKill)
		editor.lastAction = "kill"

		editor.state.lines[editor.state.cursorLine] = runeSlice(currentLine, 0, deleteFrom) + runeSliceFrom(currentLine, editor.state.cursorCol)
		editor.setCursorCol(deleteFrom)
	}
	editor.emitChange()
}

func (editor *Editor) deleteWordForward() {
	editor.exitHistoryBrowsing()
	currentLine := editor.currentLine()

	if editor.state.cursorCol >= runeLen(currentLine) {
		if editor.state.cursorLine < len(editor.state.lines)-1 {
			editor.pushUndoSnapshot()
			editor.killRing.push("\n", false, editor.lastAction == "kill")
			editor.lastAction = "kill"
			nextLine := editor.line(editor.state.cursorLine + 1)
			editor.state.lines[editor.state.cursorLine] = currentLine + nextLine
			editor.state.lines = append(editor.state.lines[:editor.state.cursorLine+1], editor.state.lines[editor.state.cursorLine+2:]...)
		}
	} else {
		editor.pushUndoSnapshot()
		wasKill := editor.lastAction == "kill"

		oldCursorCol := editor.state.cursorCol
		editor.moveWordForwards()
		deleteTo := editor.state.cursorCol
		editor.setCursorCol(oldCursorCol)

		deletedText := runeSlice(currentLine, editor.state.cursorCol, deleteTo)
		editor.killRing.push(deletedText, false, wasKill)
		editor.lastAction = "kill"

		editor.state.lines[editor.state.cursorLine] = runeSlice(currentLine, 0, editor.state.cursorCol) + runeSliceFrom(currentLine, deleteTo)
	}
	editor.emitChange()
}

func (editor *Editor) handleForwardDelete() {
	editor.exitHistoryBrowsing()
	editor.lastAction = ""
	currentLine := editor.currentLine()

	if editor.state.cursorCol < runeLen(currentLine) {
		editor.pushUndoSnapshot()
		afterCursor := runeSliceFrom(currentLine, editor.state.cursorCol)
		graphemes := editor.segment(afterCursor, segmentModeGrapheme)
		graphemeLength := 1
		if len(graphemes) > 0 {
			graphemeLength = runeLen(graphemes[0].text)
		}
		before := runeSlice(currentLine, 0, editor.state.cursorCol)
		after := runeSliceFrom(currentLine, editor.state.cursorCol+graphemeLength)
		editor.state.lines[editor.state.cursorLine] = before + after
	} else if editor.state.cursorLine < len(editor.state.lines)-1 {
		editor.pushUndoSnapshot()
		nextLine := editor.line(editor.state.cursorLine + 1)
		editor.state.lines[editor.state.cursorLine] = currentLine + nextLine
		editor.state.lines = append(editor.state.lines[:editor.state.cursorLine+1], editor.state.lines[editor.state.cursorLine+2:]...)
	}

	editor.emitChange()

	if editor.autocompleteState != "" {
		editor.updateAutocomplete()
	} else {
		textBeforeCursor := runeSlice(editor.currentLine(), 0, editor.state.cursorCol)
		if editor.isInSlashCommandContext(textBeforeCursor) {
			editor.tryTriggerAutocomplete()
		} else if matchesAutocompleteTrigger(textBeforeCursor, editor.autocompleteTriggerCharacters) {
			editor.tryTriggerAutocomplete()
		}
	}
}

// buildVisualLineMap maps visual lines to upstream UTF-16 columns.
func (editor *Editor) buildVisualLineMap(width int) []visualLine {
	var visualLines []visualLine
	for i := range editor.state.lines {
		line := editor.line(i)
		if line == "" {
			visualLines = append(visualLines, visualLine{logicalLine: i})
		} else if VisibleWidth(line) <= width {
			visualLines = append(visualLines, visualLine{logicalLine: i, length: utf16Length(line)})
		} else {
			for _, chunk := range wordWrapLine(line, width, editor.segment(line, segmentModeGrapheme)) {
				startCol := utf16Length(runeSlice(line, 0, chunk.startIndex))
				endCol := utf16Length(runeSlice(line, 0, chunk.endIndex))
				visualLines = append(visualLines, visualLine{logicalLine: i, startCol: startCol, length: endCol - startCol})
			}
		}
	}
	return visualLines
}

func findVisualLineAt(visualLines []visualLine, line, col int) int {
	for i, vl := range visualLines {
		if vl.logicalLine != line {
			continue
		}
		offset := col - vl.startCol
		isLastSegmentOfLine := i == len(visualLines)-1 || visualLines[i+1].logicalLine != vl.logicalLine
		if offset >= 0 && (offset < vl.length || (isLastSegmentOfLine && offset == vl.length)) {
			return i
		}
	}
	return len(visualLines) - 1
}

func (editor *Editor) findCurrentVisualLine(visualLines []visualLine) int {
	cursorCol := utf16Length(runeSlice(editor.currentLine(), 0, editor.state.cursorCol))
	return findVisualLineAt(visualLines, editor.state.cursorLine, cursorCol)
}

func (editor *Editor) moveCursor(deltaLine, deltaCol int) {
	editor.lastAction = ""
	visualLines := editor.buildVisualLineMap(editor.lastWidth)
	currentVisualLine := editor.findCurrentVisualLine(visualLines)

	if deltaLine != 0 {
		targetVisualLine := currentVisualLine + deltaLine
		if targetVisualLine >= 0 && targetVisualLine < len(visualLines) {
			editor.moveToVisualLine(visualLines, currentVisualLine, targetVisualLine)
		}
	}

	if deltaCol != 0 {
		currentLine := editor.currentLine()
		if deltaCol > 0 {
			if editor.state.cursorCol < runeLen(currentLine) {
				afterCursor := runeSliceFrom(currentLine, editor.state.cursorCol)
				graphemes := editor.segment(afterCursor, segmentModeGrapheme)
				step := 1
				if len(graphemes) > 0 {
					step = runeLen(graphemes[0].text)
				}
				editor.setCursorCol(editor.state.cursorCol + step)
			} else if editor.state.cursorLine < len(editor.state.lines)-1 {
				editor.state.cursorLine++
				editor.setCursorCol(0)
			} else if currentVisualLine >= 0 && currentVisualLine < len(visualLines) {
				// At the very end: remember the visual column for up/down.
				cursorCol := utf16Length(runeSlice(currentLine, 0, editor.state.cursorCol))
				editor.preferredVisualCol = cursorCol - visualLines[currentVisualLine].startCol
			}
		} else {
			if editor.state.cursorCol > 0 {
				beforeCursor := runeSlice(currentLine, 0, editor.state.cursorCol)
				graphemes := editor.segment(beforeCursor, segmentModeGrapheme)
				step := 1
				if len(graphemes) > 0 {
					step = runeLen(graphemes[len(graphemes)-1].text)
				}
				editor.setCursorCol(editor.state.cursorCol - step)
			} else if editor.state.cursorLine > 0 {
				editor.state.cursorLine--
				editor.setCursorCol(runeLen(editor.currentLine()))
			}
		}
	}

	// Keep an open autocomplete picker in sync with the moved cursor;
	// re-query so it refreshes or closes.
	if editor.autocompleteState != "" {
		editor.updateAutocomplete()
	}
}

func (editor *Editor) pageScroll(direction int) {
	editor.lastAction = ""
	terminalRows := editor.ui.Terminal().Rows()
	pageSize := max(5, terminalRows*3/10)

	visualLines := editor.buildVisualLineMap(editor.lastWidth)
	currentVisualLine := editor.findCurrentVisualLine(visualLines)
	targetVisualLine := max(0, min(len(visualLines)-1, currentVisualLine+direction*pageSize))
	editor.moveToVisualLine(visualLines, currentVisualLine, targetVisualLine)
}

func (editor *Editor) moveWordBackwards() {
	editor.lastAction = ""
	currentLine := editor.currentLine()

	if editor.state.cursorCol == 0 {
		if editor.state.cursorLine > 0 {
			editor.state.cursorLine--
			editor.setCursorCol(runeLen(editor.currentLine()))
		}
		return
	}
	editor.setCursorCol(findWordBackward(currentLine, editor.state.cursorCol, &wordNavigationOptions{
		segment:         func(text string) []segment { return editor.segment(text, segmentModeWord) },
		isAtomicSegment: isPasteMarker,
	}))
}

func (editor *Editor) moveWordForwards() {
	editor.lastAction = ""
	currentLine := editor.currentLine()

	if editor.state.cursorCol >= runeLen(currentLine) {
		if editor.state.cursorLine < len(editor.state.lines)-1 {
			editor.state.cursorLine++
			editor.setCursorCol(0)
		}
		return
	}
	editor.setCursorCol(findWordForward(currentLine, editor.state.cursorCol, &wordNavigationOptions{
		segment:         func(text string) []segment { return editor.segment(text, segmentModeWord) },
		isAtomicSegment: isPasteMarker,
	}))
}

func (editor *Editor) yank() {
	if editor.killRing.length() == 0 {
		return
	}
	editor.pushUndoSnapshot()
	editor.insertYankedText(editor.killRing.peek())
	editor.lastAction = "yank"
}

func (editor *Editor) yankPop() {
	if editor.lastAction != "yank" || editor.killRing.length() <= 1 {
		return
	}
	editor.pushUndoSnapshot()
	editor.deleteYankedText()
	editor.killRing.rotate()
	editor.insertYankedText(editor.killRing.peek())
	editor.lastAction = "yank"
}

func (editor *Editor) insertYankedText(text string) {
	editor.exitHistoryBrowsing()
	lines := strings.Split(text, "\n")

	if len(lines) == 1 {
		currentLine := editor.currentLine()
		before := runeSlice(currentLine, 0, editor.state.cursorCol)
		after := runeSliceFrom(currentLine, editor.state.cursorCol)
		editor.state.lines[editor.state.cursorLine] = before + text + after
		editor.setCursorCol(editor.state.cursorCol + runeLen(text))
	} else {
		currentLine := editor.currentLine()
		before := runeSlice(currentLine, 0, editor.state.cursorCol)
		after := runeSliceFrom(currentLine, editor.state.cursorCol)

		editor.state.lines[editor.state.cursorLine] = before + lines[0]
		for i := 1; i < len(lines)-1; i++ {
			editor.state.lines = append(editor.state.lines[:editor.state.cursorLine+i],
				append([]string{lines[i]}, editor.state.lines[editor.state.cursorLine+i:]...)...)
		}
		lastLineIndex := editor.state.cursorLine + len(lines) - 1
		editor.state.lines = append(editor.state.lines[:lastLineIndex],
			append([]string{lines[len(lines)-1] + after}, editor.state.lines[lastLineIndex:]...)...)

		editor.state.cursorLine = lastLineIndex
		editor.setCursorCol(runeLen(lines[len(lines)-1]))
	}
	editor.emitChange()
}

func (editor *Editor) deleteYankedText() {
	yankedText := editor.killRing.peek()
	if yankedText == "" {
		return
	}
	yankLines := strings.Split(yankedText, "\n")

	if len(yankLines) == 1 {
		currentLine := editor.currentLine()
		deleteLen := runeLen(yankedText)
		before := runeSlice(currentLine, 0, editor.state.cursorCol-deleteLen)
		after := runeSliceFrom(currentLine, editor.state.cursorCol)
		editor.state.lines[editor.state.cursorLine] = before + after
		editor.setCursorCol(editor.state.cursorCol - deleteLen)
	} else {
		startLine := editor.state.cursorLine - (len(yankLines) - 1)
		startCol := runeLen(editor.line(startLine)) - runeLen(yankLines[0])
		afterCursor := runeSliceFrom(editor.currentLine(), editor.state.cursorCol)
		beforeYank := runeSlice(editor.line(startLine), 0, startCol)

		merged := beforeYank + afterCursor
		editor.state.lines = append(editor.state.lines[:startLine],
			append([]string{merged}, editor.state.lines[startLine+len(yankLines):]...)...)

		editor.state.cursorLine = startLine
		editor.setCursorCol(startCol)
	}
	editor.emitChange()
}

func (editor *Editor) pushUndoSnapshot() {
	editor.undoStack.push(editor.state.clone())
}

func (editor *Editor) undo() {
	editor.exitHistoryBrowsing()
	snapshot, ok := editor.undoStack.pop()
	if !ok {
		return
	}
	editor.state = snapshot
	editor.lastAction = ""
	editor.preferredVisualCol = -1
	editor.emitChange()
}

// jumpToChar jumps to the first occurrence of char in the given direction.
// Multi-line, case-sensitive, skips the current cursor position.
func (editor *Editor) jumpToChar(char, direction string) {
	editor.lastAction = ""
	isForward := direction == "forward"
	step := 1
	end := len(editor.state.lines)
	if !isForward {
		step = -1
		end = -1
	}

	for lineIdx := editor.state.cursorLine; lineIdx != end; lineIdx += step {
		line := []rune(editor.line(lineIdx))
		target := []rune(char)
		isCurrentLine := lineIdx == editor.state.cursorLine

		var idx int
		if isForward {
			from := 0
			if isCurrentLine {
				from = editor.state.cursorCol + 1
			}
			idx = runeIndexOf(line, target, from)
		} else {
			from := len(line)
			if isCurrentLine {
				from = editor.state.cursorCol - 1
			}
			idx = runeLastIndexOf(line, target, from)
		}

		if idx != -1 {
			editor.state.cursorLine = lineIdx
			editor.setCursorCol(idx)
			return
		}
	}
}

func runeIndexOf(haystack, needle []rune, from int) int {
	from = max(0, from)
	for start := from; start+len(needle) <= len(haystack); start++ {
		if string(haystack[start:start+len(needle)]) == string(needle) {
			return start
		}
	}
	return -1
}

func runeLastIndexOf(haystack, needle []rune, from int) int {
	from = min(from, len(haystack)-len(needle))
	from = min(from, len(haystack))
	for start := from; start >= 0; start-- {
		if start+len(needle) <= len(haystack) && string(haystack[start:start+len(needle)]) == string(needle) {
			return start
		}
	}
	return -1
}

// Slash menu is only allowed on the first line of the editor.
func (editor *Editor) isSlashMenuAllowed() bool { return editor.state.cursorLine == 0 }

func (editor *Editor) isAtStartOfMessage() bool {
	if !editor.isSlashMenuAllowed() {
		return false
	}
	beforeCursor := trimWhitespace(runeSlice(editor.currentLine(), 0, editor.state.cursorCol))
	return beforeCursor == "" || beforeCursor == "/"
}

func (editor *Editor) isInSlashCommandContext(textBeforeCursor string) bool {
	return editor.isSlashMenuAllowed() && strings.HasPrefix(strings.TrimLeftFunc(textBeforeCursor, unicode.IsSpace), "/")
}

// getBestAutocompleteMatchIndex prefers an exact value match, then the first
// prefix match; -1 keeps the default highlight. Case-sensitive.
func getBestAutocompleteMatchIndex(items []AutocompleteItem, prefix string) int {
	if prefix == "" {
		return -1
	}
	firstPrefixIndex := -1
	for i, item := range items {
		if item.Value == prefix {
			return i
		}
		if firstPrefixIndex == -1 && strings.HasPrefix(item.Value, prefix) {
			firstPrefixIndex = i
		}
	}
	return firstPrefixIndex
}

func (editor *Editor) createAutocompleteList(prefix string, items []AutocompleteItem) *SelectList {
	layout := SelectListLayoutOptions{}
	if strings.HasPrefix(prefix, "/") {
		layout = slashCommandSelectListLayout
	}
	selectItems := make([]SelectItem, len(items))
	for index, item := range items {
		selectItems[index] = SelectItem(item)
	}
	return NewSelectList(selectItems, editor.autocompleteMaxVisible, editor.theme.SelectList, layout)
}

func (editor *Editor) tryTriggerAutocomplete() {
	editor.requestAutocomplete(false, false)
}

func (editor *Editor) handleTabCompletion() {
	if editor.autocompleteProvider == nil {
		return
	}
	beforeCursor := runeSlice(editor.currentLine(), 0, editor.state.cursorCol)
	if editor.isInSlashCommandContext(beforeCursor) && !strings.Contains(strings.TrimLeftFunc(beforeCursor, unicode.IsSpace), " ") {
		editor.requestAutocomplete(false, true)
	} else {
		editor.requestAutocomplete(true, true)
	}
}

func (editor *Editor) requestAutocomplete(force, explicitTab bool) {
	if editor.autocompleteProvider == nil {
		return
	}
	if force {
		if gate, ok := editor.autocompleteProvider.(FileCompletionGate); ok {
			lines := append([]string(nil), editor.state.lines...)
			cursorLine, cursorCol := editor.state.cursorLine, editor.state.cursorCol
			editor.mu.Unlock()
			shouldTrigger := gate.ShouldTriggerFileCompletion(lines, cursorLine, cursorCol)
			editor.mu.Lock()
			if !shouldTrigger || editor.autocompleteProvider == nil {
				return
			}
		}
	}

	editor.cancelAutocompleteRequest()
	editor.autocompleteStartToken++
	startToken := editor.autocompleteStartToken

	if debounce := editor.autocompleteDebounceDuration(force, explicitTab); debounce > 0 {
		editor.autocompleteBusy++
		editor.autocompleteDebounceTimer = time.AfterFunc(debounce, func() {
			editor.mu.Lock()
			editor.autocompleteDebounceTimer = nil
			if startToken == editor.autocompleteStartToken {
				editor.startAutocompleteRequest(startToken, force, explicitTab)
			}
			editor.autocompleteDone()
			editor.mu.Unlock()
		})
		return
	}
	editor.startAutocompleteRequest(startToken, force, explicitTab)
}

func (editor *Editor) setAutocompleteTriggerCharacters(triggerCharacters []string) {
	next := append([]string(nil), defaultAutocompleteTriggerCharacters...)
	for _, character := range triggerCharacters {
		if runeLen(character) != 1 || character == "/" || isWhitespaceChar(character) || containsString(next, character) {
			continue
		}
		next = append(next, character)
	}
	editor.autocompleteTriggerCharacters = next
}

func (editor *Editor) autocompleteDebounceDuration(force, explicitTab bool) time.Duration {
	if explicitTab || force {
		return 0
	}
	textBeforeCursor := runeSlice(editor.currentLine(), 0, editor.state.cursorCol)
	if matchesAutocompleteDebounce(textBeforeCursor, editor.autocompleteTriggerCharacters) {
		return attachmentAutocompleteDebounce
	}
	return 0
}

// startAutocompleteRequest runs the provider query off the input goroutine,
// mirroring upstream's chained async request task. Caller holds editor.mu.
func (editor *Editor) startAutocompleteRequest(startToken int, force, explicitTab bool) {
	editor.autocompleteBusy++
	go func() {
		editor.autocompleteRunMu.Lock()
		defer editor.autocompleteRunMu.Unlock()

		editor.mu.Lock()
		if startToken != editor.autocompleteStartToken || editor.autocompleteProvider == nil {
			editor.autocompleteDone()
			editor.mu.Unlock()
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		editor.autocompleteCancel = cancel
		editor.autocompleteRequestID++
		requestID := editor.autocompleteRequestID
		snapshotText := editor.getTextLocked()
		snapshotLine := editor.state.cursorLine
		snapshotCol := editor.state.cursorCol
		lines := append([]string(nil), editor.state.lines...)
		provider := editor.autocompleteProvider
		editor.mu.Unlock()

		suggestions := provider.GetSuggestions(ctx, lines, snapshotLine, snapshotCol, force)

		editor.mu.Lock()
		current := ctx.Err() == nil &&
			requestID == editor.autocompleteRequestID &&
			editor.getTextLocked() == snapshotText &&
			editor.state.cursorLine == snapshotLine &&
			editor.state.cursorCol == snapshotCol
		if !current {
			editor.autocompleteDone()
			editor.mu.Unlock()
			cancel()
			return
		}
		editor.autocompleteCancel = nil

		if suggestions == nil || len(suggestions.Items) == 0 {
			editor.cancelAutocomplete()
		} else if force && explicitTab && len(suggestions.Items) == 1 {
			editor.pushUndoSnapshot()
			editor.lastAction = ""
			lines := append([]string(nil), editor.state.lines...)
			cursorLine, cursorCol := editor.state.cursorLine, editor.state.cursorCol
			editor.mu.Unlock()
			result := provider.ApplyCompletion(lines, cursorLine, cursorCol, suggestions.Items[0], suggestions.Prefix)
			editor.mu.Lock()
			editor.state.lines = result.Lines
			editor.state.cursorLine = result.CursorLine
			editor.setCursorCol(result.CursorCol)
			editor.emitChange()
		} else {
			state := "regular"
			if force {
				state = "force"
			}
			editor.applyAutocompleteSuggestions(suggestions, state)
		}
		editor.autocompleteDone()
		pending := editor.pending
		editor.pending = nil
		editor.mu.Unlock()
		cancel()
		for _, callback := range pending {
			callback()
		}
		editor.ui.RequestRender()
	}()
}

func (editor *Editor) applyAutocompleteSuggestions(suggestions *AutocompleteSuggestions, state string) {
	editor.autocompletePrefix = suggestions.Prefix
	editor.autocompleteList = editor.createAutocompleteList(suggestions.Prefix, suggestions.Items)
	if bestMatchIndex := getBestAutocompleteMatchIndex(suggestions.Items, suggestions.Prefix); bestMatchIndex >= 0 {
		editor.autocompleteList.SetSelectedIndex(bestMatchIndex)
	}
	editor.autocompleteState = state
}

// autocompleteDone decrements the busy counter; caller holds editor.mu.
func (editor *Editor) autocompleteDone() {
	editor.autocompleteBusy--
	if editor.autocompleteBusy == 0 {
		editor.autocompleteIdle.Broadcast()
	}
}

// flushAutocomplete blocks until no debounce timer or request is pending
// (the Go analog of upstream tests awaiting the request task).
func (editor *Editor) flushAutocomplete() {
	editor.mu.Lock()
	for editor.autocompleteBusy > 0 {
		editor.autocompleteIdle.Wait()
	}
	editor.mu.Unlock()
}

func (editor *Editor) cancelAutocompleteRequest() {
	editor.autocompleteStartToken++
	if editor.autocompleteDebounceTimer != nil {
		if editor.autocompleteDebounceTimer.Stop() {
			editor.autocompleteDone()
		}
		editor.autocompleteDebounceTimer = nil
	}
	if editor.autocompleteCancel != nil {
		editor.autocompleteCancel()
		editor.autocompleteCancel = nil
	}
}

func (editor *Editor) clearAutocompleteUI() {
	editor.autocompleteState = ""
	editor.autocompleteList = nil
	editor.autocompletePrefix = ""
}

func (editor *Editor) cancelAutocomplete() {
	editor.cancelAutocompleteRequest()
	editor.clearAutocompleteUI()
}

func (editor *Editor) IsShowingAutocomplete() bool {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	return editor.autocompleteState != ""
}

func (editor *Editor) updateAutocomplete() {
	if editor.autocompleteState == "" || editor.autocompleteProvider == nil {
		return
	}
	editor.requestAutocomplete(editor.autocompleteState == "force", false)
}

func (editor *Editor) emitChange() {
	if editor.OnChange == nil {
		return
	}
	callback, text := editor.OnChange, editor.getTextLocked()
	editor.pending = append(editor.pending, func() { callback(text) })
}
