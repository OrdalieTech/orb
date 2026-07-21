package tools

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/internal/truncate"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

type OutputAccumulatorOptions struct {
	MaxLines       *int
	MaxBytes       *int
	TempFilePrefix *string
}

type OutputSnapshotOptions struct {
	PersistIfTruncated bool
}

type OutputSnapshot struct {
	Content        string
	Truncation     truncate.Result
	FullOutputPath string
}

// OutputAccumulator incrementally retains a bounded decoded tail while
// preserving the original byte stream if the output crosses either limit.
type OutputAccumulator struct {
	mu sync.Mutex

	maxLines        int
	maxBytes        int
	maxRollingBytes int
	tempFilePrefix  string
	decoder         streamingUTF8Decoder

	rawChunks                [][]byte
	tailText                 string
	tailBytes                int
	tailStartsAtLineBoundary bool
	totalRawBytes            int
	totalDecodedBytes        int
	completedLines           int
	totalLines               int
	currentLineBytes         int
	hasOpenLine              bool
	finished                 bool

	tempFilePath string
	tempFile     *os.File
}

func NewOutputAccumulator(options ...OutputAccumulatorOptions) *OutputAccumulator {
	maxLines := truncate.DefaultMaxLines
	maxBytes := truncate.DefaultMaxBytes
	prefix := "pi-output"
	if len(options) > 0 {
		if options[0].MaxLines != nil {
			maxLines = *options[0].MaxLines
		}
		if options[0].MaxBytes != nil {
			maxBytes = *options[0].MaxBytes
		}
		if options[0].TempFilePrefix != nil {
			prefix = *options[0].TempFilePrefix
		}
	}
	return &OutputAccumulator{
		maxLines:                 maxLines,
		maxBytes:                 maxBytes,
		maxRollingBytes:          max(maxBytes*2, 1),
		tempFilePrefix:           prefix,
		tailStartsAtLineBoundary: true,
	}
}

func (output *OutputAccumulator) Append(data []byte) error {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.append(len(data), data, output.decoder.Decode(data, false))
}

// appendTransformed records rawSize for spill selection while retaining text
// in the rolling buffer and full-output file.
func (output *OutputAccumulator) appendTransformed(rawSize int, text string) error {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.append(rawSize, []byte(text), text)
}

func (output *OutputAccumulator) append(rawSize int, persisted []byte, decoded string) error {
	if output.finished {
		return upstreamToolError("Cannot append to a finished output accumulator")
	}

	output.totalRawBytes += rawSize
	output.appendDecodedText(decoded)
	if output.tempFile != nil || output.shouldUseTempFile() {
		if err := output.ensureTempFile(); err != nil {
			return err
		}
		if output.tempFile != nil {
			return writeAll(output.tempFile, persisted)
		}
		return nil
	}
	if len(persisted) > 0 {
		output.rawChunks = append(output.rawChunks, append([]byte(nil), persisted...))
	}
	return nil
}

func (output *OutputAccumulator) Finish() error {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.finished {
		return nil
	}
	output.finished = true
	output.appendDecodedText(output.decoder.Decode(nil, true))
	if output.shouldUseTempFile() {
		return output.ensureTempFile()
	}
	return nil
}

func (output *OutputAccumulator) Snapshot(options ...OutputSnapshotOptions) (OutputSnapshot, error) {
	output.mu.Lock()
	defer output.mu.Unlock()

	tailTruncation := truncate.TruncateTail(output.snapshotText(), truncate.Options{
		MaxLines: truncate.Int(output.maxLines),
		MaxBytes: truncate.Int(output.maxBytes),
	})
	truncated := output.totalLines > output.maxLines || output.totalDecodedBytes > output.maxBytes
	truncatedBy := tailTruncation.TruncatedBy
	if truncated && truncatedBy == nil {
		if output.totalDecodedBytes > output.maxBytes {
			reason := truncate.ReasonBytes
			truncatedBy = &reason
		} else {
			reason := truncate.ReasonLines
			truncatedBy = &reason
		}
	}
	tailTruncation.Truncated = truncated
	tailTruncation.TruncatedBy = truncatedBy
	tailTruncation.TotalLines = output.totalLines
	tailTruncation.TotalBytes = output.totalDecodedBytes
	tailTruncation.MaxLines = output.maxLines
	tailTruncation.MaxBytes = output.maxBytes

	if len(options) > 0 && options[0].PersistIfTruncated && truncated {
		if err := output.ensureTempFile(); err != nil {
			return OutputSnapshot{}, err
		}
	}
	return OutputSnapshot{
		Content:        tailTruncation.Content,
		Truncation:     tailTruncation,
		FullOutputPath: output.tempFilePath,
	}, nil
}

func (output *OutputAccumulator) CloseTempFile() error {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.tempFile == nil {
		return nil
	}
	file := output.tempFile
	output.tempFile = nil
	return file.Close()
}

func (output *OutputAccumulator) LastLineBytes() int {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.currentLineBytes
}

func (output *OutputAccumulator) appendDecodedText(text string) {
	if text == "" {
		return
	}
	decodedBytes := len(text)
	output.totalDecodedBytes += decodedBytes
	output.tailText += text
	output.tailBytes += decodedBytes
	if output.tailBytes > output.maxRollingBytes*2 {
		output.trimTail()
	}

	newlines := strings.Count(text, "\n")
	if newlines == 0 {
		output.currentLineBytes += decodedBytes
		output.hasOpenLine = true
	} else {
		output.completedLines += newlines
		tail := text[strings.LastIndexByte(text, '\n')+1:]
		output.currentLineBytes = len(tail)
		output.hasOpenLine = tail != ""
	}
	output.totalLines = output.completedLines
	if output.hasOpenLine {
		output.totalLines++
	}
}

func (output *OutputAccumulator) trimTail() {
	buffer := []byte(output.tailText)
	if len(buffer) <= output.maxRollingBytes {
		output.tailBytes = len(buffer)
		return
	}
	start := len(buffer) - output.maxRollingBytes
	for start < len(buffer) && buffer[start]&0xc0 == 0x80 {
		start++
	}
	if start != 0 {
		output.tailStartsAtLineBoundary = buffer[start-1] == '\n'
	}
	output.tailText = string(buffer[start:])
	output.tailBytes = len(output.tailText)
}

func (output *OutputAccumulator) snapshotText() string {
	if output.tailStartsAtLineBoundary {
		return output.tailText
	}
	firstNewline := strings.IndexByte(output.tailText, '\n')
	if firstNewline == -1 {
		return output.tailText
	}
	return output.tailText[firstNewline+1:]
}

func (output *OutputAccumulator) shouldUseTempFile() bool {
	return output.totalRawBytes > output.maxBytes ||
		output.totalDecodedBytes > output.maxBytes ||
		output.totalLines > output.maxLines
}

func (output *OutputAccumulator) ensureTempFile() error {
	if output.tempFilePath != "" {
		return nil
	}
	path, file, err := createOutputTempFile(output.tempFilePrefix)
	if err != nil {
		return err
	}
	for _, chunk := range output.rawChunks {
		if err := writeAll(file, chunk); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return err
		}
	}
	output.rawChunks = nil
	output.tempFilePath = path
	output.tempFile = file
	return nil
}

func createOutputTempFile(prefix string) (string, *os.File, error) {
	for range 100 {
		var id [8]byte
		if _, err := rand.Read(id[:]); err != nil {
			return "", nil, err
		}
		path := filepath.Join(os.TempDir(), prefix+"-"+hex.EncodeToString(id[:])+".log")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if err == nil {
			return path, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, errors.New("could not allocate output temp file")
}

func writeAll(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		data = data[written:]
	}
	return nil
}

type streamingUTF8Decoder struct {
	transformer transform.Transformer
	pending     []byte
	bomChecked  bool
}

func (decoder *streamingUTF8Decoder) Decode(data []byte, atEOF bool) string {
	if decoder.transformer == nil {
		// TextDecoder strips an initial BOM and replaces the maximal invalid
		// UTF-8 subpart, rather than replacing each malformed byte separately.
		decoder.transformer = unicode.UTF8.NewDecoder()
	}
	source := make([]byte, 0, len(decoder.pending)+len(data))
	source = append(source, decoder.pending...)
	source = append(source, data...)
	decoder.pending = nil
	if !decoder.bomChecked {
		const bom = "\xef\xbb\xbf"
		prefixLength := min(len(source), len(bom))
		if string(source[:prefixLength]) == bom[:prefixLength] && len(source) < len(bom) && !atEOF {
			decoder.pending = append(decoder.pending, source...)
			return ""
		}
		decoder.bomChecked = true
		if len(source) >= len(bom) && string(source[:len(bom)]) == bom {
			source = source[len(bom):]
		}
	}
	if len(source) == 0 && !atEOF {
		return ""
	}

	var decoded strings.Builder
	for len(source) > 0 {
		destination := make([]byte, max(3, len(source)*3+3))
		written, consumed, err := decoder.transformer.Transform(destination, source, atEOF)
		decoded.Write(destination[:written])
		source = source[consumed:]
		switch err {
		case nil:
			return decoded.String()
		case transform.ErrShortDst:
			panic("UTF-8 decoder destination bound was too small")
		case transform.ErrShortSrc:
			invalidPrefix := incompleteInvalidUTF8Prefix(source)
			if invalidPrefix == 0 {
				decoder.pending = append(decoder.pending, source...)
				return decoded.String()
			}
			forced := make([]byte, invalidPrefix*3+3)
			forcedWritten, forcedConsumed, forcedErr := decoder.transformer.Transform(forced, source[:invalidPrefix], true)
			if forcedErr != nil || forcedConsumed != invalidPrefix {
				panic("UTF-8 decoder failed to consume a decidably invalid prefix")
			}
			decoded.Write(forced[:forcedWritten])
			source = source[invalidPrefix:]
		default:
			panic(err)
		}
	}
	return decoded.String()
}

func incompleteInvalidUTF8Prefix(source []byte) int {
	if len(source) < 2 {
		return 0
	}
	first := source[0]
	expected := 0
	secondLow, secondHigh := byte(0x80), byte(0xbf)
	switch {
	case first >= 0xc2 && first <= 0xdf:
		expected = 2
	case first == 0xe0:
		expected, secondLow = 3, 0xa0
	case first >= 0xe1 && first <= 0xec, first >= 0xee && first <= 0xef:
		expected = 3
	case first == 0xed:
		expected, secondHigh = 3, 0x9f
	case first == 0xf0:
		expected, secondLow = 4, 0x90
	case first >= 0xf1 && first <= 0xf3:
		expected = 4
	case first == 0xf4:
		expected, secondHigh = 4, 0x8f
	default:
		return 0
	}
	if len(source) >= expected {
		return 0
	}
	if source[1] < secondLow || source[1] > secondHigh {
		return 1
	}
	if len(source) >= 3 && (source[2] < 0x80 || source[2] > 0xbf) {
		return 2
	}
	return 0
}
