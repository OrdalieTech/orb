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

type f12TerminalImages struct {
	SchemaVersion int                  `json:"schemaVersion"`
	EncodingCases []f12EncodingCase    `json:"encodingCases"`
	CellCases     []f12CellCase        `json:"cellCases"`
	RenderCases   []f12ImageRenderCase `json:"renderCases"`
}

type f12EncodingCase struct {
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Data     string         `json:"data"`
	Options  map[string]any `json:"options"`
	Expected string         `json:"expected"`
}

type f12CellCase struct {
	Name       string              `json:"name"`
	Dimensions tui.ImageDimensions `json:"dimensions"`
	MaxWidth   float64             `json:"maxWidth"`
	MaxHeight  float64             `json:"maxHeight"`
	Cell       struct {
		WidthPx  int `json:"widthPx"`
		HeightPx int `json:"heightPx"`
	} `json:"cell"`
	Expected struct {
		Columns int `json:"columns"`
		Rows    int `json:"rows"`
	} `json:"expected"`
}

type f12ImageRenderCase struct {
	Name       string              `json:"name"`
	Protocol   *string             `json:"protocol"`
	Width      int                 `json:"width"`
	Data       string              `json:"data"`
	MimeType   string              `json:"mimeType"`
	Dimensions tui.ImageDimensions `json:"dimensions"`
	Options    struct {
		MaxWidthCells  *int    `json:"maxWidthCells"`
		MaxHeightCells *int    `json:"maxHeightCells"`
		Filename       string  `json:"filename"`
		ImageID        *uint32 `json:"imageId"`
	} `json:"options"`
	Expected []string `json:"expected"`
}

func TestF12TerminalImagesMatchUpstream(t *testing.T) {
	var fixture f12TerminalImages
	runner.LoadJSON(t, "F12", "terminal-images.json", &fixture)
	if fixture.SchemaVersion != 2 || len(fixture.EncodingCases) != 3 || len(fixture.CellCases) != 3 || len(fixture.RenderCases) != 4 {
		t.Fatalf("terminal-image fixture header = %#v", fixture)
	}
	for _, fixtureCase := range fixture.EncodingCases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			var got string
			switch fixtureCase.Kind {
			case "kitty":
				got = tui.EncodeKitty(fixtureCase.Data, jsonInt(fixtureCase.Options["columns"]), jsonInt(fixtureCase.Options["rows"]), uint32(jsonInt(fixtureCase.Options["imageId"])), jsonBoolDefault(fixtureCase.Options, "moveCursor", true))
			case "iterm2":
				got = tui.EncodeITerm2(fixtureCase.Data, fixtureCase.Options["width"], fixtureCase.Options["height"], jsonString(fixtureCase.Options["name"]), jsonBoolDefault(fixtureCase.Options, "preserveAspectRatio", true), jsonBoolDefault(fixtureCase.Options, "inline", true))
			default:
				t.Fatalf("unknown encoding kind %q", fixtureCase.Kind)
			}
			if got != fixtureCase.Expected {
				t.Fatalf("encoding differs\nwant: %q\ngot:  %q", fixtureCase.Expected, got)
			}
		})
	}
	for _, fixtureCase := range fixture.CellCases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			maxHeight := int(fixtureCase.MaxHeight)
			got := tui.CalculateImageCellSize(fixtureCase.Dimensions, int(fixtureCase.MaxWidth), &maxHeight, tui.CellDimensions{WidthPx: fixtureCase.Cell.WidthPx, HeightPx: fixtureCase.Cell.HeightPx})
			if got.Columns != fixtureCase.Expected.Columns || got.Rows != fixtureCase.Expected.Rows {
				t.Fatalf("cell size = %#v, want %#v", got, fixtureCase.Expected)
			}
		})
	}
	t.Cleanup(func() {
		tui.ResetCapabilitiesCache()
		tui.SetCellDimensions(tui.CellDimensions{WidthPx: 9, HeightPx: 18})
	})
	tui.SetCellDimensions(tui.CellDimensions{WidthPx: 10, HeightPx: 20})
	for _, fixtureCase := range fixture.RenderCases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			protocol := tui.ImageProtocol("")
			if fixtureCase.Protocol != nil {
				protocol = tui.ImageProtocol(*fixtureCase.Protocol)
			}
			tui.SetCapabilities(tui.TerminalCapabilities{Images: protocol, TrueColor: true, Hyperlinks: true})
			component := tui.NewImage(fixtureCase.Data, fixtureCase.MimeType, tui.ImageTheme{FallbackColor: func(value string) string { return "<" + value + ">" }}, &tui.ImageOptions{
				MaxWidthCells: fixtureCase.Options.MaxWidthCells, MaxHeightCells: fixtureCase.Options.MaxHeightCells, Filename: fixtureCase.Options.Filename, ImageID: fixtureCase.Options.ImageID,
			}, &fixtureCase.Dimensions)
			if diff := linesDiff(fixtureCase.Expected, component.Render(fixtureCase.Width)); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func jsonInt(value any) int {
	number, _ := value.(float64)
	return int(number)
}

func jsonString(value any) string {
	text, _ := value.(string)
	return text
}

func jsonBoolDefault(object map[string]any, key string, fallback bool) bool {
	value, exists := object[key]
	if !exists {
		return fallback
	}
	result, ok := value.(bool)
	if !ok {
		return fallback
	}
	return result
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
