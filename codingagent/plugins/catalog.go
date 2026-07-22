// Package plugins contains pigo's bundled, default-off first-party extensions.
package plugins

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

// Options supplies runtime seams used by bundled plugins. StreamFn keeps
// subagent tests and embedders independent from real providers.
type Options struct {
	StreamFn   agent.StreamFn
	HTTPClient *http.Client
}

var names = []string{"tasks", "websearch", "subagents"}

var descriptions = map[string]string{
	"tasks":     "Live session task list and todo tool",
	"websearch": "Web search and readable page fetching",
	"subagents": "In-process single or parallel child agents",
}

// Names returns the stable first-party plugin order.
func Names() []string { return append([]string(nil), names...) }

// Description returns the one-line description used by the CLI and TUI.
func Description(name string) string { return descriptions[name] }

// Catalog returns fresh extension factories. Embedders can register any
// chosen subset in an extensions.Registry and pass it through AgentSessionOptions.
func Catalog(option ...Options) map[string]extensions.Factory {
	if len(option) > 1 {
		panic("plugins: Catalog accepts at most one Options value")
	}
	var options Options
	if len(option) == 1 {
		options = option[0]
	}
	return map[string]extensions.Factory{
		"tasks":     tasksExtension(),
		"websearch": websearchExtension(options.HTTPClient),
		"subagents": subagentsExtension(options.StreamFn),
	}
}

// Control registers /plugins independently of the default-off plugin catalog.
func Control(settings *config.SettingsManager) extensions.Factory {
	return func(api extensions.API) error {
		api.RegisterCommand("plugins", extensions.Command{
			Description: "Enable or disable bundled plugins",
			Handler: func(ctx context.Context, _ string, command extensions.CommandContext) error {
				if !command.HasUI() {
					return fmt.Errorf("/plugins requires interactive mode")
				}
				dirty := false
				for {
					enabled := settings.GetPlugins()
					choices := make([]string, 0, len(names)+1)
					choiceNames := make(map[string]string, len(names))
					for _, name := range names {
						mark := " "
						if enabled[name] {
							mark = "x"
						}
						label := fmt.Sprintf("[%s] %s — %s", mark, name, descriptions[name])
						choices = append(choices, label)
						choiceNames[label] = name
					}
					choices = append(choices, "Done")
					selected, ok, err := command.UI().Select(ctx, "Bundled plugins", choices, nil)
					if err != nil {
						return err
					}
					if !ok || selected == "Done" {
						break
					}
					name := choiceNames[selected]
					if name == "" {
						continue
					}
					settings.SetPluginEnabled(name, !enabled[name])
					dirty = true
				}
				if !dirty {
					return nil
				}
				for _, settingsError := range settings.DrainErrors() {
					if strings.TrimSpace(settingsError.Error()) != "" {
						return settingsError
					}
				}
				return command.Reload(ctx)
			},
		})
		return nil
	}
}
