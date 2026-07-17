package truncate_test

import (
	"testing"

	"github.com/OrdalieTech/pi-go/internal/jsonwire"
	"github.com/OrdalieTech/pi-go/internal/truncate"
)

func TestTruncateHeadDistinguishesOmittedAndZeroLimits(t *testing.T) {
	if got := truncate.TruncateHead("alpha"); got.Truncated {
		t.Fatalf("default result was truncated: %+v", got)
	}
	got := truncate.TruncateHead("alpha", truncate.Options{MaxLines: truncate.Int(0)})
	if !got.Truncated || got.TruncatedBy == nil || *got.TruncatedBy != truncate.ReasonLines || got.Content != "" {
		t.Fatalf("zero-line result = %+v", got)
	}
}

func TestTruncateTailStartsAtUTF8Boundary(t *testing.T) {
	got := truncate.TruncateTail("prefix😀suffix", truncate.Options{MaxBytes: truncate.Int(7)})
	if got.Content != "suffix" || !got.LastLinePartial || got.OutputBytes != 6 {
		t.Fatalf("tail result = %+v", got)
	}
}

func TestTruncateTailPartialLineUsesNodeUTF8ForLoneSurrogate(t *testing.T) {
	content := "prefix" + string([]byte{0xed, 0xa0, 0x80})
	got := truncate.TruncateTail(content, truncate.Options{MaxLines: truncate.Int(10), MaxBytes: truncate.Int(3)})
	if got.Content != "\ufffd" || !got.LastLinePartial || got.OutputBytes != 3 {
		t.Fatalf("tail result = %+v", got)
	}
}

func TestTruncateLineUsesJavaScriptUTF16CodeUnits(t *testing.T) {
	got := truncate.TruncateLine("😀x", 2)
	if got.Text != "😀... [truncated]" || !got.WasTruncated {
		t.Fatalf("line result = %+v", got)
	}

	split := truncate.TruncateLine("😀x", 1)
	encoded, err := jsonwire.MarshalString(split.Text)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"\ud83d... [truncated]"`; string(encoded) != want {
		t.Fatalf("split surrogate = %s, want %s", encoded, want)
	}
}

func TestFormatSizeMatchesUpstreamUnits(t *testing.T) {
	for _, testCase := range []struct {
		bytes int
		want  string
	}{
		{bytes: 1023, want: "1023B"},
		{bytes: 1536, want: "1.5KB"},
		{bytes: 51456, want: "50.3KB"},
		{bytes: 1572864, want: "1.5MB"},
	} {
		if got := truncate.FormatSize(testCase.bytes); got != testCase.want {
			t.Errorf("FormatSize(%d) = %q, want %q", testCase.bytes, got, testCase.want)
		}
	}
}
