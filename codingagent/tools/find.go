package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
	"github.com/OrdalieTech/pi-go/internal/truncate"
)

const defaultFindLimit = 1000

var findSchema = jsonschema.Schema(`{"type":"object","required":["pattern"],"properties":{"pattern":{"type":"string","description":"Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'"},"path":{"type":"string","description":"Directory to search in (default: current directory)"},"limit":{"type":"number","description":"Maximum number of results (default: 1000)"}}}`)

type FindToolInput struct {
	Pattern string   `json:"pattern"`
	Path    *string  `json:"path,omitempty"`
	Limit   *float64 `json:"limit,omitempty"`
}

type FindToolDetails struct {
	ResultLimitReached *float64         `json:"resultLimitReached,omitempty"`
	Truncation         *truncate.Result `json:"truncation,omitempty"`
}

type FindGlobOptions struct {
	Ignore []string
	Limit  float64
}

// FindOperations is the delegation seam for remote file discovery.
type FindOperations interface {
	Exists(context.Context, string) (bool, error)
	Glob(context.Context, string, string, FindGlobOptions) ([]string, error)
}

type FindToolOptions struct {
	Operations FindOperations
}

type findTool struct {
	cwd        string
	operations FindOperations
}

func NewFindTool(cwd string, options *FindToolOptions) agent.AgentTool {
	var operations FindOperations
	if options != nil {
		operations = options.Operations
	}
	return &findTool{cwd: cwd, operations: operations}
}

func (tool *findTool) Spec() agent.AgentToolSpec {
	return agent.AgentToolSpec{
		Name:        "find",
		Label:       "find",
		Description: fmt.Sprintf("Search for files by glob pattern. Returns matching file paths relative to the search directory. Respects .gitignore. Output is truncated to %d results or %dKB (whichever is hit first).", defaultFindLimit, truncate.DefaultMaxBytes/1024),
		Parameters:  findSchema,
	}
}

func (tool *findTool) Execute(
	ctx context.Context,
	_ string,
	params any,
	_ agent.AgentToolUpdateCallback,
) (agent.AgentToolResult, error) {
	input, err := findInput(params)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if err := checkAborted(ctx); err != nil {
		return agent.AgentToolResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	searchPath := "."
	if input.Path != nil && *input.Path != "" {
		searchPath = *input.Path
	}
	searchPath, err = ResolveToCwd(searchPath, tool.cwd)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	effectiveLimit := float64(defaultFindLimit)
	if input.Limit != nil {
		effectiveLimit = *input.Limit
	}
	if tool.operations != nil {
		return tool.executeCustom(ctx, input.Pattern, searchPath, effectiveLimit)
	}
	return tool.executeFD(ctx, input.Pattern, searchPath, effectiveLimit)
}

func (tool *findTool) executeCustom(
	ctx context.Context,
	pattern string,
	searchPath string,
	effectiveLimit float64,
) (agent.AgentToolResult, error) {
	exists, err := runCancelable(ctx, func() (bool, error) {
		return tool.operations.Exists(ctx, searchPath)
	})
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if !exists {
		return agent.AgentToolResult{}, upstreamToolErrorf("Path not found: %s", searchPath)
	}
	if err := checkAborted(ctx); err != nil {
		return agent.AgentToolResult{}, err
	}
	results, err := runCancelable(ctx, func() ([]string, error) {
		return tool.operations.Glob(ctx, pattern, searchPath, FindGlobOptions{
			Ignore: []string{"**/node_modules/**", "**/.git/**"},
			Limit:  effectiveLimit,
		})
	})
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if err := checkAborted(ctx); err != nil {
		return agent.AgentToolResult{}, err
	}
	if len(results) == 0 {
		return textToolResult("No files found matching pattern", nil), nil
	}
	relativized := make([]string, len(results))
	for index, path := range results {
		if strings.HasPrefix(path, searchPath) {
			start := min(len(path), len(searchPath)+1)
			relativized[index] = filepath.ToSlash(path[start:])
			continue
		}
		relative, relErr := filepath.Rel(searchPath, path)
		if relErr != nil {
			return agent.AgentToolResult{}, relErr
		}
		relativized[index] = filepath.ToSlash(relative)
	}
	return formatFindResult(relativized, effectiveLimit, false), nil
}

func (tool *findTool) executeFD(
	ctx context.Context,
	pattern string,
	searchPath string,
	effectiveLimit float64,
) (agent.AgentToolResult, error) {
	fdPath := ensureManagedTool(ctx, managedFD)
	if err := checkAborted(ctx); err != nil {
		return agent.AgentToolResult{}, err
	}
	if fdPath == "" {
		return agent.AgentToolResult{}, upstreamToolError("fd is not available and could not be downloaded")
	}
	args := []string{"--glob", "--color=never", "--hidden"}
	if !insideGitRepository(searchPath) {
		args = append(args, "--no-require-git")
	}
	args = append(args, "--max-results", formatSearchNumber(effectiveLimit))
	effectivePattern := pattern
	if strings.Contains(pattern, "/") {
		args = append(args, "--full-path")
		if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**/") && pattern != "**" {
			effectivePattern = "**/" + pattern
		}
	}
	args = append(args, "--", effectivePattern, searchPath)

	command := exec.CommandContext(ctx, fdPath, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return agent.AgentToolResult{}, upstreamToolErrorf("Failed to run fd: %s", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return agent.AgentToolResult{}, upstreamToolErrorf("Failed to run fd: %s", err)
	}
	lines := make([]string, 0)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	scanErr := scanner.Err()
	waitErr := command.Wait()
	if err := checkAborted(ctx); err != nil {
		return agent.AgentToolResult{}, err
	}
	if scanErr != nil {
		return agent.AgentToolResult{}, scanErr
	}
	if waitErr != nil && len(lines) == 0 {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			var exitError *exec.ExitError
			if errors.As(waitErr, &exitError) {
				message = fmt.Sprintf("fd exited with code %d", exitError.ExitCode())
			} else {
				message = waitErr.Error()
			}
		}
		return agent.AgentToolResult{}, upstreamToolError(message)
	}
	if len(lines) == 0 {
		return textToolResult("No files found matching pattern", nil), nil
	}
	relativized := make([]string, 0, len(lines))
	for _, rawLine := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" {
			continue
		}
		hadTrailingSlash := strings.HasSuffix(line, "/") || strings.HasSuffix(line, "\\")
		var relativePath string
		if strings.HasPrefix(line, searchPath) {
			start := min(len(line), len(searchPath)+1)
			relativePath = line[start:]
		} else {
			relativePath, err = filepath.Rel(searchPath, line)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
		}
		if hadTrailingSlash && !strings.HasSuffix(relativePath, "/") {
			relativePath += "/"
		}
		relativized = append(relativized, filepath.ToSlash(relativePath))
	}
	if len(relativized) == 0 {
		return textToolResult("No files found matching pattern", nil), nil
	}
	return formatFindResult(relativized, effectiveLimit, true), nil
}

func insideGitRepository(searchPath string) bool {
	for current := searchPath; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
	}
}

func formatFindResult(paths []string, effectiveLimit float64, actionable bool) agent.AgentToolResult {
	resultLimitReached := float64(len(paths)) >= effectiveLimit
	rawOutput := strings.Join(paths, "\n")
	truncation := truncate.TruncateHead(rawOutput, truncate.Options{MaxLines: truncate.Int(9007199254740991)})
	output := truncation.Content
	details := FindToolDetails{}
	notices := make([]string, 0, 2)
	if resultLimitReached {
		limit := effectiveLimit
		details.ResultLimitReached = &limit
		notice := fmt.Sprintf("%s results limit reached", formatSearchNumber(limit))
		if actionable {
			notice += fmt.Sprintf(". Use limit=%s for more, or refine pattern", formatSearchNumber(limit*2))
		}
		notices = append(notices, notice)
	}
	if truncation.Truncated {
		details.Truncation = &truncation
		notices = append(notices, fmt.Sprintf("%s limit reached", truncate.FormatSize(truncate.DefaultMaxBytes)))
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	var resultDetails any
	if details.ResultLimitReached != nil || details.Truncation != nil {
		resultDetails = details
	}
	return textToolResult(output, resultDetails)
}

func findInput(params any) (FindToolInput, error) {
	object, err := toolParams(params)
	if err != nil {
		return FindToolInput{}, err
	}
	pattern, err := requiredString(object, "pattern")
	if err != nil {
		return FindToolInput{}, err
	}
	path, err := optionalString(object, "path")
	if err != nil {
		return FindToolInput{}, err
	}
	limit, err := optionalNumber(object, "limit")
	if err != nil {
		return FindToolInput{}, err
	}
	return FindToolInput{Pattern: pattern, Path: path, Limit: limit}, nil
}

func (tool *findTool) RenderCall(args any) string {
	object := renderArgs(args)
	pattern, _ := object["pattern"].(string)
	path := renderPath(object)
	if path == "" {
		path = "."
	}
	text := "find " + pattern + " in " + path
	if limit, err := optionalNumber(object, "limit"); err == nil && limit != nil {
		text += " (limit " + formatSearchNumber(*limit) + ")"
	}
	return text
}

func (*findTool) RenderResult(result agent.AgentToolResult) string {
	return renderTextResult(result)
}

var _ PlainTextRenderer = (*findTool)(nil)
