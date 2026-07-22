package extensions

import (
	"testing"

	"github.com/OrdalieTech/pigo/codingagent/tools"
	"github.com/OrdalieTech/pigo/internal/truncate"
)

func TestToolCallAccessorsDecodeMatchingEvents(t *testing.T) {
	bash, ok := BashToolCall(ToolCallEvent{ToolName: "bash", Input: map[string]any{"command": "ls -la", "timeout": 5.0}})
	if !ok || bash.Command != "ls -la" || bash.Timeout == nil || *bash.Timeout != 5 {
		t.Fatalf("BashToolCall = %#v, %v", bash, ok)
	}
	read, ok := ReadToolCall(ToolCallEvent{ToolName: "read", Input: map[string]any{"path": "a.txt", "offset": 2.0, "limit": 10.0}})
	if !ok || read.Path != "a.txt" || read.Offset == nil || *read.Offset != 2 || read.Limit == nil || *read.Limit != 10 {
		t.Fatalf("ReadToolCall = %#v, %v", read, ok)
	}
	edit, ok := EditToolCall(ToolCallEvent{ToolName: "edit", Input: map[string]any{
		"path":  "f.go",
		"edits": []any{map[string]any{"oldText": "a", "newText": "b"}},
	}})
	if !ok || edit.Path != "f.go" || len(edit.Edits) != 1 || edit.Edits[0] != (tools.Edit{OldText: "a", NewText: "b"}) {
		t.Fatalf("EditToolCall = %#v, %v", edit, ok)
	}
	write, ok := WriteToolCall(ToolCallEvent{ToolName: "write", Input: map[string]any{"path": "f.txt", "content": "hello"}})
	if !ok || write.Path != "f.txt" || write.Content != "hello" {
		t.Fatalf("WriteToolCall = %#v, %v", write, ok)
	}
	grep, ok := GrepToolCall(ToolCallEvent{ToolName: "grep", Input: map[string]any{"pattern": "x", "ignoreCase": true}})
	if !ok || grep.Pattern != "x" || grep.IgnoreCase == nil || !*grep.IgnoreCase {
		t.Fatalf("GrepToolCall = %#v, %v", grep, ok)
	}
	find, ok := FindToolCall(ToolCallEvent{ToolName: "find", Input: map[string]any{"pattern": "*.go", "path": "src"}})
	if !ok || find.Pattern != "*.go" || find.Path == nil || *find.Path != "src" {
		t.Fatalf("FindToolCall = %#v, %v", find, ok)
	}
	ls, ok := LsToolCall(ToolCallEvent{ToolName: "ls", Input: map[string]any{}})
	if !ok || ls.Path != nil || ls.Limit != nil {
		t.Fatalf("LsToolCall = %#v, %v", ls, ok)
	}
}

func TestToolCallAccessorsRejectOtherToolsAndBadInput(t *testing.T) {
	bashEvent := ToolCallEvent{ToolName: "bash", Input: map[string]any{"command": "ls"}}
	if _, ok := ReadToolCall(bashEvent); ok {
		t.Fatal("ReadToolCall matched a bash event")
	}
	if _, ok := EditToolCall(bashEvent); ok {
		t.Fatal("EditToolCall matched a bash event")
	}
	if _, ok := WriteToolCall(bashEvent); ok {
		t.Fatal("WriteToolCall matched a bash event")
	}
	if _, ok := GrepToolCall(bashEvent); ok {
		t.Fatal("GrepToolCall matched a bash event")
	}
	if _, ok := FindToolCall(bashEvent); ok {
		t.Fatal("FindToolCall matched a bash event")
	}
	if _, ok := LsToolCall(bashEvent); ok {
		t.Fatal("LsToolCall matched a bash event")
	}
	if _, ok := BashToolCall(ToolCallEvent{ToolName: "read", Input: map[string]any{"path": "a"}}); ok {
		t.Fatal("BashToolCall matched a read event")
	}
	// A custom tool that reuses a built-in field with the wrong type must not decode.
	if input, ok := BashToolCall(ToolCallEvent{ToolName: "bash", Input: map[string]any{"command": 12}}); ok {
		t.Fatalf("BashToolCall decoded invalid input as %#v", input)
	}
}

func TestToolResultAccessorsExtractMatchingDetails(t *testing.T) {
	bash, ok := BashToolResult(ToolResultEvent{ToolName: "bash", Details: tools.BashToolDetails{FullOutputPath: "/tmp/full.txt"}})
	if !ok || bash.FullOutputPath != "/tmp/full.txt" {
		t.Fatalf("BashToolResult = %#v, %v", bash, ok)
	}
	// Pointer details unwrap to the value form.
	read, ok := ReadToolResult(ToolResultEvent{ToolName: "read", Details: &tools.ReadToolDetails{Truncation: &truncate.Result{Truncated: true}}})
	if !ok || read.Truncation == nil || !read.Truncation.Truncated {
		t.Fatalf("ReadToolResult = %#v, %v", read, ok)
	}
	edit, ok := EditToolResult(ToolResultEvent{ToolName: "edit", Details: tools.EditToolDetails{Diff: "d", Patch: "p"}})
	if !ok || edit.Diff != "d" || edit.Patch != "p" {
		t.Fatalf("EditToolResult = %#v, %v", edit, ok)
	}
	if !WriteToolResult(ToolResultEvent{ToolName: "write"}) {
		t.Fatal("WriteToolResult rejected a write event")
	}
	limit := 100.0
	grep, ok := GrepToolResult(ToolResultEvent{ToolName: "grep", Details: tools.GrepToolDetails{MatchLimitReached: &limit, LinesTruncated: true}})
	if !ok || grep.MatchLimitReached == nil || *grep.MatchLimitReached != 100 || !grep.LinesTruncated {
		t.Fatalf("GrepToolResult = %#v, %v", grep, ok)
	}
	results := 1000.0
	find, ok := FindToolResult(ToolResultEvent{ToolName: "find", Details: tools.FindToolDetails{ResultLimitReached: &results}})
	if !ok || find.ResultLimitReached == nil || *find.ResultLimitReached != 1000 {
		t.Fatalf("FindToolResult = %#v, %v", find, ok)
	}
	entries := 500.0
	ls, ok := LsToolResult(ToolResultEvent{ToolName: "ls", Details: tools.LsToolDetails{EntryLimitReached: &entries}})
	if !ok || ls.EntryLimitReached == nil || *ls.EntryLimitReached != 500 {
		t.Fatalf("LsToolResult = %#v, %v", ls, ok)
	}
	// Details patched by a JavaScript extension arrive as a decoded JSON map.
	line := 3
	patched, ok := EditToolResult(ToolResultEvent{ToolName: "edit", Details: map[string]any{"diff": "dd", "patch": "pp", "firstChangedLine": 3.0}})
	if !ok || patched.Diff != "dd" || patched.Patch != "pp" || patched.FirstChangedLine == nil || *patched.FirstChangedLine != line {
		t.Fatalf("EditToolResult from map = %#v, %v", patched, ok)
	}
}

func TestToolResultAccessorsRejectOtherToolsAndMissingDetails(t *testing.T) {
	bashEvent := ToolResultEvent{ToolName: "bash", Details: tools.BashToolDetails{}}
	if _, ok := ReadToolResult(bashEvent); ok {
		t.Fatal("ReadToolResult matched a bash event")
	}
	if _, ok := EditToolResult(bashEvent); ok {
		t.Fatal("EditToolResult matched a bash event")
	}
	if WriteToolResult(bashEvent) {
		t.Fatal("WriteToolResult matched a bash event")
	}
	if _, ok := GrepToolResult(bashEvent); ok {
		t.Fatal("GrepToolResult matched a bash event")
	}
	if _, ok := FindToolResult(bashEvent); ok {
		t.Fatal("FindToolResult matched a bash event")
	}
	if _, ok := LsToolResult(bashEvent); ok {
		t.Fatal("LsToolResult matched a bash event")
	}
	if _, ok := BashToolResult(ToolResultEvent{ToolName: "read", Details: tools.ReadToolDetails{}}); ok {
		t.Fatal("BashToolResult matched a read event")
	}
	// Matching tool with absent or undecodable details reports no typed details.
	if _, ok := BashToolResult(ToolResultEvent{ToolName: "bash"}); ok {
		t.Fatal("BashToolResult extracted details from a nil-details event")
	}
	if _, ok := BashToolResult(ToolResultEvent{ToolName: "bash", Details: "free-form"}); ok {
		t.Fatal("BashToolResult extracted details from a string payload")
	}
}
