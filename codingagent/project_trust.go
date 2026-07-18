package codingagent

import (
	"context"

	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

// Port of packages/coding-agent/src/core/project-trust.ts.

type ResolveProjectTrustedOptions struct {
	CWD           string
	TrustStore    *config.ProjectTrustStore
	TrustOverride *bool
	// "ask" (default), "always", or "never".
	DefaultProjectTrust string
	// Optional extension runner whose project_trust handlers may decide.
	Runner       *extensions.Runner
	TrustContext extensions.Context
	HasUI        bool
	// SelectOption shows the trust prompt and returns the chosen label; the
	// second result is false when the user dismissed the prompt.
	SelectOption     func(title string, options []string) (string, bool)
	OnExtensionError func(message string)
}

// ResolveProjectTrusted decides project trust: CLI override, then a decisive
// project_trust extension, then the saved store, then defaultProjectTrust,
// then the interactive prompt (untrusted when no UI is available).
func ResolveProjectTrusted(ctx context.Context, options ResolveProjectTrustedOptions) (bool, error) {
	if options.TrustOverride != nil {
		return *options.TrustOverride, nil
	}
	if !config.HasTrustRequiringProjectResources(options.CWD) {
		return true, nil
	}

	if options.Runner != nil {
		result, extensionErrors := options.Runner.EmitProjectTrust(ctx, extensions.ProjectTrustEvent{CWD: options.CWD}, options.TrustContext)
		for _, extensionError := range extensionErrors {
			if options.OnExtensionError != nil {
				options.OnExtensionError("Extension \"" + extensionError.ExtensionPath + "\" project_trust error: " + extensionError.Error)
			}
		}
		if result != nil {
			trusted := result.Trusted == extensions.ProjectTrustYes
			if result.Remember {
				if err := options.TrustStore.Set(options.CWD, &trusted); err != nil {
					return false, err
				}
			}
			return trusted, nil
		}
	}

	decision, err := options.TrustStore.Get(options.CWD)
	if err != nil {
		return false, err
	}
	if decision != nil {
		return *decision, nil
	}

	switch options.DefaultProjectTrust {
	case "always":
		return true, nil
	case "never":
		return false, nil
	}

	if !options.HasUI || options.SelectOption == nil {
		return false, nil
	}

	trustOptions := config.GetProjectTrustOptions(options.CWD, true)
	labels := make([]string, 0, len(trustOptions))
	for _, option := range trustOptions {
		labels = append(labels, option.Label)
	}
	selectedLabel, selected := options.SelectOption(config.FormatProjectTrustPrompt(options.CWD), labels)
	if !selected {
		return false, nil
	}
	for _, option := range trustOptions {
		if option.Label != selectedLabel {
			continue
		}
		if len(option.Updates) > 0 {
			if err := options.TrustStore.SetMany(option.Updates); err != nil {
				return false, err
			}
		}
		return option.Trusted, nil
	}
	return false, nil
}
