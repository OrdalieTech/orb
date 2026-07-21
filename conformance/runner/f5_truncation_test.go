package runner_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	agenttools "github.com/OrdalieTech/pigo/codingagent/tools"
	"github.com/OrdalieTech/pigo/conformance/runner"
	"github.com/OrdalieTech/pigo/internal/truncate"
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

type f5AccumulatorFixture struct {
	Cases []f5AccumulatorCase `json:"cases"`
}

type f5AccumulatorCase struct {
	Name     string               `json:"name"`
	Options  f5AccumulatorOptions `json:"options"`
	Chunks   []f5AccumulatorChunk `json:"chunks"`
	Expected f5AccumulatorResult  `json:"expected"`
}

type f5AccumulatorOptions struct {
	MaxLines       *int   `json:"maxLines"`
	MaxBytes       *int   `json:"maxBytes"`
	TempFilePrefix string `json:"tempFilePrefix"`
}

type f5AccumulatorChunk struct {
	Kind            string `json:"kind"`
	Value           string `json:"value"`
	Values          []byte `json:"values"`
	Count           int    `json:"count"`
	TrailingNewline bool   `json:"trailingNewline"`
}

type f5AccumulatorSnapshot struct {
	Content           string          `json:"content"`
	Truncation        truncate.Result `json:"truncation"`
	HasFullOutputPath bool            `json:"hasFullOutputPath"`
}

type f5AccumulatorResult struct {
	ChunkSnapshots           []f5AccumulatorSnapshot `json:"chunkSnapshots"`
	FinalSnapshot            f5AccumulatorSnapshot   `json:"finalSnapshot"`
	IdempotentFinishSnapshot f5AccumulatorSnapshot   `json:"idempotentFinishSnapshot"`
	LastLineBytes            int                     `json:"lastLineBytes"`
	AppendAfterFinishError   string                  `json:"appendAfterFinishError"`
	PersistedOutputBase64    *string                 `json:"persistedOutputBase64"`
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

func TestF5OutputAccumulatorMatchesUpstream(t *testing.T) {
	var fixture f5AccumulatorFixture
	runner.LoadJSON(t, "F5", "accumulator.json", &fixture)
	if len(fixture.Cases) != 11 {
		t.Fatalf("F5 accumulator contains %d cases, want 11", len(fixture.Cases))
	}

	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			tempFilePrefix := fixtureCase.Options.TempFilePrefix
			accumulator := agenttools.NewOutputAccumulator(agenttools.OutputAccumulatorOptions{
				MaxLines:       fixtureCase.Options.MaxLines,
				MaxBytes:       fixtureCase.Options.MaxBytes,
				TempFilePrefix: &tempFilePrefix,
			})
			var fullOutputPath string
			t.Cleanup(func() {
				_ = accumulator.CloseTempFile()
				if fullOutputPath != "" {
					_ = os.Remove(fullOutputPath)
				}
			})

			if len(fixtureCase.Chunks) != len(fixtureCase.Expected.ChunkSnapshots) {
				t.Fatalf("%d chunks but %d expected snapshots", len(fixtureCase.Chunks), len(fixtureCase.Expected.ChunkSnapshots))
			}
			for index, chunk := range fixtureCase.Chunks {
				if err := accumulator.Append(materializeF5AccumulatorChunk(t, chunk)); err != nil {
					t.Fatalf("append chunk %d: %v", index, err)
				}
				snapshot, err := accumulator.Snapshot()
				if err != nil {
					t.Fatalf("snapshot after chunk %d: %v", index, err)
				}
				if snapshot.FullOutputPath != "" {
					fullOutputPath = snapshot.FullOutputPath
				}
				assertF5AccumulatorSnapshot(t, snapshot, fixtureCase.Expected.ChunkSnapshots[index])
			}

			if err := accumulator.Finish(); err != nil {
				t.Fatalf("finish: %v", err)
			}
			finalSnapshot, err := accumulator.Snapshot(agenttools.OutputSnapshotOptions{PersistIfTruncated: true})
			if err != nil {
				t.Fatalf("final snapshot: %v", err)
			}
			if finalSnapshot.FullOutputPath != "" {
				fullOutputPath = finalSnapshot.FullOutputPath
			}
			assertF5AccumulatorSnapshot(t, finalSnapshot, fixtureCase.Expected.FinalSnapshot)

			if err := accumulator.Finish(); err != nil {
				t.Fatalf("idempotent finish: %v", err)
			}
			idempotentSnapshot, err := accumulator.Snapshot(agenttools.OutputSnapshotOptions{PersistIfTruncated: true})
			if err != nil {
				t.Fatalf("snapshot after idempotent finish: %v", err)
			}
			if idempotentSnapshot.FullOutputPath != "" {
				fullOutputPath = idempotentSnapshot.FullOutputPath
			}
			assertF5AccumulatorSnapshot(t, idempotentSnapshot, fixtureCase.Expected.IdempotentFinishSnapshot)

			appendErr := accumulator.Append(nil)
			if appendErr == nil || appendErr.Error() != fixtureCase.Expected.AppendAfterFinishError {
				t.Fatalf("append-after-finish error = %v, want %q", appendErr, fixtureCase.Expected.AppendAfterFinishError)
			}
			if got := accumulator.LastLineBytes(); got != fixtureCase.Expected.LastLineBytes {
				t.Fatalf("LastLineBytes() = %d, want %d", got, fixtureCase.Expected.LastLineBytes)
			}

			if err := accumulator.CloseTempFile(); err != nil {
				t.Fatalf("close temp file: %v", err)
			}
			if fixtureCase.Expected.PersistedOutputBase64 == nil {
				if fullOutputPath != "" {
					t.Fatalf("unexpected persisted output path %q", fullOutputPath)
				}
				return
			}
			if fullOutputPath == "" {
				t.Fatal("missing persisted output path")
			}
			persistedOutput, err := os.ReadFile(fullOutputPath)
			if err != nil {
				t.Fatalf("read persisted output: %v", err)
			}
			if got := base64.StdEncoding.EncodeToString(persistedOutput); got != *fixtureCase.Expected.PersistedOutputBase64 {
				t.Fatalf("persisted output base64 = %q, want %q", got, *fixtureCase.Expected.PersistedOutputBase64)
			}
		})
	}
}

func assertF5AccumulatorSnapshot(t testing.TB, got agenttools.OutputSnapshot, want f5AccumulatorSnapshot) {
	t.Helper()
	canonical := f5AccumulatorSnapshot{
		Content:           got.Content,
		Truncation:        got.Truncation,
		HasFullOutputPath: got.FullOutputPath != "",
	}
	if !reflect.DeepEqual(canonical, want) {
		t.Fatalf("snapshot mismatch\nwant: %+v\n got: %+v", want, canonical)
	}
}

func materializeF5AccumulatorChunk(t testing.TB, chunk f5AccumulatorChunk) []byte {
	t.Helper()
	switch chunk.Kind {
	case "utf8":
		return []byte(chunk.Value)
	case "bytes":
		return chunk.Values
	case "repeatLines":
		value := strings.TrimSuffix(strings.Repeat(chunk.Value+"\n", chunk.Count), "\n")
		if chunk.TrailingNewline {
			value += "\n"
		}
		return []byte(value)
	default:
		t.Fatalf("unknown F5 accumulator chunk kind %q", chunk.Kind)
		return nil
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
