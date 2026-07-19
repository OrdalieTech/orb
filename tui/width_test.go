package tui

import (
	"strings"
	"testing"
)

func TestVisibleWidthAndTerminalNormalization(t *testing.T) {
	cases := map[string]int{"ascii": 5, "界": 2, "🙂": 2, "🇦": 2, "🇨🇳": 2, "\t\x1b[31m界\x1b[0m": 5, "ำ": 1, "ຳ": 1, "กำ": 2, "ກຳ": 2, "\u2028\u2029": 2}
	for value, expected := range cases {
		if actual := VisibleWidth(value); actual != expected {
			t.Errorf("VisibleWidth(%q) = %d, want %d", value, actual, expected)
		}
	}
	if got := NormalizeTerminalOutput("ำ\tຳ"); got != "ํา   ໍາ" {
		t.Fatalf("NormalizeTerminalOutput = %q", got)
	}
	if VisibleWidth(NormalizeTerminalOutput("ำabc")) != VisibleWidth("ำabc") {
		t.Fatal("Thai normalization changed display width")
	}
}

func TestTruncateToWidth(t *testing.T) {
	tests := []struct {
		name, text     string
		width          int
		ellipsis, want string
		pad            bool
	}{
		{name: "ascii", text: "abcdefgh", width: 6, ellipsis: "...", want: "abc\x1b[0m...\x1b[0m"},
		{name: "wide ellipsis clipped", text: "abcdef", width: 2, ellipsis: "🙂", want: "\x1b[0m🙂\x1b[0m"},
		{name: "wide ellipsis too narrow", text: "abcdef", width: 1, ellipsis: "🙂", want: ""},
		{name: "styled", text: "\x1b[31mhello hello hello", width: 8, ellipsis: "…", want: "\x1b[31mhello h\x1b[0m…\x1b[0m"},
		{name: "contiguous", text: "🙂\t界 \x1b_abc\x07", width: 7, ellipsis: "…", pad: true, want: "🙂\t\x1b[0m…\x1b[0m "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := TruncateToWidth(test.text, test.width, test.ellipsis, test.pad)
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
			if VisibleWidth(got) > test.width {
				t.Fatalf("width %d > %d", VisibleWidth(got), test.width)
			}
		})
	}
}

func TestSliceByColumnMatchesUpstreamWideAndANSIColumns(t *testing.T) {
	if got := SliceByColumn("abcd让EFGH", 0, 5, true); got != "abcd" {
		t.Fatalf("strict slice through wide boundary = %q, want %q", got, "abcd")
	}
	styled := "\x1b[31mabcd让EFGH\x1b[0m"
	if got := SliceByColumn(styled, 4, 4, true); !strings.Contains(got, "让EF") || VisibleWidth(got) != 4 {
		t.Fatalf("styled wide slice = %q, width %d", got, VisibleWidth(got))
	}
	if got := SliceByColumn("🙂x", 0, 1, false); got != "🙂" {
		t.Fatalf("non-strict wide slice = %q", got)
	}
	if got := SliceByColumn("🙂x", 0, 1, true); got != "" {
		t.Fatalf("strict wide slice = %q", got)
	}
}

func TestWrapTextWithANSIPreservesStyle(t *testing.T) {
	got := WrapTextWithANSI("\x1b[31mone two three\x1b[0m", 7)
	want := []string{"\x1b[31mone two", "\x1b[31mthree\x1b[0m"}
	if !equalLines(got, want) {
		t.Fatalf("wrapped = %#v, want %#v", got, want)
	}
	for _, line := range got {
		if VisibleWidth(line) > 7 {
			t.Fatalf("line too wide: %q", line)
		}
	}
}

func TestWrapTextWithANSIUpstreamRegressions(t *testing.T) {
	if got := WrapTextWithANSI("first\nsecond\r\nthird\rfourth", 80); !equalLines(got, []string{"first", "second", "third", "fourth"}) {
		t.Fatalf("line endings = %#v", got)
	}
	if got := WrapTextWithANSI("\x1b[31mfirst\r\nsecond\rthird\x1b[0m", 80); !equalLines(got, []string{"\x1b[31mfirst", "\x1b[31msecond", "\x1b[31mthird\x1b[0m"}) {
		t.Fatalf("style over line endings = %#v", got)
	}
	cjk := "This is an example 中文汉字测试段落内容中文汉字测试段落内容."
	if got := WrapTextWithANSI(cjk, 40); !equalLines(got, []string{"This is an example 中文汉字测试段落内容", "中文汉字测试段落内容."}) {
		t.Fatalf("CJK wrapping = %#v", got)
	}
	background := WrapTextWithANSI("\x1b[44mhello world this is blue background text\x1b[0m", 15)
	for index, line := range background {
		if !strings.Contains(line, "\x1b[44m") {
			t.Fatalf("background line %d = %q", index, line)
		}
		if index < len(background)-1 && strings.HasSuffix(line, "\x1b[0m") {
			t.Fatalf("background reset at wrapped line %d: %q", index, line)
		}
	}

	const url = "https://example.com"
	open, close := "\x1b]8;;"+url+"\x1b\\", "\x1b]8;;\x1b\\"
	hyperlink := WrapTextWithANSI(open+"0123456789"+close, 6)
	if len(hyperlink) != 2 {
		t.Fatalf("hyperlink lines = %#v", hyperlink)
	}
	for index, line := range hyperlink {
		if !strings.Contains(line, open) {
			t.Fatalf("hyperlink line %d does not reopen: %q", index, line)
		}
		if index < len(hyperlink)-1 && !strings.HasSuffix(line, close) {
			t.Fatalf("hyperlink line %d does not close: %q", index, line)
		}
	}
}
