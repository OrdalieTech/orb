package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/platforms/native"
	herdrext "github.com/OrdalieTech/orb/plugins/herdr"
)

func compiledExtensionsForEnvironment(getenv func(string) string, resumeArgs ...string) []extensions.CompiledExtension {
	rows := append([]extensions.CompiledExtension(nil), compiledExtensions...)
	if getenv("HERDR_ENV") != "1" || getenv("HERDR_BIN_PATH") == "" || getenv("HERDR_PANE_ID") == "" {
		return rows
	}
	return append(rows, extensions.CompiledExtension{
		Name: "herdr", Hidden: true, DefaultEnabled: true,
		Factory: herdrext.Extension(herdrBinary(getenv("HERDR_BIN_PATH")), getenv("HERDR_PANE_ID"), resumeArgs...),
	})
}

// herdrResumeArguments deliberately excludes credentials, prompts, messages and
// unknown extension flags. Model selection is already persisted in the session.
func herdrResumeArguments(args CLIArgs) []string {
	var result []string
	for _, option := range []struct {
		enabled bool
		flag    string
	}{
		{args.Auto, "--auto"},
		{args.NoExtensions, "--no-extensions"},
		{args.NoTools, "--no-tools"},
		{args.NoBuiltinTools, "--no-builtin-tools"},
		{args.NoContextFiles, "--no-context-files"},
		{args.NoSkills, "--no-skills"},
		{args.NoPromptTemplates, "--no-prompt-templates"},
		{args.NoThemes, "--no-themes"},
		{args.Offline, "--offline"},
	} {
		if option.enabled {
			result = append(result, option.flag)
		}
	}
	for _, option := range []struct{ flag, value string }{
		{"--tools", strings.Join(args.Tools, ",")},
		{"--exclude-tools", strings.Join(args.ExcludeTools, ",")},
		{"--bridge", args.BridgeProfile},
		{"--instance", args.InstanceAlias},
	} {
		if option.value != "" {
			result = append(result, option.flag, option.value)
		}
	}
	for _, group := range []struct {
		flag   string
		values []string
	}{
		{"--extension", args.Extensions}, {"--skill", args.Skills},
		{"--prompt-template", args.PromptTemplates}, {"--theme", args.Themes},
	} {
		for _, value := range group.values {
			result = append(result, group.flag, value)
		}
	}
	if args.UseTheme != "" {
		result = append(result, "--use-theme", args.UseTheme)
	}
	if args.ProjectTrusted != nil && !*args.ProjectTrusted {
		result = append(result, "--no-approve")
	}
	return result
}

// Both roots are necessary: making the default agent directory explicit would
// otherwise change native.Path's precedence and select a different database.
func herdrResumeRoots(agentDir string) ([]string, error) {
	agentDir, err := filepath.Abs(agentDir)
	if err != nil {
		return nil, err
	}
	statePath, err := native.Path(agentDir)
	if err != nil {
		return nil, err
	}
	args := []string{"--agent-dir", agentDir, "--state-home", filepath.Dir(statePath)}
	if bridgeHome := os.Getenv("ORB_BRIDGE_HOME"); bridgeHome != "" {
		bridgeHome, err = filepath.Abs(bridgeHome)
		if err != nil {
			return nil, err
		}
		args = append(args, "--bridge-home", bridgeHome)
	}
	return args, nil
}

// herdrBinary is the Herdr client to report through. A Herdr server updated
// in place still names its replaced executable, which Linux reports with a
// " (deleted)" suffix; the new binary sits at the same path, and failing that
// the one on PATH answers the same socket.
func herdrBinary(path string) string {
	path = strings.TrimSuffix(path, " (deleted)")
	if _, err := os.Stat(path); err != nil {
		if found, lookErr := exec.LookPath("herdr"); lookErr == nil {
			return found
		}
	}
	return path
}
