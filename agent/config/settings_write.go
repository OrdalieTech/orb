package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/host"
	"github.com/OrdalieTech/orb/internal/filelock"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

func parseSettingsObject(data []byte) (jsonwire.RawObject, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if len(bytes.TrimSpace(data)) == 0 {
		return jsonwire.RawObject{}, nil
	}
	object, ok := jsonwire.ParseRawObject(data)
	if !ok {
		return nil, errors.New("settings must be a JSON object")
	}
	return object, nil
}

// indentSettings lays object out as JSON.stringify(object, null, 2) does,
// keeping each value's number and string spelling.
func indentSettings(object jsonwire.RawObject) ([]byte, error) {
	compact, _ := object.MarshalJSON()
	var output bytes.Buffer
	err := json.Indent(&output, compact, "", "  ")
	return output.Bytes(), err
}

func encodeSetting(value any) (json.RawMessage, error) {
	encoded, err := jsonwire.Marshal(value)
	return json.RawMessage(encoded), err
}

func migrateSettingsObject(object jsonwire.RawObject) (jsonwire.RawObject, error) {
	if queueMode, exists := object.Get("queueMode"); exists {
		if _, hasSteeringMode := object.Get("steeringMode"); !hasSteeringMode {
			object.Set("steeringMode", queueMode)
			object.Delete("queueMode")
		}
	}
	if websockets, exists := object.Get("websockets"); exists {
		if _, hasTransport := object.Get("transport"); !hasTransport {
			var enabled bool
			if json.Unmarshal(websockets, &enabled) == nil {
				transport := "sse"
				if enabled {
					transport = "websocket"
				}
				encoded, err := encodeSetting(transport)
				if err != nil {
					return nil, err
				}
				object.Set("transport", encoded)
				object.Delete("websockets")
			}
		}
	}
	if skillsRaw, exists := object.Get("skills"); exists {
		if skills, err := parseSettingsObject(skillsRaw); err == nil {
			if enabled, present := skills.Get("enableSkillCommands"); present {
				if _, alreadySet := object.Get("enableSkillCommands"); !alreadySet {
					object.Set("enableSkillCommands", enabled)
				}
			}
			var directories []json.RawMessage
			customDirectories, present := skills.Get("customDirectories")
			if present && json.Unmarshal(customDirectories, &directories) == nil && len(directories) > 0 {
				object.Set("skills", customDirectories)
			} else {
				object.Delete("skills")
			}
		}
	}
	if retryRaw, exists := object.Get("retry"); exists {
		if retry, err := parseSettingsObject(retryRaw); err == nil {
			if delay, hasDelay := retry.Get("maxDelayMs"); hasDelay && json.Valid(delay) {
				var numeric json.Number
				decoder := json.NewDecoder(bytes.NewReader(delay))
				decoder.UseNumber()
				if decoder.Decode(&numeric) == nil {
					provider := jsonwire.RawObject{}
					if raw, hasProvider := retry.Get("provider"); hasProvider {
						if decoded, decodeErr := parseSettingsObject(raw); decodeErr == nil {
							provider = decoded
						}
					}
					current, hasCurrent := provider.Get("maxRetryDelayMs")
					if !hasCurrent || bytes.Equal(bytes.TrimSpace(current), []byte("null")) {
						provider.Set("maxRetryDelayMs", delay)
						encoded, encodeErr := indentSettings(provider)
						if encodeErr != nil {
							return nil, encodeErr
						}
						retry.Set("provider", encoded)
					}
				}
			}
			retry.Delete("maxDelayMs")
			encoded, encodeErr := indentSettings(retry)
			if encodeErr != nil {
				return nil, encodeErr
			}
			object.Set("retry", encoded)
		}
	}
	return object, nil
}

// fileDocument is a kernel file shared with upstream pi: updates hold its
// proper-lockfile lock and rewrite the file in place, as pi does.
func fileDocument(path string, perm os.FileMode) host.Document {
	return filelock.File{Path: path, Perm: perm}
}

// authFile is auth.json: upstream's lock on it heartbeats, so a dead
// holder's lock takes AsyncStale to expire.
func authFile(path string) host.Document {
	return filelock.File{Path: path, Perm: 0o600, Stale: filelock.AsyncStale}
}

func writeGlobalSettings(path string, values jsonwire.RawObject, nestedField, nestedKey string, nestedValue json.RawMessage) error {
	return fileDocument(path, 0o644).Update(context.Background(), func(current []byte) ([]byte, error) {
		return updatedSettings(current, values, nestedField, nestedKey, nestedValue)
	})
}

func updatedSettings(current []byte, values jsonwire.RawObject, nestedField, nestedKey string, nestedValue json.RawMessage) ([]byte, error) {
	object, err := parseSettingsObject(current)
	if err != nil {
		return nil, err
	}
	object, err = migrateSettingsObject(object)
	if err != nil {
		return nil, err
	}
	for _, value := range values {
		object.Set(value.Name, value.Value)
	}
	if nestedField != "" {
		raw, exists := object.Get(nestedField)
		nested := jsonwire.RawObject{}
		if exists {
			if decoded, decodeErr := parseSettingsObject(raw); decodeErr == nil {
				nested = decoded
			}
		}
		// A nil nestedValue deletes the key; an emptied object drops the
		// whole field rather than leaving "{}" behind.
		if nestedValue == nil {
			nested.Delete(nestedKey)
		} else {
			nested.Set(nestedKey, nestedValue)
		}
		if len(nested) == 0 {
			object.Delete(nestedField)
		} else {
			raw, err = indentSettings(nested)
			if err != nil {
				return nil, err
			}
			object.Set(nestedField, raw)
		}
	}
	return indentSettings(object)
}

func (manager *SettingsManager) writeGlobalSettings(values jsonwire.RawObject, nestedField, nestedKey string, nestedValue json.RawMessage) error {
	if manager.globalDocument == nil {
		return writeGlobalSettings(manager.globalPath, values, nestedField, nestedKey, nestedValue)
	}
	return manager.globalDocument.Update(context.Background(), func(current []byte) ([]byte, error) {
		return updatedSettings(current, values, nestedField, nestedKey, nestedValue)
	})
}

// setGlobalValues persists first and only then advances in-memory state: a
// setter that reported the new value while the file still held the old one
// left the manager permanently disagreeing with disk, and the void signature
// makes DrainErrors the only place a caller ever learns of the failure.
func (manager *SettingsManager) setGlobalValues(values ...jsonwire.RawMember) {
	decoded := make([]any, len(values))
	for index, value := range values {
		decoder := json.NewDecoder(bytes.NewReader(value.Value))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded[index]); err != nil {
			panic(fmt.Sprintf("config: invalid setting value: %v", err))
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.globalLoadError {
		if err := manager.writeGlobalSettings(jsonwire.RawObject(values), "", "", nil); err != nil {
			manager.errors = append(manager.errors, SettingsError{Scope: GlobalSettings, Err: err})
			return
		}
	}
	for index, value := range values {
		manager.global[value.Name] = decoded[index]
	}
	manager.effective = mergeSettings(manager.global, manager.project)
}

func (manager *SettingsManager) setGlobalNested(field, key string, value any) {
	raw, err := encodeSetting(value)
	if err != nil {
		panic(fmt.Sprintf("config: invalid setting value: %v", err))
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.globalLoadError {
		if err := manager.writeGlobalSettings(nil, field, key, raw); err != nil {
			manager.errors = append(manager.errors, SettingsError{Scope: GlobalSettings, Err: err})
			return
		}
	}
	object := nestedObject(manager.global, field)
	if object == nil {
		object = map[string]any{}
	} else {
		object = cloneMap(object)
	}
	object[key] = cloneValue(value)
	manager.global[field] = object
	manager.effective = mergeSettings(manager.global, manager.project)
}

func settingMember(name string, value any) jsonwire.RawMember {
	raw, err := encodeSetting(value)
	if err != nil {
		panic(fmt.Sprintf("config: invalid setting value: %v", err))
	}
	return jsonwire.RawMember{Name: name, Value: raw}
}

func (manager *SettingsManager) SetDefaultModelAndProvider(provider, modelID string) {
	manager.setGlobalValues(settingMember("defaultProvider", provider), settingMember("defaultModel", modelID))
}

func (manager *SettingsManager) SetDefaultThinkingLevel(level ai.ModelThinkingLevel) {
	manager.setGlobalValues(settingMember("defaultThinkingLevel", level))
}

func (manager *SettingsManager) SetModelThinkingLevel(provider, modelID string, level ai.ModelThinkingLevel) {
	manager.setGlobalNested("modelThinkingLevels", provider+"/"+modelID, string(level))
}

func (manager *SettingsManager) SetSteeringMode(mode string) {
	manager.setGlobalValues(settingMember("steeringMode", mode))
}

func (manager *SettingsManager) SetFollowUpMode(mode string) {
	manager.setGlobalValues(settingMember("followUpMode", mode))
}

func (manager *SettingsManager) SetShowImages(show bool) {
	manager.setGlobalNested("terminal", "showImages", show)
}

func (manager *SettingsManager) SetImageWidthCells(width int) {
	manager.setGlobalNested("terminal", "imageWidthCells", max(1, width))
}

func (manager *SettingsManager) SetHideThinkingBlock(hidden bool) {
	manager.setGlobalValues(settingMember("hideThinkingBlock", hidden))
}

func (manager *SettingsManager) SetMermaidRenderingMode(mode string) {
	manager.setGlobalNested("markdown", "mermaid", mode)
}

func (manager *SettingsManager) SetShowCacheMissNotices(show bool) {
	manager.setGlobalValues(settingMember("showCacheMissNotices", show))
}

func (manager *SettingsManager) SetQuietStartup(quiet string) {
	var value any = quiet == "true"
	if quiet == "header" {
		value = quiet
	}
	manager.setGlobalValues(settingMember("quietStartup", value))
}

func (manager *SettingsManager) SetDefaultProjectTrust(value string) {
	manager.setGlobalValues(settingMember("defaultProjectTrust", value))
}

func (manager *SettingsManager) SetDoubleEscapeAction(action string) {
	manager.setGlobalValues(settingMember("doubleEscapeAction", action))
}

func (manager *SettingsManager) SetTreeFilterMode(mode string) {
	manager.setGlobalValues(settingMember("treeFilterMode", mode))
}

func (manager *SettingsManager) SetShowHardwareCursor(enabled bool) {
	manager.setGlobalValues(settingMember("showHardwareCursor", enabled))
}

func (manager *SettingsManager) SetEditorPaddingX(padding int) {
	manager.setGlobalValues(settingMember("editorPaddingX", max(0, min(3, padding))))
}

func (manager *SettingsManager) SetOutputPad(padding int) {
	if padding != 0 {
		padding = 1
	}
	manager.setGlobalValues(settingMember("outputPad", padding))
}

func (manager *SettingsManager) SetAutocompleteMaxVisible(maxVisible int) {
	manager.setGlobalValues(settingMember("autocompleteMaxVisible", max(3, min(20, maxVisible))))
}

func (manager *SettingsManager) SetClearOnShrink(enabled bool) {
	manager.setGlobalNested("terminal", "clearOnShrink", enabled)
}

func (manager *SettingsManager) SetShowTerminalProgress(enabled bool) {
	manager.setGlobalNested("terminal", "showTerminalProgress", enabled)
}

func (manager *SettingsManager) SetImageAutoResize(enabled bool) {
	manager.setGlobalNested("images", "autoResize", enabled)
}

func (manager *SettingsManager) SetEnableSkillCommands(enabled bool) {
	manager.setGlobalValues(settingMember("enableSkillCommands", enabled))
}

// SetPluginEnabled persists a user-level gate while project settings continue
// to overlay it through the existing one-level merge.
func (manager *SettingsManager) SetPluginEnabled(name string, enabled bool) {
	manager.mu.RLock()
	configured := nestedObject(nestedObject(manager.global, "plugins"), name)
	if configured != nil {
		configured = cloneMap(configured)
	}
	manager.mu.RUnlock()
	if configured != nil {
		configured["enabled"] = enabled
		manager.setGlobalNested("plugins", name, configured)
		return
	}
	manager.setGlobalNested("plugins", name, enabled)
}

// SetPluginSetting persists one value without discarding the plugin's rules.
func (manager *SettingsManager) SetPluginSetting(name, key string, value any) {
	manager.mu.RLock()
	raw := nestedObject(manager.global, "plugins")[name]
	configured := nestedObject(nestedObject(manager.global, "plugins"), name)
	if configured != nil {
		configured = cloneMap(configured)
	}
	manager.mu.RUnlock()
	if configured == nil {
		// Preserve an explicit boolean gate when promoting it to the object
		// form: writing a setting must never flip the plugin on as a side
		// effect.
		enabled := true
		if gate, isBool := raw.(bool); isBool {
			enabled = gate
		}
		configured = map[string]any{"enabled": enabled}
	}
	configured[key] = cloneValue(value)
	manager.setGlobalNested("plugins", name, configured)
}

// GlobalPluginSettings returns one plugin's structured configuration from the
// global scope only — the scope the plugin setters write. Read-modify-write
// flows must use it instead of the merged view, which project settings can
// shadow.
func (manager *SettingsManager) GlobalPluginSettings(name string) map[string]any {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	configured := nestedObject(nestedObject(manager.global, "plugins"), name)
	if configured == nil {
		return nil
	}
	return cloneMap(configured)
}

// ProjectDefinesPlugin reports whether the project settings define the plugin
// at all; the one-level merge then shadows the whole global object.
func (manager *SettingsManager) ProjectDefinesPlugin(name string) bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	_, exists := nestedObject(manager.project, "plugins")[name]
	return exists
}

func (manager *SettingsManager) SetTransport(transport ai.Transport) {
	manager.setGlobalValues(settingMember("transport", transport))
}

// SetHTTPIdleTimeoutMS persists the provider idle timeout; negative values are
// rejected upstream before reaching the store, so they are ignored here.
func (manager *SettingsManager) SetHTTPIdleTimeoutMS(timeoutMS int64) {
	if timeoutMS < 0 {
		return
	}
	manager.setGlobalValues(settingMember("httpIdleTimeoutMs", timeoutMS))
}

func (manager *SettingsManager) SetEnabledModels(models []string) {
	manager.setGlobalValues(settingMember("enabledModels", append([]string(nil), models...)))
}

func (manager *SettingsManager) SetCompactionEnabled(enabled bool) {
	manager.setGlobalNested("compaction", "enabled", enabled)
}

func (manager *SettingsManager) SetRetryEnabled(enabled bool) {
	manager.setGlobalNested("retry", "enabled", enabled)
}

func (manager *SettingsManager) SetBlockImages(blocked bool) {
	manager.setGlobalNested("images", "blockImages", blocked)
}
