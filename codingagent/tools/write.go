package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
)

var writeSchema = jsonschema.Schema(`{"type":"object","required":["path","content"],"properties":{"path":{"type":"string","description":"Path to the file to write (relative or absolute)"},"content":{"type":"string","description":"Content to write to the file"}}}`)

type WriteToolInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteOperations is the delegation seam for the write tool.
type WriteOperations interface {
	WriteFile(context.Context, string, string) error
	MkdirAll(context.Context, string) error
}

type WriteToolOptions struct {
	Operations WriteOperations
}

type writeTool struct {
	cwd        string
	operations WriteOperations
}

type localWriteOperations struct{}

func (localWriteOperations) WriteFile(_ context.Context, path, content string) error {
	return asNodeFilesystemError("open", path, os.WriteFile(path, []byte(encodeNodeUTF8(content)), 0o666))
}

func (localWriteOperations) MkdirAll(_ context.Context, path string) error {
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return nodeFilesystemError{code: "EEXIST", operation: "mkdir", path: path}
	}
	if danglingPath, exact := danglingSymlinkComponent(path); danglingPath != "" {
		code := "ENOTDIR"
		if exact {
			code = "ENOENT"
		}
		return nodeFilesystemError{code: code, operation: "mkdir", path: danglingPath}
	}
	err := os.MkdirAll(path, 0o777)
	if err != nil {
		if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
			return nodeFilesystemError{code: "EEXIST", operation: "mkdir", path: path}
		}
	}
	return asNodeFilesystemErrorAt("mkdir", path, err)
}

func danglingSymlinkComponent(path string) (string, bool) {
	requested := filepath.Clean(path)
	for current := requested; ; current = filepath.Dir(current) {
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			if _, statErr := os.Stat(current); errors.Is(statErr, os.ErrNotExist) {
				return current, current == requested
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
	}
}

func NewWriteTool(cwd string, options *WriteToolOptions) agent.AgentTool {
	operations := WriteOperations(localWriteOperations{})
	if options != nil && options.Operations != nil {
		operations = options.Operations
	}
	return &writeTool{cwd: cwd, operations: operations}
}

func (tool *writeTool) Spec() agent.AgentToolSpec {
	return agent.AgentToolSpec{
		Name:        "write",
		Label:       "write",
		Description: "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories.",
		Parameters:  writeSchema,
	}
}

func (tool *writeTool) Execute(
	ctx context.Context,
	_ string,
	params any,
	_ agent.AgentToolUpdateCallback,
) (agent.AgentToolResult, error) {
	input, err := writeInput(params)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	absolutePath, err := ResolveToCwd(input.Path, tool.cwd)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	return withFileMutationQueueContext(ctx, absolutePath, func() (agent.AgentToolResult, error) {
		if err := checkAborted(ctx); err != nil {
			return agent.AgentToolResult{}, err
		}
		if err := tool.operations.MkdirAll(ctx, filepath.Dir(absolutePath)); err != nil {
			return agent.AgentToolResult{}, err
		}
		if err := checkAborted(ctx); err != nil {
			return agent.AgentToolResult{}, err
		}
		if err := tool.operations.WriteFile(ctx, absolutePath, input.Content); err != nil {
			return agent.AgentToolResult{}, err
		}
		if err := checkAborted(ctx); err != nil {
			return agent.AgentToolResult{}, err
		}
		return agent.AgentToolResult{
			Content: ai.ToolResultContent{&ai.TextContent{Text: fmt.Sprintf("Successfully wrote %d bytes to %s", javascriptUTF16Length(input.Content), input.Path)}},
		}, nil
	})
}

func (tool *writeTool) PrepareParallelExecution(ctx context.Context, params any) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	input, err := writeInput(params)
	if err != nil {
		return ctx, nil, err
	}
	absolutePath, err := ResolveToCwd(input.Path, tool.cwd)
	if err != nil {
		return ctx, nil, err
	}
	reservation, err := reserveFileMutation(absolutePath)
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, mutationReservationContextKey{}, reservation), reservation.cancel, nil
}

func writeInput(params any) (WriteToolInput, error) {
	object, err := toolParams(params)
	if err != nil {
		return WriteToolInput{}, err
	}
	path, err := requiredString(object, "path")
	if err != nil {
		return WriteToolInput{}, err
	}
	content, err := requiredString(object, "content")
	if err != nil {
		return WriteToolInput{}, err
	}
	return WriteToolInput{Path: path, Content: content}, nil
}
