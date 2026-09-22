package main

import (
	"context"
	"errors"
	"fmt"

	_ "github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

type documents map[string]string

func (d documents) ReadFile(_ context.Context, path string) ([]byte, error) {
	return []byte(d[path]), nil
}
func (d documents) WriteFile(_ context.Context, path, text string) error { d[path] = text; return nil }
func (d documents) Access(context.Context, string) error                 { return nil }
func (d documents) MkdirAll(context.Context, string) error               { return nil }

type shell struct{}

func (shell) Exec(_ context.Context, command, _ string, opts tools.BashExecOptions) (tools.BashExecResult, error) {
	opts.OnData([]byte(command))
	code := 0
	return tools.BashExecResult{ExitCode: &code}, nil
}

func run(tool engine.AgentTool, params any) engine.AgentToolResult {
	result, err := tool.Execute(context.Background(), "test", params, nil)
	if err != nil {
		panic(err)
	}
	return result
}

func main() {
	docs := documents{}
	run(tools.NewWriteTool("/workspace", &tools.WriteToolOptions{Operations: docs}), map[string]any{"path": "note", "content": "hello"})
	run(tools.NewEditTool("/workspace", &tools.EditToolOptions{Operations: docs}), map[string]any{"path": "note", "edits": []any{map[string]any{"oldText": "hello", "newText": "portable"}}})
	result := run(tools.NewReadTool("/workspace", &tools.ReadToolOptions{Operations: docs}), map[string]any{"path": "note"})
	if text := result.Content[0].(*ai.TextContent).Text; text != "portable" {
		panic(text)
	}
	run(tools.NewBashTool("/workspace", &tools.BashToolOptions{Operations: shell{}}), map[string]any{"command": "host tool"})
	for _, tool := range []engine.AgentTool{tools.NewBashTool("/workspace", nil), tools.NewGrepTool("/workspace", nil), tools.NewFindTool("/workspace", nil)} {
		_, err := tool.Execute(context.Background(), "test", map[string]any{"command": "echo forbidden", "pattern": "*"}, nil)
		if !errors.Is(err, errors.ErrUnsupported) {
			panic(fmt.Sprintf("%s: %v", tool.Spec().Name, err))
		}
	}
	fmt.Println("portable tools OK")
}
