package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func suggestionValues(result *AutocompleteSuggestions) []string {
	if result == nil {
		return nil
	}
	values := make([]string, len(result.Items))
	for index, item := range result.Items {
		values[index] = item.Value
	}
	return values
}

func containsValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestProviderSlashCommands(t *testing.T) {
	commands := []SlashCommand{
		{Name: "help", Description: "Show help"},
		{Name: "hotkeys", Description: "Show hotkeys", ArgumentHint: "<name>"},
		{Name: "clear", Description: "Clear session"},
	}
	provider := NewCombinedAutocompleteProvider(commands, t.TempDir(), "")
	ctx := context.Background()

	result := provider.GetSuggestions(ctx, []string{"/h"}, 0, 2, false)
	if result == nil || result.Prefix != "/h" {
		t.Fatalf("slash suggestions = %+v", result)
	}
	values := suggestionValues(result)
	if !containsValue(values, "help") || !containsValue(values, "hotkeys") || containsValue(values, "clear") {
		t.Fatalf("values = %v", values)
	}
	for _, item := range result.Items {
		if item.Value == "hotkeys" && item.Description != "<name> — Show hotkeys" {
			t.Fatalf("argument hint description = %q", item.Description)
		}
	}

	// Argument completion.
	commands[0].GetArgumentCompletions = func(prefix string) []AutocompleteItem {
		return []AutocompleteItem{{Value: "topic-" + prefix, Label: "topic-" + prefix}}
	}
	provider = NewCombinedAutocompleteProvider(commands, t.TempDir(), "")
	result = provider.GetSuggestions(ctx, []string{"/help to"}, 0, 8, false)
	if result == nil || result.Prefix != "to" || result.Items[0].Value != "topic-to" {
		t.Fatalf("argument suggestions = %+v", result)
	}

	// Command without argument completions yields nothing after a space.
	result = provider.GetSuggestions(ctx, []string{"/clear x"}, 0, 8, false)
	if result != nil {
		t.Fatalf("unexpected argument suggestions: %+v", result)
	}
}

func TestProviderQuotedPaths(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(baseDir, "my folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"test.txt", "other.txt"} {
		if err := os.WriteFile(filepath.Join(baseDir, "my folder", name), []byte("content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	provider := NewCombinedAutocompleteProvider(nil, baseDir, "")
	ctx := context.Background()

	result := provider.GetSuggestions(ctx, []string{"my"}, 0, 2, true)
	if !containsValue(suggestionValues(result), `"my folder/"`) {
		t.Fatalf("values = %v", suggestionValues(result))
	}

	line := `"my folder/"`
	result = provider.GetSuggestions(ctx, []string{line}, 0, runeLen(line)-1, true)
	values := suggestionValues(result)
	if !containsValue(values, `"my folder/test.txt"`) || !containsValue(values, `"my folder/other.txt"`) {
		t.Fatalf("values = %v", values)
	}

	line = `"my folder/te"`
	cursorCol := runeLen(line) - 1
	result = provider.GetSuggestions(ctx, []string{line}, 0, cursorCol, true)
	var item *AutocompleteItem
	for index := range result.Items {
		if result.Items[index].Value == `"my folder/test.txt"` {
			item = &result.Items[index]
		}
	}
	if item == nil {
		t.Fatalf("test.txt suggestion missing: %v", suggestionValues(result))
	}
	applied := provider.ApplyCompletion([]string{line}, 0, cursorCol, *item, result.Prefix)
	if applied.Lines[0] != `"my folder/test.txt"` {
		t.Fatalf("applied line = %q", applied.Lines[0])
	}
}

func TestProviderFuzzyFdSuggestions(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(baseDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "src", "main.ts"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fdPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeFDEnv, "src/\nsrc/main.ts\n")
	provider := NewCombinedAutocompleteProvider(nil, baseDir, fdPath)
	result := provider.GetSuggestions(context.Background(), []string{"@main"}, 0, 5, false)
	if result == nil || result.Prefix != "@main" {
		t.Fatalf("fd suggestions = %+v", result)
	}
	if !containsValue(suggestionValues(result), "@src/main.ts") {
		t.Fatalf("values = %v", suggestionValues(result))
	}
}
