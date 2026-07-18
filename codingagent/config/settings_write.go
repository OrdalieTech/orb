package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonwire"
)

type settingsMember struct {
	name  string
	value json.RawMessage
}

type settingsObject []settingsMember

func parseSettingsObject(data []byte) (settingsObject, error) {
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
	release, err := acquireSettingsLock(path + ".lock")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	return operation()
}

func acquireSettingsLock(lockPath string) (func() error, error) {
	const (
		attempts = 10
		retry    = 20 * time.Millisecond
		stale    = 10 * time.Second
		update   = 5 * time.Second
	)
	for attempt := 1; attempt <= attempts; attempt++ {
		err := os.Mkdir(lockPath, 0o755)
		if err == nil {
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(update)
				defer ticker.Stop()
				for {
					select {
					case now := <-ticker.C:
						_ = os.Chtimes(lockPath, now, now)
					case <-stop:
						return
					}
				}
			}()
			return func() error {
				close(stop)
				<-done
				return os.Remove(lockPath)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > stale {
			if removeErr := os.Remove(lockPath); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				continue
			}
		}
		if attempt < attempts {
			time.Sleep(retry)
		}
	}
	return nil, fmt.Errorf("settings lock is already held: %s", lockPath)
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
			nested = nested.set(nestedKey, nestedValue)
			raw, err = nested.marshalIndented()
			if err != nil {
				return err
			}
			object = object.set(nestedField, raw)
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

func (manager *SettingsManager) setGlobalValues(values ...settingsMember) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for _, value := range values {
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(value.value))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			panic(fmt.Sprintf("config: invalid setting value: %v", err))
		}
		manager.global[value.name] = decoded
	}
	manager.effective = mergeSettings(manager.global, manager.project)
	if manager.globalLoadError {
		return
	}
	if err := writeGlobalSettings(manager.globalPath, settingsObject(values), "", "", nil); err != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: GlobalSettings, Err: err})
	}
}

func (manager *SettingsManager) setGlobalNested(field, key string, value any) {
	raw, err := encodeSetting(value)
	if err != nil {
		panic(fmt.Sprintf("config: invalid setting value: %v", err))
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	object := nestedObject(manager.global, field)
	if object == nil {
		object = map[string]any{}
	} else {
		object = cloneMap(object)
	}
	object[key] = cloneValue(value)
	manager.global[field] = object
	manager.effective = mergeSettings(manager.global, manager.project)
	if manager.globalLoadError {
		return
	}
	if err := writeGlobalSettings(manager.globalPath, nil, field, key, raw); err != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: GlobalSettings, Err: err})
	}
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

func (manager *SettingsManager) SetTransport(transport ai.Transport) {
	manager.setGlobalValues(settingMember("transport", transport))
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
