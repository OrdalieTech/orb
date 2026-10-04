package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
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

func TestSettingsMutationsPersistLikeUpstream(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeRaw(t, settingsPath, `{"z":1,"compaction":{"keepRecentTokens":10},"a":"<tag>"}`)
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}

	manager.SetDefaultModelAndProvider("faux", "faux-1")
	manager.SetDefaultThinkingLevel(ai.ModelThinkingHigh)
	manager.SetSteeringMode("all")
	manager.SetFollowUpMode("all")
	manager.SetCompactionEnabled(false)
	manager.SetRetryEnabled(false)
	if got := manager.DrainErrors(); len(got) != 0 {
		t.Fatalf("mutation errors = %v", got)
	}

	contents, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "z": 1,
  "compaction": {
    "keepRecentTokens": 10,
    "enabled": false
  },
  "a": "<tag>",
  "defaultProvider": "faux",
  "defaultModel": "faux-1",
  "defaultThinkingLevel": "high",
  "steeringMode": "all",
  "followUpMode": "all",
  "retry": {
    "enabled": false
  }
}`
	if string(contents) != want {
		t.Fatalf("settings bytes =\n%s\nwant:\n%s", contents, want)
	}
	if _, err := os.Stat(settingsPath + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("settings lock remains after write: %v", err)
	}
	if manager.GetDefaultProvider() != "faux" || manager.GetDefaultModel() != "faux-1" || manager.GetDefaultThinkingLevel() != ai.ModelThinkingHigh {
		t.Fatal("effective model settings were not updated")
	}
	if manager.GetCompactionSettings().Enabled || manager.GetRetrySettings().Enabled {
		t.Fatal("effective policy settings were not updated")
	}
}

func TestSettingsMutationInteroperatesWithProperLockfileDirectory(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(agentDir, "settings.json.lock")
	if err := os.MkdirAll(lockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = os.Remove(lockPath)
		close(released)
	}()
	manager.SetSteeringMode("all")
	<-released
	if errors := manager.DrainErrors(); len(errors) != 0 {
		t.Fatalf("mutation errors = %v", errors)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock path after mutation: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(agentDir, "settings.json"))
	if err != nil || !bytes.Contains(contents, []byte(`"steeringMode": "all"`)) {
		t.Fatalf("settings = %s, %v", contents, err)
	}
}

func TestSettingsMutationMergesCurrentFileAndRefusesParseError(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeRaw(t, settingsPath, `{"initial":1}`)
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, settingsPath, `{"external":true,"initial":2}`)
	manager.SetSteeringMode("all")
	contents, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "{\n  \"external\": true,\n  \"initial\": 2,\n  \"steeringMode\": \"all\"\n}" {
		t.Fatalf("merged settings = %s", contents)
	}

	brokenDir := filepath.Join(root, "broken-agent")
	brokenPath := filepath.Join(brokenDir, "settings.json")
	writeRaw(t, brokenPath, `{ broken`)
	broken, err := NewSettingsManager(root, WithAgentDir(brokenDir))
	if err != nil {
		t.Fatal(err)
	}
	if got := broken.DrainErrors(); len(got) != 1 {
		t.Fatalf("initial errors = %v", got)
	}
	broken.SetSteeringMode("all")
	contents, err = os.ReadFile(brokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != `{ broken` {
		t.Fatalf("parse-error settings were overwritten: %q", contents)
	}
	if got := broken.DrainErrors(); len(got) != 0 {
		t.Fatalf("mutation duplicated load error: %v", got)
	}
}

func TestSettingsMutationPersistsPendingMigrations(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	settingsPath := filepath.Join(agentDir, "settings.json")
	writeRaw(t, settingsPath, `{"queueMode":"all","websockets":true,"skills":{"enableSkillCommands":false,"customDirectories":["x"]},"retry":{"maxDelayMs":500,"maxRetries":4}}`)
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager.SetFollowUpMode("all")
	if got := manager.DrainErrors(); len(got) != 0 {
		t.Fatalf("mutation errors = %v", got)
	}
	contents, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "skills": [
    "x"
  ],
  "retry": {
    "maxRetries": 4,
    "provider": {
      "maxRetryDelayMs": 500
    }
  },
  "steeringMode": "all",
  "transport": "websocket",
  "enableSkillCommands": false,
  "followUpMode": "all"
}`
	if string(contents) != want {
		t.Fatalf("migrated settings =\n%s\nwant:\n%s", contents, want)
	}
}

func TestStructuredPluginSettingsEnableAndPersistWithoutLosingRules(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"plugins": map[string]any{"permissions": map[string]any{
			"mode": "log", "rules": []any{map[string]any{"tool": "bash", "action": "deny"}},
		}},
	})
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if !manager.GetPlugins()["permissions"] || manager.GetPluginSettings("permissions")["mode"] != "log" {
		t.Fatalf("plugin settings = %#v", manager.GetPluginSettings("permissions"))
	}
	manager.SetPluginSetting("permissions", "mode", "enforce")
	manager.SetPluginEnabled("permissions", false)
	configured := manager.GetPluginSettings("permissions")
	if configured["mode"] != "enforce" || configured["enabled"] != false || configured["rules"] == nil {
		t.Fatalf("persisted plugin settings = %#v", configured)
	}
}

// Concurrent managers sharing one agent dir contend on the settings lock. A
// flat 10 x 20 ms poll dropped a double-digit percentage of writes outright,
// and the void setters reported nothing until a later DrainErrors.
func TestConcurrentSettingsWritersLoseNothing(t *testing.T) {
	agentDir := t.TempDir()
	const managers, writes = 64, 4
	all := make([]*SettingsManager, managers)
	for index := range all {
		manager, err := NewSettingsManager(t.TempDir(), WithAgentDir(agentDir))
		if err != nil {
			t.Fatal(err)
		}
		all[index] = manager
	}
	var group sync.WaitGroup
	for index, manager := range all {
		group.Add(1)
		go func() {
			defer group.Done()
			for write := range writes {
				manager.setGlobalValues(settingMember(fmt.Sprintf("k%d-%d", index, write), index*writes+write))
			}
		}()
	}
	group.Wait()

	raw, err := os.ReadFile(filepath.Join(agentDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber() // match the manager's own in-memory decoding
	if err := decoder.Decode(&persisted); err != nil {
		t.Fatalf("settings file is not valid JSON after %d concurrent writes: %v", managers*writes, err)
	}
	if len(persisted) != managers*writes {
		t.Fatalf("persisted %d of %d updates", len(persisted), managers*writes)
	}
	for index, manager := range all {
		if errs := manager.DrainErrors(); len(errs) != 0 {
			t.Fatalf("manager %d reported write errors: %v", index, errs)
		}
		// Every value the manager believes it set must be on disk: in-memory
		// state may never run ahead of a failed write.
		for name, value := range manager.GetGlobalSettings() {
			if !reflect.DeepEqual(persisted[name], value) {
				t.Fatalf("manager %d holds %s=%v, file holds %v", index, name, value, persisted[name])
			}
		}
	}
}

func TestFailedSettingsWriteLeavesInMemoryStateAlone(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	// A directory where the settings file belongs makes the write fail after
	// the manager has already decided on the new value.
	if err := os.MkdirAll(filepath.Join(agentDir, "settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager.SetSteeringMode("all")
	if errs := manager.DrainErrors(); len(errs) != 1 {
		t.Fatalf("write errors = %v, want exactly one", errs)
	}
	if mode := manager.GetSteeringMode(); mode != "one-at-a-time" {
		t.Fatalf("steering mode = %q after a failed write, want the unchanged default", mode)
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

func TestDefaultToolsModifiersLayerProjectOverUser(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	projectDir := filepath.Join(root, "project")
	load := func(user, project any) []string {
		t.Helper()
		writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{"defaultTools": user})
		writeSettings(t, filepath.Join(projectDir, ".pi", "settings.json"), map[string]any{"defaultTools": project})
		manager, err := NewSettingsManager(projectDir, WithAgentDir(agentDir))
		if err != nil {
			t.Fatal(err)
		}
		return manager.GetDefaultTools()
	}
	if got := load([]string{"+grep"}, []string{"-bash", "+find"}); !reflect.DeepEqual(got, []string{"read", "edit", "write", "grep", "find"}) {
		t.Fatalf("modifiers = %v", got)
	}
	if got := load([]string{"+grep"}, []string{"read", "+ls"}); !reflect.DeepEqual(got, []string{"read", "ls"}) {
		t.Fatalf("plain project list = %v", got)
	}
}
