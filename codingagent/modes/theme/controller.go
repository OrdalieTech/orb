package theme

import (
	"fmt"
	"os"
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
	registry *Registry
	current  *Theme
	name     string
	onChange func()
}

func NewController(registry *Registry, setting string, terminal TerminalTheme, onChange func()) *Controller {
	controller := &Controller{registry: registry, onChange: onChange}
	name, ok := ResolveSetting(setting, terminal)
	if !ok {
		name = string(terminal)
	}
	if err := controller.Set(name); err != nil {
		_ = controller.Set("dark")
	}
	return controller
}

func (controller *Controller) Current() *Theme { return controller.current }
func (controller *Controller) Name() string    { return controller.name }

func (controller *Controller) Available() []string { return controller.registry.Available() }

func (controller *Controller) Set(name string) error {
	theme, ok := controller.registry.Get(name)
	if !ok {
		fallback, fallbackOK := controller.registry.Get("dark")
		if fallbackOK {
			controller.current, controller.name = fallback, "dark"
		}
		return fmt.Errorf("theme not found: %s", name)
	}
	controller.current, controller.name = theme, name
	SetCurrent(theme)
	if controller.onChange != nil {
		controller.onChange()
	}
	return nil
}

func (controller *Controller) SetInstance(value *Theme) error {
	if value == nil {
		return fmt.Errorf("theme instance is nil")
	}
	controller.current, controller.name = value, "<in-memory>"
	SetCurrent(value)
	if controller.onChange != nil {
		controller.onChange()
	}
	return nil
}

func (controller *Controller) Reload() error {
	if controller.current == nil || controller.current.SourcePath == "" {
		return nil
	}
	data, err := os.ReadFile(controller.current.SourcePath)
	if err != nil {
		return err
	}
	reloaded, err := Parse(controller.current.SourcePath, data, controller.current.mode)
	if err != nil {
		return err
	}
	reloaded.SourcePath = controller.current.SourcePath
	reloaded.SourceInfo = controller.current.SourceInfo
	if err := controller.registry.Register(reloaded); err != nil {
		return err
	}
	controller.current = reloaded
	SetCurrent(reloaded)
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
