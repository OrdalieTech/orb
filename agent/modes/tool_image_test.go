package modes

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/tui"
)

// Expanding a read of an image shows the image the model was given, kept to
// a glance however tall it is; collapsed, the row stays one line.
func TestExpandedImageReadShowsTheImage(t *testing.T) {
	initTestTheme(t)
	tui.SetCapabilities(tui.TerminalCapabilities{Images: tui.ImageProtocolKitty, TrueColor: true})
	t.Cleanup(tui.ResetCapabilitiesCache)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 400, 2000))); err != nil {
		t.Fatal(err)
	}
	read := NewToolExecutionComponent("read", "call-1", map[string]any{"path": "/tmp/screenshot.png"}, true, nil, nil, "/tmp")
	read.UpdateResult(ai.ToolResultContent{
		&ai.TextContent{Text: "Read image file [image/png]"},
		&ai.ImageContent{Data: base64.StdEncoding.EncodeToString(encoded.Bytes()), MimeType: "image/png"},
	}, false, nil, false)

	if collapsed := strings.Join(read.Render(100), "\n"); strings.Contains(collapsed, "\x1b_G") {
		t.Fatalf("collapsed read drew the image: %q", collapsed)
	}
	read.SetExpanded(true)
	lines := read.Render(100)
	drawn := slices.IndexFunc(lines, func(line string) bool { return strings.Contains(line, "\x1b_G") })
	if drawn < 0 {
		t.Fatalf("expanded read drew no image: %q", strings.Join(lines, "\n"))
	}
	if rows := len(lines) - drawn; rows > inlineImageRows {
		t.Fatalf("the image takes %d rows", rows)
	}
}
