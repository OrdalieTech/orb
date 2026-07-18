package tui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ImageProtocol string

const (
	ImageProtocolKitty  ImageProtocol = "kitty"
	ImageProtocolITerm2 ImageProtocol = "iterm2"
)

type TerminalCapabilities struct {
	Images     ImageProtocol
	TrueColor  bool
	Hyperlinks bool
}

type CellDimensions struct {
	WidthPx  int
	HeightPx int
}

type ImageDimensions struct {
	WidthPx  int
	HeightPx int
}

type ImageCellSize struct {
	Columns int
	Rows    int
}

type ImageRenderOptions struct {
	MaxWidthCells       *int
	MaxHeightCells      *int
	PreserveAspectRatio *bool
	ImageID             *uint32
	MoveCursor          *bool
}

type ImageRenderResult struct {
	Sequence string
	Rows     int
	ImageID  *uint32
}

var terminalImageState = struct {
	sync.RWMutex
	capabilities *TerminalCapabilities
	cell         CellDimensions
}{cell: CellDimensions{WidthPx: 9, HeightPx: 18}}

func probeTmuxHyperlinks() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{client_termfeatures}").Output()
	if err != nil {
		return false
	}
	for _, feature := range strings.Split(string(output), ",") {
		if strings.TrimSpace(feature) == "hyperlinks" {
			return true
		}
	}
	return false
}

func DetectCapabilities(tmuxForwardsHyperlink func() bool) TerminalCapabilities {
	termProgram := strings.ToLower(os.Getenv("TERM_PROGRAM"))
	terminalEmulator := strings.ToLower(os.Getenv("TERMINAL_EMULATOR"))
	term := strings.ToLower(os.Getenv("TERM"))
	colorTerm := strings.ToLower(os.Getenv("COLORTERM"))
	trueColorHint := colorTerm == "truecolor" || colorTerm == "24bit"
	if os.Getenv("TMUX") != "" || strings.HasPrefix(term, "tmux") {
		forwarded := false
		if tmuxForwardsHyperlink != nil {
			forwarded = tmuxForwardsHyperlink()
		}
		return TerminalCapabilities{TrueColor: trueColorHint, Hyperlinks: forwarded}
	}
	if strings.HasPrefix(term, "screen") {
		return TerminalCapabilities{TrueColor: trueColorHint}
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || termProgram == "kitty" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if termProgram == "ghostty" || strings.Contains(term, "ghostty") || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("WEZTERM_PANE") != "" || termProgram == "wezterm" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if termProgram == "warpterminal" || os.Getenv("WARP_SESSION_ID") != "" || os.Getenv("WARP_TERMINAL_SESSION_UUID") != "" {
		return TerminalCapabilities{Images: ImageProtocolKitty, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("ITERM_SESSION_ID") != "" || termProgram == "iterm.app" {
		return TerminalCapabilities{Images: ImageProtocolITerm2, TrueColor: true, Hyperlinks: true}
	}
	if os.Getenv("WT_SESSION") != "" || termProgram == "vscode" || termProgram == "alacritty" {
		return TerminalCapabilities{TrueColor: true, Hyperlinks: true}
	}
	if terminalEmulator == "jetbrains-jediterm" {
		return TerminalCapabilities{TrueColor: true}
	}
	return TerminalCapabilities{TrueColor: trueColorHint}
}

func GetCapabilities() TerminalCapabilities {
	terminalImageState.RLock()
	if terminalImageState.capabilities != nil {
		capabilities := *terminalImageState.capabilities
		terminalImageState.RUnlock()
		return capabilities
	}
	terminalImageState.RUnlock()
	capabilities := DetectCapabilities(probeTmuxHyperlinks)
	terminalImageState.Lock()
	if terminalImageState.capabilities == nil {
		terminalImageState.capabilities = &capabilities
	} else {
		capabilities = *terminalImageState.capabilities
	}
	terminalImageState.Unlock()
	return capabilities
}

func SetCapabilities(capabilities TerminalCapabilities) {
	terminalImageState.Lock()
	terminalImageState.capabilities = &capabilities
	terminalImageState.Unlock()
}

func ResetCapabilitiesCache() {
	terminalImageState.Lock()
	terminalImageState.capabilities = nil
	terminalImageState.Unlock()
}

func GetCellDimensions() CellDimensions {
	terminalImageState.RLock()
	dimensions := terminalImageState.cell
	terminalImageState.RUnlock()
	return dimensions
}

func SetCellDimensions(dimensions CellDimensions) {
	terminalImageState.Lock()
	terminalImageState.cell = dimensions
	terminalImageState.Unlock()
}

func IsImageLine(line string) bool {
	return strings.Contains(line, "\x1b_G") || strings.Contains(line, "\x1b]1337;File=")
}

func parseKittyImageHeader(line string) ([]uint32, int) {
	start := strings.Index(line, "\x1b_G")
	if start < 0 {
		return nil, 1
	}
	start += len("\x1b_G")
	end := strings.IndexByte(line[start:], ';')
	if end < 0 {
		return nil, 1
	}
	var ids []uint32
	rows := 1
	for _, parameter := range strings.Split(line[start:start+end], ",") {
		key, value, ok := strings.Cut(parameter, "=")
		if !ok {
			continue
		}
		number, err := strconv.ParseUint(value, 10, 32)
		if err != nil || number == 0 {
			continue
		}
		switch key {
		case "i":
			ids = append(ids, uint32(number))
		case "r":
			rows = int(number)
		}
	}
	return ids, rows
}

func AllocateImageID() uint32 {
	var bytes [4]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		if id := binary.BigEndian.Uint32(bytes[:]); id != 0 {
			return id
		}
	}
	return uint32(time.Now().UnixNano()) | 1
}

func EncodeKitty(base64Data string, columns, rows int, imageID uint32, moveCursor bool) string {
	params := []string{"a=T", "f=100", "q=2"}
	if !moveCursor {
		params = append(params, "C=1")
	}
	if columns != 0 {
		params = append(params, "c="+strconv.Itoa(columns))
	}
	if rows != 0 {
		params = append(params, "r="+strconv.Itoa(rows))
	}
	if imageID != 0 {
		params = append(params, "i="+strconv.FormatUint(uint64(imageID), 10))
	}
	const chunkSize = 4096
	if len(base64Data) <= chunkSize {
		return "\x1b_G" + strings.Join(params, ",") + ";" + base64Data + "\x1b\\"
	}
	var result strings.Builder
	for offset := 0; offset < len(base64Data); offset += chunkSize {
		end := min(len(base64Data), offset+chunkSize)
		switch {
		case offset == 0:
			result.WriteString("\x1b_G" + strings.Join(params, ",") + ",m=1;")
		case end == len(base64Data):
			result.WriteString("\x1b_Gm=0;")
		default:
			result.WriteString("\x1b_Gm=1;")
		}
		result.WriteString(base64Data[offset:end])
		result.WriteString("\x1b\\")
	}
	return result.String()
}

func DeleteKittyImage(imageID uint32) string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", imageID)
}

func DeleteAllKittyImages() string { return "\x1b_Ga=d,d=A,q=2\x1b\\" }

func EncodeITerm2(base64Data string, width, height any, name string, preserveAspectRatio, inline bool) string {
	inlineValue := 0
	if inline {
		inlineValue = 1
	}
	params := []string{"inline=" + strconv.Itoa(inlineValue)}
	if width != nil {
		params = append(params, fmt.Sprintf("width=%v", width))
	}
	if height != nil {
		params = append(params, fmt.Sprintf("height=%v", height))
	}
	if name != "" {
		params = append(params, "name="+base64.StdEncoding.EncodeToString([]byte(name)))
	}
	if !preserveAspectRatio {
		params = append(params, "preserveAspectRatio=0")
	}
	return "\x1b]1337;File=" + strings.Join(params, ";") + ":" + base64Data + "\x07"
}

func CalculateImageCellSize(image ImageDimensions, maxWidthCells int, maxHeightCells *int, cell CellDimensions) ImageCellSize {
	maxWidth := max(1, maxWidthCells)
	var maxHeight *int
	if maxHeightCells != nil {
		value := max(1, *maxHeightCells)
		maxHeight = &value
	}
	imageWidth, imageHeight := max(1, image.WidthPx), max(1, image.HeightPx)
	cellWidth, cellHeight := max(1, cell.WidthPx), max(1, cell.HeightPx)
	widthScale := float64(maxWidth*cellWidth) / float64(imageWidth)
	heightScale := widthScale
	if maxHeight != nil {
		heightScale = float64(*maxHeight*cellHeight) / float64(imageHeight)
	}
	scale := math.Min(widthScale, heightScale)
	columns := int(math.Ceil(float64(imageWidth) * scale / float64(cellWidth)))
	rows := int(math.Ceil(float64(imageHeight) * scale / float64(cellHeight)))
	columns = max(1, min(maxWidth, columns))
	if maxHeight != nil {
		rows = min(*maxHeight, rows)
	}
	return ImageCellSize{Columns: columns, Rows: max(1, rows)}
}

func CalculateImageRows(image ImageDimensions, targetWidthCells int, cell CellDimensions) int {
	return CalculateImageCellSize(image, targetWidthCells, nil, cell).Rows
}

func decodeImageBase64(data string) ([]byte, bool) {
	decoded, err := base64.StdEncoding.DecodeString(data)
	return decoded, err == nil
}

func GetPNGDimensions(data string) *ImageDimensions {
	decoded, ok := decodeImageBase64(data)
	if !ok || len(decoded) < 24 || string(decoded[:4]) != "\x89PNG" {
		return nil
	}
	return &ImageDimensions{WidthPx: int(binary.BigEndian.Uint32(decoded[16:20])), HeightPx: int(binary.BigEndian.Uint32(decoded[20:24]))}
}

func GetJPEGDimensions(data string) *ImageDimensions {
	decoded, ok := decodeImageBase64(data)
	if !ok || len(decoded) < 2 || decoded[0] != 0xff || decoded[1] != 0xd8 {
		return nil
	}
	for offset := 2; offset < len(decoded)-9; {
		if decoded[offset] != 0xff {
			offset++
			continue
		}
		marker := decoded[offset+1]
		if marker >= 0xc0 && marker <= 0xc2 {
			return &ImageDimensions{WidthPx: int(binary.BigEndian.Uint16(decoded[offset+7 : offset+9])), HeightPx: int(binary.BigEndian.Uint16(decoded[offset+5 : offset+7]))}
		}
		if offset+3 >= len(decoded) {
			return nil
		}
		length := int(binary.BigEndian.Uint16(decoded[offset+2 : offset+4]))
		if length < 2 {
			return nil
		}
		offset += 2 + length
	}
	return nil
}

func GetGIFDimensions(data string) *ImageDimensions {
	decoded, ok := decodeImageBase64(data)
	if !ok || len(decoded) < 10 || string(decoded[:6]) != "GIF87a" && string(decoded[:6]) != "GIF89a" {
		return nil
	}
	return &ImageDimensions{WidthPx: int(binary.LittleEndian.Uint16(decoded[6:8])), HeightPx: int(binary.LittleEndian.Uint16(decoded[8:10]))}
}

func GetWebPDimensions(data string) *ImageDimensions {
	decoded, ok := decodeImageBase64(data)
	if !ok || len(decoded) < 30 || string(decoded[:4]) != "RIFF" || string(decoded[8:12]) != "WEBP" {
		return nil
	}
	switch string(decoded[12:16]) {
	case "VP8 ":
		return &ImageDimensions{WidthPx: int(binary.LittleEndian.Uint16(decoded[26:28]) & 0x3fff), HeightPx: int(binary.LittleEndian.Uint16(decoded[28:30]) & 0x3fff)}
	case "VP8L":
		bits := binary.LittleEndian.Uint32(decoded[21:25])
		return &ImageDimensions{WidthPx: int(bits&0x3fff) + 1, HeightPx: int((bits>>14)&0x3fff) + 1}
	case "VP8X":
		width := int(decoded[24]) | int(decoded[25])<<8 | int(decoded[26])<<16
		height := int(decoded[27]) | int(decoded[28])<<8 | int(decoded[29])<<16
		return &ImageDimensions{WidthPx: width + 1, HeightPx: height + 1}
	default:
		return nil
	}
}

func GetImageDimensions(data, mimeType string) *ImageDimensions {
	switch mimeType {
	case "image/png":
		return GetPNGDimensions(data)
	case "image/jpeg":
		return GetJPEGDimensions(data)
	case "image/gif":
		return GetGIFDimensions(data)
	case "image/webp":
		return GetWebPDimensions(data)
	default:
		return nil
	}
}

func RenderImage(data string, dimensions ImageDimensions, options ImageRenderOptions) *ImageRenderResult {
	capabilities := GetCapabilities()
	if capabilities.Images == "" {
		return nil
	}
	maxWidth := 80
	if options.MaxWidthCells != nil {
		maxWidth = *options.MaxWidthCells
	}
	size := CalculateImageCellSize(dimensions, maxWidth, options.MaxHeightCells, GetCellDimensions())
	if capabilities.Images == ImageProtocolKitty {
		moveCursor := true
		if options.MoveCursor != nil {
			moveCursor = *options.MoveCursor
		}
		imageID := uint32(0)
		if options.ImageID != nil {
			imageID = *options.ImageID
		}
		return &ImageRenderResult{Sequence: EncodeKitty(data, size.Columns, size.Rows, imageID, moveCursor), Rows: size.Rows, ImageID: options.ImageID}
	}
	preserve := true
	if options.PreserveAspectRatio != nil {
		preserve = *options.PreserveAspectRatio
	}
	return &ImageRenderResult{Sequence: EncodeITerm2(data, size.Columns, "auto", "", preserve, true), Rows: size.Rows}
}

func ImageFallback(mimeType string, dimensions *ImageDimensions, filename string) string {
	parts := make([]string, 0, 3)
	if filename != "" {
		parts = append(parts, filename)
	}
	parts = append(parts, "["+mimeType+"]")
	if dimensions != nil {
		parts = append(parts, fmt.Sprintf("%dx%d", dimensions.WidthPx, dimensions.HeightPx))
	}
	return "[Image: " + strings.Join(parts, " ") + "]"
}
