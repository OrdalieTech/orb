package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
	"github.com/OrdalieTech/pi-go/internal/truncate"
)

const defaultGrepLimit = 100

var grepSchema = jsonschema.Schema(`{"type":"object","required":["pattern"],"properties":{"pattern":{"type":"string","description":"Search pattern (regex or literal string)"},"path":{"type":"string","description":"Directory or file to search (default: current directory)"},"glob":{"type":"string","description":"Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'"},"ignoreCase":{"type":"boolean","description":"Case-insensitive search (default: false)"},"literal":{"type":"boolean","description":"Treat pattern as literal string instead of regex (default: false)"},"context":{"type":"number","description":"Number of lines to show before and after each match (default: 0)"},"limit":{"type":"number","description":"Maximum number of matches to return (default: 100)"}}}`)

type GrepToolInput struct {
	Pattern    string   `json:"pattern"`
	Path       *string  `json:"path,omitempty"`
	Glob       *string  `json:"glob,omitempty"`
	IgnoreCase *bool    `json:"ignoreCase,omitempty"`
	Literal    *bool    `json:"literal,omitempty"`
	Context    *float64 `json:"context,omitempty"`
	Limit      *float64 `json:"limit,omitempty"`
}

type GrepToolDetails struct {
	MatchLimitReached *float64         `json:"matchLimitReached,omitempty"`
	Truncation        *truncate.Result `json:"truncation,omitempty"`
	LinesTruncated    bool             `json:"linesTruncated,omitempty"`
}

// GrepOperations is the delegation seam for grep file metadata and context reads.
type GrepOperations interface {
	IsDirectory(context.Context, string) (bool, error)
	ReadFile(context.Context, string) (string, error)
}

type GrepToolOptions struct {
	Operations GrepOperations
}

type grepTool struct {
	cwd        string
	operations GrepOperations
}

type localGrepOperations struct{}

func (localGrepOperations) IsDirectory(_ context.Context, path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func (localGrepOperations) ReadFile(_ context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return decodeNodeUTF8(data), nil
}

func NewGrepTool(cwd string, options *GrepToolOptions) agent.AgentTool {
	operations := GrepOperations(localGrepOperations{})
	if options != nil && options.Operations != nil {
		operations = options.Operations
	}
	return &grepTool{cwd: cwd, operations: operations}
}

func (tool *grepTool) Spec() agent.AgentToolSpec {
	return agent.AgentToolSpec{
		Name:        "grep",
		Label:       "grep",
		Description: fmt.Sprintf("Search file contents for a pattern. Returns matching lines with file paths and line numbers. Respects .gitignore. Output is truncated to %d matches or %dKB (whichever is hit first). Long lines are truncated to %d chars.", defaultGrepLimit, truncate.DefaultMaxBytes/1024, truncate.GrepMaxLineLength),
		Parameters:  grepSchema,
	}
}

func (tool *grepTool) Execute(
	ctx context.Context,
	_ string,
	params any,
	_ agent.AgentToolUpdateCallback,
) (agent.AgentToolResult, error) {
	input, err := grepInput(params)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if err := checkAborted(ctx); err != nil {
		return agent.AgentToolResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	preSpawnContext := context.WithoutCancel(ctx)
	rgPath := ensureManagedTool(preSpawnContext, managedRG)
	if rgPath == "" {
		return agent.AgentToolResult{}, upstreamToolError("ripgrep (rg) is not available and could not be downloaded")
	}
	searchPath := "."
	if input.Path != nil && *input.Path != "" {
		searchPath = *input.Path
	}
	searchPath, err = ResolveToCwd(searchPath, tool.cwd)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	isDirectory, err := tool.operations.IsDirectory(preSpawnContext, searchPath)
	if err != nil {
		return agent.AgentToolResult{}, upstreamToolErrorf("Path not found: %s", searchPath)
	}

	contextValue := 0.0
	if input.Context != nil && *input.Context > 0 {
		contextValue = *input.Context
	}
	effectiveLimit := float64(defaultGrepLimit)
	if input.Limit != nil {
		effectiveLimit = math.Max(1, *input.Limit)
	}
	args := []string{"--json", "--line-number", "--color=never", "--hidden"}
	if input.IgnoreCase != nil && *input.IgnoreCase {
		args = append(args, "--ignore-case")
	}
	if input.Literal != nil && *input.Literal {
		args = append(args, "--fixed-strings")
	}
	if input.Glob != nil && *input.Glob != "" {
		args = append(args, "--glob", *input.Glob)
	}
	args = append(args, "--", input.Pattern, searchPath)

	command := exec.Command(rgPath, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return agent.AgentToolResult{}, upstreamToolErrorf("Failed to run ripgrep: %s", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return agent.AgentToolResult{}, upstreamToolErrorf("Failed to run ripgrep: %s", err)
	}
	executionDone := make(chan struct{})
	var aborted atomic.Bool
	var cancelWatcherDone chan struct{}
	if ctx.Err() == nil {
		cancelWatcherDone = make(chan struct{})
		go func() {
			defer close(cancelWatcherDone)
			select {
			case <-ctx.Done():
				aborted.Store(true)
				_ = command.Process.Kill()
			case <-executionDone:
			}
		}()
	}

	type rgMatch struct {
		FilePath   string
		LineNumber float64
		LineText   *string
	}
	type rgEvent struct {
		Type string `json:"type"`
		Data struct {
			Path struct {
				Text string `json:"text"`
			} `json:"path"`
			LineNumber *float64 `json:"line_number"`
			Lines      struct {
				Text *string `json:"text"`
			} `json:"lines"`
		} `json:"data"`
	}
	reader := bufio.NewReader(stdout)
	matches := make([]rgMatch, 0, defaultGrepLimit)
	matchCount := 0
	matchLimitReached := false
	killedDueToLimit := false
	var stdoutErr error
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if strings.TrimSpace(line) != "" && !(float64(matchCount) >= effectiveLimit) {
			var event rgEvent
			if json.Unmarshal([]byte(line), &event) == nil && event.Type == "match" {
				matchCount++
				if event.Data.Path.Text != "" && event.Data.LineNumber != nil {
					matches = append(matches, rgMatch{
						FilePath:   event.Data.Path.Text,
						LineNumber: *event.Data.LineNumber,
						LineText:   event.Data.Lines.Text,
					})
				}
				if float64(matchCount) >= effectiveLimit {
					matchLimitReached = true
					killedDueToLimit = true
					_ = command.Process.Kill()
					break
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, os.ErrClosed) && !errors.Is(readErr, context.Canceled) {
				stdoutErr = readErr
			}
			break
		}
	}
	waitErr := command.Wait()
	close(executionDone)
	if cancelWatcherDone != nil {
		<-cancelWatcherDone
	}
	if aborted.Load() {
		return agent.AgentToolResult{}, errOperationAborted
	}
	if stdoutErr != nil {
		return agent.AgentToolResult{}, stdoutErr
	}
	if !killedDueToLimit && waitErr != nil {
		var exitError *exec.ExitError
		if !errors.As(waitErr, &exitError) || exitError.ExitCode() != 1 {
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				if errors.As(waitErr, &exitError) {
					message = fmt.Sprintf("ripgrep exited with code %d", exitError.ExitCode())
				} else {
					message = waitErr.Error()
				}
			}
			return agent.AgentToolResult{}, upstreamToolError(message)
		}
	}
	if matchCount == 0 {
		return textToolResult("No matches found", nil), nil
	}

	linesTruncated := false
	fileCache := make(map[string][]string)
	outputLines := make([]string, 0, len(matches))
	formatPath := func(path string) string {
		if isDirectory {
			relative, relErr := filepath.Rel(searchPath, path)
			if relErr == nil && relative != "" && !strings.HasPrefix(relative, "..") {
				return filepath.ToSlash(relative)
			}
		}
		return filepath.Base(path)
	}
	getFileLines := func(path string) []string {
		if lines, ok := fileCache[path]; ok {
			return lines
		}
		content, readErr := tool.operations.ReadFile(preSpawnContext, path)
		if readErr != nil {
			fileCache[path] = nil
			return nil
		}
		content = strings.ReplaceAll(content, "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
		lines := strings.Split(content, "\n")
		fileCache[path] = lines
		return lines
	}
	appendLine := func(prefix, text string) {
		truncated := truncate.TruncateLine(strings.ReplaceAll(text, "\r", ""))
		linesTruncated = linesTruncated || truncated.WasTruncated
		outputLines = append(outputLines, prefix+truncated.Text)
	}
	for _, match := range matches {
		relativePath := formatPath(match.FilePath)
		if contextValue == 0 && match.LineText != nil {
			lineText := strings.ReplaceAll(*match.LineText, "\r\n", "\n")
			lineText = strings.ReplaceAll(lineText, "\r", "")
			lineText = strings.TrimSuffix(lineText, "\n")
			appendLine(relativePath+":"+formatSearchNumber(match.LineNumber)+": ", lineText)
			continue
		}
		lines := getFileLines(match.FilePath)
		if len(lines) == 0 {
			outputLines = append(outputLines, relativePath+":"+formatSearchNumber(match.LineNumber)+": (unable to read file)")
			continue
		}
		start := math.Max(1, match.LineNumber-contextValue)
		end := math.Min(float64(len(lines)), match.LineNumber+contextValue)
		for current := start; current <= end; current++ {
			lineText := ""
			lineIndex := current - 1
			if lineIndex == math.Trunc(lineIndex) && lineIndex >= 0 && lineIndex < float64(len(lines)) {
				lineText = lines[int(lineIndex)]
			}
			lineNumber := formatSearchNumber(current)
			separator := fmt.Sprintf("%s-%s- ", relativePath, lineNumber)
			if current == match.LineNumber {
				separator = fmt.Sprintf("%s:%s: ", relativePath, lineNumber)
			}
			appendLine(separator, lineText)
		}
	}

	rawOutput := strings.Join(outputLines, "\n")
	truncation := truncate.TruncateHead(rawOutput, truncate.Options{MaxLines: truncate.Int(9007199254740991)})
	output := truncation.Content
	details := GrepToolDetails{}
	notices := make([]string, 0, 3)
	if matchLimitReached {
		limit := effectiveLimit
		details.MatchLimitReached = &limit
		notices = append(notices, fmt.Sprintf("%s matches limit reached. Use limit=%s for more, or refine pattern", formatSearchNumber(limit), formatSearchNumber(limit*2)))
	}
	if truncation.Truncated {
		details.Truncation = &truncation
		notices = append(notices, fmt.Sprintf("%s limit reached", truncate.FormatSize(truncate.DefaultMaxBytes)))
	}
	if linesTruncated {
		details.LinesTruncated = true
		notices = append(notices, fmt.Sprintf("Some lines truncated to %d chars. Use read tool to see full lines", truncate.GrepMaxLineLength))
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	var resultDetails any
	if details.MatchLimitReached != nil || details.Truncation != nil || details.LinesTruncated {
		resultDetails = details
	}
	return textToolResult(output, resultDetails), nil
}

func grepInput(params any) (GrepToolInput, error) {
	object, err := toolParams(params)
	if err != nil {
		return GrepToolInput{}, err
	}
	pattern, err := requiredString(object, "pattern")
	if err != nil {
		return GrepToolInput{}, err
	}
	path, err := optionalString(object, "path")
	if err != nil {
		return GrepToolInput{}, err
	}
	glob, err := optionalString(object, "glob")
	if err != nil {
		return GrepToolInput{}, err
	}
	ignoreCase, err := optionalToolBoolean(object, "ignoreCase")
	if err != nil {
		return GrepToolInput{}, err
	}
	literal, err := optionalToolBoolean(object, "literal")
	if err != nil {
		return GrepToolInput{}, err
	}
	contextLines, err := optionalNumber(object, "context")
	if err != nil {
		return GrepToolInput{}, err
	}
	limit, err := optionalNumber(object, "limit")
	if err != nil {
		return GrepToolInput{}, err
	}
	return GrepToolInput{
		Pattern: pattern, Path: path, Glob: glob, IgnoreCase: ignoreCase,
		Literal: literal, Context: contextLines, Limit: limit,
	}, nil
}

func optionalToolBoolean(object map[string]any, name string) (*bool, error) {
	value, ok := object[name]
	if !ok || value == nil {
		return nil, nil
	}
	boolean, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("invalid tool arguments: %s must be a boolean", name)
	}
	return &boolean, nil
}

func textToolResult(text string, details any) agent.AgentToolResult {
	return agent.AgentToolResult{
		Content: ai.ToolResultContent{&ai.TextContent{Text: text}},
		Details: details,
	}
}

func (tool *grepTool) RenderCall(args any) string {
	object := renderArgs(args)
	pattern, _ := object["pattern"].(string)
	path := renderPath(object)
	if path == "" {
		path = "."
	}
	text := "grep /" + pattern + "/ in " + ShortenPath(path)
	if glob, ok := object["glob"].(string); ok && glob != "" {
		text += " (" + glob + ")"
	}
	if limit, err := optionalNumber(object, "limit"); err == nil && limit != nil {
		text += " limit " + formatSearchNumber(*limit)
	}
	return text
}

func (*grepTool) RenderResult(result agent.AgentToolResult) string {
	return renderTextResult(result)
}

var _ PlainTextRenderer = (*grepTool)(nil)

func formatSearchNumber(value float64) string {
	switch {
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	default:
		return formatJSNumber(value)
	}
}
