package memory

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestToolsOverMemoryFileSystem(t *testing.T) {
	fsys := New(Options{Root: "/workspace", MaxFiles: 2})
	options := tools.FileSystemToolsOptions(fsys)
	cwd := fsys.WorkingDirectory()
	run := func(tool engine.AgentTool, params map[string]any) (string, error) {
		t.Helper()
		result, err := tool.Execute(t.Context(), "call", params, nil)
		return ai.ContentText(result.Content), err
	}
	read, write, edit := tools.NewReadTool(cwd, options.Read), tools.NewWriteTool(cwd, options.Write), tools.NewEditTool(cwd, options.Edit)
	ls, find := tools.NewLsTool(cwd, options.Ls), tools.NewFindTool(cwd, options.Find)

	for _, step := range []struct {
		tool   engine.AgentTool
		params map[string]any
		want   string
	}{
		{write, map[string]any{"path": "notes/a.txt", "content": "alpha\nbeta"}, "Successfully wrote to notes/a.txt"},
		{edit, map[string]any{"path": "notes/a.txt", "edits": []any{map[string]any{"oldText": "beta", "newText": "gamma"}}}, "Successfully replaced 1 block(s) in notes/a.txt."},
		{read, map[string]any{"path": "notes/a.txt"}, "alpha\ngamma"},
		{read, map[string]any{"path": "/workspace/notes/a.txt", "offset": 2}, "gamma"},
		{ls, map[string]any{}, "notes/"},
		{ls, map[string]any{"path": "notes"}, "a.txt"},
		{find, map[string]any{"pattern": "*.txt"}, "notes/a.txt"},
		{find, map[string]any{"pattern": "notes/*"}, "notes/a.txt"},
	} {
		if got, err := run(step.tool, step.params); err != nil || got != step.want {
			t.Fatalf("%s %v = %q, %v; want %q", step.tool.Spec().Name, step.params, got, err, step.want)
		}
	}
	if got := fsys.Snapshot(); len(got) != 1 || got["/workspace/notes/a.txt"] != "alpha\ngamma" {
		t.Fatalf("snapshot = %v", got)
	}

	for _, failure := range []struct {
		tool   engine.AgentTool
		params map[string]any
		want   string
	}{
		{read, map[string]any{"path": "missing.txt"}, "ENOENT: no such file or directory, access '"},
		{read, map[string]any{"path": "notes"}, "EISDIR: illegal operation on a directory, read"},
		{read, map[string]any{"path": "notes/a.txt/child"}, "ENOTDIR: not a directory, access '"},
		{read, map[string]any{"path": "/etc/passwd"}, "EACCES: permission denied, access '"},
		{write, map[string]any{"path": "../escape.txt", "content": "x"}, "EACCES: permission denied, "},
		{write, map[string]any{"path": "notes", "content": "x"}, "EISDIR: illegal operation on a directory, open '"},
		{write, map[string]any{"path": "notes/a.txt/child", "content": "x"}, "EEXIST: file already exists, mkdir '"},
		{edit, map[string]any{"path": "missing.txt", "edits": []any{map[string]any{"oldText": "a", "newText": "b"}}}, "Could not edit file: missing.txt. Error code: ENOENT."},
		{ls, map[string]any{"path": "notes/a.txt"}, "Not a directory: "},
		{find, map[string]any{"pattern": "*", "path": "missing"}, "Path not found: "},
	} {
		if _, err := run(failure.tool, failure.params); err == nil || !strings.HasPrefix(err.Error(), failure.want) {
			t.Fatalf("%s %v error = %v, want prefix %q", failure.tool.Spec().Name, failure.params, err, failure.want)
		}
	}
	if _, err := run(write, map[string]any{"path": "b.txt", "content": "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(write, map[string]any{"path": "c.txt", "content": "c"}); err == nil || !strings.Contains(err.Error(), ErrLimitExceeded.Error()) {
		t.Fatalf("limit error = %v", err)
	}
}
