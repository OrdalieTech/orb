//go:build !wasm

package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/truncate"
)

func (tool *grepTool) Execute(
	ctx context.Context,
	_ string,
	params any,
	_ engine.AgentToolUpdateCallback,
) (engine.AgentToolResult, error) {
	input, err := grepInput(params)
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	if err := checkAborted(ctx); err != nil {
		return engine.AgentToolResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	preSpawnContext := context.WithoutCancel(ctx)
	rgPath := ensureManagedTool(preSpawnContext, managedRG)
	if rgPath == "" {
		return engine.AgentToolResult{}, upstreamToolError("ripgrep (rg) is not available and could not be downloaded")
	}
	searchPath := "."
	if input.Path != nil && *input.Path != "" {
		searchPath = *input.Path
	}
	searchPath, err = ResolveToCwd(searchPath, tool.cwd)
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	isDirectory, err := tool.operations.IsDirectory(preSpawnContext, searchPath)
	if err != nil {
		return engine.AgentToolResult{}, upstreamToolErrorf("Path not found: %s", searchPath)
	}

	contextValue := 0.0
	if input.Context != nil && *input.Context > 0 {
		// Clamped so the argument never needs exponent notation, which rg rejects.
		contextValue = math.Min(math.Floor(*input.Context), truncate.MaxSafeInteger)
	}
	effectiveLimit := float64(defaultGrepLimit)
	if input.Limit != nil {
		effectiveLimit = math.Max(1, math.Floor(*input.Limit))
	}
	// --no-config: a RIPGREP_CONFIG_PATH with --fixed-strings would turn every regex literal.
	args := []string{"--no-config", "--json", "--line-number", "--color=never", "--hidden"}
	if input.IgnoreCase != nil && *input.IgnoreCase {
		args = append(args, "--ignore-case")
	}
	if input.Literal != nil && *input.Literal {
		args = append(args, "--fixed-strings")
	}
	if input.Glob != nil && *input.Glob != "" {
		// rg never matches a leading "./"; a leading "!" negates.
		negation, glob := "", *input.Glob
		if strings.HasPrefix(glob, "!") {
			negation, glob = "!", glob[1:]
		}
		for strings.HasPrefix(glob, "./") {
			glob = glob[2:]
		}
		args = append(args, "--glob", negation+glob)
	}
	if contextValue > 0 {
		args = append(args, "--context", strconv.FormatFloat(contextValue, 'f', -1, 64))
	}
	// rg roots --glob at its cwd, so a directory search runs inside searchPath and searches ".":
	// rg canonicalizes its cwd, so a symlinked absolute searchPath would not lie under the glob root.
	target := searchPath
	if isDirectory {
		target = "."
	}
	command := exec.Command(rgPath, append(args, "--", input.Pattern, target)...)
	if isDirectory {
		command.Dir = searchPath
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return engine.AgentToolResult{}, upstreamToolErrorf("Failed to run ripgrep: %s", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return engine.AgentToolResult{}, upstreamToolErrorf("Failed to run ripgrep: %s", err)
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

	formatPath := func(file string) string {
		if isDirectory {
			if !filepath.IsAbs(file) {
				file = filepath.Join(searchPath, file)
			}
			if relative, relErr := filepath.Rel(searchPath, file); relErr == nil && relative != "." && !strings.HasPrefix(relative, "..") {
				return filepath.ToSlash(relative)
			}
		}
		return filepath.Base(file)
	}
	var outputLines []string
	// The full output size, including the lines past the byte cap that are counted but not stored.
	outputBytes, outputLineCount, matchCount := 0, 0, 0
	linesTruncated := false
	lastFile := ""
	pushLine := func(file string, lineNumber float64, text string, isMatch bool) {
		lastFile = file
		// Only the line terminator goes: a lone CR inside a line is content, as in the read tool.
		truncated := truncate.TruncateLine(strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r"))
		linesTruncated = linesTruncated || truncated.WasTruncated
		separator := ":"
		if !isMatch {
			separator = "-"
		}
		line := formatPath(file) + separator + formatSearchNumber(lineNumber) + separator + " " + truncated.Text
		// outputBytes counts a newline after every line; the joined output has one fewer.
		if outputBytes-1 <= truncate.DefaultMaxBytes {
			outputLines = append(outputLines, line)
		}
		outputBytes += len(line) + 1
		outputLineCount++
	}

	reader := bufio.NewReader(stdout)
	// limitHit is set at the last allowed match: rg still sends its trailing context, kept up to
	// trailingContextEnd, and the limit notice waits for proof of one more match.
	limitHit, matchLimitReached, killedDueToLimit := false, false, false
	trailingContextEnd := 0.0
	var stdoutErr error
read:
	for {
		line, readErr := reader.ReadString('\n')
		var event rgEvent
		if json.Unmarshal([]byte(line), &event) == nil && (event.Type == "match" || event.Type == "context") {
			isMatch := event.Type == "match"
			file, lineNumber := event.Data.Path.decode(), event.Data.LineNumber
			switch {
			case file == "" || lineNumber == nil:
			case !limitHit:
				pushLine(file, *lineNumber, event.Data.Lines.decode(), isMatch)
				if isMatch {
					matchCount++
					if limitHit = float64(matchCount) >= effectiveLimit; limitHit {
						trailingContextEnd = *lineNumber + contextValue
					}
				}
			default:
				inWindow := file == lastFile && *lineNumber <= trailingContextEnd
				// A match inside the last match's trailing context shows as context.
				if inWindow {
					pushLine(file, *lineNumber, event.Data.Lines.decode(), false)
				}
				matchLimitReached = matchLimitReached || isMatch
				if matchLimitReached && !(inWindow && *lineNumber < trailingContextEnd) {
					killedDueToLimit = true
					_ = command.Process.Kill()
					break read
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
		return engine.AgentToolResult{}, errOperationAborted
	}
	if stdoutErr != nil {
		return engine.AgentToolResult{}, stdoutErr
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
			return engine.AgentToolResult{}, upstreamToolError(message)
		}
	}
	if matchCount == 0 {
		return textToolResult("No matches found", nil), nil
	}

	truncation := truncate.TruncateHead(strings.Join(outputLines, "\n"), truncate.Options{MaxLines: truncate.Int(truncate.MaxSafeInteger)})
	truncation.TotalLines, truncation.TotalBytes = outputLineCount, outputBytes-1
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

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path       rgText   `json:"path"`
		LineNumber *float64 `json:"line_number"`
		Lines      rgText   `json:"lines"`
	} `json:"data"`
}

// rgText is a ripgrep JSON text field; non-UTF-8 data arrives base64-encoded in bytes.
type rgText struct {
	Text  string `json:"text"`
	Bytes string `json:"bytes"`
}

func (text rgText) decode() string {
	if text.Bytes == "" {
		return text.Text
	}
	decoded, _ := base64.StdEncoding.DecodeString(text.Bytes)
	return decodeNodeUTF8(decoded)
}
