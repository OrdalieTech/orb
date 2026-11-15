package tools

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/jsonschema"
	"github.com/OrdalieTech/orb/internal/truncate"
	"github.com/bmatcuk/doublestar/v4"
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

func NewFindTool(cwd string, options *FindToolOptions) engine.AgentTool {
	var operations FindOperations
	if options != nil {
		operations = options.Operations
	}
	return &findTool{cwd: cwd, operations: operations}
}

var findDescription = fmt.Sprintf("Search for files by glob pattern. Returns matching file paths relative to the search directory. Respects .gitignore. Output is truncated to %d results or %dKB (whichever is hit first).", defaultFindLimit, truncate.DefaultMaxBytes/1024)

func (tool *findTool) Spec() engine.AgentToolSpec {
	return engine.AgentToolSpec{
		Name:        "find",
		Label:       "find",
		Description: findDescription,
		Parameters:  findSchema,
	}
}

func (tool *findTool) Execute(
	ctx context.Context,
	_ string,
	params any,
	_ engine.AgentToolUpdateCallback,
) (engine.AgentToolResult, error) {
	input, err := findInput(params)
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	if err := checkAborted(ctx); err != nil {
		return engine.AgentToolResult{}, err
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
		return engine.AgentToolResult{}, err
	}
	effectiveLimit := defaultFindLimit
	if input.Limit != nil {
		// fd treats --max-results 0 as unlimited, so a limit below 1 means 1.
		effectiveLimit = int(math.Min(math.Max(1, math.Floor(*input.Limit)), truncate.MaxSafeInteger-1))
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
	effectiveLimit int,
) (engine.AgentToolResult, error) {
	exists, err := runCancelable(ctx, func() (bool, error) {
		return tool.operations.Exists(ctx, searchPath)
	})
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	if !exists {
		return engine.AgentToolResult{}, upstreamToolErrorf("Path not found: %s", searchPath)
	}
	if err := checkAborted(ctx); err != nil {
		return engine.AgentToolResult{}, err
	}
	// One result past the limit tells "exactly N matches" apart from "more than N".
	results, err := runCancelable(ctx, func() ([]string, error) {
		return tool.operations.Glob(ctx, pattern, searchPath, FindGlobOptions{
			Ignore: []string{"**/node_modules/**", "**/.git/**"},
			Limit:  float64(effectiveLimit + 1),
		})
	})
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	if err := checkAborted(ctx); err != nil {
		return engine.AgentToolResult{}, err
	}
	if len(results) == 0 {
		return textToolResult("No files found matching pattern", nil), nil
	}
	limitReached := len(results) > effectiveLimit
	relativized := make([]string, min(len(results), effectiveLimit))
	for index := range relativized {
		relativized[index] = relativizeFindResultPath(results[index], searchPath)
	}
	truncation := truncate.TruncateHead(strings.Join(relativized, "\n"), truncate.Options{MaxLines: truncate.Int(truncate.MaxSafeInteger)})
	return formatFindResult(truncation, effectiveLimit, limitReached, false), nil
}

// relativizeFindResultPath mirrors upstream relativizeFindResultPath: absolute
// results become relative to the search root (path.relative keeps a target on
// another win32 drive absolute), separators become "/", and a trailing
// separator survives.
func relativizeFindResultPath(resultPath, searchPath string) string {
	separator := string(filepath.Separator)
	hadTrailingSeparator := strings.HasSuffix(resultPath, separator) || (filepath.Separator == '\\' && strings.HasSuffix(resultPath, "/"))
	relativePath := resultPath
	if filepath.IsAbs(resultPath) {
		relative, err := filepath.Rel(searchPath, resultPath)
		switch {
		case err != nil:
			relativePath = filepath.Clean(resultPath)
		case relative == ".":
			relativePath = ""
		default:
			relativePath = relative
		}
	}
	posixPath := strings.ReplaceAll(relativePath, separator, "/")
	if hadTrailingSeparator && !strings.HasSuffix(posixPath, "/") {
		posixPath += "/"
	}
	return posixPath
}

func formatFindResult(truncation truncate.Result, effectiveLimit int, resultLimitReached, actionable bool) engine.AgentToolResult {
	output := truncation.Content
	details := FindToolDetails{}
	notices := make([]string, 0, 2)
	if resultLimitReached {
		limit := float64(effectiveLimit)
		details.ResultLimitReached = &limit
		notice := fmt.Sprintf("%d results limit reached", effectiveLimit)
		if actionable {
			notice += fmt.Sprintf(". Use limit=%d for more, or refine pattern", effectiveLimit*2)
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

// findPathMatcher matches fd's glob behavior for a path pattern: '*' matches dotfiles, an
// all-lowercase pattern matches case-insensitively (smart case), and 'src/**' does not match
// 'src' itself.
func findPathMatcher(pattern string) func(string) bool {
	if strings.HasSuffix(pattern, "/**") {
		pattern += "/*"
	}
	foldCase := pattern == strings.ToLower(pattern)
	return func(path string) bool {
		if foldCase {
			path = strings.ToLower(path)
		}
		matched, _ := doublestar.Match(pattern, path)
		return matched
	}
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
	text := "find " + pattern + " in " + ShortenPath(path)
	if limit, err := optionalNumber(object, "limit"); err == nil && limit != nil {
		text += " (limit " + formatSearchNumber(*limit) + ")"
	}
	return text
}

func (*findTool) RenderResult(result engine.AgentToolResult) string {
	return renderTextResult(result)
}

var _ PlainTextRenderer = (*findTool)(nil)
