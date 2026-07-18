package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResourcePathSettersPreserveGlobalDocument(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agentDir, "settings.json")
	initial := `{
  "before": {"keep": true},
  "extensions": ["old.ts"],
  "after": 7
}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir), WithProjectTrusted(false))
	if err != nil {
		t.Fatal(err)
	}
	setters := []struct {
		name string
		set  func([]string) error
		want []string
	}{
		{"extensions", manager.SetExtensionPaths, []string{"extensions/a.ts", "-extensions/b.ts"}},
		{"skills", manager.SetSkillPaths, []string{"skills/review/SKILL.md"}},
		{"prompts", manager.SetPromptTemplatePaths, []string{"+prompts/review.md"}},
		{"themes", manager.SetThemePaths, []string{}},
	}
	for _, setter := range setters {
		if err := setter.set(setter.want); err != nil {
			t.Fatalf("set %s: %v", setter.name, err)
		}
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(contents, &decoded); err != nil {
		t.Fatal(err)
	}
	before, ok := decoded["before"].(map[string]any)
	if !ok || before["keep"] != true || decoded["after"] != float64(7) {
		t.Fatalf("unrelated settings changed: %#v", decoded)
	}
	for _, setter := range setters {
		if got := settingsStringSlice(Settings(decoded), setter.name); !reflect.DeepEqual(got, setter.want) {
			t.Fatalf("%s = %#v, want %#v", setter.name, got, setter.want)
		}
	}
	text := string(contents)
	if strings.Index(text, `"before"`) >= strings.Index(text, `"extensions"`) ||
		strings.Index(text, `"extensions"`) >= strings.Index(text, `"after"`) {
		t.Fatalf("existing field order changed:\n%s", text)
	}
}

func TestResourcePathSettersPreserveProjectDocumentAndTrustGate(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	projectDir := filepath.Join(root, "project")
	projectConfigDir := filepath.Join(projectDir, ConfigDirName)
	for _, directory := range []string{agentDir, projectConfigDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	projectPath := filepath.Join(projectConfigDir, "settings.json")
	initial := `{
  "before": "keep",
  "extensions": ["old.ts"],
  "after": {"nested": 1}
}`
	if err := os.WriteFile(projectPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	untrusted, err := NewSettingsManager(projectDir, WithAgentDir(agentDir), WithProjectTrusted(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := untrusted.SetProjectExtensionPaths([]string{"blocked.ts"}); err == nil ||
		err.Error() != "Project is not trusted; refusing to write project settings" {
		t.Fatalf("untrusted write error = %v", err)
	}
	if contents, err := os.ReadFile(projectPath); err != nil || string(contents) != initial {
		t.Fatalf("untrusted write changed file: err=%v contents=%q", err, contents)
	}

	trusted, err := NewSettingsManager(projectDir, WithAgentDir(agentDir), WithProjectTrusted(true))
	if err != nil {
		t.Fatal(err)
	}
	setters := []struct {
		name string
		set  func([]string) error
		want []string
	}{
		{"extensions", trusted.SetProjectExtensionPaths, []string{"+extensions/a.ts"}},
		{"skills", trusted.SetProjectSkillPaths, []string{"-skills/old/SKILL.md"}},
		{"prompts", trusted.SetProjectPromptTemplatePaths, []string{}},
		{"themes", trusted.SetProjectThemePaths, []string{"themes/local.json"}},
	}
	for _, setter := range setters {
		if err := setter.set(setter.want); err != nil {
			t.Fatalf("set project %s: %v", setter.name, err)
		}
	}
	contents, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(contents, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["before"] != "keep" || !reflect.DeepEqual(decoded["after"], map[string]any{"nested": float64(1)}) {
		t.Fatalf("unrelated project settings changed: %#v", decoded)
	}
	for _, setter := range setters {
		if got := settingsStringSlice(Settings(decoded), setter.name); !reflect.DeepEqual(got, setter.want) {
			t.Fatalf("project %s = %#v, want %#v", setter.name, got, setter.want)
		}
	}
	text := string(contents)
	if strings.Index(text, `"before"`) >= strings.Index(text, `"extensions"`) ||
		strings.Index(text, `"extensions"`) >= strings.Index(text, `"after"`) {
		t.Fatalf("existing project field order changed:\n%s", text)
	}
}
