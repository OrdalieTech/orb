package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/jsonschema"
	"github.com/OrdalieTech/orb/internal/truncate"
)

var readSchema = jsonschema.Schema(`{"type":"object","required":["path"],"properties":{"path":{"type":"string","description":"Path to the file to read (relative or absolute)"},"offset":{"type":"number","description":"Line number to start reading from (1-indexed)"},"limit":{"type":"number","description":"Maximum number of lines to read"}}}`)

type ReadToolInput struct {
	Path   string   `json:"path"`
	Offset *float64 `json:"offset,omitempty"`
	Limit  *float64 `json:"limit,omitempty"`
}

type ReadToolDetails struct {
	Truncation *truncate.Result `json:"truncation,omitempty"`
}

// ReadOperations is the delegation seam for the read tool.
type ReadOperations interface {
	ReadFile(context.Context, string) ([]byte, error)
	Access(context.Context, string) error
}

// ReadStreamOperations lets ReadOperations stream text files, so a read keeps only the lines it
// shows in memory instead of the whole file.
type ReadStreamOperations interface {
	OpenFile(context.Context, string) (io.ReadCloser, error)
}

// ReadImageOperations is the optional image-detection extension used by
// delegated filesystems that cannot be sniffed locally before reading.
type ReadImageOperations interface {
	DetectImageMimeType(context.Context, string) (string, error)
}

type ReadToolOptions struct {
	Operations       ReadOperations
	AutoResizeImages *bool
}

type readTool struct {
	cwd              string
	operations       ReadOperations
	autoResizeImages bool
}

type localReadOperations struct{}

func (localReadOperations) ReadFile(_ context.Context, path string) ([]byte, error) {
	if err := nodeNullPathError(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	return data, asNodeFilesystemError("open", path, err)
}

func (localReadOperations) OpenFile(_ context.Context, path string) (io.ReadCloser, error) {
	if err := nodeNullPathError(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	return file, asNodeFilesystemError("open", path, err)
}

func (localReadOperations) Access(_ context.Context, path string) error {
	if err := nodeNullPathError(path); err != nil {
		return err
	}
	return asNodeFilesystemError("access", path, accessFile(path, accessRead))
}

func (operations localReadOperations) DetectImageMimeType(ctx context.Context, path string) (string, error) {
	file, err := operations.OpenFile(ctx, path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, 4100)
	count, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "", asNodeFilesystemError("read", path, err)
	}
	return DetectSupportedImageMimeType(buffer[:count]), nil
}

func NewReadTool(cwd string, options *ReadToolOptions) engine.AgentTool {
	operations := ReadOperations(localReadOperations{})
	autoResizeImages := true
	if options != nil && options.Operations != nil {
		operations = options.Operations
	}
	if options != nil && options.AutoResizeImages != nil {
		autoResizeImages = *options.AutoResizeImages
	}
	return &readTool{cwd: cwd, operations: operations, autoResizeImages: autoResizeImages}
}

var readDescription = fmt.Sprintf("Read the contents of a file. Supports text files and images (jpg, png, gif, webp, bmp). Images are sent as attachments. For text files, output is truncated to %d lines or %dKB (whichever is hit first). Use offset/limit for large files. When you need the full file, continue with offset until complete.", truncate.DefaultMaxLines, truncate.DefaultMaxBytes/1024)

func (tool *readTool) Spec() engine.AgentToolSpec {
	return engine.AgentToolSpec{
		Name:                "read",
		Label:               "read",
		Description:         readDescription,
		Parameters:          readSchema,
		ConstrainedSampling: strictToolSampling,
	}
}

func (tool *readTool) Execute(
	ctx context.Context,
	_ string,
	params any,
	_ engine.AgentToolUpdateCallback,
) (engine.AgentToolResult, error) {
	input, err := readInput(params)
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	absolutePath, err := runCancelable(ctx, func() (string, error) {
		return resolveReadPath(input.Path, tool.cwd, func(path string) bool { return tool.operations.Access(ctx, path) == nil })
	})
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	if err := runCancelableError(ctx, func() error { return tool.operations.Access(ctx, absolutePath) }); err != nil {
		return engine.AgentToolResult{}, err
	}
	mimeType := ""
	if detector, ok := tool.operations.(ReadImageOperations); ok {
		mimeType, err = runCancelable(ctx, func() (string, error) { return detector.DetectImageMimeType(ctx, absolutePath) })
		if err != nil {
			return engine.AgentToolResult{}, err
		}
	}
	startLine := 0.0
	if input.Offset != nil && *input.Offset != 0 {
		startLine = math.Max(0, *input.Offset-1)
	}
	endLine := math.Inf(1)
	if input.Limit != nil {
		endLine = startLine + *input.Limit
	}
	var source io.Reader
	if streamer, ok := tool.operations.(ReadStreamOperations); ok && mimeType == "" && endLine >= startLine {
		file, err := runCancelable(ctx, func() (io.ReadCloser, error) { return streamer.OpenFile(ctx, absolutePath) })
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		defer func() { _ = file.Close() }()
		source = file
	} else {
		data, err := runCancelable(ctx, func() ([]byte, error) { return tool.operations.ReadFile(ctx, absolutePath) })
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		if mimeType != "" {
			return tool.imageResult(ctx, data, mimeType), nil
		}
		if endLine < startLine {
			// A negative limit slices from the end, as JavaScript's Array.slice.
			total := bytes.Count(data, []byte("\n")) + 1
			endLine = float64(jsSliceIndex(math.Min(endLine, float64(total)), total))
		}
		source = bytes.NewReader(data)
	}
	selection, err := runCancelable(ctx, func() (lineSelection, error) {
		return selectLines(source, math.Trunc(startLine), math.Trunc(endLine))
	})
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	totalLines, truncation := selection.totalLines, selection.truncation
	if startLine >= float64(totalLines) {
		return engine.AgentToolResult{}, upstreamToolErrorf("Offset %s is beyond end of file (%d lines total)", formatJSNumber(*input.Offset), totalLines)
	}
	var userLimitedLines *float64
	if input.Limit != nil {
		limited := math.Min(startLine+*input.Limit, float64(totalLines)) - startLine
		userLimitedLines = &limited
	}
	startLineDisplay := startLine + 1
	outputText := truncation.Content
	var details any
	if truncation.FirstLineExceedsLimit {
		if startLine != math.Trunc(startLine) {
			return engine.AgentToolResult{}, upstreamToolError(`The "string" argument must be of type string or an instance of Buffer or ArrayBuffer. Received undefined`)
		}
		firstLineSize := truncate.FormatSize(selection.firstLineBytes)
		line := formatJSNumber(startLineDisplay)
		outputText = fmt.Sprintf("[Line %s is %s, exceeds %s limit. Use bash: sed -n '%sp' %s | head -c %d]", line, firstLineSize, truncate.FormatSize(truncate.DefaultMaxBytes), line, input.Path, truncate.DefaultMaxBytes)
		details = ReadToolDetails{Truncation: &truncation}
	} else if truncation.Truncated {
		endLineDisplay := startLineDisplay + float64(truncation.OutputLines) - 1
		nextOffset := endLineDisplay + 1
		if truncation.TruncatedBy != nil && *truncation.TruncatedBy == truncate.ReasonLines {
			outputText += fmt.Sprintf("\n\n[Showing lines %s-%s of %d. Use offset=%s to continue.]", formatJSNumber(startLineDisplay), formatJSNumber(endLineDisplay), totalLines, formatJSNumber(nextOffset))
		} else {
			outputText += fmt.Sprintf("\n\n[Showing lines %s-%s of %d (%s limit). Use offset=%s to continue.]", formatJSNumber(startLineDisplay), formatJSNumber(endLineDisplay), totalLines, truncate.FormatSize(truncate.DefaultMaxBytes), formatJSNumber(nextOffset))
		}
		details = ReadToolDetails{Truncation: &truncation}
	} else if userLimitedLines != nil && startLine+*userLimitedLines < float64(totalLines) {
		remaining := float64(totalLines) - (startLine + *userLimitedLines)
		nextOffset := startLine + *userLimitedLines + 1
		outputText += fmt.Sprintf("\n\n[%s more lines in file. Use offset=%s to continue.]", formatJSNumber(remaining), formatJSNumber(nextOffset))
	}
	return engine.AgentToolResult{
		Content: ai.ToolResultContent{&ai.TextContent{Text: outputText}},
		Details: details,
	}, nil
}

type lineSelection struct {
	truncation     truncate.Result
	totalLines     int
	firstLineBytes int
}

// selectLines reads the lines [start, end) of a file. Only the lines truncateHead can show are
// stored; the rest are counted, so memory does not grow with the file. Line counts follow
// strings.Split(text, "\n").
func selectLines(source io.Reader, start, end float64) (lineSelection, error) {
	reader := bufio.NewReaderSize(source, 64*1024)
	var stored []string
	var current []byte
	var selection lineSelection
	storing, lastLineEmpty := true, true
	line, storedBytes, selectedLines, selectedBytes, lineBytes := 0, 0, 0, 0, 0
	for {
		chunk, err := reader.ReadSlice('\n')
		if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
			return lineSelection{}, err
		}
		chunk = bytes.TrimSuffix(chunk, []byte("\n"))
		selected := float64(line) >= start && float64(line) < end
		if selected {
			// A line over the byte cap is never shown, and a first line over it is only measured.
			if storing && lineBytes <= truncate.DefaultMaxBytes {
				current = append(current, chunk...)
			}
			lineBytes += len(chunk)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if selected {
			if selectedLines == 0 {
				selection.firstLineBytes = lineBytes
			}
			if storing {
				// Storing continues one line past the caps: truncateHead ignores a trailing empty line,
				// so it needs the line after the caps to decide truncation as for the full text.
				storing = len(stored) <= truncate.DefaultMaxLines && storedBytes <= truncate.DefaultMaxBytes
				text := decodeNodeUTF8(current)
				storedBytes += len(text) + min(len(stored), 1)
				stored = append(stored, text)
			}
			selectedBytes += lineBytes + 1
			selectedLines++
			lastLineEmpty = lineBytes == 0
			current, lineBytes = current[:0], 0
		}
		if err == io.EOF {
			break
		}
		line++
	}
	selection.truncation = truncate.TruncateHead(strings.Join(stored, "\n"))
	// truncateHead's counts for the joined selected lines, which drop a trailing empty line.
	selection.truncation.TotalBytes = max(0, selectedBytes-1)
	if lastLineEmpty {
		selectedLines--
	}
	selection.truncation.TotalLines = max(0, selectedLines)
	selection.totalLines = line + 1
	return selection, nil
}

func (tool *readTool) imageResult(ctx context.Context, data []byte, mimeType string) engine.AgentToolResult {
	autoResize := tool.autoResizeImages
	model := engine.ToolExecutionModel(ctx)
	processed := ProcessImage(data, mimeType, &ProcessImageOptions{AutoResizeImages: &autoResize, ResizeOptions: ModelResizeOptions(model)})
	vision := model == nil || slices.Contains(model.Input, ai.InputImage)
	nonVisionNote := ""
	if !vision {
		nonVisionNote = "[Current model does not support images. The image will be omitted from this request.]"
	}
	if !processed.OK {
		text := "Read image file [" + mimeType + "]\n" + processed.Message
		if nonVisionNote != "" {
			text += "\n" + nonVisionNote
		}
		return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: text}}}
	}
	text := "Read image file [" + processed.MimeType + "]"
	if len(processed.Hints) > 0 {
		text += "\n" + strings.Join(processed.Hints, "\n")
	}
	if nonVisionNote != "" {
		text += "\n" + nonVisionNote
	}
	return engine.AgentToolResult{Content: ai.ToolResultContent{
		&ai.TextContent{Text: text},
		&ai.ImageContent{Data: processed.Data, MimeType: processed.MimeType},
	}}
}

func readInput(params any) (ReadToolInput, error) {
	object, err := toolParams(params)
	if err != nil {
		return ReadToolInput{}, err
	}
	path, err := requiredString(object, "path")
	if err != nil {
		return ReadToolInput{}, err
	}
	offset, err := optionalNumber(object, "offset")
	if err != nil {
		return ReadToolInput{}, err
	}
	limit, err := optionalNumber(object, "limit")
	if err != nil {
		return ReadToolInput{}, err
	}
	input := ReadToolInput{Path: path}
	if offset != nil {
		input.Offset = offset
	}
	if limit != nil {
		input.Limit = limit
	}
	return input, nil
}
