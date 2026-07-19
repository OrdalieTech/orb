package tui

// VisualTruncateResult contains the tail visual lines of wrapped text and the
// number of visual lines omitted before them.
type VisualTruncateResult struct {
	VisualLines  []string
	SkippedCount int
}

// TruncateToVisualLines renders text with Text's width and padding semantics,
// then retains the last maxVisualLines physical terminal rows.
func TruncateToVisualLines(text string, maxVisualLines, width, paddingX int) VisualTruncateResult {
	if text == "" {
		return VisualTruncateResult{}
	}
	allVisualLines := NewText(text, paddingX, 0, nil).Render(width)
	if len(allVisualLines) <= maxVisualLines {
		return VisualTruncateResult{VisualLines: allVisualLines}
	}
	start := len(allVisualLines) - maxVisualLines
	if maxVisualLines <= 0 {
		start = 0
	}
	return VisualTruncateResult{
		VisualLines:  allVisualLines[start:],
		SkippedCount: len(allVisualLines) - maxVisualLines,
	}
}
