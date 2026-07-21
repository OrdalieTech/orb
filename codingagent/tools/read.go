package tools

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"syscall"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
	"github.com/OrdalieTech/pigo/internal/truncate"
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

func (localReadOperations) Access(_ context.Context, path string) error {
	if err := nodeNullPathError(path); err != nil {
		return err
	}
	return asNodeFilesystemError("access", path, syscall.Access(path, accessRead))
}

func (localReadOperations) DetectImageMimeType(_ context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", asNodeFilesystemError("open", path, err)
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, 4100)
	count, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "", asNodeFilesystemError("read", path, err)
	}
	return DetectSupportedImageMimeType(buffer[:count]), nil
}

func NewReadTool(cwd string, options *ReadToolOptions) agent.AgentTool {
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

func (tool *readTool) Spec() agent.AgentToolSpec {
	return agent.AgentToolSpec{
		Name:        "read",
		Label:       "read",
		Description: fmt.Sprintf("Read the contents of a file. Supports text files and images (jpg, png, gif, webp, bmp). Images are sent as attachments. For text files, output is truncated to %d lines or %dKB (whichever is hit first). Use offset/limit for large files. When you need the full file, continue with offset until complete.", truncate.DefaultMaxLines, truncate.DefaultMaxBytes/1024),
		Parameters:  readSchema,
	}
}

func (tool *readTool) Execute(
	ctx context.Context,
	_ string,
	params any,
	_ agent.AgentToolUpdateCallback,
) (agent.AgentToolResult, error) {
	input, err := readInput(params)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	absolutePath, err := runCancelable(ctx, func() (string, error) {
		return ResolveReadPath(input.Path, tool.cwd)
	})
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if err := runCancelableError(ctx, func() error { return tool.operations.Access(ctx, absolutePath) }); err != nil {
		return agent.AgentToolResult{}, err
	}
	mimeType := ""
	if detector, ok := tool.operations.(ReadImageOperations); ok {
		mimeType, err = runCancelable(ctx, func() (string, error) { return detector.DetectImageMimeType(ctx, absolutePath) })
		if err != nil {
			return agent.AgentToolResult{}, err
		}
	}
	data, err := runCancelable(ctx, func() ([]byte, error) { return tool.operations.ReadFile(ctx, absolutePath) })
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if mimeType != "" {
		return tool.imageResult(ctx, data, mimeType), nil
	}
	text := decodeNodeUTF8(data)
	allLines := strings.Split(text, "\n")
	startLine := 0.0
	if input.Offset != nil && *input.Offset != 0 {
		startLine = math.Max(0, *input.Offset-1)
	}
	if startLine >= float64(len(allLines)) {
		return agent.AgentToolResult{}, upstreamToolErrorf("Offset %s is beyond end of file (%d lines total)", formatJSNumber(*input.Offset), len(allLines))
	}

	sliceStart := jsSliceIndex(startLine, len(allLines))
	selectedLines := allLines[sliceStart:]
	var userLimitedLines *float64
	if input.Limit != nil {
		endLine := math.Min(startLine+*input.Limit, float64(len(allLines)))
		sliceEnd := jsSliceIndex(endLine, len(allLines))
		if sliceEnd < sliceStart {
			selectedLines = nil
		} else {
			selectedLines = allLines[sliceStart:sliceEnd]
		}
		limited := endLine - startLine
		userLimitedLines = &limited
	}
	selectedContent := strings.Join(selectedLines, "\n")
	truncation := truncate.TruncateHead(selectedContent)
	startLineDisplay := startLine + 1
	outputText := truncation.Content
	var details any
	if truncation.FirstLineExceedsLimit {
		if startLine != math.Trunc(startLine) {
			return agent.AgentToolResult{}, upstreamToolError(`The "string" argument must be of type string or an instance of Buffer or ArrayBuffer. Received undefined`)
		}
		firstLineSize := truncate.FormatSize(len(allLines[sliceStart]))
		line := formatJSNumber(startLineDisplay)
		outputText = fmt.Sprintf("[Line %s is %s, exceeds %s limit. Use bash: sed -n '%sp' %s | head -c %d]", line, firstLineSize, truncate.FormatSize(truncate.DefaultMaxBytes), line, input.Path, truncate.DefaultMaxBytes)
		details = ReadToolDetails{Truncation: &truncation}
	} else if truncation.Truncated {
		endLineDisplay := startLineDisplay + float64(truncation.OutputLines) - 1
		nextOffset := endLineDisplay + 1
		if truncation.TruncatedBy != nil && *truncation.TruncatedBy == truncate.ReasonLines {
			outputText += fmt.Sprintf("\n\n[Showing lines %s-%s of %d. Use offset=%s to continue.]", formatJSNumber(startLineDisplay), formatJSNumber(endLineDisplay), len(allLines), formatJSNumber(nextOffset))
		} else {
			outputText += fmt.Sprintf("\n\n[Showing lines %s-%s of %d (%s limit). Use offset=%s to continue.]", formatJSNumber(startLineDisplay), formatJSNumber(endLineDisplay), len(allLines), truncate.FormatSize(truncate.DefaultMaxBytes), formatJSNumber(nextOffset))
		}
		details = ReadToolDetails{Truncation: &truncation}
	} else if userLimitedLines != nil && startLine+*userLimitedLines < float64(len(allLines)) {
		remaining := float64(len(allLines)) - (startLine + *userLimitedLines)
		nextOffset := startLine + *userLimitedLines + 1
		outputText += fmt.Sprintf("\n\n[%s more lines in file. Use offset=%s to continue.]", formatJSNumber(remaining), formatJSNumber(nextOffset))
	}
	return agent.AgentToolResult{
		Content: ai.ToolResultContent{&ai.TextContent{Text: outputText}},
		Details: details,
	}, nil
}

func (tool *readTool) imageResult(ctx context.Context, data []byte, mimeType string) agent.AgentToolResult {
	autoResize := tool.autoResizeImages
	processed := ProcessImage(data, mimeType, &ProcessImageOptions{AutoResizeImages: &autoResize})
	model := agent.ToolExecutionModel(ctx)
	vision := model == nil
	if model != nil {
		for _, modality := range model.Input {
			if modality == ai.InputImage {
				vision = true
				break
			}
		}
	}
	nonVisionNote := ""
	if !vision {
		nonVisionNote = "[Current model does not support images. The image will be omitted from this request.]"
	}
	if !processed.OK {
		text := "Read image file [" + mimeType + "]\n" + processed.Message
		if nonVisionNote != "" {
			text += "\n" + nonVisionNote
		}
		return agent.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: text}}}
	}
	text := "Read image file [" + processed.MimeType + "]"
	if len(processed.Hints) > 0 {
		text += "\n" + strings.Join(processed.Hints, "\n")
	}
	if nonVisionNote != "" {
		text += "\n" + nonVisionNote
	}
	return agent.AgentToolResult{Content: ai.ToolResultContent{
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
