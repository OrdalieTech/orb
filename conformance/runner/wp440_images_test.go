package runner_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"strconv"
	"testing"

	"github.com/OrdalieTech/pigo/codingagent/tools"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type wp440Fixture struct {
	SchemaVersion    int                    `json:"schemaVersion"`
	FormatCases      []wp440FormatCase      `json:"formatCases"`
	ResampleCases    []wp440ResampleCase    `json:"resampleCases"`
	OrientationCases []wp440OrientationCase `json:"orientationCases"`
	PipelineCases    []wp440PipelineCase    `json:"pipelineCases"`
}

type wp440ResizeOptions struct {
	MaxWidth  int `json:"maxWidth"`
	MaxHeight int `json:"maxHeight"`
	MaxBytes  int `json:"maxBytes"`
}

type wp440ResizeExpected struct {
	OriginalWidth  int    `json:"originalWidth"`
	OriginalHeight int    `json:"originalHeight"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	WasResized     bool   `json:"wasResized"`
	MimeType       string `json:"mimeType"`
	DataStable     bool   `json:"dataStable"`
	Note           string `json:"note"`
	SampleColumns  int    `json:"sampleColumns"`
	SampleRows     int    `json:"sampleRows"`
	PixelSignature string `json:"pixelSignature"`
	RawRGBA        string `json:"rawRGBA"`
}

type wp440FormatCase struct {
	Name             string              `json:"name"`
	InputBase64      string              `json:"inputBase64"`
	InputSHA256      string              `json:"inputSHA256"`
	MimeType         string              `json:"mimeType"`
	DetectedMimeType string              `json:"detectedMimeType"`
	Options          wp440ResizeOptions  `json:"options"`
	Expected         wp440ResizeExpected `json:"expected"`
}

type wp440OrientationCase struct {
	Orientation int                 `json:"orientation"`
	InputBase64 string              `json:"inputBase64"`
	InputSHA256 string              `json:"inputSHA256"`
	Options     wp440ResizeOptions  `json:"options"`
	Expected    wp440ResizeExpected `json:"expected"`
}

type wp440ResampleCase struct {
	Name        string               `json:"name"`
	InputBase64 string               `json:"inputBase64"`
	InputSHA256 string               `json:"inputSHA256"`
	MimeType    string               `json:"mimeType"`
	Options     wp440ResizeOptions   `json:"options"`
	Expected    *wp440ResizeExpected `json:"expected"`
}

type wp440PipelineCase struct {
	Name             string `json:"name"`
	InputBase64      string `json:"inputBase64"`
	InputSHA256      string `json:"inputSHA256"`
	MimeType         string `json:"mimeType"`
	AutoResizeImages bool   `json:"autoResizeImages"`
	Expected         struct {
		MimeType string   `json:"mimeType"`
		Hints    []string `json:"hints"`
		PNGMagic string   `json:"pngMagic"`
	} `json:"expected"`
}

func TestWP440ImageProcessingMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "WP440")
	if manifest.Family != "WP440" || manifest.Generator != "conformance/extract/wp440-images.ts" {
		t.Fatalf("unexpected WP440 manifest: %+v", manifest)
	}
	var fixture wp440Fixture
	runner.LoadJSON(t, "WP440", "images.json", &fixture)
	if fixture.SchemaVersion != 3 || len(fixture.FormatCases) != 4 || len(fixture.ResampleCases) != 3 || len(fixture.OrientationCases) != 8 || len(fixture.PipelineCases) != 2 {
		t.Fatalf("WP440 fixture header = version %d, formats %d, resamples %d, orientations %d, pipelines %d", fixture.SchemaVersion, len(fixture.FormatCases), len(fixture.ResampleCases), len(fixture.OrientationCases), len(fixture.PipelineCases))
	}
	for _, fixtureCase := range fixture.FormatCases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			input := decodeWP440Input(t, fixtureCase.InputBase64, fixtureCase.InputSHA256)
			if got := tools.DetectSupportedImageMimeType(input); got != fixtureCase.DetectedMimeType {
				t.Fatalf("detected mime = %q, want %q", got, fixtureCase.DetectedMimeType)
			}
			options := tools.ImageResizeOptions{MaxWidth: fixtureCase.Options.MaxWidth, MaxHeight: fixtureCase.Options.MaxHeight, MaxBytes: fixtureCase.Options.MaxBytes}
			got := tools.ResizeImage(input, fixtureCase.MimeType, &options)
			assertWP440Resize(t, got, fixtureCase.Expected)
			again := tools.ResizeImage(input, fixtureCase.MimeType, &options)
			if got == nil || again == nil || got.Data != again.Data {
				t.Fatal("Go resize output is not byte-stable across identical invocations")
			}
			if fixtureCase.Expected.DataStable && got.Data != fixtureCase.InputBase64 {
				t.Fatal("upstream pass-through bytes were not preserved")
			}
		})
	}
	for _, fixtureCase := range fixture.ResampleCases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			input := decodeWP440Input(t, fixtureCase.InputBase64, fixtureCase.InputSHA256)
			options := tools.ImageResizeOptions{MaxWidth: fixtureCase.Options.MaxWidth, MaxHeight: fixtureCase.Options.MaxHeight, MaxBytes: fixtureCase.Options.MaxBytes}
			got := tools.ResizeImage(input, fixtureCase.MimeType, &options)
			if fixtureCase.Expected == nil {
				if got != nil {
					t.Fatalf("resize = %#v, want nil", got)
				}
				return
			}
			assertWP440Resize(t, got, *fixtureCase.Expected)
			if fixtureCase.Expected.RawRGBA != "" {
				if raw := wp440RawRGBA(t, got.Data); raw != fixtureCase.Expected.RawRGBA {
					t.Fatalf("resampled RGBA = %q, want %q", raw, fixtureCase.Expected.RawRGBA)
				}
			}
		})
	}
	for _, fixtureCase := range fixture.OrientationCases {
		t.Run("exif-"+strconv.Itoa(fixtureCase.Orientation), func(t *testing.T) {
			input := decodeWP440Input(t, fixtureCase.InputBase64, fixtureCase.InputSHA256)
			options := tools.ImageResizeOptions{MaxWidth: fixtureCase.Options.MaxWidth, MaxHeight: fixtureCase.Options.MaxHeight, MaxBytes: fixtureCase.Options.MaxBytes}
			got := tools.ResizeImage(input, "image/jpeg", &options)
			assertWP440Resize(t, got, fixtureCase.Expected)
			if note := tools.FormatDimensionNote(*got); note != fixtureCase.Expected.Note {
				t.Fatalf("dimension note = %q, want %q", note, fixtureCase.Expected.Note)
			}
			if signature := wp440PixelSignature(t, got.Data, fixtureCase.Expected.SampleColumns, fixtureCase.Expected.SampleRows); signature != fixtureCase.Expected.PixelSignature {
				t.Fatalf("oriented pixel signature = %q, want %q", signature, fixtureCase.Expected.PixelSignature)
			}
		})
	}
	for _, fixtureCase := range fixture.PipelineCases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			input := decodeWP440Input(t, fixtureCase.InputBase64, fixtureCase.InputSHA256)
			autoResize := fixtureCase.AutoResizeImages
			got := tools.ProcessImage(input, fixtureCase.MimeType, &tools.ProcessImageOptions{AutoResizeImages: &autoResize})
			if !got.OK || got.MimeType != fixtureCase.Expected.MimeType || linesDiff(fixtureCase.Expected.Hints, got.Hints) != "" {
				t.Fatalf("pipeline result = %#v, want %#v", got, fixtureCase.Expected)
			}
			decoded, err := base64.StdEncoding.DecodeString(got.Data)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(decoded[:min(4, len(decoded))]) != fixtureCase.Expected.PNGMagic {
				t.Fatalf("output magic = %x, want %s", decoded[:min(4, len(decoded))], fixtureCase.Expected.PNGMagic)
			}
		})
	}
}

func wp440RawRGBA(t *testing.T, encoded string) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	bounds := decoded.Bounds()
	raw := make([]byte, 0, bounds.Dx()*bounds.Dy()*4)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := color.NRGBAModel.Convert(decoded.At(x, y)).(color.NRGBA)
			raw = append(raw, pixel.R, pixel.G, pixel.B, pixel.A)
		}
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func wp440PixelSignature(t *testing.T, encoded string, columns, rows int) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	palette := []struct {
		label byte
		color color.RGBA
	}{
		{'R', color.RGBA{R: 255, A: 255}},
		{'G', color.RGBA{G: 255, A: 255}},
		{'B', color.RGBA{B: 255, A: 255}},
		{'Y', color.RGBA{R: 255, G: 255, A: 255}},
		{'C', color.RGBA{G: 255, B: 255, A: 255}},
		{'M', color.RGBA{R: 255, B: 255, A: 255}},
	}
	bounds := decoded.Bounds()
	result := make([]byte, 0, columns*rows)
	for row := range rows {
		for column := range columns {
			x := bounds.Min.X + min(bounds.Dx()-1, int((float64(column)+0.5)*float64(bounds.Dx())/float64(columns)))
			y := bounds.Min.Y + min(bounds.Dy()-1, int((float64(row)+0.5)*float64(bounds.Dy())/float64(rows)))
			red, green, blue, _ := decoded.At(x, y).RGBA()
			closest, closestDistance := byte(0), int64(^uint64(0)>>1)
			for _, candidate := range palette {
				candidateRed, candidateGreen, candidateBlue := int64(candidate.color.R), int64(candidate.color.G), int64(candidate.color.B)
				deltaRed, deltaGreen, deltaBlue := int64(red>>8)-candidateRed, int64(green>>8)-candidateGreen, int64(blue>>8)-candidateBlue
				distance := deltaRed*deltaRed + deltaGreen*deltaGreen + deltaBlue*deltaBlue
				if distance < closestDistance {
					closest, closestDistance = candidate.label, distance
				}
			}
			result = append(result, closest)
		}
	}
	return string(result)
}

func decodeWP440Input(t *testing.T, encoded, wantHash string) []byte {
	t.Helper()
	input, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(input)
	if got := hex.EncodeToString(hash[:]); got != wantHash {
		t.Fatalf("input SHA-256 = %s, want %s", got, wantHash)
	}
	return input
}

func assertWP440Resize(t *testing.T, got *tools.ResizedImage, want wp440ResizeExpected) {
	t.Helper()
	if got == nil {
		t.Fatal("resize returned nil")
	}
	if got.OriginalWidth != want.OriginalWidth || got.OriginalHeight != want.OriginalHeight || got.Width != want.Width || got.Height != want.Height || got.WasResized != want.WasResized || got.MimeType != want.MimeType {
		t.Fatalf("resize = %#v, want %#v", got, want)
	}
}
