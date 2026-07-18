package tui

import (
	"fmt"
	"math"
)

type ImageTheme struct {
	FallbackColor StyleFunc
}

type ImageOptions struct {
	MaxWidthCells  *int
	MaxHeightCells *int
	Filename       string
	ImageID        *uint32
}

type Image struct {
	base64Data string
	mimeType   string
	dimensions ImageDimensions
	theme      ImageTheme
	options    ImageOptions
	imageID    *uint32
	cached     []string
	cacheWidth int
}

func NewImage(base64Data, mimeType string, theme ImageTheme, options *ImageOptions, dimensions *ImageDimensions) *Image {
	resolvedOptions := ImageOptions{}
	if options != nil {
		resolvedOptions = *options
	}
	resolvedDimensions := dimensions
	if resolvedDimensions == nil {
		resolvedDimensions = GetImageDimensions(base64Data, mimeType)
	}
	if resolvedDimensions == nil {
		resolvedDimensions = &ImageDimensions{WidthPx: 800, HeightPx: 600}
	}
	return &Image{
		base64Data: base64Data,
		mimeType:   mimeType,
		dimensions: *resolvedDimensions,
		theme:      theme,
		options:    resolvedOptions,
		imageID:    resolvedOptions.ImageID,
		cacheWidth: -1,
	}
}

func (image *Image) GetImageID() *uint32 { return image.imageID }

func (image *Image) Invalidate() {
	image.cached = nil
	image.cacheWidth = -1
}

func (image *Image) Render(width int) []string {
	if image.cached != nil && image.cacheWidth == width {
		return append([]string(nil), image.cached...)
	}
	configuredWidth := 60
	if image.options.MaxWidthCells != nil {
		configuredWidth = *image.options.MaxWidthCells
	}
	maxWidth := max(1, min(width-2, configuredWidth))
	cell := GetCellDimensions()
	defaultMaxHeight := max(1, int(math.Ceil(float64(maxWidth*max(1, cell.WidthPx))/float64(max(1, cell.HeightPx)))))
	maxHeight := image.options.MaxHeightCells
	if maxHeight == nil {
		maxHeight = &defaultMaxHeight
	}
	capabilities := GetCapabilities()
	var lines []string
	if capabilities.Images != "" {
		if capabilities.Images == ImageProtocolKitty && image.imageID == nil {
			allocated := AllocateImageID()
			image.imageID = &allocated
		}
		moveCursor := false
		result := RenderImage(image.base64Data, image.dimensions, ImageRenderOptions{
			MaxWidthCells: &maxWidth, MaxHeightCells: maxHeight, ImageID: image.imageID, MoveCursor: &moveCursor,
		})
		if result != nil {
			if result.ImageID != nil {
				image.imageID = result.ImageID
			}
			if capabilities.Images == ImageProtocolKitty {
				lines = []string{result.Sequence}
				for range result.Rows - 1 {
					lines = append(lines, "")
				}
			} else {
				lines = make([]string, max(0, result.Rows-1))
				moveUp := ""
				if result.Rows > 1 {
					moveUp = fmt.Sprintf("\x1b[%dA", result.Rows-1)
				}
				lines = append(lines, moveUp+result.Sequence)
			}
		}
	}
	if lines == nil {
		fallback := ImageFallback(image.mimeType, &image.dimensions, image.options.Filename)
		if image.theme.FallbackColor != nil {
			fallback = image.theme.FallbackColor(fallback)
		}
		lines = []string{fallback}
	}
	image.cached, image.cacheWidth = append([]string(nil), lines...), width
	return lines
}
