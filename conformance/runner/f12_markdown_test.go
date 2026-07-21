package runner_test

import (
	"testing"

	"github.com/OrdalieTech/pigo/conformance/runner"
	"github.com/OrdalieTech/pigo/tui"
)

type f12MarkdownFixture struct {
	SchemaVersion int               `json:"schemaVersion"`
	Cases         []f12MarkdownCase `json:"cases"`
}

type f12MarkdownCase struct {
	Name                       string   `json:"name"`
	Text                       string   `json:"text"`
	Width                      int      `json:"width"`
	PaddingX                   int      `json:"paddingX"`
	PaddingY                   int      `json:"paddingY"`
	DefaultStyle               string   `json:"defaultStyle"`
	PreserveOrderedListMarkers bool     `json:"preserveOrderedListMarkers"`
	PreserveBackslashEscapes   bool     `json:"preserveBackslashEscapes"`
	Hyperlinks                 bool     `json:"hyperlinks"`
	Expected                   []string `json:"expected"`
}

func TestF12MarkdownRendersMatchUpstream(t *testing.T) {
	var fixture f12MarkdownFixture
	runner.LoadJSON(t, "F12", "markdown.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Cases) != 71 {
		t.Fatalf("F12 markdown header = version %d, cases %d", fixture.SchemaVersion, len(fixture.Cases))
	}
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			options := &tui.MarkdownOptions{
				PreserveOrderedListMarkers: fixtureCase.PreserveOrderedListMarkers,
				PreserveBackslashEscapes:   fixtureCase.PreserveBackslashEscapes,
				Hyperlinks:                 fixtureCase.Hyperlinks,
			}
			component := tui.NewMarkdown(
				fixtureCase.Text,
				fixtureCase.PaddingX,
				fixtureCase.PaddingY,
				f12MarkdownTheme(),
				f12MarkdownDefaultStyle(fixtureCase.DefaultStyle),
				options,
			)
			if diff := linesDiff(fixtureCase.Expected, component.Render(fixtureCase.Width)); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func f12MarkdownTheme() tui.MarkdownTheme {
	return tui.MarkdownTheme{
		Heading:         func(value string) string { return ansiBold(ansiForeground(36, value)) },
		Link:            func(value string) string { return ansiForeground(34, value) },
		LinkURL:         ansiDim,
		Code:            func(value string) string { return ansiForeground(33, value) },
		CodeBlock:       func(value string) string { return ansiForeground(32, value) },
		CodeBlockBorder: ansiDim,
		Quote:           ansiItalic,
		QuoteBorder:     ansiDim,
		HorizontalRule:  ansiDim,
		ListBullet:      func(value string) string { return ansiForeground(36, value) },
		Bold:            ansiBold,
		Italic:          ansiItalic,
		Strikethrough:   ansiStrikethrough,
		Underline:       ansiUnderline,
	}
}

func f12MarkdownDefaultStyle(name string) *tui.DefaultTextStyle {
	switch name {
	case "gray-italic":
		return &tui.DefaultTextStyle{Color: func(value string) string { return ansiForeground(90, value) }, Italic: true}
	case "magenta":
		return &tui.DefaultTextStyle{Color: func(value string) string { return ansiForeground(35, value) }}
	case "cyan":
		return &tui.DefaultTextStyle{Color: func(value string) string { return ansiForeground(36, value) }}
	case "yellow-italic":
		return &tui.DefaultTextStyle{Color: func(value string) string { return ansiForeground(33, value) }, Italic: true}
	default:
		return nil
	}
}

func ansiForeground(code int, value string) string {
	return "\x1b[" + integerStringForTest(code) + "m" + value + "\x1b[39m"
}

func ansiBold(value string) string          { return "\x1b[1m" + value + "\x1b[22m" }
func ansiDim(value string) string           { return "\x1b[2m" + value + "\x1b[22m" }
func ansiItalic(value string) string        { return "\x1b[3m" + value + "\x1b[23m" }
func ansiUnderline(value string) string     { return "\x1b[4m" + value + "\x1b[24m" }
func ansiStrikethrough(value string) string { return "\x1b[9m" + value + "\x1b[29m" }

func integerStringForTest(value int) string {
	if value < 10 {
		return string(rune('0' + value))
	}
	return string([]byte{byte('0' + value/10), byte('0' + value%10)})
}
