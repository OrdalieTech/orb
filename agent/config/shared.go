package config

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
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

// sharedDefaults are the entries for settings that travel as one: a model is its provider and id
// together, so two devices' choices never mix into a pair neither made.
var sharedDefaults = map[string][]string{"model": {"defaultProvider", "defaultModel"}, "thinking": {"defaultThinkingLevel"}}

// localPlugins run against their device's programs, peers or permissions.
var localPlugins = []string{"claude-sessions", "mitosis", "mitosis-agent-calls", "bridge", "bridge-agent-calls", "permissions", "plugin-control"}

const (
	pluginEntry = "plugins/"
	levelEntry  = "levels/" // a model's own reasoning level, by "provider/id" as modelThinkingLevels keys it
)

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
	for entry, keys := range sharedDefaults {
		if values := defaultValues(object, keys); values != nil {
			out[entry], _ = json.Marshal(values)
		}
	}
	for name, enabled := range pluginGates(object) {
		if !slices.Contains(localPlugins, name) {
			out[pluginEntry+name], _ = json.Marshal(enabled)
		}
	}
	for model, level := range nestedStrings(object, "modelThinkingLevels") {
		out[levelEntry+model], _ = json.Marshal(level)
	}
	return out, nil
}

// MergeShared takes the group's settings, and an owner takes a member's hand-in over its own too:
// a member hands in only what it changed since the group's version, and Mitosis has already dropped
// a change made over a value the group or this owner changed since.
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
				if model, ok := strings.CutPrefix(key, levelEntry); ok {
					var level string
					if json.Unmarshal(raw, &level) != nil || level == "" || nestedStrings(object, "modelThinkingLevels")[model] == level {
						continue
					}
					setNested(&object, "modelThinkingLevels", model, raw)
					changed = append(changed, key)
					continue
				}
				keys, ok := sharedDefaults[key]
				var next map[string]string
				if !ok || json.Unmarshal(raw, &next) != nil || len(next) != len(keys) || maps.Equal(next, defaultValues(object, keys)) {
					continue
				}
				if slices.ContainsFunc(keys, func(k string) bool { return next[k] == "" }) {
					continue
				}
				for _, k := range keys {
					value, _ := json.Marshal(next[k])
					object.Set(k, value)
				}
				changed = append(changed, key)
			}
		}
		for _, key := range removed {
			if keepOwn {
				break
			}
			if keys, ok := sharedDefaults[key]; ok && defaultValues(object, keys) != nil {
				for _, k := range keys {
					object.Delete(k)
				}
				changed = append(changed, key)
			}
			if model, ok := strings.CutPrefix(key, levelEntry); ok {
				if _, set := nestedStrings(object, "modelThinkingLevels")[model]; set {
					setNested(&object, "modelThinkingLevels", model, nil)
					changed = append(changed, key)
				}
			}
			// A gate the group no longer holds is gone here too, back to its default (off): a
			// device keeping its own would hand it in again as new. A plugin's object keeps its
			// settings, off.
			if name, ok := strings.CutPrefix(key, pluginEntry); ok && !slices.Contains(localPlugins, name) {
				if _, set := gates[name]; !set {
					continue
				}
				var gate bool
				if json.Unmarshal(pluginValue(object, name), &gate) == nil {
					setNested(&object, "plugins", name, nil)
				} else {
					setPluginGate(&object, name, false)
				}
				delete(gates, name)
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

// nestedStrings reads field as an object of strings, as modelThinkingLevels is.
func nestedStrings(object jsonwire.RawObject, field string) map[string]string {
	out := map[string]string{}
	raw, ok := object.Get(field)
	if !ok {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// setNested sets field.key to value, or deletes it for nil, dropping an emptied field.
func setNested(object *jsonwire.RawObject, field, key string, value json.RawMessage) {
	nested := jsonwire.RawObject{}
	if raw, ok := object.Get(field); ok {
		if parsed, err := parseSettingsObject(raw); err == nil {
			nested = parsed
		}
	}
	if value == nil {
		nested.Delete(key)
	} else {
		nested.Set(key, value)
	}
	if len(nested) == 0 {
		object.Delete(field)
		return
	}
	encoded, _ := nested.MarshalJSON()
	object.Set(field, encoded)
}

// pluginValue is name's value in the plugins object: a gate, or the plugin's settings object.
func pluginValue(object jsonwire.RawObject, name string) json.RawMessage {
	raw, _ := object.Get("plugins")
	plugins, err := parseSettingsObject(raw)
	if err != nil {
		return nil
	}
	value, _ := plugins.Get(name)
	return value
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

// defaultValues reads keys as the strings the setters write; nil unless every one is set.
func defaultValues(object jsonwire.RawObject, keys []string) map[string]string {
	values := map[string]string{}
	for _, k := range keys {
		raw, _ := object.Get(k)
		var value string
		if json.Unmarshal(raw, &value) != nil || value == "" {
			return nil
		}
		values[k] = value
	}
	return values
}
