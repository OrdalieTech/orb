package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
)

func portFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"file.txt":        "one\ntwo\nthree\n",
		"dir/nested.txt":  "nested",
		"dir/sub/leaf.md": "leaf",
		".hidden":         "h",
		"img.png":         string(mustBase64(t, tinyPNG)),
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func summarizeToolRun(root string, result engine.AgentToolResult, err error) string {
	var parts []string
	for _, block := range result.Content {
		switch block := block.(type) {
		case *ai.TextContent:
			parts = append(parts, "text:"+block.Text)
		case *ai.ImageContent:
			parts = append(parts, "image:"+block.MimeType)
		}
	}
	details, _ := json.Marshal(result.Details)
	parts = append(parts, "details:"+string(details), fmt.Sprintf("error:%v", err))
	return strings.ReplaceAll(strings.Join(parts, "\n"), root, "<root>")
}

// The port adapter must be indistinguishable from the native operations.
func TestFileSystemToolsOptionsMatchNativeOperations(t *testing.T) {
	type build func(cwd string, options *ToolsOptions) engine.AgentTool
	read := func(cwd string, options *ToolsOptions) engine.AgentTool { return NewReadTool(cwd, options.Read) }
	write := func(cwd string, options *ToolsOptions) engine.AgentTool { return NewWriteTool(cwd, options.Write) }
	edit := func(cwd string, options *ToolsOptions) engine.AgentTool { return NewEditTool(cwd, options.Edit) }
	ls := func(cwd string, options *ToolsOptions) engine.AgentTool { return NewLsTool(cwd, options.Ls) }
	for _, tc := range []struct {
		name   string
		tool   build
		params map[string]any
	}{
		{"read", read, map[string]any{"path": "file.txt"}},
		{"read range", read, map[string]any{"path": "file.txt", "offset": 2, "limit": 1}},
		{"read image", read, map[string]any{"path": "img.png"}},
		{"read missing", read, map[string]any{"path": "missing.txt"}},
		{"read directory", read, map[string]any{"path": "dir"}},
		{"read beneath file", read, map[string]any{"path": "file.txt/child"}},
		{"read null byte", read, map[string]any{"path": "bad\x00name"}},
		{"write nested", write, map[string]any{"path": "new/deep/out.txt", "content": "fresh"}},
		{"write overwrite", write, map[string]any{"path": "file.txt", "content": "replaced"}},
		{"write directory", write, map[string]any{"path": "dir", "content": "x"}},
		{"write beneath file", write, map[string]any{"path": "file.txt/child", "content": "x"}},
		{"write deep beneath file", write, map[string]any{"path": "file.txt/sub/child", "content": "x"}},
		{"edit", edit, map[string]any{"path": "file.txt", "edits": []any{map[string]any{"oldText": "two", "newText": "2"}}}},
		{"edit missing text", edit, map[string]any{"path": "file.txt", "edits": []any{map[string]any{"oldText": "absent", "newText": "x"}}}},
		{"edit missing file", edit, map[string]any{"path": "missing.txt", "edits": []any{map[string]any{"oldText": "a", "newText": "b"}}}},
		{"edit directory", edit, map[string]any{"path": "dir", "edits": []any{map[string]any{"oldText": "a", "newText": "b"}}}},
		{"ls root", ls, map[string]any{}},
		{"ls nested", ls, map[string]any{"path": "dir"}},
		{"ls limit", ls, map[string]any{"limit": 2}},
		{"ls missing", ls, map[string]any{"path": "missing"}},
		{"ls file", ls, map[string]any{"path": "file.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nativeRoot, portRoot := portFixture(t), portFixture(t)
			nativeResult, nativeErr := tc.tool(nativeRoot, &ToolsOptions{}).Execute(t.Context(), "native", tc.params, nil)
			portOptions := FileSystemToolsOptions(&harness.NodeExecutionEnv{CWD: portRoot})
			portResult, portErr := tc.tool(portRoot, portOptions).Execute(t.Context(), "port", tc.params, nil)
			native, port := summarizeToolRun(nativeRoot, nativeResult, nativeErr), summarizeToolRun(portRoot, portResult, portErr)
			if native != port {
				t.Fatalf("port run diverged from native\nnative: %s\nport:   %s", native, port)
			}
			if nativeTree, portTree := treeSnapshot(t, nativeRoot), treeSnapshot(t, portRoot); !slices.Equal(nativeTree, portTree) {
				t.Fatalf("file trees diverged\nnative: %q\nport:   %q", nativeTree, portTree)
			}
		})
	}
}

func treeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		if entry.IsDir() {
			entries = append(entries, relative+"/")
			return nil
		}
		content, err := os.ReadFile(path)
		entries = append(entries, relative+"="+string(content))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestFileSystemToolsOptionsGlobFollowsFDConventions(t *testing.T) {
	root := portFixture(t)
	for _, name := range []string{"node_modules/pkg/index.js", ".git/config", "dir/UPPER.TXT"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	find := NewFindTool(root, FileSystemToolsOptions(&harness.NodeExecutionEnv{CWD: root}).Find)
	for _, tc := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"pattern": "*.txt"}, "dir/UPPER.TXT\ndir/nested.txt\nfile.txt"},
		{map[string]any{"pattern": "*.TXT"}, "dir/UPPER.TXT"},
		{map[string]any{"pattern": "sub/*.md"}, "dir/sub/leaf.md"},
		{map[string]any{"pattern": "**/*.md"}, "dir/sub/leaf.md"},
		{map[string]any{"pattern": "su*"}, "dir/sub/"},
		{map[string]any{"pattern": ".*"}, ".hidden"},
		{map[string]any{"pattern": "*.js"}, "No files found matching pattern"},
		{map[string]any{"pattern": "*.md", "path": "dir"}, "sub/leaf.md"},
		{map[string]any{"pattern": "*", "limit": 2}, ".hidden\ndir/\n\n[2 results limit reached]"},
	} {
		result, err := find.Execute(context.Background(), "find", tc.params, nil)
		if err != nil {
			t.Fatalf("%v: %v", tc.params, err)
		}
		if got := toolResultText(t, result); got != tc.want {
			t.Fatalf("%v = %q, want %q", tc.params, got, tc.want)
		}
	}
	if _, err := find.Execute(context.Background(), "find", map[string]any{"pattern": "*", "path": "missing"}, nil); err == nil || !strings.HasPrefix(err.Error(), "Path not found: ") {
		t.Fatalf("missing search path error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := find.Execute(cancelled, "find", map[string]any{"pattern": "*"}, nil); err == nil || err.Error() != "Operation aborted" {
		t.Fatalf("cancelled find error = %v", err)
	}
}
