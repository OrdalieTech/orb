package agent

import (
	"context"
	"fmt"
	"slices"

	"github.com/OrdalieTech/orb/agent/config"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/host"
)

// Project settings are read from the process filesystem, so a Host session
// keeps them untrusted until they are served through the FS port.
func hostSettings(h *host.Host, cwd, agentDir string) (*config.SettingsManager, error) {
	return config.NewSettingsManager(cwd, config.WithAgentDir(agentDir),
		config.WithGlobalDocument(h.Document("settings.json")), config.WithProjectTrusted(false))
}

// Provider model discovery stays offline: a Host has no outbound-network port yet.
func hostModelRegistry(h *host.Host, agentDir string) (*config.ModelRegistry, error) {
	auth, err := config.NewAuthStorageWithDocument(h.Document("auth.json"))
	if err != nil {
		return nil, err
	}
	var options []config.ModelRegistryOption
	if h.Env != nil {
		options = append(options, config.WithEnvironment(h.Env))
	}
	return config.NewModelRegistryWithDocuments(agentDir, auth, h.Document("models.json"), h.Document("models-store.json"), false, options...)
}

func hostSessionManager(h *host.Host, cwd string) (*sessionstore.SessionManager, error) {
	repo := h.SessionRepo()
	created, err := repo.Create(context.Background(), harness.SessionCreateOptions{CWD: cwd})
	if err != nil {
		return nil, fmt.Errorf("agent: create host session: %w", err)
	}
	return sessionstore.FromHarnessStorage(created.Storage(), sessionstore.WithHarnessRepo(repo), sessionstore.WithCwdOverride(cwd))
}

// withHostTools derives built-in tool operations from the FS and Exec ports.
// A host without Exec omits bash instead of failing at call time.
func withHostTools(opts AgentSessionOptions) AgentSessionOptions {
	h := opts.Host
	if opts.ToolOptions == nil && h.FS != nil {
		opts.ToolOptions = tools.FileSystemToolsOptions(h.FS)
		if h.Exec != nil {
			opts.ToolOptions.Bash = &tools.BashToolOptions{Operations: tools.ShellBashOperations(h.Exec)}
		}
	}
	if h.Exec == nil && !slices.Contains(opts.ExcludeTools, "bash") {
		opts.ExcludeTools = append(slices.Clone(opts.ExcludeTools), "bash")
	}
	return opts
}
