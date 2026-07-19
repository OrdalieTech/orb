package modes

import (
	_ "embed"
	"encoding/base64"
	"sync"

	"github.com/OrdalieTech/pi-go/tui"

	theme "github.com/OrdalieTech/pi-go/codingagent/modes/theme"
)

const (
	earendilBlogURL       = "https://mariozechner.at/posts/2026-04-08-ive-sold-out/"
	earendilImageFilename = "clankolas.png"
)

//go:embed assets/clankolas.png
var earendilImage []byte

var (
	earendilImageOnce   sync.Once
	earendilImageBase64 string
)

type EarendilAnnouncementComponent struct {
	tui.Container
}

func NewEarendilAnnouncementComponent() *EarendilAnnouncementComponent {
	component := &EarendilAnnouncementComponent{}
	component.AddChild(NewDynamicBorderWithColor(func(text string) string { return theme.FG("accent", text) }))
	component.AddChild(tui.NewText(theme.Bold(theme.FG("accent", "pi has joined Earendil")), 1, 0, nil))
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(tui.NewText(theme.FG("muted", "Read the blog post:"), 1, 0, nil))
	component.AddChild(tui.NewText(theme.FG("mdLink", earendilBlogURL), 1, 0, nil))
	component.AddChild(tui.NewSpacer(1))

	earendilImageOnce.Do(func() {
		earendilImageBase64 = base64.StdEncoding.EncodeToString(earendilImage)
	})
	if earendilImageBase64 != "" {
		maxWidth := 56
		component.AddChild(tui.NewImage(
			earendilImageBase64,
			"image/png",
			tui.ImageTheme{FallbackColor: func(text string) string { return theme.FG("muted", text) }},
			&tui.ImageOptions{MaxWidthCells: &maxWidth, Filename: earendilImageFilename},
			nil,
		))
		component.AddChild(tui.NewSpacer(1))
	}

	component.AddChild(NewDynamicBorderWithColor(func(text string) string { return theme.FG("accent", text) }))
	return component
}
