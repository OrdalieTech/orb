package tools

import (
	"context"
	"fmt"
	"math"
	"os"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/jsonschema"
	"github.com/OrdalieTech/orb/internal/truncate"
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

func NewGrepTool(cwd string, options *GrepToolOptions) engine.AgentTool {
	operations := GrepOperations(localGrepOperations{})
	if options != nil && options.Operations != nil {
		operations = options.Operations
	}
	return &grepTool{cwd: cwd, operations: operations}
}

func (tool *grepTool) Spec() engine.AgentToolSpec {
	return engine.AgentToolSpec{
		Name:        "grep",
		Label:       "grep",
		Description: fmt.Sprintf("Search file contents for a pattern. Returns matching lines with file paths and line numbers. Respects .gitignore. Output is truncated to %d matches or %dKB (whichever is hit first). Long lines are truncated to %d chars.", defaultGrepLimit, truncate.DefaultMaxBytes/1024, truncate.GrepMaxLineLength),
		Parameters:  grepSchema,
	}
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

func textToolResult(text string, details any) engine.AgentToolResult {
	return engine.AgentToolResult{
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

func (*grepTool) RenderResult(result engine.AgentToolResult) string {
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
