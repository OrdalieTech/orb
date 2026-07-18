package truncate

import (
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/OrdalieTech/pi-go/internal/jsonwire"
)

const (
	DefaultMaxLines   = 2000
	DefaultMaxBytes   = 50 * 1024
	GrepMaxLineLength = 500
)

type Reason string

const (
	ReasonLines Reason = "lines"
	ReasonBytes Reason = "bytes"
)

// Options uses pointers because zero is an upstream-visible limit, while nil
// means the option was omitted and selects the default.
type Options struct {
	MaxLines *int `json:"maxLines,omitempty"`
	MaxBytes *int `json:"maxBytes,omitempty"`
}

type Result struct {
	Content               string  `json:"content"`
	Truncated             bool    `json:"truncated"`
	TruncatedBy           *Reason `json:"truncatedBy"`
	TotalLines            int     `json:"totalLines"`
	TotalBytes            int     `json:"totalBytes"`
	OutputLines           int     `json:"outputLines"`
	OutputBytes           int     `json:"outputBytes"`
	LastLinePartial       bool    `json:"lastLinePartial"`
	FirstLineExceedsLimit bool    `json:"firstLineExceedsLimit"`
	MaxLines              int     `json:"maxLines"`
	MaxBytes              int     `json:"maxBytes"`
}

type LineResult struct {
	Text         string `json:"text"`
	WasTruncated bool   `json:"wasTruncated"`
}

func Int(value int) *int {
	return &value
}

func FormatSize(bytes int) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		return formatFixedOne(bytes, 1024) + "KB"
	default:
		return formatFixedOne(bytes, 1024*1024) + "MB"
	}
}

func formatFixedOne(value, unit int) string {
	whole := value / unit
	tenths := ((value%unit)*10 + unit/2) / unit
	if tenths == 10 {
		whole++
		tenths = 0
	}
	return fmt.Sprintf("%d.%d", whole, tenths)
}

func TruncateHead(content string, options ...Options) Result {
	maxLines, maxBytes := limits(options)
	totalBytes := len(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return Result{
			Content:     content,
			TruncatedBy: nil,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    maxLines,
			MaxBytes:    maxBytes,
		}
	}

	if len(lines) > 0 && len(lines[0]) > maxBytes {
		return Result{
			Truncated:             true,
			TruncatedBy:           reason(ReasonBytes),
			TotalLines:            totalLines,
			TotalBytes:            totalBytes,
			FirstLineExceedsLimit: true,
			MaxLines:              maxLines,
			MaxBytes:              maxBytes,
		}
	}

	outputLines := make([]string, 0, min(totalLines, max(0, maxLines)))
	outputBytes := 0
	truncatedBy := ReasonLines
	for index := 0; index < len(lines) && index < maxLines; index++ {
		lineBytes := len(lines[index])
		if index > 0 {
			lineBytes++
		}
		if outputBytes+lineBytes > maxBytes {
			truncatedBy = ReasonBytes
			break
		}
		outputLines = append(outputLines, lines[index])
		outputBytes += lineBytes
	}
	if len(outputLines) >= maxLines && outputBytes <= maxBytes {
		truncatedBy = ReasonLines
	}
	output := strings.Join(outputLines, "\n")

	return Result{
		Content:     output,
		Truncated:   true,
		TruncatedBy: reason(truncatedBy),
		TotalLines:  totalLines,
		TotalBytes:  totalBytes,
		OutputLines: len(outputLines),
		OutputBytes: len(output),
		MaxLines:    maxLines,
		MaxBytes:    maxBytes,
	}
}

func TruncateTail(content string, options ...Options) Result {
	maxLines, maxBytes := limits(options)
	totalBytes := len(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return Result{
			Content:     content,
			TruncatedBy: nil,
			TotalLines:  totalLines,
			TotalBytes:  totalBytes,
			OutputLines: totalLines,
			OutputBytes: totalBytes,
			MaxLines:    maxLines,
			MaxBytes:    maxBytes,
		}
	}

	outputLines := make([]string, 0, min(totalLines, max(0, maxLines)))
	outputBytes := 0
	truncatedBy := ReasonLines
	lastLinePartial := false
	for index := len(lines) - 1; index >= 0 && len(outputLines) < maxLines; index-- {
		lineBytes := len(lines[index])
		if len(outputLines) > 0 {
			lineBytes++
		}
		if outputBytes+lineBytes > maxBytes {
			truncatedBy = ReasonBytes
			if len(outputLines) == 0 {
				line := truncateStringToBytesFromEnd(lines[index], maxBytes)
				outputLines = append(outputLines, line)
				outputBytes = len(line)
				lastLinePartial = true
			}
			break
		}
		outputLines = append(outputLines, "")
		copy(outputLines[1:], outputLines[:len(outputLines)-1])
		outputLines[0] = lines[index]
		outputBytes += lineBytes
	}
	if len(outputLines) >= maxLines && outputBytes <= maxBytes {
		truncatedBy = ReasonLines
	}
	output := strings.Join(outputLines, "\n")

	return Result{
		Content:         output,
		Truncated:       true,
		TruncatedBy:     reason(truncatedBy),
		TotalLines:      totalLines,
		TotalBytes:      totalBytes,
		OutputLines:     len(outputLines),
		OutputBytes:     len(output),
		LastLinePartial: lastLinePartial,
		MaxLines:        maxLines,
		MaxBytes:        maxBytes,
	}
}

func TruncateLine(line string, maxChars ...int) LineResult {
	limit := GrepMaxLineLength
	if len(maxChars) > 0 {
		limit = maxChars[0]
	}
	units := utf16Units(line)
	if len(units) <= limit {
		return LineResult{Text: line}
	}
	end := limit
	if end < 0 {
		end = len(units) + end
	}
	end = max(0, min(len(units), end))
	return LineResult{
		Text:         stringFromUTF16Units(units[:end]) + "... [truncated]",
		WasTruncated: true,
	}
}

func limits(options []Options) (int, int) {
	maxLines := DefaultMaxLines
	maxBytes := DefaultMaxBytes
	if len(options) == 0 {
		return maxLines, maxBytes
	}
	if options[0].MaxLines != nil {
		maxLines = *options[0].MaxLines
	}
	if options[0].MaxBytes != nil {
		maxBytes = *options[0].MaxBytes
	}
	return maxLines, maxBytes
}

func splitLinesForCounting(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func truncateStringToBytesFromEnd(value string, maxBytes int) string {
	encoded := []byte(string(utf16.Decode(utf16Units(value))))
	if len(encoded) <= maxBytes {
		return string(encoded)
	}
	start := len(encoded) - maxBytes
	start = min(len(encoded), max(0, start))
	for start < len(encoded) && encoded[start]&0xc0 == 0x80 {
		start++
	}
	return string(encoded[start:])
}

func reason(value Reason) *Reason {
	return &value
}

func utf16Units(value string) []uint16 {
	units := make([]uint16, 0, len(value))
	for index := 0; index < len(value); {
		character, size := utf8.DecodeRuneInString(value[index:])
		if character == utf8.RuneError && size == 1 {
			if surrogate, ok := jsonwire.DecodeWTF8Surrogate(value[index:]); ok {
				units = append(units, surrogate)
				index += 3
				continue
			}
			units = append(units, uint16(utf8.RuneError))
			index++
			continue
		}
		if character <= 0xffff {
			units = append(units, uint16(character))
		} else {
			first, second := utf16.EncodeRune(character)
			units = append(units, uint16(first), uint16(second))
		}
		index += size
	}
	return units
}

func stringFromUTF16Units(units []uint16) string {
	var output strings.Builder
	output.Grow(len(units))
	for index := 0; index < len(units); index++ {
		unit := units[index]
		if unit >= 0xd800 && unit <= 0xdbff && index+1 < len(units) {
			next := units[index+1]
			if next >= 0xdc00 && next <= 0xdfff {
				output.WriteRune(utf16.DecodeRune(rune(unit), rune(next)))
				index++
				continue
			}
		}
		if unit >= 0xd800 && unit <= 0xdfff {
			output.WriteByte(byte(0xe0 | unit>>12))
			output.WriteByte(byte(0x80 | unit>>6&0x3f))
			output.WriteByte(byte(0x80 | unit&0x3f))
			continue
		}
		output.WriteRune(rune(unit))
	}
	return output.String()
}
