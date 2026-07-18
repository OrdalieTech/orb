package main

import (
	"reflect"
	"testing"
)

func TestParseArgsCoreSubset(t *testing.T) {
	args := ParseArgs([]string{
		"--provider", "openai",
		"--model", "gpt-4o",
		"--api-key", "secret",
		"--thinking", "high",
		"--continue",
		"--session-dir", "~/sessions",
		"--system-prompt", "system.md",
		"--append-system-prompt", "one",
		"--append-system-prompt", "two",
		"--tools", " read, grep ,,find ",
		"-xt", "bash, write",
		"--no-context-files",
		"@prompt.md",
		"message",
	})

	if args.Provider == nil || *args.Provider != "openai" || args.Model == nil || *args.Model != "gpt-4o" {
		t.Fatalf("provider/model = %v/%v", args.Provider, args.Model)
	}
	if args.APIKey == nil || *args.APIKey != "secret" || args.Thinking == nil || *args.Thinking != "high" {
		t.Fatalf("api key/thinking = %v/%v", args.APIKey, args.Thinking)
	}
	if !args.Continue || args.SessionDir == nil || *args.SessionDir != "~/sessions" || !args.NoContextFiles {
		t.Fatalf("session/context flags = %#v", args)
	}
	if args.SystemPrompt == nil || *args.SystemPrompt != "system.md" {
		t.Fatalf("system prompt = %v", args.SystemPrompt)
	}
	if !reflect.DeepEqual(args.AppendSystemPrompt, []string{"one", "two"}) {
		t.Fatalf("append prompts = %#v", args.AppendSystemPrompt)
	}
	if !reflect.DeepEqual(args.Tools, []string{"read", "grep", "find"}) {
		t.Fatalf("tools = %#v", args.Tools)
	}
	if !reflect.DeepEqual(args.ExcludeTools, []string{"bash", "write"}) {
		t.Fatalf("excluded tools = %#v", args.ExcludeTools)
	}
	if !reflect.DeepEqual(args.FileArgs, []string{"prompt.md"}) || !reflect.DeepEqual(args.Messages, []string{"message"}) {
		t.Fatalf("files/messages = %#v/%#v", args.FileArgs, args.Messages)
	}
}

func TestParseArgsModelCatalogFlags(t *testing.T) {
	args := ParseArgs([]string{"--models", " sonnet:high, ,openai/gpt-4o ", "--list-models", "gpt 4"})
	if !reflect.DeepEqual(args.Models, []string{"sonnet:high", "", "openai/gpt-4o"}) {
		t.Fatalf("models = %#v", args.Models)
	}
	if args.ListModels == nil || *args.ListModels != "gpt 4" {
		t.Fatalf("list-models = %#v", args.ListModels)
	}
	withoutSearch := ParseArgs([]string{"--list-models", "--print"})
	if withoutSearch.ListModels == nil || *withoutSearch.ListModels != "" || !withoutSearch.Print {
		t.Fatalf("list without search = %#v", withoutSearch)
	}
}

func TestParseArgsSessionTreeAndExportFlags(t *testing.T) {
	args := ParseArgs([]string{
		"--resume", "--session", "abc123", "--session-id", "exact-id", "--fork", "source",
		"--name", "named", "--export", "input.jsonl", "output.html",
	})
	if !args.Resume || args.Session == nil || *args.Session != "abc123" || args.SessionID == nil || *args.SessionID != "exact-id" {
		t.Fatalf("session selection flags = %#v", args)
	}
	if args.Fork == nil || *args.Fork != "source" || args.Name == nil || *args.Name != "named" {
		t.Fatalf("fork/name flags = %#v", args)
	}
	if args.Export == nil || *args.Export != "input.jsonl" || !reflect.DeepEqual(args.Messages, []string{"output.html"}) {
		t.Fatalf("export flags = %#v", args)
	}

	missingName := ParseArgs([]string{"--name"})
	if len(missingName.Diagnostics) != 1 || missingName.Diagnostics[0] != (CLIDiagnostic{Type: "error", Message: "--name requires a value"}) {
		t.Fatalf("missing name diagnostics = %#v", missingName.Diagnostics)
	}
}

func TestParseArgsPrintConsumesOnlyEligibleNextArgument(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		messages []string
		files    []string
	}{
		{name: "plain", argv: []string{"-p", "hello", "again"}, messages: []string{"hello", "again"}},
		{name: "triple dash is message", argv: []string{"--print", "---hello"}, messages: []string{"---hello"}},
		{name: "file is not consumed", argv: []string{"-p", "@prompt.md", "hello"}, messages: []string{"hello"}, files: []string{"prompt.md"}},
		{name: "flag is not consumed", argv: []string{"-p", "--provider", "openai"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := ParseArgs(test.argv)
			if !args.Print || !reflect.DeepEqual(args.Messages, nonNil(test.messages)) || !reflect.DeepEqual(args.FileArgs, nonNil(test.files)) {
				t.Fatalf("parsed = %#v", args)
			}
		})
	}
}

func TestParseArgsKeepsUnknownLongFlagsSeparateFromDiagnostics(t *testing.T) {
	args := ParseArgs([]string{
		"--extension-string=value",
		"--extension-value", "next",
		"--extension-bool", "@file",
		"--thinking", "impossible",
		"-z",
		"--provider",
	})

	if !reflect.DeepEqual(args.FileArgs, []string{"file"}) {
		t.Fatalf("file args = %#v", args.FileArgs)
	}
	if args.Provider != nil {
		t.Fatalf("provider = %#v, want absent", args.Provider)
	}
	if !reflect.DeepEqual(args.Messages, []string{}) {
		t.Fatalf("messages = %#v", args.Messages)
	}
	if len(args.Diagnostics) != 2 ||
		args.Diagnostics[0].Type != "warning" ||
		args.Diagnostics[1] != (CLIDiagnostic{Type: "error", Message: "Unknown option: -z"}) {
		t.Fatalf("diagnostics = %#v", args.Diagnostics)
	}
	wantUnknown := []CLIUnknownFlag{
		{Name: "extension-string", Value: stringValue("value")},
		{Name: "extension-value", Value: stringValue("next")},
		{Name: "extension-bool"},
		{Name: "provider"},
	}
	if !reflect.DeepEqual(args.UnknownFlags, wantUnknown) {
		t.Fatalf("unknown flags = %#v, want %#v", args.UnknownFlags, wantUnknown)
	}
	if args.Thinking != nil {
		t.Fatalf("invalid thinking level was accepted: %q", *args.Thinking)
	}
}

func TestParseArgsUnknownLongFlagsUseMapOrderAndLastValue(t *testing.T) {
	args := ParseArgs([]string{
		"--first=one",
		"--second", "two",
		"--first=last",
		"--third",
		"--second",
		"--empty=",
	})
	want := []CLIUnknownFlag{
		{Name: "first", Value: stringValue("last")},
		{Name: "second"},
		{Name: "third"},
		{Name: "empty", Value: stringValue("")},
	}
	if !reflect.DeepEqual(args.UnknownFlags, want) || len(args.Diagnostics) != 0 {
		t.Fatalf("unknown flags = %#v, diagnostics = %#v", args.UnknownFlags, args.Diagnostics)
	}
}

func TestParseArgsDistinguishesAbsentAndExplicitEmptyToolList(t *testing.T) {
	absent := ParseArgs(nil)
	if absent.Tools != nil || absent.ExcludeTools != nil || absent.AppendSystemPrompt != nil {
		t.Fatalf("absent slices lost undefined state: %#v", absent)
	}
	explicit := ParseArgs([]string{"--tools", ", ,", "--exclude-tools", ""})
	if explicit.Tools == nil || len(explicit.Tools) != 0 || explicit.ExcludeTools == nil || len(explicit.ExcludeTools) != 0 {
		t.Fatalf("explicit empty lists lost defined state: %#v", explicit)
	}
}

func TestParseArgsRecognizedValueConsumesFollowingOptionToken(t *testing.T) {
	args := ParseArgs([]string{"--provider", "--thinking", "high"})
	if args.Provider == nil || *args.Provider != "--thinking" {
		t.Fatalf("provider = %#v", args.Provider)
	}
	if args.Thinking != nil || !reflect.DeepEqual(args.Messages, []string{"high"}) {
		t.Fatalf("parsed = %#v", args)
	}
}

func TestParseToolListUsesJavaScriptTrimSet(t *testing.T) {
	args := ParseArgs([]string{"--tools", "\ufeffread\ufeff,\u0085bash\u0085"})
	if !reflect.DeepEqual(args.Tools, []string{"read", "\u0085bash\u0085"}) {
		t.Fatalf("tools = %#v", args.Tools)
	}
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
