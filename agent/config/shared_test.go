package config

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/OrdalieTech/orb/internal/document"
)

func sharedSettingsDoc(t *testing.T, data string) (SharedSettings, *document.Memory) {
	t.Helper()
	doc := &document.Memory{}
	if err := doc.Update(context.Background(), func([]byte) ([]byte, error) { return []byte(data), nil }); err != nil {
		t.Fatal(err)
	}
	return SharedSettings{Document: doc}, doc
}

func TestSharedSettingsShareDefaultsAndGates(t *testing.T) {
	s, _ := sharedSettingsDoc(t, `{
  "theme": "dark",
  "defaultProvider": "google",
  "defaultModel": "gemini-4-neon",
  "defaultThinkingLevel": "high",
  "plugins": {"memory": true, "tasks": false, "permissions": {"enabled": true, "mode": "auto"}, "claude-sessions": {"model": "opus"}, "memtree": {"enabled": false, "depth": 3}}
}`)
	got, err := s.Shareable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"defaultProvider": `"google"`, "defaultModel": `"gemini-4-neon"`, "defaultThinkingLevel": `"high"`,
		"plugins/memory": "true", "plugins/tasks": "false", "plugins/memtree": "false",
	}
	if len(got) != len(want) {
		t.Fatalf("shared %v, want the keys of %v", slices.Sorted(maps.Keys(got)), want)
	}
	for key, value := range want {
		if string(got[key]) != value {
			t.Errorf("%s = %s, want %s", key, got[key], value)
		}
	}
}

func TestSharedSettingsMergeKeepsDeviceSettings(t *testing.T) {
	s, doc := sharedSettingsDoc(t, `{
  "theme": "light",
  "defaultModel": "old",
  "plugins": {
    "memtree": {"enabled": true, "depth": 3},
    "permissions": {"enabled": false},
    "titles": true
  }
}`)
	incoming := map[string]json.RawMessage{
		"defaultProvider": json.RawMessage(`"google"`), "defaultModel": json.RawMessage(`"gemini-4-neon"`),
		"plugins/memtree": json.RawMessage(`false`), "plugins/memory": json.RawMessage(`true`),
		"plugins/permissions": json.RawMessage(`true`), "theme": json.RawMessage(`"dark"`),
	}
	changed, err := s.MergeShared(context.Background(), []map[string]json.RawMessage{incoming}, false, []string{"plugins/titles"})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(changed)
	if want := []string{"defaultModel", "defaultProvider", "plugins/memory", "plugins/memtree"}; !slices.Equal(changed, want) {
		t.Fatalf("changed %v, want %v", changed, want)
	}
	data, _ := doc.Read(context.Background())
	var got struct {
		Theme         string         `json:"theme"`
		DefaultModel  string         `json:"defaultModel"`
		DefaultProvdr string         `json:"defaultProvider"`
		Plugins       map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	memtree, _ := got.Plugins["memtree"].(map[string]any)
	permissions, _ := got.Plugins["permissions"].(map[string]any)
	switch {
	case got.Theme != "light", got.DefaultModel != "gemini-4-neon", got.DefaultProvdr != "google":
		t.Fatalf("top-level settings: %s", data)
	case memtree["enabled"] != false || memtree["depth"] != float64(3):
		t.Fatalf("memtree's gate changes beside its settings: %s", data)
	case permissions["enabled"] != false, got.Plugins["memory"] != true, got.Plugins["titles"] != true:
		t.Fatalf("plugins: %s", data)
	}

	// An owner folding a hand-in takes the member's change but drops nothing; a member drops a
	// default the group no longer shares.
	if changed, _ = s.MergeShared(context.Background(), []map[string]json.RawMessage{{"defaultModel": json.RawMessage(`"gemini-4-neon"`), "plugins/memory": json.RawMessage(`false`)}}, true, []string{"defaultModel"}); !slices.Equal(changed, []string{"plugins/memory"}) {
		t.Fatalf("keepOwn changed %v", changed)
	}
	if changed, _ = s.MergeShared(context.Background(), nil, false, []string{"defaultModel"}); !slices.Equal(changed, []string{"defaultModel"}) {
		t.Fatalf("removal changed %v", changed)
	}
	before, _ := doc.Read(context.Background())
	if changed, _ = s.MergeShared(context.Background(), []map[string]json.RawMessage{{"plugins/memory": json.RawMessage(`false`)}}, false, nil); len(changed) != 0 {
		t.Fatalf("a round that changes nothing changed %v", changed)
	}
	if after, _ := doc.Read(context.Background()); string(after) != string(before) {
		t.Fatalf("a round that changes nothing rewrote settings.json")
	}
}
