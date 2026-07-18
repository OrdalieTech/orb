package tui

// Spacer renders a configurable number of empty rows.
type Spacer struct{ lines int }

func NewSpacer(lines int) *Spacer         { return &Spacer{lines: lines} }
func (spacer *Spacer) SetLines(lines int) { spacer.lines = lines }
func (spacer *Spacer) Invalidate()        {}
func (spacer *Spacer) Render(_ int) []string {
	return make([]string, max(0, spacer.lines))
}
