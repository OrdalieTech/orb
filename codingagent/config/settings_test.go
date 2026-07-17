package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
)

func TestSettingsLoadMigrateMergeAndPreserveUnknown(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	projectDir := filepath.Join(root, "project")
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"queueMode":  "all",
		"websockets": true,
		"terminal":   map[string]any{"showImages": false, "imageWidthCells": 40},
		"extensions": []string{"global.ts"},
		"mystery":    map[string]any{"kept": true},
		// A wrong-typed known key must not reject the document.
		"defaultProvider": 42,
	})
	writeSettings(t, filepath.Join(projectDir, ".pi", "settings.json"), map[string]any{
		"terminal":   map[string]any{"imageWidthCells": 80},
		"extensions": []string{"project.ts"},
	})

	manager, err := NewSettingsManager(projectDir, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if errors := manager.DrainErrors(); len(errors) != 0 {
		t.Fatalf("load errors = %v", errors)
	}
	if got := manager.GetSteeringMode(); got != "all" {
		t.Fatalf("steering mode = %q", got)
	}
	if got := manager.GetTransport(); got != ai.TransportWebSocket {
		t.Fatalf("transport = %q", got)
	}

	settings := manager.GetSettings()
	terminal := settings["terminal"].(Settings)
	if terminal["showImages"] != false || terminal["imageWidthCells"] != json.Number("80") {
		t.Fatalf("terminal merge = %#v", terminal)
	}
	if got := settings["extensions"]; !reflect.DeepEqual(got, []any{"project.ts"}) {
		t.Fatalf("extensions = %#v", got)
	}
	if _, ok := settings["mystery"]; !ok {
		t.Fatal("unknown key was discarded")
	}
	if got := manager.GetDefaultProvider(); got != "" {
		t.Fatalf("wrong-typed provider should be tolerated, got %q", got)
	}

	global := manager.GetGlobalSettings()
	if _, exists := global["queueMode"]; exists {
		t.Fatal("queueMode was not removed during migration")
	}
	if _, exists := global["websockets"]; exists {
		t.Fatal("websockets was not removed during migration")
	}

	global["mystery"] = "changed"
	if _, ok := manager.GetGlobalSettings()["mystery"].(Settings); !ok {
		t.Fatal("GetGlobalSettings returned manager-owned data")
	}
	terminal["showImages"] = true
	if got := manager.GetSettings()["terminal"].(Settings)["showImages"]; got != false {
		t.Fatal("GetSettings returned manager-owned nested data")
	}
}

func TestSettingsMigrationsMatchUpstream(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"queueMode":    "all",
		"steeringMode": "one-at-a-time",
		"websockets":   true,
		"transport":    "sse",
		"skills": map[string]any{
			"enableSkillCommands": false,
			"customDirectories":   []string{"global-skills"},
		},
		"retry": map[string]any{
			"maxDelayMs": 500,
			"provider":   map[string]any{"maxRetryDelayMs": nil},
		},
	})

	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	settings := manager.GetGlobalSettings()
	if settings["queueMode"] != "all" || settings["websockets"] != true {
		t.Fatalf("legacy keys with replacements should remain: %#v", settings)
	}
	if settings["enableSkillCommands"] != false {
		t.Fatalf("enableSkillCommands = %#v", settings["enableSkillCommands"])
	}
	if got := settings["skills"]; !reflect.DeepEqual(got, []any{"global-skills"}) {
		t.Fatalf("skills migration = %#v", got)
	}
	if got := settings["retry"]; !reflect.DeepEqual(got, Settings{
		"provider": Settings{"maxRetryDelayMs": json.Number("500")},
	}) {
		t.Fatalf("retry migration = %#v", got)
	}

	secondAgentDir := filepath.Join(root, "second-agent")
	writeSettings(t, filepath.Join(secondAgentDir, "settings.json"), map[string]any{
		"enableSkillCommands": true,
		"skills":              map[string]any{"enableSkillCommands": false, "customDirectories": []string{}},
		"retry":               map[string]any{"maxDelayMs": 500, "provider": map[string]any{"maxRetryDelayMs": 700}},
	})
	second, err := NewSettingsManager(root, WithAgentDir(secondAgentDir))
	if err != nil {
		t.Fatal(err)
	}
	secondSettings := second.GetGlobalSettings()
	if secondSettings["enableSkillCommands"] != true {
		t.Fatalf("existing enableSkillCommands was overwritten: %#v", secondSettings)
	}
	if _, exists := secondSettings["skills"]; exists {
		t.Fatalf("empty legacy skills were retained: %#v", secondSettings["skills"])
	}
	if got := secondSettings["retry"]; !reflect.DeepEqual(got, Settings{
		"provider": Settings{"maxRetryDelayMs": json.Number("700")},
	}) {
		t.Fatalf("existing retry delay was overwritten: %#v", got)
	}
}

func TestProjectSettingsLoadAndReadDoesNotCreateProjectDirectory(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	projectDir := filepath.Join(root, "project")
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{"marker": "global"})
	writeSettings(t, filepath.Join(projectDir, ".pi", "settings.json"), map[string]any{"marker": "project"})

	manager, err := NewSettingsManager(projectDir, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.GetSettings()["marker"]; got != "project" {
		t.Fatalf("effective marker = %#v", got)
	}
	if got := manager.GetProjectSettings()["marker"]; got != "project" {
		t.Fatalf("project marker = %#v", got)
	}

	projectWithoutConfig := filepath.Join(root, "empty-project")
	if _, err := NewSettingsManager(projectWithoutConfig, WithAgentDir(agentDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(projectWithoutConfig, ".pi")); !os.IsNotExist(err) {
		t.Fatalf("read created .pi directory: %v", err)
	}
}

func TestLoadErrorsAndReloadKeepPreviousScope(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	projectDir := filepath.Join(root, "project")
	writeRaw(t, filepath.Join(agentDir, "settings.json"), `{ invalid`)
	writeRaw(t, filepath.Join(projectDir, ".pi", "settings.json"), `{ also invalid`)
	manager, err := NewSettingsManager(projectDir, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	errors := manager.DrainErrors()
	if len(errors) != 2 || errors[0].Scope != GlobalSettings || errors[1].Scope != ProjectSettings {
		t.Fatalf("errors = %#v", errors)
	}
	if len(manager.DrainErrors()) != 0 {
		t.Fatal("DrainErrors did not clear errors")
	}

	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{"marker": "valid"})
	writeSettings(t, filepath.Join(projectDir, ".pi", "settings.json"), map[string]any{})
	manager.Reload()
	if got := manager.GetSettings()["marker"]; got != "valid" {
		t.Fatalf("marker after valid reload = %#v", got)
	}
	writeRaw(t, filepath.Join(agentDir, "settings.json"), `{ broken`)
	manager.Reload()
	if got := manager.GetSettings()["marker"]; got != "valid" {
		t.Fatalf("invalid reload discarded prior value: %#v", got)
	}
	if got := manager.DrainErrors(); len(got) != 1 || got[0].Scope != GlobalSettings {
		t.Fatalf("reload errors = %#v", got)
	}
}

func TestConsumedSettingsGetters(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	shellPath := filepath.Join(root, "bin", "bash")
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"defaultProvider":      "openai",
		"defaultModel":         "gpt-test",
		"defaultThinkingLevel": "high",
		"transport":            "sse",
		"steeringMode":         "all",
		"followUpMode":         "all",
		"shellPath":            shellPath,
		"shellCommandPrefix":   "env TEST=1",
	})
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetDefaultProvider() != "openai" || manager.GetDefaultModel() != "gpt-test" {
		t.Fatal("model defaults differ")
	}
	if manager.GetDefaultThinkingLevel() != ai.ModelThinkingLevel("high") {
		t.Fatalf("thinking level = %q", manager.GetDefaultThinkingLevel())
	}
	if manager.GetTransport() != ai.TransportSSE || manager.GetSteeringMode() != "all" || manager.GetFollowUpMode() != "all" {
		t.Fatal("runtime settings differ")
	}
	if got, err := manager.GetShellPath(); err != nil || got != shellPath {
		t.Fatalf("shell path = %q, %v", got, err)
	}
	if got := manager.GetShellCommandPrefix(); got != "env TEST=1" {
		t.Fatalf("shell command prefix = %q", got)
	}

	empty, err := NewSettingsManager(root, WithAgentDir(filepath.Join(root, "empty-agent")))
	if err != nil {
		t.Fatal(err)
	}
	if empty.GetTransport() != ai.TransportAuto || empty.GetSteeringMode() != "one-at-a-time" || empty.GetFollowUpMode() != "one-at-a-time" {
		t.Fatal("message delivery defaults differ")
	}
}

func TestSessionDirectoryPrecedence(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{"sessionDir": "~/from-settings"})
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	settingsDir, err := ResolveSessionDir("", manager)
	if err != nil || settingsDir != filepath.Join(home, "from-settings") {
		t.Fatalf("settings session dir = %q, %v", settingsDir, err)
	}
	t.Setenv(EnvSessionDir, "~/from-env")
	environmentDir, err := ResolveSessionDir("", manager)
	if err != nil || environmentDir != filepath.Join(home, "from-env") {
		t.Fatalf("environment session dir = %q, %v", environmentDir, err)
	}
	cliDir, err := ResolveSessionDir("~/from-cli", manager)
	if err != nil || cliDir != filepath.Join(home, "from-cli") {
		t.Fatalf("CLI session dir = %q, %v", cliDir, err)
	}
}

func TestAgentDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvAgentDir, "file://"+filepath.ToSlash(root)+"/agent%20dir")
	dir, err := GetAgentDir()
	if err != nil || dir != filepath.Join(root, "agent dir") {
		t.Fatalf("agent dir = %q, %v", dir, err)
	}
}

func writeSettings(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, path, string(encoded))
}

func writeRaw(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
