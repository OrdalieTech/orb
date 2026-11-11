package tools

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/internal/jstrim"
	udiff "github.com/aymanbagabas/go-udiff"
	"github.com/aymanbagabas/go-udiff/myers"
	"golang.org/x/text/unicode/norm"
)

type Edit struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type AppliedEditsResult struct {
	BaseContent string `json:"baseContent"`
	NewContent  string `json:"newContent"`
	// replacements are the sorted, disjoint byte ranges of BaseContent that turn it into NewContent.
	replacements []textReplacement
}

type FuzzyMatchResult struct {
	Found                 bool   `json:"found"`
	Index                 int    `json:"index"`
	MatchLength           int    `json:"matchLength"`
	UsedFuzzyMatch        bool   `json:"usedFuzzyMatch"`
	ContentForReplacement string `json:"contentForReplacement"`
}

type DiffResult struct {
	Diff             string `json:"diff"`
	FirstChangedLine *int   `json:"firstChangedLine,omitempty"`
}

type matchedEdit struct {
	editIndex  int
	matchIndex int
	matchLen   int
	newText    string
}

type textReplacement struct {
	matchIndex int
	matchLen   int
	newText    string
}

type fuzzyMatch struct {
	FuzzyMatchResult
	byteIndex int
	byteLen   int
}

type lineSpan struct {
	start int
	end   int
}

func NormalizeToLF(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

// ApplyReplacementsToOriginal applies an edit matched in NormalizeToLF(original) to original
// itself. Text outside the replaced ranges keeps its bytes, line endings included; line breaks in
// the new text get the line ending of the line where the replaced range starts.
func ApplyReplacementsToOriginal(original string, applied AppliedEditsResult) string {
	if !strings.Contains(original, "\r") {
		return applied.NewContent
	}
	// Only CRLF changes length when normalizing: adding the CRLFs before a normalized offset
	// gives the original offset, and a range covering the LF of a CRLF covers the whole CRLF.
	crlfBefore, nextCRLF := 0, strings.Index(original, "\r\n")
	toOriginal := func(offset int) int {
		for nextCRLF != -1 && nextCRLF-crlfBefore < offset {
			crlfBefore++
			if next := strings.Index(original[nextCRLF+2:], "\r\n"); next == -1 {
				nextCRLF = -1
			} else {
				nextCRLF += 2 + next
			}
		}
		return offset + crlfBefore
	}
	var result strings.Builder
	push := func(part string) {
		// A CR before a seam and an LF after it would merge into one CRLF line break; an extra LF
		// keeps the line breaks the diff shows.
		if written := result.String(); part != "" && written != "" && written[len(written)-1] == '\r' && part[0] == '\n' {
			result.WriteByte('\n')
		}
		result.WriteString(part)
	}
	position := 0
	for _, replacement := range applied.replacements {
		start := toOriginal(replacement.matchIndex)
		push(original[position:start])
		push(strings.ReplaceAll(replacement.newText, "\n", lineEndingAt(original, start)))
		position = toOriginal(replacement.matchIndex + replacement.matchLen)
	}
	push(original[position:])
	return result.String()
}

// lineEndingAt is the line ending of the line containing offset, or of the line before for an
// unterminated last line. A lone CR counts only in files without any LF (classic Mac line
// endings); elsewhere it is more likely a control character inside a line.
func lineEndingAt(content string, offset int) string {
	lineFeed := strings.IndexByte(content[offset:], '\n')
	if lineFeed != -1 {
		lineFeed += offset
	} else if lineFeed = strings.LastIndexByte(content[:offset], '\n'); lineFeed == -1 {
		if strings.Contains(content, "\r") {
			return "\r"
		}
		return "\n"
	}
	if lineFeed > 0 && content[lineFeed-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

func NormalizeForFuzzyMatch(text string) string {
	lines := strings.Split(norm.NFKC.String(text), "\n")
	for index := range lines {
		lines[index] = strings.TrimRightFunc(lines[index], jstrim.IsSpace)
	}
	normalized := strings.Join(lines, "\n")
	return strings.NewReplacer(
		"\u2018", "'", "\u2019", "'", "\u201a", "'", "\u201b", "'",
		"\u201c", "\"", "\u201d", "\"", "\u201e", "\"", "\u201f", "\"",
		"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-",
		"\u2014", "-", "\u2015", "-", "\u2212", "-",
		"\u00a0", " ", "\u2002", " ", "\u2003", " ", "\u2004", " ",
		"\u2005", " ", "\u2006", " ", "\u2007", " ", "\u2008", " ",
		"\u2009", " ", "\u200a", " ", "\u202f", " ", "\u205f", " ",
		"\u3000", " ",
	).Replace(normalized)
}

func FuzzyFindText(content, oldText string) FuzzyMatchResult {
	return fuzzyFindText(content, oldText).FuzzyMatchResult
}

func fuzzyFindText(content, oldText string) fuzzyMatch {
	if exactIndex := strings.Index(content, oldText); exactIndex != -1 {
		return fuzzyMatch{
			FuzzyMatchResult: FuzzyMatchResult{
				Found:                 true,
				Index:                 javascriptUTF16Length(content[:exactIndex]),
				MatchLength:           javascriptUTF16Length(oldText),
				ContentForReplacement: content,
			},
			byteIndex: exactIndex,
			byteLen:   len(oldText),
		}
	}

	fuzzyContent := NormalizeForFuzzyMatch(content)
	fuzzyOldText := NormalizeForFuzzyMatch(oldText)
	fuzzyIndex := strings.Index(fuzzyContent, fuzzyOldText)
	if fuzzyIndex == -1 {
		return fuzzyMatch{
			FuzzyMatchResult: FuzzyMatchResult{
				Index:                 -1,
				ContentForReplacement: content,
			},
			byteIndex: -1,
		}
	}
	return fuzzyMatch{
		FuzzyMatchResult: FuzzyMatchResult{
			Found:                 true,
			Index:                 javascriptUTF16Length(fuzzyContent[:fuzzyIndex]),
			MatchLength:           javascriptUTF16Length(fuzzyOldText),
			UsedFuzzyMatch:        true,
			ContentForReplacement: fuzzyContent,
		},
		byteIndex: fuzzyIndex,
		byteLen:   len(fuzzyOldText),
	}
}

func StripBOM(content string) (bom, text string) {
	if strings.HasPrefix(content, "\ufeff") {
		return "\ufeff", strings.TrimPrefix(content, "\ufeff")
	}
	return "", content
}

func ApplyEditsToNormalizedContent(normalizedContent string, edits []Edit, path string) (AppliedEditsResult, error) {
	normalizedEdits := make([]Edit, len(edits))
	for index, edit := range edits {
		normalizedEdits[index] = Edit{OldText: NormalizeToLF(edit.OldText), NewText: NormalizeToLF(edit.NewText)}
		if normalizedEdits[index].OldText == "" {
			return AppliedEditsResult{}, emptyOldTextError(path, index, len(edits))
		}
	}

	usedFuzzyMatch := slices.ContainsFunc(normalizedEdits, func(edit Edit) bool {
		return fuzzyFindText(normalizedContent, edit.OldText).UsedFuzzyMatch
	})
	replacementBase := normalizedContent
	if usedFuzzyMatch {
		replacementBase = NormalizeForFuzzyMatch(normalizedContent)
	}

	matched := make([]matchedEdit, 0, len(normalizedEdits))
	for index, edit := range normalizedEdits {
		match := fuzzyFindText(replacementBase, edit.OldText)
		if !match.Found {
			return AppliedEditsResult{}, notFoundError(path, index, len(edits))
		}
		occurrences := countOccurrences(replacementBase, edit.OldText)
		if occurrences > 1 {
			return AppliedEditsResult{}, duplicateError(path, index, len(edits), occurrences)
		}
		matched = append(matched, matchedEdit{
			editIndex: index, matchIndex: match.byteIndex, matchLen: match.byteLen, newText: edit.NewText,
		})
	}

	sortMatchedEdits(matched)
	for index := 1; index < len(matched); index++ {
		previous := matched[index-1]
		current := matched[index]
		if previous.matchIndex+previous.matchLen > current.matchIndex {
			return AppliedEditsResult{}, upstreamToolErrorf(
				"edits[%d] and edits[%d] overlap in %s. Merge them into one edit or target disjoint regions.",
				previous.editIndex, current.editIndex, path,
			)
		}
	}

	replacements := make([]textReplacement, len(matched))
	for index, match := range matched {
		replacements[index] = textReplacement{matchIndex: match.matchIndex, matchLen: match.matchLen, newText: match.newText}
	}
	if usedFuzzyMatch {
		var err error
		if replacements, err = widenReplacementsToLines(normalizedContent, replacementBase, replacements); err != nil {
			return AppliedEditsResult{}, err
		}
	}
	newContent := applyReplacements(normalizedContent, replacements, 0)
	if normalizedContent == newContent {
		return AppliedEditsResult{}, noChangeError(path, len(edits))
	}
	return AppliedEditsResult{BaseContent: normalizedContent, NewContent: newContent, replacements: replacements}, nil
}

func sortMatchedEdits(edits []matchedEdit) {
	for index := 1; index < len(edits); index++ {
		for position := index; position > 0 && edits[position].matchIndex < edits[position-1].matchIndex; position-- {
			edits[position], edits[position-1] = edits[position-1], edits[position]
		}
	}
}

func countOccurrences(content, oldText string) int {
	fuzzyOldText := NormalizeForFuzzyMatch(oldText)
	if fuzzyOldText == "" {
		return javascriptUTF16Length(NormalizeForFuzzyMatch(content)) - 1
	}
	return strings.Count(NormalizeForFuzzyMatch(content), fuzzyOldText)
}

func notFoundError(path string, editIndex, total int) error {
	if total == 1 {
		return upstreamToolErrorf("Could not find the exact text in %s. The old text must match exactly including all whitespace and newlines.", path)
	}
	return upstreamToolErrorf(
		"Could not find edits[%d] in %s. The oldText must match exactly including all whitespace and newlines.",
		editIndex, path,
	)
}

func duplicateError(path string, editIndex, total, occurrences int) error {
	if total == 1 {
		return upstreamToolErrorf(
			"Found %d occurrences of the text in %s. The text must be unique. Please provide more context to make it unique.",
			occurrences, path,
		)
	}
	return upstreamToolErrorf(
		"Found %d occurrences of edits[%d] in %s. Each oldText must be unique. Please provide more context to make it unique.",
		occurrences, editIndex, path,
	)
}

func emptyOldTextError(path string, editIndex, total int) error {
	if total == 1 {
		return upstreamToolErrorf("oldText must not be empty in %s.", path)
	}
	return upstreamToolErrorf("edits[%d].oldText must not be empty in %s.", editIndex, path)
}

func noChangeError(path string, total int) error {
	if total == 1 {
		return upstreamToolErrorf(
			"No changes made to %s. The replacement produced identical content. This might indicate an issue with special characters or the text not existing as expected.",
			path,
		)
	}
	return upstreamToolErrorf("No changes made to %s. The replacements produced identical content.", path)
}

func getLineSpans(content string) []lineSpan {
	var spans []lineSpan
	for start := 0; start < len(content); {
		end := len(content)
		if lineFeed := strings.IndexByte(content[start:], '\n'); lineFeed != -1 {
			end = start + lineFeed + 1
		}
		spans = append(spans, lineSpan{start: start, end: end})
		start = end
	}
	return spans
}

func replacementLineRange(lines []lineSpan, replacement textReplacement) (startLine, endLine int, err error) {
	replacementEnd := replacement.matchIndex + replacement.matchLen
	startLine = -1
	for index, line := range lines {
		if replacement.matchIndex >= line.start && replacement.matchIndex < line.end {
			startLine = index
			break
		}
	}
	if startLine == -1 {
		return 0, 0, upstreamToolError("Replacement range is outside the base content.")
	}
	endLine = startLine
	for endLine < len(lines) && lines[endLine].end < replacementEnd {
		endLine++
	}
	if endLine >= len(lines) {
		return 0, 0, upstreamToolError("Replacement range is outside the base content.")
	}
	return startLine, endLine + 1, nil
}

func applyReplacements(content string, replacements []textReplacement, offset int) string {
	result := content
	for index := len(replacements) - 1; index >= 0; index-- {
		replacement := replacements[index]
		matchIndex := replacement.matchIndex - offset
		result = result[:matchIndex] + replacement.newText + result[matchIndex+replacement.matchLen:]
	}
	return result
}

// widenReplacementsToLines turns replacements matched in baseContent, a fuzzy-normalized view of
// originalContent, into replacements of originalContent: each group of touched lines is rewritten
// from the normalized base, and every other line keeps its original text.
func widenReplacementsToLines(originalContent, baseContent string, replacements []textReplacement) ([]textReplacement, error) {
	originalLines := getLineSpans(originalContent)
	baseLines := getLineSpans(baseContent)
	if len(originalLines) != len(baseLines) {
		return nil, upstreamToolError("Cannot preserve unchanged lines because the base content has a different line count.")
	}

	type replacementGroup struct {
		startLine    int
		endLine      int
		replacements []textReplacement
	}
	groups := make([]replacementGroup, 0, len(replacements))
	for _, replacement := range replacements {
		startLine, endLine, err := replacementLineRange(baseLines, replacement)
		if err != nil {
			return nil, err
		}
		if len(groups) > 0 && startLine < groups[len(groups)-1].endLine {
			current := &groups[len(groups)-1]
			if endLine > current.endLine {
				current.endLine = endLine
			}
			current.replacements = append(current.replacements, replacement)
			continue
		}
		groups = append(groups, replacementGroup{startLine: startLine, endLine: endLine, replacements: []textReplacement{replacement}})
	}

	widened := make([]textReplacement, len(groups))
	for index, group := range groups {
		groupStart := baseLines[group.startLine].start
		groupEnd := baseLines[group.endLine-1].end
		originalStart := originalLines[group.startLine].start
		widened[index] = textReplacement{
			matchIndex: originalStart,
			matchLen:   originalLines[group.endLine-1].end - originalStart,
			newText:    applyReplacements(baseContent[groupStart:groupEnd], group.replacements, groupStart),
		}
	}
	return widened, nil
}

func GenerateUnifiedPatch(path, oldContent, newContent string, contextLines int) (string, error) {
	var patch strings.Builder
	fmt.Fprintf(&patch, "--- %s\n+++ %s\n", path, path)
	if err := writeUnifiedHunks(&patch, oldContent, newContent, contextLines, 0, 0); err != nil {
		return "", err
	}
	return patch.String(), nil
}

// writeUnifiedHunks writes the hunks of a diff of two texts that start after oldLine and newLine
// lines of their files.
func writeUnifiedHunks(patch *strings.Builder, oldContent, newContent string, contextLines, oldLine, newLine int) error {
	structured, err := udiff.ToUnifiedDiff("", "", oldContent, myers.ComputeEdits(oldContent, newContent), contextLines)
	if err != nil {
		return err
	}
	// go-udiff advances its new-file cursor only when it opens a hunk
	// (unified.go:143), so every hunk after an edit that was joined into a
	// previous hunk reports a ToLine short by the joined line count. Derive each
	// start from the previous hunk: unchanged lines between hunks span the same
	// count on both sides.
	fromEnd, toEnd := 0, 0
	for index, hunk := range structured.Hunks {
		fromCount := 0
		toCount := 0
		for _, line := range hunk.Lines {
			switch line.Kind {
			case udiff.Delete:
				fromCount++
			case udiff.Insert:
				toCount++
			default:
				fromCount++
				toCount++
			}
		}
		fromLine := hunk.FromLine
		toLine := hunk.ToLine
		if index > 0 {
			toLine = toEnd + fromLine - fromEnd
		}
		fromEnd, toEnd = fromLine+fromCount, toLine+toCount
		if fromLine == 1 && fromCount == 0 {
			fromLine = 0
		}
		if toLine == 1 && toCount == 0 {
			toLine = 0
		}
		fmt.Fprintf(patch, "@@ -%d,%d +%d,%d @@\n", fromLine+oldLine, fromCount, toLine+newLine, toCount)
		for _, line := range hunk.Lines {
			prefix := " "
			switch line.Kind {
			case udiff.Delete:
				prefix = "-"
			case udiff.Insert:
				prefix = "+"
			}
			patch.WriteString(prefix)
			patch.WriteString(line.Content)
			if !strings.HasSuffix(line.Content, "\n") {
				patch.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	return nil
}

type diffPart struct {
	kind  udiff.OpKind
	lines []string
}

func GenerateDiffString(oldContent, newContent string, contextLines int) DiffResult {
	output, firstChangedLine := formatDisplayDiff(lineDiffParts(oldContent, newContent), 1, 1, lineNumberWidth(oldContent, newContent), contextLines)
	return DiffResult{Diff: strings.Join(output, "\n"), FirstChangedLine: firstChangedLine}
}

// diffWindowMarginLines is the unchanged lines diffed on each side of a replacement: well above
// the context shown, so a change aligns within its window as it would over the whole file.
const diffWindowMarginLines = 32

// GenerateEditDiff diffs an edit only around its replacements, so time and memory follow the
// size of the change instead of the file.
func GenerateEditDiff(path string, applied AppliedEditsResult) (EditToolDetails, error) {
	width := lineNumberWidth(applied.BaseContent, applied.NewContent)
	skipMarker := " " + strings.Repeat(" ", width) + " ..."
	var output []string
	var firstChangedLine *int
	var patch strings.Builder
	fmt.Fprintf(&patch, "--- %s\n+++ %s\n", path, path)
	for _, window := range diffWindows(applied.BaseContent, applied.replacements) {
		oldText := applied.BaseContent[window.oldStart:window.oldEnd]
		newText := applied.NewContent[window.newStart:window.newEnd]
		shown, first := formatDisplayDiff(lineDiffParts(oldText, newText), window.oldLine+1, window.newLine+1, width, 4)
		if firstChangedLine == nil {
			firstChangedLine = first
		}
		// Both windows around a run of unchanged lines mark it as skipped; the whole-file diff shows one marker.
		if len(output) > 0 && len(shown) > 0 && output[len(output)-1] == skipMarker && shown[0] == skipMarker {
			shown = shown[1:]
		}
		output = append(output, shown...)
		if err := writeUnifiedHunks(&patch, oldText, newText, 4, window.oldLine, window.newLine); err != nil {
			return EditToolDetails{}, err
		}
	}
	return EditToolDetails{Diff: strings.Join(output, "\n"), Patch: patch.String(), FirstChangedLine: firstChangedLine}, nil
}

// diffWindow is a pair of matching whole-line ranges of the old and new contents: byte offsets
// and 0-based first lines.
type diffWindow struct {
	oldStart, oldEnd, oldLine int
	newStart, newEnd, newLine int
}

func lineStartBefore(text string, offset int) int {
	return strings.LastIndexByte(text[:offset], '\n') + 1
}

// diffWindows returns whole-line windows with margin around the sorted, disjoint replacements;
// overlapping windows merge.
func diffWindows(base string, replacements []textReplacement) []diffWindow {
	var windows []diffWindow
	offsetDelta, lineDelta, cursorOffset, cursorLine := 0, 0, 0, 0
	for _, replacement := range replacements {
		matchEnd := replacement.matchIndex + replacement.matchLen
		start := lineStartBefore(base, replacement.matchIndex)
		for range diffWindowMarginLines {
			if start == 0 {
				break
			}
			start = lineStartBefore(base, start-1)
		}
		end := matchEnd
		for index := 0; index <= diffWindowMarginLines && end < len(base); index++ {
			from := end
			if index == 0 {
				from = max(matchEnd-1, replacement.matchIndex)
			}
			end = len(base)
			if lineFeed := strings.IndexByte(base[from:], '\n'); lineFeed != -1 {
				end = from + lineFeed + 1
			}
		}
		replacementOffsetDelta := len(replacement.newText) - replacement.matchLen
		replacementLineDelta := strings.Count(replacement.newText, "\n") - strings.Count(base[replacement.matchIndex:matchEnd], "\n")
		if count := len(windows); count > 0 && start <= windows[count-1].oldEnd {
			previous := &windows[count-1]
			previous.oldEnd = max(previous.oldEnd, end)
			previous.newEnd = previous.oldEnd + offsetDelta + replacementOffsetDelta
		} else {
			cursorLine += strings.Count(base[cursorOffset:start], "\n")
			cursorOffset = start
			windows = append(windows, diffWindow{
				oldStart: start, oldEnd: end, oldLine: cursorLine,
				newStart: start + offsetDelta, newEnd: end + offsetDelta + replacementOffsetDelta, newLine: cursorLine + lineDelta,
			})
		}
		offsetDelta += replacementOffsetDelta
		lineDelta += replacementLineDelta
	}
	return windows
}

func lineNumberWidth(oldContent, newContent string) int {
	return len(strconv.Itoa(max(strings.Count(oldContent, "\n"), strings.Count(newContent, "\n")) + 1))
}

// lineDiffParts groups a line diff into runs of equal, deleted and inserted lines.
func lineDiffParts(oldContent, newContent string) []diffPart {
	edits := myers.ComputeEdits(oldContent, newContent)
	if len(edits) == 0 {
		return nil
	}
	context := strings.Count(oldContent, "\n") + strings.Count(newContent, "\n") + 3
	structured, err := udiff.ToUnifiedDiff("", "", oldContent, edits, context)
	if err != nil {
		return nil
	}
	var parts []diffPart
	for _, hunk := range structured.Hunks {
		for _, line := range hunk.Lines {
			if len(parts) == 0 || parts[len(parts)-1].kind != line.Kind {
				parts = append(parts, diffPart{kind: line.Kind})
			}
			parts[len(parts)-1].lines = append(parts[len(parts)-1].lines, strings.TrimSuffix(line.Content, "\n"))
		}
	}
	return parts
}

// formatDisplayDiff formats line diff parts as display diff lines, numbering from the given
// 1-based old and new lines.
func formatDisplayDiff(parts []diffPart, oldLineNumber, newLineNumber, lineWidth, contextLines int) ([]string, *int) {
	lastWasChange := false
	var firstChangedLine *int
	var output []string
	for index, part := range parts {
		if part.kind == udiff.Insert || part.kind == udiff.Delete {
			if firstChangedLine == nil {
				line := newLineNumber
				firstChangedLine = &line
			}
			for _, line := range part.lines {
				if part.kind == udiff.Insert {
					output = append(output, fmt.Sprintf("+%*d %s", lineWidth, newLineNumber, line))
					newLineNumber++
				} else {
					output = append(output, fmt.Sprintf("-%*d %s", lineWidth, oldLineNumber, line))
					oldLineNumber++
				}
			}
			lastWasChange = true
			continue
		}

		nextIsChange := index < len(parts)-1 && (parts[index+1].kind == udiff.Insert || parts[index+1].kind == udiff.Delete)
		hasLeadingChange := lastWasChange
		if hasLeadingChange && nextIsChange {
			if len(part.lines) <= contextLines*2 {
				for _, line := range part.lines {
					output = append(output, fmt.Sprintf(" %*d %s", lineWidth, oldLineNumber, line))
					oldLineNumber++
					newLineNumber++
				}
			} else {
				leading := part.lines[:contextLines]
				trailing := part.lines[len(part.lines)-contextLines:]
				skipped := len(part.lines) - len(leading) - len(trailing)
				for _, line := range leading {
					output = append(output, fmt.Sprintf(" %*d %s", lineWidth, oldLineNumber, line))
					oldLineNumber++
					newLineNumber++
				}
				output = append(output, " "+strings.Repeat(" ", lineWidth)+" ...")
				oldLineNumber += skipped
				newLineNumber += skipped
				for _, line := range trailing {
					output = append(output, fmt.Sprintf(" %*d %s", lineWidth, oldLineNumber, line))
					oldLineNumber++
					newLineNumber++
				}
			}
		} else if hasLeadingChange {
			shown := part.lines
			if len(shown) > contextLines {
				shown = shown[:contextLines]
			}
			for _, line := range shown {
				output = append(output, fmt.Sprintf(" %*d %s", lineWidth, oldLineNumber, line))
				oldLineNumber++
				newLineNumber++
			}
			skipped := len(part.lines) - len(shown)
			if skipped > 0 {
				output = append(output, " "+strings.Repeat(" ", lineWidth)+" ...")
				oldLineNumber += skipped
				newLineNumber += skipped
			}
		} else if nextIsChange {
			skipped := len(part.lines) - contextLines
			if skipped < 0 {
				skipped = 0
			}
			if skipped > 0 {
				output = append(output, " "+strings.Repeat(" ", lineWidth)+" ...")
				oldLineNumber += skipped
				newLineNumber += skipped
			}
			for _, line := range part.lines[skipped:] {
				output = append(output, fmt.Sprintf(" %*d %s", lineWidth, oldLineNumber, line))
				oldLineNumber++
				newLineNumber++
			}
		} else {
			oldLineNumber += len(part.lines)
			newLineNumber += len(part.lines)
		}
		lastWasChange = false
	}
	return output, firstChangedLine
}
