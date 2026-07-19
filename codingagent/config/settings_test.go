package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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
		"enabledModels":        []string{"sonnet:high", "openai/*"},
		"goExtensions":         map[string]any{"pirate": true, "status-line": false, "invalid": "yes"},
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
	if got := manager.GetEnabledModels(); !reflect.DeepEqual(got, []string{"sonnet:high", "openai/*"}) {
		t.Fatalf("enabled models = %#v", got)
	}
	if got := manager.GetGoExtensions(); !reflect.DeepEqual(got, map[string]bool{"pirate": true, "status-line": false}) {
		t.Fatalf("Go extensions = %#v", got)
	}

	empty, err := NewSettingsManager(root, WithAgentDir(filepath.Join(root, "empty-agent")))
	if err != nil {
		t.Fatal(err)
	}
	if empty.GetTransport() != ai.TransportAuto || empty.GetSteeringMode() != "one-at-a-time" || empty.GetFollowUpMode() != "one-at-a-time" {
		t.Fatal("message delivery defaults differ")
	}
	if empty.GetEnabledModels() != nil {
		t.Fatalf("absent enabled models = %#v", empty.GetEnabledModels())
	}
}

func TestBlockImagesSettingReadsAndWrites(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"images": map[string]any{"blockImages": true},
	})
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if !manager.GetBlockImages() {
		t.Fatal("blockImages setting was not loaded")
	}
	manager.SetBlockImages(false)
	if manager.GetBlockImages() {
		t.Fatal("blockImages setting was not updated")
	}
	reloaded, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.GetBlockImages() {
		t.Fatal("blockImages setting was not persisted")
	}
}

func TestHarnessPolicySettings(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"compaction":                map[string]any{"enabled": false, "reserveTokens": 1200, "keepRecentTokens": 300},
		"branchSummary":             map[string]any{"reserveTokens": 900, "skipPrompt": true},
		"httpIdleTimeoutMs":         1234.9,
		"websocketConnectTimeoutMs": 5678,
		"thinkingBudgets":           map[string]any{"minimal": 1, "low": 2, "medium": 3, "high": 4},
		"retry": map[string]any{
			"enabled": false, "maxRetries": 4, "baseDelayMs": 25,
			"provider": map[string]any{"timeoutMs": 50, "maxRetries": 2, "maxRetryDelayMs": 75},
		},
	})
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.GetCompactionSettings(); got != (CompactionSettings{false, 1200, 300}) {
		t.Fatalf("compaction = %#v", got)
	}
	if got := manager.GetBranchSummarySettings(); got != (BranchSummarySettings{900, true}) {
		t.Fatalf("branch summary = %#v", got)
	}
	if got := manager.GetRetrySettings(); got != (RetrySettings{false, 4, 25}) {
		t.Fatalf("retry = %#v", got)
	}
	provider := manager.GetProviderRetrySettings()
	if provider.TimeoutMS == nil || *provider.TimeoutMS != 50 || provider.MaxRetries == nil || *provider.MaxRetries != 2 || provider.MaxRetryDelayMS != 75 {
		t.Fatalf("provider retry = %#v", provider)
	}
	if got, err := manager.GetHTTPIdleTimeoutMS(); err != nil || got != 1234 {
		t.Fatalf("http idle timeout = %d, %v", got, err)
	}
	if got, err := manager.GetWebSocketConnectTimeoutMS(); err != nil || got == nil || *got != 5678 {
		t.Fatalf("websocket timeout = %#v, %v", got, err)
	}
	budgets := manager.GetThinkingBudgets()
	if budgets == nil || budgets.Minimal == nil || *budgets.Minimal != 1 || budgets.Low == nil || *budgets.Low != 2 || budgets.Medium == nil || *budgets.Medium != 3 || budgets.High == nil || *budgets.High != 4 {
		t.Fatalf("thinking budgets = %#v", budgets)
	}

	empty, err := NewSettingsManager(root, WithAgentDir(filepath.Join(root, "empty")))
	if err != nil {
		t.Fatal(err)
	}
	if got := empty.GetCompactionSettings(); got != (CompactionSettings{true, 16384, 20000}) {
		t.Fatalf("default compaction = %#v", got)
	}
	if got := empty.GetBranchSummarySettings(); got != (BranchSummarySettings{16384, false}) {
		t.Fatalf("default branch summary = %#v", got)
	}
	if got := empty.GetRetrySettings(); got != (RetrySettings{true, 3, 2000}) {
		t.Fatalf("default retry = %#v", got)
	}
	if got := empty.GetProviderRetrySettings(); got.TimeoutMS != nil || got.MaxRetries != nil || got.MaxRetryDelayMS != 60000 {
		t.Fatalf("default provider retry = %#v", got)
	}
	if got, err := empty.GetHTTPIdleTimeoutMS(); err != nil || got != 300000 {
		t.Fatalf("default http idle timeout = %d, %v", got, err)
	}
	if got, err := empty.GetWebSocketConnectTimeoutMS(); err != nil || got != nil {
		t.Fatalf("default websocket timeout = %#v, %v", got, err)
	}
	if got := empty.GetThinkingBudgets(); got != nil {
		t.Fatalf("default thinking budgets = %#v", got)
	}
}

func TestInvalidTransportTimeoutSettings(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"httpIdleTimeoutMs":         -1,
		"websocketConnectTimeoutMs": "soon",
	})
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetHTTPIdleTimeoutMS(); err == nil || err.Error() != "Invalid httpIdleTimeoutMs setting: -1" {
		t.Fatalf("http timeout error = %v", err)
	}
	if _, err := manager.GetWebSocketConnectTimeoutMS(); err == nil || err.Error() != "Invalid websocketConnectTimeoutMs setting: soon" {
		t.Fatalf("websocket timeout error = %v", err)
	}
}

func TestTransportTimeoutStringSettingsMatchUpstream(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name    string
		value   any
		want    int64
		wantErr string
	}{
		{name: "disabled", value: " DISABLED ", want: 0},
		{name: "numeric", value: " 1234.9 ", want: 1234},
		{name: "empty", value: "", wantErr: "Invalid httpIdleTimeoutMs setting: "},
		{name: "null", value: nil, wantErr: "Invalid httpIdleTimeoutMs setting: null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			agentDir := filepath.Join(root, test.name)
			writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{"httpIdleTimeoutMs": test.value})
			manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
			if err != nil {
				t.Fatal(err)
			}
			got, err := manager.GetHTTPIdleTimeoutMS()
			if test.wantErr != "" {
				if err == nil || err.Error() != test.wantErr {
					t.Fatalf("timeout error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("timeout = %d, %v; want %d", got, err, test.want)
			}
		})
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

func TestHTTPIdleTimeoutSettingRoundTripAndLabels(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	manager, err := NewSettingsManager(root, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager.SetHTTPIdleTimeoutMS(60_000)
	if got, err := manager.GetHTTPIdleTimeoutMS(); err != nil || got != 60_000 {
		t.Fatalf("GetHTTPIdleTimeoutMS = %d, %v", got, err)
	}
	manager.SetHTTPIdleTimeoutMS(0)
	if got, err := manager.GetHTTPIdleTimeoutMS(); err != nil || got != 0 {
		t.Fatalf("disabled GetHTTPIdleTimeoutMS = %d, %v", got, err)
	}
	manager.SetHTTPIdleTimeoutMS(-5)
	if got, err := manager.GetHTTPIdleTimeoutMS(); err != nil || got != 0 {
		t.Fatalf("negative write must be rejected; got %d, %v", got, err)
	}
	// Labels and order pin upstream http-dispatcher HTTP_IDLE_TIMEOUT_CHOICES.
	labels := make([]string, 0, len(HTTPIdleTimeoutChoices))
	for _, choice := range HTTPIdleTimeoutChoices {
		labels = append(labels, choice.Label)
	}
	want := []string{"30 sec", "1 min", "2 min", "5 min", "disabled"}
	if len(labels) != len(want) {
		t.Fatalf("choices = %v", labels)
	}
	for index := range want {
		if labels[index] != want[index] {
			t.Fatalf("choices = %v, want %v", labels, want)
		}
	}
	if FormatHTTPIdleTimeoutMS(300_000) != "5 min" || FormatHTTPIdleTimeoutMS(0) != "disabled" || FormatHTTPIdleTimeoutMS(1_500) != "1.5 sec" {
		t.Fatalf("FormatHTTPIdleTimeoutMS labels diverge: %q %q %q",
			FormatHTTPIdleTimeoutMS(300_000), FormatHTTPIdleTimeoutMS(0), FormatHTTPIdleTimeoutMS(1_500))
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

func TestThemeSettingsAndPersistence(t *testing.T) {
	agentDir, projectDir := t.TempDir(), t.TempDir()
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"theme":  "light/dark",
		"themes": []string{"themes/global.json"},
		"markdown": map[string]any{
			"codeBlockIndent": ">>",
		},
	})
	manager, err := NewSettingsManager(projectDir, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetThemeSetting() != "light/dark" || manager.GetTheme() != "" {
		t.Fatalf("theme setting = %q, simple = %q", manager.GetThemeSetting(), manager.GetTheme())
	}
	if got := manager.GetThemePaths(); !reflect.DeepEqual(got, []string{"themes/global.json"}) {
		t.Fatalf("theme paths = %#v", got)
	}
	if manager.GetMarkdownCodeBlockIndent() != ">>" {
		t.Fatalf("markdown indent = %q", manager.GetMarkdownCodeBlockIndent())
	}
	manager.SetTheme("custom")
	reloaded, err := NewSettingsManager(projectDir, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.GetTheme() != "custom" || reloaded.GetThemeSetting() != "custom" {
		t.Fatalf("persisted theme = %q", reloaded.GetThemeSetting())
	}
	if got := reloaded.GetSettings()["markdown"]; got == nil {
		t.Fatal("SetTheme discarded unrelated settings")
	}
}

func TestMarkdownIndentDefaults(t *testing.T) {
	manager, err := NewSettingsManager(t.TempDir(), WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetMarkdownCodeBlockIndent() != "  " {
		t.Fatalf("default indent = %q", manager.GetMarkdownCodeBlockIndent())
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

func TestSettingsResourcePathAndSkillCommandGetters(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	cwd := filepath.Join(root, "project")
	writeSettings(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"skills": []string{"global-skills"}, "prompts": []string{"global-prompts"},
		"enableSkillCommands": false,
	})
	writeSettings(t, filepath.Join(cwd, ".pi", "settings.json"), map[string]any{
		"skills": []string{"project-skills"}, "prompts": []string{"project-prompts"},
	})
	manager, err := NewSettingsManager(cwd, WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manager.GetGlobalSkillPaths(), []string{"global-skills"}) ||
		!reflect.DeepEqual(manager.GetProjectSkillPaths(), []string{"project-skills"}) ||
		!reflect.DeepEqual(manager.GetGlobalPromptTemplatePaths(), []string{"global-prompts"}) ||
		!reflect.DeepEqual(manager.GetProjectPromptTemplatePaths(), []string{"project-prompts"}) {
		t.Fatalf("resource paths = %#v %#v %#v %#v", manager.GetGlobalSkillPaths(), manager.GetProjectSkillPaths(), manager.GetGlobalPromptTemplatePaths(), manager.GetProjectPromptTemplatePaths())
	}
	if manager.GetEnableSkillCommands() {
		t.Fatal("enableSkillCommands override was ignored")
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
