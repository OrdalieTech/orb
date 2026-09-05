package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/filelock"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

type settingsMember struct {
	name  string
	value json.RawMessage
}

type settingsObject []settingsMember

func parseSettingsObject(data []byte) (settingsObject, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if len(bytes.TrimSpace(data)) == 0 {
		return settingsObject{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("settings must be a JSON object")
	}
	object := settingsObject{}
	for decoder.More() {
		nameStart := decoder.InputOffset()
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		name, err := jsonwire.UnmarshalStringToken(data[nameStart:decoder.InputOffset()])
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object = object.set(name, value)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("settings contain multiple JSON values")
		}
		return nil, err
	}
	return object, nil
}

func (object settingsObject) get(name string) (json.RawMessage, bool) {
	for _, member := range object {
		if member.name == name {
			return append(json.RawMessage(nil), member.value...), true
		}
	}
	return nil, false
}

func (object settingsObject) set(name string, value json.RawMessage) settingsObject {
	value = append(json.RawMessage(nil), value...)
	for index := range object {
		if object[index].name == name {
			object[index].value = value
			return object
		}
	}
	return append(object, settingsMember{name: name, value: value})
}

func (object settingsObject) delete(name string) settingsObject {
	for index := range object {
		if object[index].name == name {
			return append(object[:index], object[index+1:]...)
		}
	}
	return object
}

func (object settingsObject) marshalIndented() ([]byte, error) {
	var compact bytes.Buffer
	compact.WriteByte('{')
	for index, member := range object {
		if index > 0 {
			compact.WriteByte(',')
		}
		name, err := jsonwire.MarshalString(member.name)
		if err != nil {
			return nil, err
		}
		compact.Write(name)
		compact.WriteByte(':')
		if len(member.value) == 0 {
			compact.WriteString("null")
		} else {
			compact.Write(member.value)
		}
	}
	compact.WriteByte('}')
	var output bytes.Buffer
	if err := json.Indent(&output, compact.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func encodeSetting(value any) (json.RawMessage, error) {
	encoded, err := jsonwire.Marshal(value)
	return json.RawMessage(encoded), err
}

func migrateSettingsObject(object settingsObject) (settingsObject, error) {
	if queueMode, exists := object.get("queueMode"); exists {
		if _, hasSteeringMode := object.get("steeringMode"); !hasSteeringMode {
			object = object.set("steeringMode", queueMode).delete("queueMode")
		}
	}
	if websockets, exists := object.get("websockets"); exists {
		if _, hasTransport := object.get("transport"); !hasTransport {
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
				object = object.set("transport", encoded).delete("websockets")
			}
		}
	}
	if skillsRaw, exists := object.get("skills"); exists {
		if skills, err := parseSettingsObject(skillsRaw); err == nil {
			if enabled, present := skills.get("enableSkillCommands"); present {
				if _, alreadySet := object.get("enableSkillCommands"); !alreadySet {
					object = object.set("enableSkillCommands", enabled)
				}
			}
			var directories []json.RawMessage
			customDirectories, present := skills.get("customDirectories")
			if present && json.Unmarshal(customDirectories, &directories) == nil && len(directories) > 0 {
				object = object.set("skills", customDirectories)
			} else {
				object = object.delete("skills")
			}
		}
	}
	if retryRaw, exists := object.get("retry"); exists {
		if retry, err := parseSettingsObject(retryRaw); err == nil {
			if delay, hasDelay := retry.get("maxDelayMs"); hasDelay && json.Valid(delay) {
				var numeric json.Number
				decoder := json.NewDecoder(bytes.NewReader(delay))
				decoder.UseNumber()
				if decoder.Decode(&numeric) == nil {
					provider := settingsObject{}
					if raw, hasProvider := retry.get("provider"); hasProvider {
						if decoded, decodeErr := parseSettingsObject(raw); decodeErr == nil {
							provider = decoded
						}
					}
					current, hasCurrent := provider.get("maxRetryDelayMs")
					if !hasCurrent || bytes.Equal(bytes.TrimSpace(current), []byte("null")) {
						provider = provider.set("maxRetryDelayMs", delay)
						encoded, encodeErr := provider.marshalIndented()
						if encodeErr != nil {
							return nil, encodeErr
						}
						retry = retry.set("provider", encoded)
					}
				}
			}
			retry = retry.delete("maxDelayMs")
			encoded, encodeErr := retry.marshalIndented()
			if encodeErr != nil {
				return nil, encodeErr
			}
			object = object.set("retry", encoded)
		}
	}
	return object, nil
}

func withSettingsLock(path string, operation func() error) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	release, err := filelock.Acquire(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	return operation()
}

func writeGlobalSettings(path string, values settingsObject, nestedField, nestedKey string, nestedValue json.RawMessage) error {
	return withSettingsLock(path, func() error {
		current, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		object, err := parseSettingsObject(current)
		if err != nil {
			return err
		}
		object, err = migrateSettingsObject(object)
		if err != nil {
			return err
		}
		for _, value := range values {
			object = object.set(value.name, value.value)
		}
		if nestedField != "" {
			raw, exists := object.get(nestedField)
			nested := settingsObject{}
			if exists {
				if decoded, decodeErr := parseSettingsObject(raw); decodeErr == nil {
					nested = decoded
				}
			}
			// A nil nestedValue deletes the key; an emptied object drops the
			// whole field rather than leaving "{}" behind.
			if nestedValue == nil {
				nested = nested.delete(nestedKey)
			} else {
				nested = nested.set(nestedKey, nestedValue)
			}
			if len(nested) == 0 {
				object = object.delete(nestedField)
			} else {
				raw, err = nested.marshalIndented()
				if err != nil {
					return err
				}
				object = object.set(nestedField, raw)
			}
		}
		encoded, err := object.marshalIndented()
		if err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(encoded)
		return errors.Join(writeErr, file.Close())
	})
}

// setGlobalValues persists first and only then advances in-memory state: a
// setter that reported the new value while the file still held the old one
// left the manager permanently disagreeing with disk, and the void signature
// makes DrainErrors the only place a caller ever learns of the failure.
func (manager *SettingsManager) setGlobalValues(values ...settingsMember) {
	decoded := make([]any, len(values))
	for index, value := range values {
		decoder := json.NewDecoder(bytes.NewReader(value.value))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded[index]); err != nil {
			panic(fmt.Sprintf("config: invalid setting value: %v", err))
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.globalLoadError {
		if err := writeGlobalSettings(manager.globalPath, settingsObject(values), "", "", nil); err != nil {
			manager.errors = append(manager.errors, SettingsError{Scope: GlobalSettings, Err: err})
			return
		}
	}
	for index, value := range values {
		manager.global[value.name] = decoded[index]
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
		if err := writeGlobalSettings(manager.globalPath, nil, field, key, raw); err != nil {
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

func (manager *SettingsManager) removeGlobalNested(field, key string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if !manager.globalLoadError {
		if err := writeGlobalSettings(manager.globalPath, nil, field, key, nil); err != nil {
			manager.errors = append(manager.errors, SettingsError{Scope: GlobalSettings, Err: err})
			return
		}
	}
	if object := nestedObject(manager.global, field); object != nil {
		object = cloneMap(object)
		delete(object, key)
		if len(object) == 0 {
			delete(manager.global, field)
		} else {
			manager.global[field] = object
		}
	}
	manager.effective = mergeSettings(manager.global, manager.project)
}

func settingMember(name string, value any) settingsMember {
	raw, err := encodeSetting(value)
	if err != nil {
		panic(fmt.Sprintf("config: invalid setting value: %v", err))
	}
	return settingsMember{name: name, value: raw}
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

func (manager *SettingsManager) RemoveModelThinkingLevel(provider, modelID string) {
	manager.removeGlobalNested("modelThinkingLevels", provider+"/"+modelID)
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

func (manager *SettingsManager) SetQuietStartup(quiet bool) {
	manager.setGlobalValues(settingMember("quietStartup", quiet))
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
