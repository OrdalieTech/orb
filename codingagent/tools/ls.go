package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
	"github.com/OrdalieTech/pi-go/internal/truncate"
	"golang.org/x/text/cases"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const defaultLsLimit = 500

var lsSchema = jsonschema.Schema(`{"type":"object","properties":{"path":{"type":"string","description":"Directory to list (default: current directory)"},"limit":{"type":"number","description":"Maximum number of entries to return (default: 500)"}}}`)

type LsToolInput struct {
	Path  *string  `json:"path,omitempty"`
	Limit *float64 `json:"limit,omitempty"`
}

type LsToolDetails struct {
	EntryLimitReached *float64         `json:"entryLimitReached,omitempty"`
	Truncation        *truncate.Result `json:"truncation,omitempty"`
}

type LsPathStat struct {
	Directory bool
}

func (stat LsPathStat) IsDirectory() bool {
	return stat.Directory
}

// LsOperations is the delegation seam for the ls tool.
type LsOperations interface {
	Exists(context.Context, string) (bool, error)
	Stat(context.Context, string) (LsPathStat, error)
	ReadDir(context.Context, string) ([]string, error)
}

type LsToolOptions struct {
	Operations LsOperations
}

type lsTool struct {
	cwd        string
	operations LsOperations
}

type localLsOperations struct{}

func (localLsOperations) Exists(_ context.Context, path string) (bool, error) {
	_, err := os.Stat(path)
	return err == nil, nil
}

func (localLsOperations) Stat(_ context.Context, path string) (LsPathStat, error) {
	info, err := os.Stat(path)
	if err != nil {
		return LsPathStat{}, asNodeFilesystemError("stat", path, err)
	}
	return LsPathStat{Directory: info.IsDir()}, nil
}

func (localLsOperations) ReadDir(_ context.Context, path string) ([]string, error) {
	directory, err := os.Open(path)
	if err != nil {
		return nil, asNodeFilesystemErrorAt("scandir", path, err)
	}
	names, readErr := directory.Readdirnames(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return nil, asNodeFilesystemErrorAt("scandir", path, readErr)
	}
	if closeErr != nil {
		return nil, asNodeFilesystemErrorAt("scandir", path, closeErr)
	}
	for index := range names {
		names[index] = decodeNodeUTF8([]byte(names[index]))
	}
	return names, nil
}

func NewLsTool(cwd string, options *LsToolOptions) agent.AgentTool {
	operations := LsOperations(localLsOperations{})
	if options != nil && options.Operations != nil {
		operations = options.Operations
	}
	return &lsTool{cwd: cwd, operations: operations}
}

func (tool *lsTool) Spec() agent.AgentToolSpec {
	return agent.AgentToolSpec{
		Name:        "ls",
		Label:       "ls",
		Description: fmt.Sprintf("List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. Includes dotfiles. Output is truncated to %d entries or %dKB (whichever is hit first).", defaultLsLimit, truncate.DefaultMaxBytes/1024),
		Parameters:  lsSchema,
	}
}

func (tool *lsTool) Execute(
	ctx context.Context,
	_ string,
	params any,
	_ agent.AgentToolUpdateCallback,
) (agent.AgentToolResult, error) {
	input, err := lsInput(params)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	return runCancelable(ctx, func() (agent.AgentToolResult, error) {
		operationContext := context.Background()
		if ctx != nil {
			operationContext = context.WithoutCancel(ctx)
		}
		return tool.execute(operationContext, input)
	})
}

func (tool *lsTool) execute(ctx context.Context, input LsToolInput) (agent.AgentToolResult, error) {
	path := "."
	if input.Path != nil && *input.Path != "" {
		path = *input.Path
	}
	directoryPath, err := ResolveToCwd(path, tool.cwd)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	effectiveLimit := float64(defaultLsLimit)
	if input.Limit != nil {
		effectiveLimit = *input.Limit
	}

	exists, err := tool.operations.Exists(ctx, directoryPath)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if !exists {
		return agent.AgentToolResult{}, upstreamToolErrorf("Path not found: %s", directoryPath)
	}
	stat, err := tool.operations.Stat(ctx, directoryPath)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if !stat.IsDirectory() {
		return agent.AgentToolResult{}, upstreamToolErrorf("Not a directory: %s", directoryPath)
	}
	entries, err := tool.operations.ReadDir(ctx, directoryPath)
	if err != nil {
		return agent.AgentToolResult{}, upstreamToolErrorf("Cannot read directory: %s", err)
	}
	collator := collate.New(defaultCollationLanguage())
	lower := cases.Lower(language.Und)
	sort.SliceStable(entries, func(left, right int) bool {
		return collator.CompareString(lower.String(entries[left]), lower.String(entries[right])) < 0
	})

	capacity := len(entries)
	if effectiveLimit <= 0 {
		capacity = 0
	} else if effectiveLimit < float64(capacity) {
		capacity = int(math.Ceil(effectiveLimit))
	}
	results := make([]string, 0, capacity)
	entryLimitReached := false
	for _, entry := range entries {
		if float64(len(results)) >= effectiveLimit {
			entryLimitReached = true
			break
		}
		fullPath := filepath.Join(directoryPath, entry)
		entryStat, statErr := tool.operations.Stat(ctx, fullPath)
		if statErr != nil {
			continue
		}
		if entryStat.IsDirectory() {
			entry += "/"
		}
		results = append(results, entry)
	}
	if len(results) == 0 {
		return agent.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "(empty directory)"}}}, nil
	}

	rawOutput := strings.Join(results, "\n")
	truncation := truncate.TruncateHead(rawOutput, truncate.Options{MaxLines: truncate.Int(9007199254740991)})
	output := truncation.Content
	details := LsToolDetails{}
	var notices []string
	if entryLimitReached {
		limit := effectiveLimit
		details.EntryLimitReached = &limit
		notices = append(notices, fmt.Sprintf("%s entries limit reached. Use limit=%s for more", formatJSNumber(effectiveLimit), formatJSNumber(effectiveLimit*2)))
	}
	if truncation.Truncated {
		details.Truncation = &truncation
		notices = append(notices, fmt.Sprintf("%s limit reached", truncate.FormatSize(truncate.DefaultMaxBytes)))
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	var resultDetails any
	if details.EntryLimitReached != nil || details.Truncation != nil {
		resultDetails = details
	}
	return agent.AgentToolResult{
		Content: ai.ToolResultContent{&ai.TextContent{Text: output}},
		Details: resultDetails,
	}, nil
}

func defaultCollationLanguage() language.Tag {
	locale := ""
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(name); value != "" {
			locale = value
			break
		}
	}
	locale = strings.SplitN(locale, ".", 2)[0]
	locale = strings.SplitN(locale, "@", 2)[0]
	if locale == "" || locale == "C" || locale == "POSIX" {
		return language.AmericanEnglish
	}
	tag, err := language.Parse(strings.ReplaceAll(locale, "_", "-"))
	if err != nil {
		return language.AmericanEnglish
	}
	return tag
}

func lsInput(params any) (LsToolInput, error) {
	object, err := toolParams(params)
	if err != nil {
		return LsToolInput{}, err
	}
	path, err := optionalString(object, "path")
	if err != nil {
		return LsToolInput{}, err
	}
	limit, err := optionalNumber(object, "limit")
	if err != nil {
		return LsToolInput{}, err
	}
	input := LsToolInput{Path: path}
	if limit != nil {
		input.Limit = limit
	}
	return input, nil
}
