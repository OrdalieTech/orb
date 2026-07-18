package runner_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/conformance/runner"
	"github.com/OrdalieTech/pi-go/tui"
)

type f12Fixture struct {
	SchemaVersion int       `json:"schemaVersion"`
	Cases         []f12Case `json:"cases"`
}

type f12Case struct {
	Name     string   `json:"name"`
	Width    int      `json:"width"`
	Node     f12Node  `json:"node"`
	Expected []string `json:"expected"`
}

type f12Node struct {
	Type         string    `json:"type"`
	Text         string    `json:"text"`
	Message      string    `json:"message"`
	PaddingX     *int      `json:"paddingX"`
	PaddingY     *int      `json:"paddingY"`
	Lines        *int      `json:"lines"`
	Style        string    `json:"style"`
	SpinnerStyle string    `json:"spinnerStyle"`
	MessageStyle string    `json:"messageStyle"`
	Frames       []string  `json:"frames"`
	Children     []f12Node `json:"children"`
}

func TestF12PrimitiveRendersMatchUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "F12")
	if manifest.Family != "F12" || manifest.Generator != "conformance/extract/f12-tui.ts" {
		t.Fatalf("unexpected F12 manifest: %+v", manifest)
	}
	var fixture f12Fixture
	runner.LoadJSON(t, "F12", "primitives.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Cases) != 14 {
		t.Fatalf("F12 header = version %d, cases %d", fixture.SchemaVersion, len(fixture.Cases))
	}
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			component, cleanup := buildF12Node(t, fixtureCase.Node)
			defer cleanup()
			got := component.Render(fixtureCase.Width)
			if diff := linesDiff(fixtureCase.Expected, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func buildF12Node(t *testing.T, node f12Node) (tui.Component, func()) {
	t.Helper()
	paddingX, paddingY := valueOr(node.PaddingX, 0), valueOr(node.PaddingY, 0)
	switch node.Type {
	case "text":
		if node.PaddingX == nil {
			paddingX = 1
		}
		if node.PaddingY == nil {
			paddingY = 1
		}
		return tui.NewText(node.Text, paddingX, paddingY, f12OptionalStyle(node.Style)), func() {}
	case "truncated-text":
		return tui.NewTruncatedText(node.Text, paddingX, paddingY), func() {}
	case "spacer":
		return tui.NewSpacer(valueOr(node.Lines, 1)), func() {}
	case "container":
		container := &tui.Container{}
		for _, childNode := range node.Children {
			child, _ := buildF12Node(t, childNode)
			container.AddChild(child)
		}
		return container, func() {}
	case "box":
		if node.PaddingX == nil {
			paddingX = 1
		}
		if node.PaddingY == nil {
			paddingY = 1
		}
		box := tui.NewBox(paddingX, paddingY, f12OptionalStyle(node.Style))
		for _, childNode := range node.Children {
			child, _ := buildF12Node(t, childNode)
			box.AddChild(child)
		}
		return box, func() {}
	case "loader":
		message := node.Message
		if message == "" {
			message = "Loading..."
		}
		loader := tui.NewLoader(nil, f12Style(node.SpinnerStyle), f12Style(node.MessageStyle), message, &tui.LoaderIndicatorOptions{Frames: node.Frames, Interval: 100_000 * time.Millisecond})
		return loader, loader.Stop
	default:
		t.Fatalf("unknown F12 node type %q", node.Type)
		return nil, func() {}
	}
}

func f12OptionalStyle(name string) tui.StyleFunc {
	if name == "" {
		return nil
	}
	return f12Style(name)
}

func f12Style(name string) tui.StyleFunc {
	switch name {
	case "red":
		return func(value string) string { return "\x1b[31m" + value + "\x1b[39m" }
	case "blue-bg":
		return func(value string) string { return "\x1b[44m" + value + "\x1b[49m" }
	case "bracket":
		return func(value string) string { return "[" + value + "]" }
	default:
		return func(value string) string { return value }
	}
}

func valueOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func linesDiff(want, got []string) string {
	if len(want) != len(got) {
		return fmt.Sprintf("line count differs: want %d, got %d\nwant: %q\ngot:  %q", len(want), len(got), want, got)
	}
	for index := range want {
		if want[index] != got[index] {
			return fmt.Sprintf("line differs at index %d: want %q, got %q", index, want[index], got[index])
		}
	}
	return ""
}
