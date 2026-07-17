package tools

import (
	"fmt"
	"strconv"
	"strings"

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

func DetectLineEnding(content string) string {
	crlfIndex := strings.Index(content, "\r\n")
	lfIndex := strings.Index(content, "\n")
	if lfIndex == -1 || crlfIndex == -1 {
		return "\n"
	}
	if crlfIndex < lfIndex {
		return "\r\n"
	}
	return "\n"
}

func NormalizeToLF(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

func RestoreLineEndings(text, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

func NormalizeForFuzzyMatch(text string) string {
	lines := strings.Split(norm.NFKC.String(text), "\n")
	for index := range lines {
		lines[index] = strings.TrimRightFunc(lines[index], javascriptWhitespace)
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

func javascriptWhitespace(value rune) bool {
	switch value {
	case '\u0009', '\u000b', '\u000c', '\u000d', '\u0020', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return value >= '\u2000' && value <= '\u200a'
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

	usedFuzzyMatch := false
	for _, edit := range normalizedEdits {
		if fuzzyFindText(normalizedContent, edit.OldText).UsedFuzzyMatch {
			usedFuzzyMatch = true
			break
		}
	}
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
	newContent := ""
	if usedFuzzyMatch {
		var err error
		newContent, err = applyReplacementsPreservingUnchangedLines(normalizedContent, replacementBase, replacements)
		if err != nil {
			return AppliedEditsResult{}, err
		}
	} else {
		newContent = applyReplacements(replacementBase, replacements, 0)
	}
	if normalizedContent == newContent {
		return AppliedEditsResult{}, noChangeError(path, len(edits))
	}
	return AppliedEditsResult{BaseContent: normalizedContent, NewContent: newContent}, nil
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

func splitLinesWithEndings(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func getLineSpans(content string) []lineSpan {
	lines := splitLinesWithEndings(content)
	spans := make([]lineSpan, len(lines))
	offset := 0
	for index, line := range lines {
		spans[index] = lineSpan{start: offset, end: offset + len(line)}
		offset += len(line)
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

func applyReplacementsPreservingUnchangedLines(
	originalContent, baseContent string,
	replacements []textReplacement,
) (string, error) {
	originalLines := splitLinesWithEndings(originalContent)
	baseLines := getLineSpans(baseContent)
	if len(originalLines) != len(baseLines) {
		return "", upstreamToolError("Cannot preserve unchanged lines because the base content has a different line count.")
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
			return "", err
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

	var result strings.Builder
	originalLineIndex := 0
	for _, group := range groups {
		for _, line := range originalLines[originalLineIndex:group.startLine] {
			result.WriteString(line)
		}
		groupStart := baseLines[group.startLine].start
		groupEnd := baseLines[group.endLine-1].end
		result.WriteString(applyReplacements(baseContent[groupStart:groupEnd], group.replacements, groupStart))
		originalLineIndex = group.endLine
	}
	for _, line := range originalLines[originalLineIndex:] {
		result.WriteString(line)
	}
	return result.String(), nil
}

func GenerateUnifiedPatch(path, oldContent, newContent string, contextLines int) (string, error) {
	edits := myers.ComputeEdits(oldContent, newContent)
	structured, err := udiff.ToUnifiedDiff(path, path, oldContent, edits, contextLines)
	if err != nil {
		return "", err
	}
	if len(structured.Hunks) == 0 {
		return fmt.Sprintf("--- %s\n+++ %s\n", path, path), nil
	}
	var result strings.Builder
	fmt.Fprintf(&result, "--- %s\n+++ %s\n", path, path)
	for _, hunk := range structured.Hunks {
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
		if fromLine == 1 && fromCount == 0 {
			fromLine = 0
		}
		if toLine == 1 && toCount == 0 {
			toLine = 0
		}
		fmt.Fprintf(&result, "@@ -%d,%d +%d,%d @@\n", fromLine, fromCount, toLine, toCount)
		for _, line := range hunk.Lines {
			prefix := " "
			switch line.Kind {
			case udiff.Delete:
				prefix = "-"
			case udiff.Insert:
				prefix = "+"
			}
			result.WriteString(prefix)
			result.WriteString(line.Content)
			if !strings.HasSuffix(line.Content, "\n") {
				result.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	return result.String(), nil
}

type diffPart struct {
	kind  udiff.OpKind
	lines []string
}

func GenerateDiffString(oldContent, newContent string, contextLines int) DiffResult {
	edits := myers.ComputeEdits(oldContent, newContent)
	if len(edits) == 0 {
		return DiffResult{}
	}
	context := len(strings.Split(oldContent, "\n")) + len(strings.Split(newContent, "\n")) + 1
	structured, err := udiff.ToUnifiedDiff("", "", oldContent, edits, context)
	if err != nil || len(structured.Hunks) == 0 {
		return DiffResult{}
	}
	parts := make([]diffPart, 0)
	for _, hunk := range structured.Hunks {
		for _, line := range hunk.Lines {
			text := strings.TrimSuffix(line.Content, "\n")
			if len(parts) == 0 || parts[len(parts)-1].kind != line.Kind {
				parts = append(parts, diffPart{kind: line.Kind})
			}
			parts[len(parts)-1].lines = append(parts[len(parts)-1].lines, text)
		}
	}

	maxLines := len(strings.Split(oldContent, "\n"))
	if count := len(strings.Split(newContent, "\n")); count > maxLines {
		maxLines = count
	}
	lineWidth := len(strconv.Itoa(maxLines))
	oldLineNumber := 1
	newLineNumber := 1
	lastWasChange := false
	var firstChangedLine *int
	output := make([]string, 0)
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
	return DiffResult{Diff: strings.Join(output, "\n"), FirstChangedLine: firstChangedLine}
}
