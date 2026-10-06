package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestPaddingSafety(t *testing.T) {
	constructors := map[string]func(int, int) Component{
		"Markdown": func(x, y int) Component { return NewMarkdown("ab", x, y, MarkdownTheme{}, nil, nil) },
		"Box": func(x, y int) Component {
			box := NewBox(x, y, nil)
			box.AddChild(NewText("ab", 0, 0, nil))
			return box
		},
	}
	for name, newComponent := range constructors {
		t.Run(name, func(t *testing.T) {
			for _, padding := range [][2]int{{-2, 0}, {0, -2}, {-2, -2}, {-2, 1}, {1, -2}} {
				for _, width := range []int{0, 1, 2, 8} {
					t.Run(fmt.Sprintf("x=%d/y=%d/width=%d", padding[0], padding[1], width), func(t *testing.T) {
						defer func() {
							if value := recover(); value != nil {
								t.Fatalf("render panicked: %v", value)
							}
						}()
						component := newComponent(padding[0], padding[1])
						want := newComponent(max(0, padding[0]), max(0, padding[1])).Render(width)
						for range 2 {
							got := component.Render(width)
							if !slices.Equal(got, want) {
								t.Fatalf("got %q, want normalized padding %q", got, want)
							}
							var content strings.Builder
							for _, line := range got {
								content.WriteString(strings.TrimSpace(StripANSI(line)))
							}
							if content.String() != "ab" {
								t.Fatalf("content lost: %q", got)
							}
						}
					})
				}
			}
			for _, tc := range []struct {
				x, y int
				want []string
			}{
				{0, 0, []string{"ab      "}},
				{1, 0, []string{" ab     "}},
				{0, 1, []string{"        ", "ab      ", "        "}},
				{1, 1, []string{"        ", " ab     ", "        "}},
			} {
				t.Run(fmt.Sprintf("nonnegative/x=%d/y=%d", tc.x, tc.y), func(t *testing.T) {
					if got := newComponent(tc.x, tc.y).Render(8); !slices.Equal(got, tc.want) {
						t.Fatalf("got %q, want %q", got, tc.want)
					}
				})
			}
		})
	}
}
