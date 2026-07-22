package tui

import (
	"fmt"
	"testing"
	"time"
)

type benchmarkLine struct {
	lines []string
	copy  bool
}

func (line *benchmarkLine) Render(int) []string {
	if line.copy {
		return append([]string(nil), line.lines...)
	}
	return line.lines
}

type benchmarkTerminal struct {
	columns, rows int
	lastBytes     int
}

func (terminal *benchmarkTerminal) Start(func(string), func()) error        { return nil }
func (terminal *benchmarkTerminal) Stop() error                             { return nil }
func (terminal *benchmarkTerminal) DrainInput(time.Duration, time.Duration) {}
func (terminal *benchmarkTerminal) Write(data string)                       { terminal.lastBytes = len(data) }
func (terminal *benchmarkTerminal) Columns() int                            { return terminal.columns }
func (terminal *benchmarkTerminal) Rows() int                               { return terminal.rows }
func (terminal *benchmarkTerminal) KittyProtocolActive() bool               { return false }
func (terminal *benchmarkTerminal) MoveBy(int)                              {}
func (terminal *benchmarkTerminal) HideCursor()                             {}
func (terminal *benchmarkTerminal) ShowCursor()                             {}
func (terminal *benchmarkTerminal) ClearLine()                              {}
func (terminal *benchmarkTerminal) ClearFromCursor()                        {}
func (terminal *benchmarkTerminal) ClearScreen()                            {}
func (terminal *benchmarkTerminal) SetTitle(string)                         {}
func (terminal *benchmarkTerminal) SetProgress(bool)                        {}

func BenchmarkViewportHugeHistory(b *testing.B) {
	for _, lines := range []int{100_000, 1_000_000} {
		setup := func() (*TUI, *Container, *benchmarkLine, *benchmarkTerminal) {
			body := NewWindowedContainer()
			historyLine := &benchmarkLine{lines: []string{"history"}}
			for range lines - 1 {
				body.AddChild(historyLine)
			}
			tail := &benchmarkLine{lines: []string{"tail"}}
			body.AddChild(tail)
			terminal := &benchmarkTerminal{columns: 120, rows: 40}
			ui := NewTUI(terminal)
			ui.SetViewport(body, &benchmarkLine{lines: []string{"input"}})
			_ = ui.renderViewport(120, 40)
			return ui, body, tail, terminal
		}

		b.Run(fmt.Sprintf("%d-lines/steady", lines), func(b *testing.B) {
			ui, _, _, _ := setup()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = ui.renderViewport(120, 40)
			}
		})

		b.Run(fmt.Sprintf("%d-lines/tail-update", lines), func(b *testing.B) {
			ui, body, tail, _ := setup()
			flip := false
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				flip = !flip
				if flip {
					tail.lines[0] = "tail a"
				} else {
					tail.lines[0] = "tail b"
				}
				body.ChildChanged(tail)
				_ = ui.renderViewport(120, 40)
			}
		})

		b.Run(fmt.Sprintf("%d-lines/full-frame", lines), func(b *testing.B) {
			ui, body, tail, _ := setup()
			ui.setStopped(false)
			ui.RenderNow()
			flip := false
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				flip = !flip
				if flip {
					tail.lines[0] = "tail a"
				} else {
					tail.lines[0] = "tail b"
				}
				body.ChildChanged(tail)
				ui.RenderNow()
			}
		})

		b.Run(fmt.Sprintf("%d-lines/append-detached", lines), func(b *testing.B) {
			ui, body, _, _ := setup()
			ui.viewportFollow = false
			ui.viewportEnd = lines - 100
			appended := &benchmarkLine{lines: []string{"appended"}}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				body.AddChild(appended)
				_ = ui.renderViewport(120, 40)
			}
		})

		b.Run(fmt.Sprintf("%d-lines/dirty-tail-detached", lines), func(b *testing.B) {
			ui, body, tail, _ := setup()
			ui.viewportFollow = false
			ui.viewportEnd = lines - 100
			tail.lines = make([]string, 10_000)
			tail.copy = true
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				body.ChildChanged(tail)
				_ = ui.renderViewport(120, 40)
			}
		})
	}
}

func BenchmarkWindowedContainerColdUniqueHistory(b *testing.B) {
	for _, lines := range []int{100_000, 1_000_000} {
		b.Run(fmt.Sprintf("%d-lines", lines), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				body := NewWindowedContainer()
				for range lines {
					body.AddChild(&benchmarkLine{lines: []string{"history"}})
				}
				_ = body.LineCount(120)
			}
		})
	}
}
