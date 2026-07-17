package runner_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/OrdalieTech/pi-go/conformance/runner"
	"github.com/OrdalieTech/pi-go/internal/truncate"
)

type f5Fixture struct {
	Cases []f5Case `json:"cases"`
}

type f5Case struct {
	Name      string           `json:"name"`
	Operation string           `json:"operation"`
	Input     f5Input          `json:"input"`
	Options   truncate.Options `json:"options"`
	MaxChars  *int             `json:"maxChars"`
	Bytes     int              `json:"bytes"`
	Expected  json.RawMessage  `json:"expected"`
}

type f5Input struct {
	Kind            string   `json:"kind"`
	Value           string   `json:"value"`
	Count           int      `json:"count"`
	TrailingNewline bool     `json:"trailingNewline"`
	Units           []uint16 `json:"units"`
}

func TestF5TruncationMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "F5")
	if manifest.Family != "F5" || manifest.Generator != "conformance/extract/f5-truncation.ts" {
		t.Fatalf("unexpected F5 manifest: %+v", manifest)
	}
	var fixture f5Fixture
	runner.LoadJSON(t, "F5", "cases.json", &fixture)
	if len(fixture.Cases) != 27 {
		t.Fatalf("F5 contains %d cases, want 27", len(fixture.Cases))
	}
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			switch fixtureCase.Operation {
			case "head":
				input := materializeF5Input(t, fixtureCase.Input)
				var want truncate.Result
				decodeF5Expected(t, fixtureCase.Expected, &want)
				got := truncate.TruncateHead(input, fixtureCase.Options)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("result mismatch\nwant: %+v\n got: %+v", want, got)
				}
			case "tail":
				input := materializeF5Input(t, fixtureCase.Input)
				var want truncate.Result
				decodeF5Expected(t, fixtureCase.Expected, &want)
				got := truncate.TruncateTail(input, fixtureCase.Options)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("result mismatch\nwant: %+v\n got: %+v", want, got)
				}
			case "line":
				input := materializeF5Input(t, fixtureCase.Input)
				var want truncate.LineResult
				decodeF5Expected(t, fixtureCase.Expected, &want)
				var got truncate.LineResult
				if fixtureCase.MaxChars == nil {
					got = truncate.TruncateLine(input)
				} else {
					got = truncate.TruncateLine(input, *fixtureCase.MaxChars)
				}
				if got != want {
					t.Fatalf("result mismatch\nwant: %+v\n got: %+v", want, got)
				}
			case "size":
				var want string
				decodeF5Expected(t, fixtureCase.Expected, &want)
				if got := truncate.FormatSize(fixtureCase.Bytes); got != want {
					t.Fatalf("FormatSize(%d) = %q, want %q", fixtureCase.Bytes, got, want)
				}
			default:
				t.Fatalf("unknown F5 operation %q", fixtureCase.Operation)
			}
		})
	}
}

func materializeF5Input(t testing.TB, input f5Input) string {
	t.Helper()
	switch input.Kind {
	case "literal":
		return input.Value
	case "repeat":
		return strings.Repeat(input.Value, input.Count)
	case "repeatLines":
		value := strings.TrimSuffix(strings.Repeat(input.Value+"\n", input.Count), "\n")
		if input.TrailingNewline {
			value += "\n"
		}
		return value
	case "utf16":
		return f5StringFromUTF16(input.Units)
	default:
		t.Fatalf("unknown F5 input kind %q", input.Kind)
		return ""
	}
}

func f5StringFromUTF16(units []uint16) string {
	var output strings.Builder
	for index := 0; index < len(units); index++ {
		unit := units[index]
		if unit >= 0xd800 && unit <= 0xdbff && index+1 < len(units) && units[index+1] >= 0xdc00 && units[index+1] <= 0xdfff {
			output.WriteRune(utf16.DecodeRune(rune(unit), rune(units[index+1])))
			index++
			continue
		}
		if unit >= 0xd800 && unit <= 0xdfff {
			output.Write([]byte{byte(0xe0 | unit>>12), byte(0x80 | unit>>6&0x3f), byte(0x80 | unit&0x3f)})
			continue
		}
		output.WriteRune(rune(unit))
	}
	return output.String()
}

func decodeF5Expected(t testing.TB, data json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode F5 expected result: %v", err)
	}
}
