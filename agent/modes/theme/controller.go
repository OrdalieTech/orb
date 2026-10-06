package theme

import (
	"fmt"
	"os"
	"sync"
)

type AutoSetting struct {
	Light string
	Dark  string
}

func ParseAutoSetting(setting string) (AutoSetting, bool) {
	first := -1
	for index, character := range setting {
		if character != '/' {
			continue
		}
		if first >= 0 {
			return AutoSetting{}, false
		}
		first = index
	}
	if first < 0 {
		return AutoSetting{}, false
	}
	result := AutoSetting{Light: trim(setting[:first]), Dark: trim(setting[first+1:])}
	return result, result.Light != "" && result.Dark != ""
}

func ResolveSetting(setting string, terminal TerminalTheme) (string, bool) {
	if automatic, ok := ParseAutoSetting(setting); ok {
		if terminal == Light {
			return automatic.Light, true
		}
		return automatic.Dark, true
	}
	if setting == "" || containsSlash(setting) {
		return "", false
	}
	return setting, true
}

type Controller struct {
	mu       sync.RWMutex
	setting  string
	terminal TerminalTheme
	registry *Registry
	current  *Theme
	name     string
	onChange func()
}

func NewController(registry *Registry, setting string, terminal TerminalTheme, onChange func()) *Controller {
	controller := &Controller{registry: registry, onChange: onChange, terminal: terminal}
	if _, ok := ResolveSetting(setting, terminal); !ok {
		setting = string(terminal)
	}
	if err := controller.Set(setting); err != nil {
		_ = controller.Set("dark")
	}
	return controller
}

func (controller *Controller) Current() *Theme {
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	return controller.current
}
func (controller *Controller) Name() string {
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	return controller.name
}

func (controller *Controller) Available() []string { return controller.registry.Available() }

func (controller *Controller) Set(setting string) error {
	controller.mu.Lock()
	name, _ := ResolveSetting(setting, controller.terminal)
	err := controller.setLocked(name)
	if err == nil {
		controller.setting = setting
	}
	controller.mu.Unlock()
	if err == nil && controller.onChange != nil {
		controller.onChange()
	}
	return err
}

func (controller *Controller) setLocked(name string) error {
	value, ok := controller.registry.Get(name)
	if !ok {
		return fmt.Errorf("theme not found: %s", name)
	}
	controller.current, controller.name = value, name
	SetCurrent(value)
	return nil
}

// SetTerminalAppearance follows pairs, never explicit names or extension instances.
func (controller *Controller) SetTerminalAppearance(appearance TerminalTheme) {
	controller.mu.Lock()
	controller.terminal = appearance
	_, automatic := ParseAutoSetting(controller.setting)
	name, _ := ResolveSetting(controller.setting, appearance)
	changed := automatic && name != controller.name && controller.setLocked(name) == nil
	controller.mu.Unlock()
	if changed && controller.onChange != nil {
		controller.onChange()
	}
}

func (controller *Controller) SetInstance(value *Theme) error {
	if value == nil {
		return fmt.Errorf("theme instance is nil")
	}
	controller.mu.Lock()
	controller.current, controller.name, controller.setting = value, "<in-memory>", ""
	SetCurrent(value)
	controller.mu.Unlock()
	if controller.onChange != nil {
		controller.onChange()
	}
	return nil
}

func (controller *Controller) Reload() error {
	current := controller.Current()
	if current == nil || current.SourcePath == "" {
		return nil
	}
	data, err := os.ReadFile(current.SourcePath)
	if err != nil {
		return err
	}
	reloaded, err := Parse(current.SourcePath, data, current.mode)
	if err != nil {
		return err
	}
	reloaded.SourcePath = current.SourcePath
	reloaded.SourceInfo = current.SourceInfo
	if err := controller.registry.Register(reloaded); err != nil {
		return err
	}
	controller.mu.Lock()
	if controller.current != current {
		controller.mu.Unlock()
		return nil
	}
	controller.current = reloaded
	SetCurrent(reloaded)
	controller.mu.Unlock()
	if controller.onChange != nil {
		controller.onChange()
	}
	return nil
}

func trim(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t' || value[start] == '\n' || value[start] == '\r') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\n' || value[end-1] == '\r') {
		end--
	}
	return value[start:end]
}

func containsSlash(value string) bool {
	for _, character := range value {
		if character == '/' {
			return true
		}
	}
	return false
}
