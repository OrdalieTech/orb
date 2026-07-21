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

// Ported from upstream packages/tui/test/autocomplete.test.ts
// "extractPathPrefix".
func TestProviderExtractPathPrefix(t *testing.T) {
	provider := NewCombinedAutocompleteProvider(nil, "/tmp", "")
	ctx := context.Background()

	result := provider.GetSuggestions(ctx, []string{"hey /"}, 0, 5, true)
	if result == nil || result.Prefix != "/" {
		t.Fatalf("forced '/' suggestions = %+v", result)
	}

	if result := provider.GetSuggestions(ctx, []string{"/A"}, 0, 2, true); result != nil && result.Prefix != "/A" {
		t.Fatalf("prefix = %q, want /A", result.Prefix)
	}

	if result := provider.GetSuggestions(ctx, []string{"/model"}, 0, 6, true); result != nil {
		t.Fatalf("slash command should not trigger file completion: %+v", result)
	}

	result = provider.GetSuggestions(ctx, []string{"/command /"}, 0, 10, true)
	if result == nil || result.Prefix != "/" {
		t.Fatalf("slash-argument path suggestions = %+v", result)
	}
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

func TestProviderApplySlashCompletion(t *testing.T) {
	provider := NewCombinedAutocompleteProvider(nil, "/tmp", "")
	applied := provider.ApplyCompletion([]string{"/he"}, 0, 3, AutocompleteItem{Value: "help", Label: "help"}, "/he")
	if applied.Lines[0] != "/help " || applied.CursorCol != 6 {
		t.Fatalf("applied = %+v", applied)
	}
}

func TestProviderDotSlashCompletion(t *testing.T) {
	baseDir := t.TempDir()
	writeFile := func(name, content string) {
		t.Helper()
		path := filepath.Join(baseDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("update.sh", "#!/bin/bash")
	writeFile("utils.ts", "export {};")
	provider := NewCombinedAutocompleteProvider(nil, baseDir, "")

	result := provider.GetSuggestions(context.Background(), []string{"./up"}, 0, 4, true)
	if !containsValue(suggestionValues(result), "./update.sh") {
		t.Fatalf("values = %v", suggestionValues(result))
	}

	writeFile("src/index.ts", "export {};")
	result = provider.GetSuggestions(context.Background(), []string{"./sr"}, 0, 4, true)
	if !containsValue(suggestionValues(result), "./src/") {
		t.Fatalf("values = %v", suggestionValues(result))
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

func TestProviderDirectoriesFirst(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(baseDir, "zdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "afile.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	provider := NewCombinedAutocompleteProvider(nil, baseDir, "")
	result := provider.GetSuggestions(context.Background(), []string{"x "}, 0, 2, true)
	values := suggestionValues(result)
	if len(values) != 2 || values[0] != "zdir/" || values[1] != "afile.txt" {
		t.Fatalf("values = %v", values)
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
	fdPath := filepath.Join(t.TempDir(), "fd")
	if err := os.WriteFile(fdPath, []byte("#!/bin/sh\nprintf '%s\\n' 'src/' 'src/main.ts'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	provider := NewCombinedAutocompleteProvider(nil, baseDir, fdPath)
	result := provider.GetSuggestions(context.Background(), []string{"@main"}, 0, 5, false)
	if result == nil || result.Prefix != "@main" {
		t.Fatalf("fd suggestions = %+v", result)
	}
	if !containsValue(suggestionValues(result), "@src/main.ts") {
		t.Fatalf("values = %v", suggestionValues(result))
	}
}

func TestProviderLocaleCompareOrder(t *testing.T) {
	t.Setenv("LC_ALL", "C.UTF-8")
	names := []string{"A", "a", "Á", "á", "ä", "z", "Z", "é", "e", "10", "2", "_a", "-a", "界", "你"}
	suggestions := make([]AutocompleteItem, len(names))
	for index, name := range names {
		suggestions[index] = AutocompleteItem{Value: name, Label: name}
	}
	sortAutocompleteSuggestions(suggestions)
	got := make([]string, len(suggestions))
	for index, suggestion := range suggestions {
		got[index] = suggestion.Value
	}
	want := []string{"_a", "-a", "10", "2", "a", "A", "á", "Á", "ä", "e", "é", "z", "Z", "你", "界"}
	if len(got) != len(want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("values = %v, want %v", got, want)
		}
	}

	// Directory grouping remains primary even when collation would place a
	// file first.
	suggestions = append(suggestions, AutocompleteItem{Value: "zz-directory/", Label: "zz-directory/"})
	sortAutocompleteSuggestions(suggestions)
	if suggestions[0].Value != "zz-directory/" {
		t.Fatalf("directories not first: %v", suggestions)
	}
}

func TestProviderShouldTriggerFileCompletion(t *testing.T) {
	provider := NewCombinedAutocompleteProvider(nil, "/tmp", "")
	if provider.ShouldTriggerFileCompletion([]string{"/mod"}, 0, 4) {
		t.Fatal("slash command should veto")
	}
	if !provider.ShouldTriggerFileCompletion([]string{"/cmd arg"}, 0, 8) {
		t.Fatal("slash command with argument should allow")
	}
	if !provider.ShouldTriggerFileCompletion([]string{"plain"}, 0, 5) {
		t.Fatal("plain text should allow")
	}
}
