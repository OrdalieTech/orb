package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/modes"
	firstpartyplugins "github.com/OrdalieTech/pigo/codingagent/plugins"
)

// Port of packages/coding-agent/src/package-manager-cli.ts (pi
// install/remove/update/list/config). D4 keeps binary updates notify-only.

type updateTarget struct {
	kind   string // "all" | "self" | "extensions" | "models"
	source string
}

type packageCommandOptions struct {
	command                 string
	source                  string
	updateTarget            *updateTarget
	showPackagesSkippedNote bool
	offline                 bool
	local                   bool
	projectTrustOverride    *bool
	help                    bool
	invalidOption           string
	invalidArgument         string
	missingOptionValue      string
	conflictingOptions      string
}

func getPackageCommandUsage(command string) string {
	switch command {
	case "install":
		return "pigo install <source> [-l] [--approve|--no-approve]"
	case "remove":
		return "pigo remove <source> [-l] [--approve|--no-approve]"
	case "update":
		return "pigo update [target] [--self|--extensions|--models|--all] [--extension <source>] [--approve|--no-approve]"
	default:
		return "pigo list [--approve|--no-approve]"
	}
}

const configCommandUsage = "pigo config [-l] [--approve|--no-approve]"

const pluginsCommandUsage = "pigo plugins list|enable|disable [name]"

func handlePluginsCommand(ctx context.Context, argv []string, streams cliStreams) (bool, int) {
	if len(argv) == 0 || argv[0] != "plugins" {
		return false, 0
	}
	if len(argv) == 2 && (argv[1] == "-h" || argv[1] == "--help") {
		_, _ = fmt.Fprintln(streams.Stdout, "Usage: "+pluginsCommandUsage)
		return true, 0
	}
	if len(argv) < 2 || argv[1] != "list" && argv[1] != "enable" && argv[1] != "disable" {
		_, _ = fmt.Fprintln(streams.Stderr, "Usage: "+pluginsCommandUsage)
		return true, 1
	}
	action := argv[1]
	if action == "list" && len(argv) != 2 || action != "list" && len(argv) != 3 {
		_, _ = fmt.Fprintln(streams.Stderr, "Usage: "+pluginsCommandUsage)
		return true, 1
	}
	cwd, agentDir, err := packageCommandDirs()
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	settings, err := createCommandSettingsManager(ctx, cwd, agentDir, nil, false)
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	reportPackageSettingsErrors(streams.Stderr, settings, "plugins command")
	if action == "list" {
		enabled := settings.GetPlugins()
		for _, name := range firstpartyplugins.Names() {
			state := "off"
			if enabled[name] {
				state = "on"
			}
			_, _ = fmt.Fprintf(streams.Stdout, "%s\t%s\t%s\n", name, state, firstpartyplugins.Description(name))
		}
		return true, 0
	}
	name := argv[2]
	if firstpartyplugins.Description(name) == "" {
		_, _ = fmt.Fprintf(streams.Stderr, "Unknown plugin %q.\n", name)
		return true, 1
	}
	enabled := action == "enable"
	settings.SetPluginEnabled(name, enabled)
	if errors := settings.DrainErrors(); len(errors) > 0 {
		return true, reportCLIError(streams.Stderr, errors[0])
	}
	_, _ = fmt.Fprintf(streams.Stdout, "%s %s\n", strings.ToUpper(action[:1])+action[1:], name)
	return true, 0
}

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
  pigo install npm:@foo/bar
  pigo install git:github.com/user/repo
  pigo install git:git@github.com:user/repo
  pigo install https://github.com/user/repo
  pigo install ssh://git@github.com/user/repo
  pigo install ./local/path
`, getPackageCommandUsage("install"), config.ConfigDirName)
	case "remove":
		_, _ = fmt.Fprintf(writer, `Usage:
  %s

Remove a package and its source from settings.
Alias: pigo uninstall <source> [-l]

Options:
  -l, --local       Remove from project settings (%s/settings.json)
  -a, --approve     Trust project-local files for this command
  -na, --no-approve Ignore project-local files for this command

Examples:
  pigo remove npm:@foo/bar
  pigo uninstall npm:@foo/bar
`, getPackageCommandUsage("remove"), config.ConfigDirName)
	case "update":
		_, _ = fmt.Fprintf(writer, `Usage:
  %s

Show how to update pigo, or update installed packages and model catalogs.

pigo never replaces its running binary. The default and --self routes print the
exact command for each supported installation method.

Options:
  --self                  Show pigo binary update commands (default)
  --extensions            Update installed packages only
  --models                Refresh model catalogs only
  --all                   Update packages, then show pigo binary update commands
  --extension <source>    Update one package only
  -a, --approve           Trust project-local files for this command
  -na, --no-approve       Ignore project-local files for this command

Routes:
  pigo update              Show pigo binary update commands
  pigo update --all        Update packages and show binary update commands
  pigo update --extensions Update all installed packages
  pigo update --models     Refresh model catalogs only
  pigo update <source>     Update one package
  pigo update pigo         Alias for pigo update (self works too)
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
	var offlineFlag bool

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
		case arg == "--offline":
			if command == "update" {
				offlineFlag = true
			} else {
				setInvalidOption(arg)
			}
		case arg == "--approve" || arg == "-a":
			options.projectTrustOverride = boolPointer(true)
		case arg == "--no-approve" || arg == "-na":
			options.projectTrustOverride = boolPointer(false)
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
			if options.source == "self" || options.source == "pigo" {
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
			options.showPackagesSkippedNote = true
		}
		if offlineFlag {
			if options.showPackagesSkippedNote {
				options.offline = true
			} else {
				setInvalidOption("--offline")
			}
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

func handleConfigCommand(ctx context.Context, argv []string, streams cliStreams, dependencies cliDependencies) (bool, int) {
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
			_, _ = fmt.Fprintf(streams.Stderr, "Use \"pigo --help\" or \"%s\".\n", configCommandUsage)
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
	if !streams.StdinTTY || !streams.StdoutTTY {
		_, _ = fmt.Fprintln(streams.Stderr, "Error: pigo config requires an interactive terminal (stdin and stdout must be TTYs).")
		return true, 1
	}

	globalSettings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir), config.WithProjectTrusted(false))
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	globalResolved, err := codingagent.NewPackageManager(codingagent.PackageManagerOptions{
		CWD: cwd, AgentDir: agentDir, Settings: globalSettings,
	}).Resolve(nil)
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	projectResolved := globalResolved
	if settings.IsProjectTrusted() {
		projectResolved, err = codingagent.NewPackageManager(codingagent.PackageManagerOptions{
			CWD: cwd, AgentDir: agentDir, Settings: settings,
		}).Resolve(nil)
		if err != nil {
			return true, reportCLIError(streams.Stderr, err)
		}
	}
	writeScope := modes.ConfigWriteGlobal
	if local {
		writeScope = modes.ConfigWriteProject
	}
	if err := dependencies.runConfig(ctx, modes.ConfigSelectorOptions{
		ResolvedPaths:   modes.ScopedResolvedPaths{Global: globalResolved, Project: projectResolved},
		SettingsManager: settings,
		CWD:             cwd, AgentDir: agentDir,
		WriteScope: writeScope, ProjectModeAvailable: settings.IsProjectTrusted(),
	}); err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	return true, 0
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
		_, _ = fmt.Fprintf(streams.Stderr, "Use \"pigo --help\" or %q.\n", getPackageCommandUsage(options.command))
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
	if options.command == "update" && options.updateTarget != nil && options.updateTarget.kind == "self" {
		if !options.showPackagesSkippedNote {
			printSelfUpdateInstructions(streams.Stdout)
			return true, 0
		}
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
		if target.kind == "self" && options.showPackagesSkippedNote {
			offline := packageUpdateOffline(options.offline)
			latestVersion := ""
			var selfCheckErr error
			if !offline && !isDevelopmentVersion(version) {
				latestVersion, selfCheckErr = fetchLatestReleaseVersion(ctx, version, http.DefaultClient, latestReleaseURL, selfUpdateCheckTimeout)
			}
			checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			if offline {
				cancel()
			}
			packageCheck, packageCheckErr := packageManager.CheckForPackageUpdates(checkCtx)
			cancel()
			printSelfUpdateStatus(streams.Stdout, version, latestVersion, selfCheckErr, offline)
			printPackageUpdateStatus(streams.Stdout, packageCheck, offline || packageCheckErr != nil)
			return true, 0
		}
		if target.kind == "all" || target.kind == "extensions" {
			updateSource := ""
			if target.kind == "extensions" {
				updateSource = target.source
			}
			updates, err := packageManager.UpdateWithResults(updateSource)
			if err != nil {
				return true, reportCLIError(streams.Stderr, err)
			}
			printUpdatedPackages(streams.Stdout, updates)
		}
		if target.kind == "all" || target.kind == "self" {
			printSelfUpdateInstructions(streams.Stdout)
		}
		return true, 0
	}
}

func printSelfUpdateInstructions(writer io.Writer) {
	_, _ = fmt.Fprint(writer, `pigo does not replace its running binary.
Re-run the method used to install it:

  Installer: curl -fsSL https://raw.githubusercontent.com/OrdalieTech/pigo/main/scripts/install.sh | sh
  Go:        go install github.com/OrdalieTech/pigo/cmd/pigo@latest
`)
}

func printSelfUpdateStatus(writer io.Writer, currentVersion, latestVersion string, checkErr error, offline bool) {
	current := displayVersion(currentVersion)
	switch {
	case isDevelopmentVersion(currentVersion):
		_, _ = fmt.Fprintln(writer, "pigo dev build — update check skipped.")
	case offline:
		_, _ = fmt.Fprintf(writer, "pigo %s — offline mode — update check skipped.\n", current)
	case checkErr != nil:
		_, _ = fmt.Fprintf(writer, "pigo %s — could not check for updates (%s).\n", current, checkErr)
	case isNewerPackageVersion(latestVersion, currentVersion):
		latest := displayVersion(latestVersion)
		_, _ = fmt.Fprintf(writer, "Update available: %s -> %s\n", current, latest)
		_, _ = fmt.Fprintf(writer, "Release: https://github.com/OrdalieTech/pigo/releases/tag/%s\n", strings.TrimSpace(latestVersion))
		printSelfUpdateInstructions(writer)
		return
	default:
		_, _ = fmt.Fprintf(writer, "pigo %s — already the latest version.\n", current)
		return
	}
	printSelfUpdateInstructions(writer)
}

func packageUpdateOffline(explicit bool) bool {
	if explicit {
		return true
	}
	value := strings.ToLower(strings.TrimSpace(os.Getenv("PI_OFFLINE")))
	return value == "1" || value == "true" || value == "yes"
}

func printPackageUpdateStatus(writer io.Writer, check codingagent.PackageUpdateCheck, skipped bool) {
	if check.Installed == 0 {
		return
	}
	if skipped {
		_, _ = fmt.Fprintf(writer, "Packages: %d installed (update check skipped) — run pigo update --extensions\n", check.Installed)
		return
	}
	if len(check.Updates) == 0 {
		_, _ = fmt.Fprintf(writer, "Packages: all %d up to date.\n", check.Installed)
		return
	}
	formatted := make([]string, len(check.Updates))
	for index, update := range check.Updates {
		formatted[index] = formatPackageUpdate(update)
	}
	_, _ = fmt.Fprintf(writer, "Package updates available: %s — run pigo update --extensions\n", strings.Join(formatted, ", "))
}

func printUpdatedPackages(writer io.Writer, updates []codingagent.PackageVersionUpdate) {
	if len(updates) == 0 {
		_, _ = fmt.Fprintln(writer, "All packages up to date.")
		return
	}
	for _, update := range updates {
		_, _ = fmt.Fprintln(writer, formatPackageUpdate(update))
	}
}

func formatPackageUpdate(update codingagent.PackageVersionUpdate) string {
	return fmt.Sprintf("%s %s -> %s", update.DisplayName, displayPackageRevision(update.Type, update.CurrentVersion), displayPackageRevision(update.Type, update.LatestVersion))
}

func displayPackageRevision(packageType, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(not installed)"
	}
	if packageType == "npm" {
		return displayVersion(value)
	}
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func displayVersion(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "v") {
		return value
	}
	return "v" + value
}

func isDevelopmentVersion(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "" || value == "dev" || strings.Contains(value, "-dev")
}
