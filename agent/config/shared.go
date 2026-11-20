package config

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// SharedSettings is a global settings.json as the "settings" class (mitosis.Class): what a new
// conversation starts with on any device of the group, the default model, its provider and
// reasoning level, and each plugin's on/off gate. A device that cannot run the shared model
// starts on its preferred available one (agent.DefaultModel). Plugins bound to their device keep
// their gate there, and a plugin's own settings beside its gate never travel.
type SharedSettings struct{ Document document.Document }

// Editable: a member's change to a setting reaches the group (mitosis.EditableClass).
func (SharedSettings) Editable() {}

var sharedSettingKeys = []string{"defaultProvider", "defaultModel", "defaultThinkingLevel"}

// localPlugins run against their device's programs, peers or permissions.
var localPlugins = []string{"claude-sessions", "mitosis", "mitosis-agent-calls", "bridge", "bridge-agent-calls", "permissions", "plugin-control"}

const pluginEntry = "plugins/"

func (s SharedSettings) Shareable(ctx context.Context) (map[string]json.RawMessage, error) {
	data, err := s.Document.Read(ctx)
	if err != nil {
		return nil, err
	}
	object, err := parseSettingsObject(data)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, key := range sharedSettingKeys {
		if raw, ok := object.Get(key); ok && sharedScalar(raw) {
			out[key] = compactSetting(raw)
		}
	}
	for name, enabled := range pluginGates(object) {
		if !slices.Contains(localPlugins, name) {
			out[pluginEntry+name], _ = json.Marshal(enabled)
		}
	}
	return out, nil
}

// MergeShared takes the group's settings, and an owner takes a member's hand-in over its own too:
// a member hands in only what it changed since the group's version, so its change is the latest.
func (s SharedSettings) MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error) {
	var changed []string
	err := s.Document.Update(ctx, func(data []byte) ([]byte, error) {
		changed = nil
		object, err := parseSettingsObject(data)
		if err != nil {
			return nil, err
		}
		gates := pluginGates(object)
		for _, entries := range incoming {
			for key, raw := range entries {
				if name, ok := strings.CutPrefix(key, pluginEntry); ok {
					var enabled bool
					have, set := gates[name]
					if slices.Contains(localPlugins, name) || json.Unmarshal(raw, &enabled) != nil || set && have == enabled {
						continue
					}
					setPluginGate(&object, name, enabled)
					gates[name] = enabled
					changed = append(changed, key)
					continue
				}
				have, set := object.Get(key)
				if !slices.Contains(sharedSettingKeys, key) || !sharedScalar(raw) || set && bytes.Equal(compactSetting(have), compactSetting(raw)) {
					continue
				}
				object.Set(key, compactSetting(raw))
				changed = append(changed, key)
			}
		}
		for _, key := range removed {
			if _, plugin := strings.CutPrefix(key, pluginEntry); keepOwn || plugin || !slices.Contains(sharedSettingKeys, key) {
				continue // a gate the group stops sharing leaves the device's own as it is
			}
			if _, set := object.Get(key); set {
				object.Delete(key)
				changed = append(changed, key)
			}
		}
		if len(changed) == 0 {
			return data, nil
		}
		return indentSettings(object)
	})
	return changed, err
}

// pluginGates reads each plugin's gate as GetPlugins does: a boolean, or an object that is on
// unless its "enabled" says otherwise.
func pluginGates(object jsonwire.RawObject) map[string]bool {
	gates := map[string]bool{}
	raw, ok := object.Get("plugins")
	if !ok {
		return gates
	}
	plugins, err := parseSettingsObject(raw)
	if err != nil {
		return gates
	}
	for _, member := range plugins {
		var gate bool
		if json.Unmarshal(member.Value, &gate) == nil {
			gates[member.Name] = gate
			continue
		}
		if configured, err := parseSettingsObject(member.Value); err == nil {
			enabled, set := configured.Get("enabled")
			gates[member.Name] = !set || bytes.Equal(bytes.TrimSpace(enabled), []byte("true"))
		}
	}
	return gates
}

// setPluginGate writes a gate as SetPluginEnabled does: inside the plugin's object when it has one.
func setPluginGate(object *jsonwire.RawObject, name string, enabled bool) {
	gate, _ := json.Marshal(enabled)
	plugins := jsonwire.RawObject{}
	if raw, ok := object.Get("plugins"); ok {
		if parsed, err := parseSettingsObject(raw); err == nil {
			plugins = parsed
		}
	}
	value := json.RawMessage(gate)
	if raw, ok := plugins.Get(name); ok {
		if configured, err := parseSettingsObject(raw); err == nil {
			configured.Set("enabled", gate)
			value, _ = configured.MarshalJSON()
		}
	}
	plugins.Set(name, value)
	encoded, _ := plugins.MarshalJSON()
	object.Set("plugins", encoded)
}

// sharedScalar is a value the shared keys may hold: a string, as the setters write them.
func sharedScalar(raw json.RawMessage) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil && value != ""
}

func compactSetting(raw json.RawMessage) json.RawMessage {
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return raw
	}
	return out.Bytes()
}
