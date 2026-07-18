package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
)

// Port of packages/coding-agent/src/package-manager-cli.ts (pi
// install/remove/update/list/config). Self-update is deferred to WP-661 (gate
// G4) and the config selector to the TUI wave (WP-450).

type updateTarget struct {
	kind   string // "all" | "self" | "extensions" | "models"
	source string
}

type packageCommandOptions struct {
	command                   string
	source                    string
	updateTarget              *updateTarget
	showExtensionsSkippedNote bool
	local                     bool
	force                     bool
	projectTrustOverride      *bool
	help                      bool
	invalidOption             string
	invalidArgument           string
	missingOptionValue        string
	conflictingOptions        string
}

func getPackageCommandUsage(command string) string {
	switch command {
	case "install":
		return "pi install <source> [-l] [--approve|--no-approve]"
	case "remove":
		return "pi remove <source> [-l] [--approve|--no-approve]"
	case "update":
		return "pi update [source|self|pi] [--self|--extensions|--models|--all] [--extension <source>] [--approve|--no-approve] [--force]"
	default:
		return "pi list [--approve|--no-approve]"
	}
}

const configCommandUsage = "pi config [-l] [--approve|--no-approve]"

func printConfigCommandHelp(writer io.Writer) {
	_, _ = fmt.Fprintf(writer, `Usage:
  %s

Open the resource configuration TUI to enable or disable package resources.
Without -l, starts in global settings (~/%s/agent/settings.json).
Press Tab in the TUI to switch between global and project-local modes.

Options:
  -l, --local       Edit project overrides (%s/settings.json)
  -a, --approve     Trust project-local files for this command with -l
  -na, --no-approve Ignore project-local files for this command with -l
`, configCommandUsage, config.ConfigDirName, config.ConfigDirName)
}

func printPackageCommandHelp(writer io.Writer, command string) {
	switch command {
	case "install":
		_, _ = fmt.Fprintf(writer, `Usage:
  %s

Install a package and add it to settings.

Options:
  -l, --local       Install project-locally (%s/settings.json)
  -a, --approve     Trust project-local files for this command
  -na, --no-approve Ignore project-local files for this command

Examples:
  pi install npm:@foo/bar
  pi install git:github.com/user/repo
  pi install git:git@github.com:user/repo
  pi install https://github.com/user/repo
  pi install ssh://git@github.com/user/repo
  pi install ./local/path
`, getPackageCommandUsage("install"), config.ConfigDirName)
	case "remove":
		_, _ = fmt.Fprintf(writer, `Usage:
  %s

Remove a package and its source from settings.
Alias: pi uninstall <source> [-l]

Options:
  -l, --local       Remove from project settings (%s/settings.json)
  -a, --approve     Trust project-local files for this command
  -na, --no-approve Ignore project-local files for this command

Examples:
  pi remove npm:@foo/bar
  pi uninstall npm:@foo/bar
`, getPackageCommandUsage("remove"), config.ConfigDirName)
	case "update":
		_, _ = fmt.Fprintf(writer, `Usage:
  %s

Update pi, installed packages, or model catalogs.

Options:
  --self                  Update pi only (default when no target is given)
  --extensions            Update installed packages only
  --models                Refresh model catalogs only
  --all                   Update pi and installed packages
  --extension <source>    Update one package only
  -a, --approve           Trust project-local files for this command
  -na, --no-approve       Ignore project-local files for this command
  --force                 Reinstall pi even if the current version is latest

Short forms:
  pi update                Update pi only
  pi update --all          Update pi and all extensions
  pi update --models       Refresh model catalogs only
  pi update <source>       Update one package
  pi update pi             Update pi only (self works as alias to pi)
`, getPackageCommandUsage("update"))
	default:
		_, _ = fmt.Fprintf(writer, `Usage:
  %s

List installed packages from user and project settings.

Options:
  -a, --approve      Trust project-local files for this command
  -na, --no-approve  Ignore project-local files for this command
`, getPackageCommandUsage("list"))
	}
}

func parsePackageCommand(args []string) *packageCommandOptions {
	if len(args) == 0 {
		return nil
	}
	rawCommand, rest := args[0], args[1:]
	command := ""
	switch rawCommand {
	case "uninstall":
		command = "remove"
	case "install", "remove", "update", "list":
		command = rawCommand
	default:
		return nil
	}

	options := &packageCommandOptions{command: command}
	var selfFlag, extensionsFlag, modelsFlag, allFlag bool
	var extensionFlagSource string

	setInvalidOption := func(arg string) {
		if options.invalidOption == "" {
			options.invalidOption = arg
		}
	}
	setConflict := func(message string) {
		if options.conflictingOptions == "" {
			options.conflictingOptions = message
		}
	}

	for index := 0; index < len(rest); index++ {
		arg := rest[index]
		switch {
		case arg == "-h" || arg == "--help":
			options.help = true
		case arg == "-l" || arg == "--local":
			if command == "install" || command == "remove" {
				options.local = true
			} else {
				setInvalidOption(arg)
			}
		case arg == "--self":
			if command == "update" {
				selfFlag = true
			} else {
				setInvalidOption(arg)
			}
		case arg == "--extensions":
			if command == "update" {
				extensionsFlag = true
			} else {
				setInvalidOption(arg)
			}
		case arg == "--models":
			if command == "update" {
				modelsFlag = true
			} else {
				setInvalidOption(arg)
			}
		case arg == "--all":
			if command == "update" {
				allFlag = true
			} else {
				setInvalidOption(arg)
			}
		case arg == "--approve" || arg == "-a":
			options.projectTrustOverride = boolPointer(true)
		case arg == "--no-approve" || arg == "-na":
			options.projectTrustOverride = boolPointer(false)
		case arg == "--force":
			if command == "update" {
				options.force = true
			} else {
				setInvalidOption(arg)
			}
		case arg == "--extension":
			if command != "update" {
				setInvalidOption(arg)
				continue
			}
			if index+1 >= len(rest) || strings.HasPrefix(rest[index+1], "-") {
				if options.missingOptionValue == "" {
					options.missingOptionValue = arg
				}
			} else if extensionFlagSource != "" {
				setConflict("--extension can only be provided once")
				index++
			} else {
				extensionFlagSource = rest[index+1]
				index++
			}
		case strings.HasPrefix(arg, "-"):
			setInvalidOption(arg)
		default:
			if options.source == "" {
				options.source = arg
			} else if options.invalidArgument == "" {
				options.invalidArgument = arg
			}
		}
	}

	if command == "update" {
		if allFlag && (selfFlag || extensionsFlag || modelsFlag || extensionFlagSource != "") {
			setConflict("--all cannot be combined with --self, --extensions, --models, or --extension")
		}
		if allFlag && options.source != "" {
			setConflict("--all cannot be combined with a positional source")
		}
		switch {
		case modelsFlag:
			if selfFlag || extensionsFlag || allFlag || extensionFlagSource != "" {
				setConflict("--models cannot be combined with --self, --extensions, --all, or --extension")
			}
			if options.source != "" {
				setConflict("--models cannot be combined with a positional source")
			}
			options.updateTarget = &updateTarget{kind: "models"}
		case extensionFlagSource != "":
			if selfFlag || extensionsFlag || allFlag {
				setConflict("--extension cannot be combined with --self, --extensions, or --all")
			}
			if options.source != "" {
				setConflict("--extension cannot be combined with a positional source")
			}
			options.updateTarget = &updateTarget{kind: "extensions", source: extensionFlagSource}
		case options.source != "":
			if options.source == "self" || options.source == "pi" {
				if extensionsFlag {
					options.updateTarget = &updateTarget{kind: "all"}
				} else {
					options.updateTarget = &updateTarget{kind: "self"}
				}
			} else {
				if extensionsFlag || selfFlag || allFlag {
					setConflict("positional update targets cannot be combined with --self, --extensions, or --all")
				}
				options.updateTarget = &updateTarget{kind: "extensions", source: options.source}
			}
		case allFlag:
			options.updateTarget = &updateTarget{kind: "all"}
		case selfFlag && extensionsFlag:
			options.updateTarget = &updateTarget{kind: "all"}
		case selfFlag:
			options.updateTarget = &updateTarget{kind: "self"}
		case extensionsFlag:
			options.updateTarget = &updateTarget{kind: "extensions"}
		default:
			options.updateTarget = &updateTarget{kind: "self"}
			options.showExtensionsSkippedNote = true
		}
	}
	return options
}

func boolPointer(value bool) *bool { return &value }

func reportPackageSettingsErrors(stderr io.Writer, settings *config.SettingsManager, commandContext string) {
	for _, settingsError := range settings.DrainErrors() {
		_, _ = fmt.Fprintf(stderr, "Warning (%s, %s settings): %s\n", commandContext, settingsError.Scope, settingsError.Err)
	}
}

// createCommandSettingsManager resolves project trust for a package command:
// saved-trust-only for update, otherwise the full trust flow (headless — no
// prompt, no project_trust extensions yet).
func createCommandSettingsManager(ctx context.Context, cwd, agentDir string, projectTrustOverride *bool, useSavedProjectTrustOnly bool) (*config.SettingsManager, error) {
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir), config.WithProjectTrusted(false))
	if err != nil {
		return nil, err
	}
	trustStore := config.NewProjectTrustStore(agentDir)
	if useSavedProjectTrustOnly {
		trusted := false
		if projectTrustOverride != nil {
			trusted = *projectTrustOverride
		} else if decision, err := trustStore.Get(cwd); err != nil {
			return nil, err
		} else if decision != nil {
			trusted = *decision
		}
		settings.SetProjectTrusted(trusted)
		return settings, nil
	}
	trusted, err := codingagent.ResolveProjectTrusted(ctx, codingagent.ResolveProjectTrustedOptions{
		CWD:                 cwd,
		TrustStore:          trustStore,
		TrustOverride:       projectTrustOverride,
		DefaultProjectTrust: settings.GetDefaultProjectTrust(),
	})
	if err != nil {
		return nil, err
	}
	settings.SetProjectTrusted(trusted)
	return settings, nil
}

func handleConfigCommand(ctx context.Context, argv []string, streams cliStreams) (bool, int) {
	if len(argv) == 0 || argv[0] != "config" {
		return false, 0
	}
	rest := argv[1:]
	for _, arg := range rest {
		if arg == "-h" || arg == "--help" {
			printConfigCommandHelp(streams.Stdout)
			return true, 0
		}
	}
	local := false
	var projectTrustOverride *bool
	for _, arg := range rest {
		switch {
		case arg == "-l" || arg == "--local":
			local = true
		case arg == "-a" || arg == "--approve":
			projectTrustOverride = boolPointer(true)
		case arg == "-na" || arg == "--no-approve":
			projectTrustOverride = boolPointer(false)
		case strings.HasPrefix(arg, "-"):
			_, _ = fmt.Fprintf(streams.Stderr, "Unknown option %s for \"config\".\n", arg)
			_, _ = fmt.Fprintf(streams.Stderr, "Use \"pi --help\" or \"%s\".\n", configCommandUsage)
			return true, 1
		default:
			_, _ = fmt.Fprintf(streams.Stderr, "Unexpected argument %s.\n", arg)
			_, _ = fmt.Fprintf(streams.Stderr, "Usage: %s\n", configCommandUsage)
			return true, 1
		}
	}

	cwd, agentDir, err := packageCommandDirs()
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	settings, err := createCommandSettingsManager(ctx, cwd, agentDir, projectTrustOverride, false)
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	if local && !settings.IsProjectTrusted() {
		_, _ = fmt.Fprintln(streams.Stderr, "Project is not trusted. Use --approve to modify local resource config.")
		return true, 1
	}
	reportPackageSettingsErrors(streams.Stderr, settings, "config command")
	// TODO(WP-450): the interactive config selector requires the TUI wave.
	_, _ = fmt.Fprintln(streams.Stderr, "Error: pi config requires the interactive TUI, which is not available yet (WP-450)")
	return true, 1
}

func packageCommandDirs() (cwd, agentDir string, err error) {
	cwd, err = os.Getwd()
	if err != nil {
		return "", "", err
	}
	agentDir, err = config.GetAgentDir()
	if err != nil {
		return "", "", err
	}
	return cwd, agentDir, nil
}

func handlePackageCommand(ctx context.Context, argv []string, streams cliStreams, dependencies cliDependencies) (bool, int) {
	options := parsePackageCommand(argv)
	if options == nil {
		return false, 0
	}

	if options.help {
		printPackageCommandHelp(streams.Stdout, options.command)
		return true, 0
	}
	if options.invalidOption != "" {
		_, _ = fmt.Fprintf(streams.Stderr, "Unknown option %s for %q.\n", options.invalidOption, options.command)
		_, _ = fmt.Fprintf(streams.Stderr, "Use \"pi --help\" or %q.\n", getPackageCommandUsage(options.command))
		return true, 1
	}
	if options.missingOptionValue != "" {
		_, _ = fmt.Fprintf(streams.Stderr, "Missing value for %s.\n", options.missingOptionValue)
		_, _ = fmt.Fprintf(streams.Stderr, "Usage: %s\n", getPackageCommandUsage(options.command))
		return true, 1
	}
	if options.invalidArgument != "" {
		_, _ = fmt.Fprintf(streams.Stderr, "Unexpected argument %s.\n", options.invalidArgument)
		_, _ = fmt.Fprintf(streams.Stderr, "Usage: %s\n", getPackageCommandUsage(options.command))
		return true, 1
	}
	if options.conflictingOptions != "" {
		_, _ = fmt.Fprintln(streams.Stderr, "Error: "+options.conflictingOptions)
		_, _ = fmt.Fprintf(streams.Stderr, "Usage: %s\n", getPackageCommandUsage(options.command))
		return true, 1
	}
	if (options.command == "install" || options.command == "remove") && options.source == "" {
		_, _ = fmt.Fprintf(streams.Stderr, "Missing %s source.\n", options.command)
		_, _ = fmt.Fprintf(streams.Stderr, "Usage: %s\n", getPackageCommandUsage(options.command))
		return true, 1
	}

	if options.command == "update" && options.updateTarget != nil && options.updateTarget.kind == "models" {
		agentDir, err := migrateStartupAuth()
		if err == nil {
			err = dependencies.refreshModels(ctx, agentDir)
		}
		if err != nil {
			return true, reportCLIError(streams.Stderr, err)
		}
		_, _ = fmt.Fprintln(streams.Stdout, "Model catalogs refreshed")
		return true, 0
	}

	cwd, agentDir, err := packageCommandDirs()
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	writesProjectPackageConfig := (options.command == "install" || options.command == "remove") && options.local
	settings, err := createCommandSettingsManager(ctx, cwd, agentDir, options.projectTrustOverride, options.command == "update")
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	if !settings.IsProjectTrusted() && writesProjectPackageConfig {
		_, _ = fmt.Fprintln(streams.Stderr, "Project is not trusted. Use --approve to modify local package config.")
		return true, 1
	}
	reportPackageSettingsErrors(streams.Stderr, settings, "package command")

	packageManager := codingagent.NewPackageManager(codingagent.PackageManagerOptions{CWD: cwd, AgentDir: agentDir, Settings: settings})
	packageManager.SetProgressCallback(func(event codingagent.ProgressEvent) {
		if event.Type == "start" {
			_, _ = fmt.Fprintln(streams.Stdout, event.Message)
		}
	})

	switch options.command {
	case "install":
		if err := packageManager.InstallAndPersist(options.source, options.local); err != nil {
			return true, reportCLIError(streams.Stderr, err)
		}
		_, _ = fmt.Fprintln(streams.Stdout, "Installed "+options.source)
		return true, 0

	case "remove":
		removed, err := packageManager.RemoveAndPersist(options.source, options.local)
		if err != nil {
			return true, reportCLIError(streams.Stderr, err)
		}
		if !removed {
			_, _ = fmt.Fprintln(streams.Stderr, "No matching package found for "+options.source)
			return true, 1
		}
		_, _ = fmt.Fprintln(streams.Stdout, "Removed "+options.source)
		return true, 0

	case "list":
		configuredPackages := packageManager.ListConfiguredPackages()
		if len(configuredPackages) == 0 {
			_, _ = fmt.Fprintln(streams.Stdout, "No packages installed.")
			return true, 0
		}
		printPackage := func(pkg codingagent.ConfiguredPackage) {
			display := pkg.Source
			if pkg.Filtered {
				display += " (filtered)"
			}
			_, _ = fmt.Fprintln(streams.Stdout, "  "+display)
			if pkg.InstalledPath != "" {
				_, _ = fmt.Fprintln(streams.Stdout, "    "+pkg.InstalledPath)
			}
		}
		printedUser := false
		for _, pkg := range configuredPackages {
			if pkg.Scope != "user" {
				continue
			}
			if !printedUser {
				_, _ = fmt.Fprintln(streams.Stdout, "User packages:")
				printedUser = true
			}
			printPackage(pkg)
		}
		printedProject := false
		for _, pkg := range configuredPackages {
			if pkg.Scope != "project" {
				continue
			}
			if !printedProject {
				if printedUser {
					_, _ = fmt.Fprintln(streams.Stdout)
				}
				_, _ = fmt.Fprintln(streams.Stdout, "Project packages:")
				printedProject = true
			}
			printPackage(pkg)
		}
		return true, 0

	default: // update
		target := options.updateTarget
		if target == nil {
			target = &updateTarget{kind: "self"}
		}
		if options.showExtensionsSkippedNote {
			_, _ = fmt.Fprintln(streams.Stdout, "Extensions are skipped. Run pi update --extensions to update extensions.")
		}
		if target.kind == "all" || target.kind == "extensions" {
			updateSource := ""
			if target.kind == "extensions" {
				updateSource = target.source
			}
			if err := packageManager.Update(updateSource); err != nil {
				return true, reportCLIError(streams.Stderr, err)
			}
			if updateSource != "" {
				_, _ = fmt.Fprintln(streams.Stdout, "Updated "+updateSource)
			} else {
				_, _ = fmt.Fprintln(streams.Stdout, "Updated packages")
			}
		}
		if target.kind == "all" || target.kind == "self" {
			// Self-update is decided by gate G4 and lands with WP-661.
			_, _ = fmt.Fprintln(streams.Stderr, "error: pi cannot self-update this installation.")
			_, _ = fmt.Fprintln(streams.Stderr, "Download the latest release from https://github.com/OrdalieTech/pi-go/releases")
			return true, 1
		}
		return true, 0
	}
}
