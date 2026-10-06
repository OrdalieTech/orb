package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestANSITokens(t *testing.T) {
	const red = "\x1b[31m"
	const reset = "\x1b[0m"
	const link = "\x1b]8;;https://example.com\x1b\\"
	const apc = "\x1b_test\a"
	cases := []struct {
		name, text string
		want       []string
	}{
		{"empty", "", []string{}},
		{"spaces and tabs", "  ab\tcd  \t ef\u00a0gh", []string{"  ", "ab\tcd", "  ", "\t", " ", "ef\u00a0gh"}},
		{"ansi boundaries", red + "ab" + reset + "  " + red + "cd" + reset, []string{red + "ab", reset + "  ", red + "cd" + reset}},
		{"ansi only", red + reset + link + apc, []string{red + reset + link + apc}},
		{"cjk trailing ansi", "a" + red + "中日" + reset, []string{"a", red + "中", "日" + reset}},
		{"unicode", "e\u0301👩‍💻中\u0301한カ a\u2028b", []string{"e\u0301👩‍💻", "中\u0301", "한", "カ", " ", "a\u2028b"}},
		{"ansi within word", link + "e\u0301" + red + "👩‍💻" + apc + "x" + reset, []string{link + "e\u0301" + red + "👩‍💻" + apc + "x" + reset}},
		{"ansi splits grapheme", "e" + red + "\u0301中" + reset, []string{"e" + red + "\u0301", "中" + reset}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ansiTokens(tc.text)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			if strings.Join(got, "") != tc.text {
				t.Fatal("tokenization changed source bytes")
			}
		})
	}
}

var ansiTokensBenchmarkSink []string

func BenchmarkANSITokensLong(b *testing.B) {
	for _, tc := range []struct{ name, unit string }{
		{"ASCII", "x"}, {"UnicodeANSI", "\x1b[31me\u0301👩‍💻"}, {"ANSIOnly", "\x1b[31m"},
	} {
		for _, n := range []int{1024, 4096, 16384} {
			b.Run(fmt.Sprintf("%s/%d", tc.name, n), func(b *testing.B) {
				text := strings.Repeat(tc.unit, n)
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				b.ResetTimer()
				for b.Loop() {
					ansiTokensBenchmarkSink = ansiTokens(text)
				}
			})
		}
	}
}

func TestANSITokensLongAllocations(t *testing.T) {
	for _, unit := range []string{"x", "\x1b[31m"} {
		text := strings.Repeat(unit, 16384)
		allocs := testing.AllocsPerRun(5, func() {
			ansiTokensBenchmarkSink = ansiTokens(text)
		})
		if len(ansiTokensBenchmarkSink) != 1 || ansiTokensBenchmarkSink[0] != text {
			t.Fatal("long token did not preserve source")
		}
		// Geometric builder growth needs only tens of allocations, not one per
		// grapheme/escape. Keep headroom for allocator growth policy changes.
		if allocs > 64 {
			t.Fatalf("%q: %.0f allocations for one long token, want <= 64", unit, allocs)
		}
	}
}

func TestANSITokensWrapRegression(t *testing.T) {
	cases := []struct {
		text  string
		width int
		want  []string
	}{
		{"\x1b[31mabcd\x1b[0m ef", 3, []string{"\x1b[31mabc", "\x1b[31md\x1b[0m", "ef"}},
		{"e\u0301👩‍💻中", 3, []string{"e\u0301👩‍💻", "中"}},
		{"ab  cd", 3, []string{"ab", "cd"}},
		{"a\tb", 3, []string{"a", "", "b"}},
	}
	for _, tc := range cases {
		if got := WrapTextWithANSI(tc.text, tc.width); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("WrapTextWithANSI(%q, %d) = %#v, want %#v", tc.text, tc.width, got, tc.want)
		}
	}
}
