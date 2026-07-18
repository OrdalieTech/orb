package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

const (
	defaultImageMaxWidth  = 2000
	defaultImageMaxHeight = 2000
	defaultImageMaxBytes  = int(4.5 * 1024 * 1024)
	defaultJPEGQuality    = 80
)

type ImageResizeOptions struct {
	MaxWidth    int
	MaxHeight   int
	MaxBytes    int
	JPEGQuality int
}

type ResizedImage struct {
	Data           string
	MimeType       string
	OriginalWidth  int
	OriginalHeight int
	Width          int
	Height         int
	WasResized     bool
}

type ProcessImageOptions struct {
	AutoResizeImages *bool
	ResizeOptions    *ImageResizeOptions
}

type ProcessImageResult struct {
	OK       bool
	Data     string
	MimeType string
	Hints    []string
	Message  string
}

func DetectSupportedImageMimeType(buffer []byte) string {
	if startsWithBytes(buffer, []byte{0xff, 0xd8, 0xff}) {
		if len(buffer) > 3 && buffer[3] == 0xf7 {
			return ""
		}
		return "image/jpeg"
	}
	if startsWithBytes(buffer, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}) {
		if isPNG(buffer) && !isAnimatedPNG(buffer) {
			return "image/png"
		}
		return ""
	}
	if startsWithASCII(buffer, 0, "GIF") {
		return "image/gif"
	}
	if startsWithASCII(buffer, 0, "RIFF") && startsWithASCII(buffer, 8, "WEBP") {
		return "image/webp"
	}
	if startsWithASCII(buffer, 0, "BM") && isBMP(buffer) {
		return "image/bmp"
	}
	return ""
}

func startsWithBytes(buffer, prefix []byte) bool {
	return len(buffer) >= len(prefix) && bytes.Equal(buffer[:len(prefix)], prefix)
}
func startsWithASCII(buffer []byte, offset int, value string) bool {
	return offset >= 0 && len(buffer) >= offset+len(value) && string(buffer[offset:offset+len(value)]) == value
}

func isPNG(buffer []byte) bool {
	return len(buffer) >= 16 && binary.BigEndian.Uint32(buffer[8:12]) == 13 && startsWithASCII(buffer, 12, "IHDR")
}

func isAnimatedPNG(buffer []byte) bool {
	for offset := 8; offset+8 <= len(buffer); {
		length := int(binary.BigEndian.Uint32(buffer[offset : offset+4]))
		switch {
		case startsWithASCII(buffer, offset+4, "acTL"):
			return true
		case startsWithASCII(buffer, offset+4, "IDAT"):
			return false
		}
		next := offset + 8 + length + 4
		if next <= offset || next > len(buffer) {
			return false
		}
		offset = next
	}
	return false
}

func isBMP(buffer []byte) bool {
	if len(buffer) < 26 {
		return false
	}
	fileSize := binary.LittleEndian.Uint32(buffer[2:6])
	pixelOffset := binary.LittleEndian.Uint32(buffer[10:14])
	dibSize := binary.LittleEndian.Uint32(buffer[14:18])
	if fileSize != 0 && fileSize < 26 || uint64(pixelOffset) < 14+uint64(dibSize) || fileSize != 0 && pixelOffset >= fileSize {
		return false
	}
	var planes, bits uint16
	switch {
	case dibSize == 12:
		planes, bits = binary.LittleEndian.Uint16(buffer[22:24]), binary.LittleEndian.Uint16(buffer[24:26])
	case dibSize >= 40 && dibSize <= 124 && len(buffer) >= 30:
		planes, bits = binary.LittleEndian.Uint16(buffer[26:28]), binary.LittleEndian.Uint16(buffer[28:30])
	default:
		return false
	}
	return planes == 1 && (bits == 1 || bits == 4 || bits == 8 || bits == 16 || bits == 24 || bits == 32)
}

func ProcessImage(input []byte, mimeType string, options *ProcessImageOptions) ProcessImageResult {
	autoResize := true
	if options != nil && options.AutoResizeImages != nil {
		autoResize = *options.AutoResizeImages
	}
	normalizedBytes, normalizedMime, convertedFrom, ok := normalizeImage(input, mimeType)
	if !ok {
		return ProcessImageResult{Message: "[Image omitted: could not be converted to a supported inline image format.]"}
	}
	if !autoResize {
		hints := conversionHints(convertedFrom, normalizedMime)
		return ProcessImageResult{OK: true, Data: base64.StdEncoding.EncodeToString(normalizedBytes), MimeType: normalizedMime, Hints: hints}
	}
	var resizeOptions *ImageResizeOptions
	if options != nil {
		resizeOptions = options.ResizeOptions
	}
	resized := ResizeImage(normalizedBytes, normalizedMime, resizeOptions)
	if resized == nil {
		return ProcessImageResult{Message: "[Image omitted: could not be resized below the inline image size limit.]"}
	}
	hints := conversionHints(convertedFrom, resized.MimeType)
	if note := FormatDimensionNote(*resized); note != "" {
		hints = append(hints, note)
	}
	return ProcessImageResult{OK: true, Data: resized.Data, MimeType: resized.MimeType, Hints: hints}
}

func normalizeImage(input []byte, mimeType string) ([]byte, string, string, bool) {
	baseMime := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	switch baseMime {
	case "image/png", "image/gif", "image/webp":
		return append([]byte(nil), input...), baseMime, "", true
	case "image/jpeg", "image/jpg":
		return append([]byte(nil), input...), "image/jpeg", "", true
	}
	decoded, _, err := image.Decode(bytes.NewReader(input))
	if err != nil {
		return nil, "", "", false
	}
	oriented := applyEXIFOrientation(decoded, getEXIFOrientation(input))
	var output bytes.Buffer
	if err := png.Encode(&output, oriented); err != nil {
		return nil, "", "", false
	}
	return output.Bytes(), "image/png", baseMime, true
}

func conversionHints(from, to string) []string {
	if from == "" || from == to {
		return nil
	}
	return []string{fmt.Sprintf("[Image converted from %s to %s.]", from, to)}
}

func normalizedResizeOptions(options *ImageResizeOptions) ImageResizeOptions {
	result := ImageResizeOptions{MaxWidth: defaultImageMaxWidth, MaxHeight: defaultImageMaxHeight, MaxBytes: defaultImageMaxBytes, JPEGQuality: defaultJPEGQuality}
	if options == nil {
		return result
	}
	if options.MaxWidth != 0 {
		result.MaxWidth = options.MaxWidth
	}
	if options.MaxHeight != 0 {
		result.MaxHeight = options.MaxHeight
	}
	if options.MaxBytes != 0 {
		result.MaxBytes = options.MaxBytes
	}
	if options.JPEGQuality != 0 {
		result.JPEGQuality = options.JPEGQuality
	}
	return result
}

func ResizeImage(input []byte, mimeType string, options *ImageResizeOptions) *ResizedImage {
	opts := normalizedResizeOptions(options)
	if opts.MaxWidth < 1 || opts.MaxHeight < 1 || opts.MaxBytes < 1 {
		return nil
	}
	decoded, _, err := image.Decode(bytes.NewReader(input))
	if err != nil {
		return nil
	}
	oriented := applyEXIFOrientation(decoded, getEXIFOrientation(input))
	bounds := oriented.Bounds()
	originalWidth, originalHeight := bounds.Dx(), bounds.Dy()
	inputBase64Size := ((len(input) + 2) / 3) * 4
	if originalWidth <= opts.MaxWidth && originalHeight <= opts.MaxHeight && inputBase64Size < opts.MaxBytes {
		return &ResizedImage{
			Data: base64.StdEncoding.EncodeToString(input), MimeType: mimeType,
			OriginalWidth: originalWidth, OriginalHeight: originalHeight,
			Width: originalWidth, Height: originalHeight,
		}
	}
	targetWidth, targetHeight := originalWidth, originalHeight
	if targetWidth > opts.MaxWidth {
		targetHeight = int(math.Round(float64(targetHeight*opts.MaxWidth) / float64(targetWidth)))
		targetWidth = opts.MaxWidth
	}
	if targetHeight > opts.MaxHeight {
		targetWidth = int(math.Round(float64(targetWidth*opts.MaxHeight) / float64(targetHeight)))
		targetHeight = opts.MaxHeight
	}
	if targetWidth < 1 || targetHeight < 1 {
		return nil
	}
	qualities := uniqueInts([]int{opts.JPEGQuality, 85, 70, 55, 40})
	for {
		resized := resizeLanczos3(oriented, targetWidth, targetHeight)
		for _, candidate := range encodeImageCandidates(resized, qualities) {
			encoded := base64.StdEncoding.EncodeToString(candidate.data)
			if len(encoded) < opts.MaxBytes {
				return &ResizedImage{
					Data: encoded, MimeType: candidate.mimeType,
					OriginalWidth: originalWidth, OriginalHeight: originalHeight,
					Width: targetWidth, Height: targetHeight, WasResized: true,
				}
			}
		}
		if targetWidth == 1 && targetHeight == 1 {
			return nil
		}
		nextWidth, nextHeight := targetWidth, targetHeight
		if nextWidth != 1 {
			nextWidth = max(1, int(math.Floor(float64(nextWidth)*0.75)))
		}
		if nextHeight != 1 {
			nextHeight = max(1, int(math.Floor(float64(nextHeight)*0.75)))
		}
		if nextWidth == targetWidth && nextHeight == targetHeight {
			return nil
		}
		targetWidth, targetHeight = nextWidth, nextHeight
	}
}

type lanczosWeight struct {
	index  int
	weight float32
}

func resizeLanczos3(source image.Image, width, height int) *image.NRGBA {
	bounds := source.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	if sourceWidth < 1 || sourceHeight < 1 || width < 1 || height < 1 {
		return nil
	}
	sourcePixels := make([]uint8, sourceWidth*sourceHeight*4)
	for y := range sourceHeight {
		for x := range sourceWidth {
			pixel := color.NRGBAModel.Convert(source.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			offset := (y*sourceWidth + x) * 4
			sourcePixels[offset], sourcePixels[offset+1], sourcePixels[offset+2], sourcePixels[offset+3] = pixel.R, pixel.G, pixel.B, pixel.A
		}
	}
	if sourceWidth == width && sourceHeight == height {
		result := image.NewNRGBA(image.Rect(0, 0, width, height))
		copy(result.Pix, sourcePixels)
		return result
	}

	verticalWeights := precomputeLanczos3Weights(sourceHeight, height)
	vertical := make([]float32, sourceWidth*height*4)
	for y, weights := range verticalWeights {
		for x := range sourceWidth {
			outputOffset := (y*sourceWidth + x) * 4
			for _, entry := range weights {
				inputOffset := (entry.index*sourceWidth + x) * 4
				for channel := range 4 {
					vertical[outputOffset+channel] += float32(sourcePixels[inputOffset+channel]) * entry.weight
				}
			}
		}
	}

	horizontalWeights := precomputeLanczos3Weights(sourceWidth, width)
	result := image.NewNRGBA(image.Rect(0, 0, width, height))
	for x, weights := range horizontalWeights {
		for y := range height {
			outputOffset := y*result.Stride + x*4
			for channel := range 4 {
				var value float32
				for _, entry := range weights {
					value += vertical[(y*sourceWidth+entry.index)*4+channel] * entry.weight
				}
				result.Pix[outputOffset+channel] = roundClampUint8(value)
			}
		}
	}
	return result
}

func precomputeLanczos3Weights(sourceSize, targetSize int) [][]lanczosWeight {
	ratio := float32(sourceSize) / float32(targetSize)
	scale := max(float32(1), ratio)
	support := float32(3) * scale
	result := make([][]lanczosWeight, targetSize)
	for target := range targetSize {
		center := (float32(target) + 0.5) * ratio
		left := max(0, min(sourceSize-1, int(math.Floor(float64(center-support)))))
		right := max(left+1, min(sourceSize, int(math.Ceil(float64(center+support)))))
		center -= 0.5
		weights := make([]lanczosWeight, 0, right-left)
		var sum float32
		for index := left; index < right; index++ {
			weight := lanczos3((float32(index) - center) / scale)
			weights = append(weights, lanczosWeight{index: index, weight: weight})
			sum += weight
		}
		for index := range weights {
			weights[index].weight /= sum
		}
		result[target] = weights
	}
	return result
}

func lanczos3(value float32) float32 {
	if value < 0 {
		value = -value
	}
	if value >= 3 {
		return 0
	}
	return sinc32(value) * sinc32(value/3)
}

func sinc32(value float32) float32 {
	if value == 0 {
		return 1
	}
	angle := value * float32(math.Pi)
	return float32(math.Sin(float64(angle))) / angle
}

func roundClampUint8(value float32) uint8 {
	value = min(float32(255), max(float32(0), value))
	return uint8(math.Round(float64(value)))
}

type encodedImage struct {
	data     []byte
	mimeType string
}

func encodeImageCandidates(value image.Image, qualities []int) []encodedImage {
	result := make([]encodedImage, 0, len(qualities)+1)
	var pngOutput bytes.Buffer
	if png.Encode(&pngOutput, value) == nil {
		result = append(result, encodedImage{data: pngOutput.Bytes(), mimeType: "image/png"})
	}
	for _, quality := range qualities {
		var jpegOutput bytes.Buffer
		if jpeg.Encode(&jpegOutput, value, &jpeg.Options{Quality: quality}) == nil {
			result = append(result, encodedImage{data: jpegOutput.Bytes(), mimeType: "image/jpeg"})
		}
	}
	return result
}

func uniqueInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func FormatDimensionNote(result ResizedImage) string {
	if !result.WasResized {
		return ""
	}
	scale := float64(result.OriginalWidth) / float64(result.Width)
	return fmt.Sprintf("[Image: original %dx%d, displayed at %dx%d. Multiply coordinates by %.2f to map to original image.]", result.OriginalWidth, result.OriginalHeight, result.Width, result.Height, scale)
}

func getEXIFOrientation(input []byte) int {
	tiff := -1
	if len(input) >= 2 && input[0] == 0xff && input[1] == 0xd8 {
		tiff = findJPEGExif(input)
	} else if len(input) >= 12 && string(input[:4]) == "RIFF" && string(input[8:12]) == "WEBP" {
		tiff = findWebPExif(input)
	}
	if tiff < 0 {
		return 1
	}
	return readTIFFOrientation(input, tiff)
}

func findJPEGExif(input []byte) int {
	for offset := 2; offset < len(input)-1; {
		if input[offset] != 0xff {
			return -1
		}
		marker := input[offset+1]
		if marker == 0xff {
			offset++
			continue
		}
		if offset+4 > len(input) {
			return -1
		}
		if marker == 0xe1 {
			start := offset + 4
			if start+6 > len(input) || !hasExifHeader(input, start) {
				return -1
			}
			return start + 6
		}
		length := int(binary.BigEndian.Uint16(input[offset+2 : offset+4]))
		if length < 2 {
			return -1
		}
		offset += 2 + length
	}
	return -1
}

func findWebPExif(input []byte) int {
	for offset := 12; offset+8 <= len(input); {
		size := int(binary.LittleEndian.Uint32(input[offset+4 : offset+8]))
		start := offset + 8
		if string(input[offset:offset+4]) == "EXIF" {
			if start+size > len(input) {
				return -1
			}
			if size >= 6 && hasExifHeader(input, start) {
				return start + 6
			}
			return start
		}
		next := start + size + size%2
		if next <= offset || next > len(input) {
			return -1
		}
		offset = next
	}
	return -1
}

func hasExifHeader(input []byte, offset int) bool {
	return offset >= 0 && offset+6 <= len(input) && string(input[offset:offset+6]) == "Exif\x00\x00"
}

func readTIFFOrientation(input []byte, start int) int {
	if start < 0 || start+8 > len(input) {
		return 1
	}
	little := input[start] == 'I' && input[start+1] == 'I'
	read16 := func(offset int) (uint16, bool) {
		if offset < 0 || offset+2 > len(input) {
			return 0, false
		}
		if little {
			return binary.LittleEndian.Uint16(input[offset : offset+2]), true
		}
		return binary.BigEndian.Uint16(input[offset : offset+2]), true
	}
	read32 := func(offset int) (uint32, bool) {
		if offset < 0 || offset+4 > len(input) {
			return 0, false
		}
		if little {
			return binary.LittleEndian.Uint32(input[offset : offset+4]), true
		}
		return binary.BigEndian.Uint32(input[offset : offset+4]), true
	}
	offset, ok := read32(start + 4)
	if !ok || uint64(start)+uint64(offset)+2 > uint64(len(input)) {
		return 1
	}
	ifd := start + int(offset)
	count, ok := read16(ifd)
	if !ok {
		return 1
	}
	for index := 0; index < int(count); index++ {
		entry := ifd + 2 + index*12
		if entry+12 > len(input) {
			return 1
		}
		tag, _ := read16(entry)
		if tag != 0x0112 {
			continue
		}
		value, _ := read16(entry + 8)
		if value >= 1 && value <= 8 {
			return int(value)
		}
		return 1
	}
	return 1
}

func applyEXIFOrientation(source image.Image, orientation int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	destinationWidth, destinationHeight := width, height
	if orientation >= 5 && orientation <= 8 {
		destinationWidth, destinationHeight = height, width
	}
	destination := image.NewNRGBA(image.Rect(0, 0, destinationWidth, destinationHeight))
	for y := 0; y < destinationHeight; y++ {
		for x := 0; x < destinationWidth; x++ {
			var sourceX, sourceY int
			switch orientation {
			case 2:
				sourceX, sourceY = width-1-x, y
			case 3:
				sourceX, sourceY = width-1-x, height-1-y
			case 4:
				sourceX, sourceY = x, height-1-y
			case 5:
				sourceX, sourceY = y, x
			case 6:
				sourceX, sourceY = y, height-1-x
			case 7:
				sourceX, sourceY = width-1-y, height-1-x
			case 8:
				sourceX, sourceY = width-1-y, x
			default:
				sourceX, sourceY = x, y
			}
			destination.Set(x, y, source.At(bounds.Min.X+sourceX, bounds.Min.Y+sourceY))
		}
	}
	return destination
}
