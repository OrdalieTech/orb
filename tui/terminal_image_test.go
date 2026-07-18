package tui

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

func TestDetectCapabilitiesMatchesUpstreamProfiles(t *testing.T) {
	variables := []string{"TMUX", "TERM", "TERM_PROGRAM", "TERMINAL_EMULATOR", "COLORTERM", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "WEZTERM_PANE", "WARP_SESSION_ID", "WARP_TERMINAL_SESSION_UUID", "ITERM_SESSION_ID", "WT_SESSION"}
	tests := []struct {
		name string
		env  map[string]string
		want TerminalCapabilities
	}{
		{"kitty", map[string]string{"TERM_PROGRAM": "kitty"}, TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}},
		{"ghostty-term", map[string]string{"TERM": "xterm-ghostty"}, TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}},
		{"iterm", map[string]string{"ITERM_SESSION_ID": "session"}, TerminalCapabilities{Images: ImageProtocolITerm2, TrueColor: true, Hyperlinks: true}},
		{"vscode", map[string]string{"TERM_PROGRAM": "vscode"}, TerminalCapabilities{TrueColor: true, Hyperlinks: true}},
		{"screen", map[string]string{"TERM": "screen-256color", "COLORTERM": "truecolor"}, TerminalCapabilities{TrueColor: true}},
		{"unknown", map[string]string{"COLORTERM": "24bit"}, TerminalCapabilities{TrueColor: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, name := range variables {
				t.Setenv(name, "")
			}
			for name, value := range test.env {
				t.Setenv(name, value)
			}
			if got := DetectCapabilities(func() bool { return false }); got != test.want {
				t.Fatalf("capabilities = %#v, want %#v", got, test.want)
			}
		})
	}
	t.Run("tmux-hyperlinks", func(t *testing.T) {
		for _, name := range variables {
			t.Setenv(name, "")
		}
		t.Setenv("TMUX", "1")
		got := DetectCapabilities(func() bool { return true })
		if got.Images != "" || !got.Hyperlinks {
			t.Fatalf("tmux capabilities = %#v", got)
		}
	})
}

func TestTerminalImageEncodingsMatchUpstream(t *testing.T) {
	if got, want := EncodeKitty("QUJD", 10, 4, 42, false), "\x1b_Ga=T,f=100,q=2,C=1,c=10,r=4,i=42;QUJD\x1b\\"; got != want {
		t.Fatalf("kitty = %q, want %q", got, want)
	}
	chunked := EncodeKitty(strings.Repeat("a", 8200), 0, 0, 0, true)
	if strings.Count(chunked, "\x1b_G") != 3 || !strings.Contains(chunked, ",m=1;") || !strings.HasSuffix(chunked, "\x1b_Gm=0;aaaaaaaa\x1b\\") {
		t.Fatalf("chunked kitty framing = %q", chunked)
	}
	if got, want := DeleteKittyImage(7), "\x1b_Ga=d,d=I,i=7,q=2\x1b\\"; got != want {
		t.Fatalf("delete = %q", got)
	}
	if got, want := EncodeITerm2("QUJD", 12, "auto", "cat.png", false, true), "\x1b]1337;File=inline=1;width=12;height=auto;name=Y2F0LnBuZw==;preserveAspectRatio=0:QUJD\x07"; got != want {
		t.Fatalf("iterm = %q, want %q", got, want)
	}
}

func TestImageDimensionsAndCellSize(t *testing.T) {
	png := make([]byte, 24)
	copy(png, []byte("\x89PNG\r\n\x1a\n"))
	binary.BigEndian.PutUint32(png[16:20], 640)
	binary.BigEndian.PutUint32(png[20:24], 480)
	encoded := base64.StdEncoding.EncodeToString(png)
	if got := GetImageDimensions(encoded, "image/png"); got == nil || *got != (ImageDimensions{WidthPx: 640, HeightPx: 480}) {
		t.Fatalf("png dimensions = %#v", got)
	}
	maxHeight := 20
	if got, want := CalculateImageCellSize(ImageDimensions{WidthPx: 1000, HeightPx: 500}, 40, &maxHeight, CellDimensions{WidthPx: 10, HeightPx: 20}), (ImageCellSize{Columns: 40, Rows: 10}); got != want {
		t.Fatalf("cell size = %#v, want %#v", got, want)
	}
	if !IsImageLine("prefix\x1b_Ga=T;abc\x1b\\") || !IsImageLine("\x1b[2A\x1b]1337;File=inline=1:x\x07") || IsImageLine("plain") {
		t.Fatal("image-line detection disagrees with protocol prefixes")
	}
}

func TestImageComponentKittyITermAndFallback(t *testing.T) {
	dimensions := &ImageDimensions{WidthPx: 400, HeightPx: 200}
	maxWidth := 20
	maxHeight := 10
	imageID := uint32(99)
	options := &ImageOptions{MaxWidthCells: &maxWidth, MaxHeightCells: &maxHeight, Filename: "sample.png", ImageID: &imageID}
	t.Cleanup(func() {
		ResetCapabilitiesCache()
		SetCellDimensions(CellDimensions{WidthPx: 9, HeightPx: 18})
	})
	SetCellDimensions(CellDimensions{WidthPx: 10, HeightPx: 20})
	SetCapabilities(TerminalCapabilities{Images: ImageProtocolKitty})
	image := NewImage("QUJD", "image/png", ImageTheme{}, options, dimensions)
	lines := image.Render(80)
	if len(lines) != 5 || !strings.Contains(lines[0], "C=1,c=20,r=5,i=99") {
		t.Fatalf("kitty lines = %#v", lines)
	}
	SetCapabilities(TerminalCapabilities{Images: ImageProtocolITerm2})
	image.Invalidate()
	lines = image.Render(80)
	if len(lines) != 5 || !strings.HasPrefix(lines[4], "\x1b[4A\x1b]1337;File=") {
		t.Fatalf("iterm lines = %#v", lines)
	}
	SetCapabilities(TerminalCapabilities{})
	image.Invalidate()
	if got, want := image.Render(80), []string{"[Image: sample.png [image/png] 400x200]"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("fallback = %#v", got)
	}
}
