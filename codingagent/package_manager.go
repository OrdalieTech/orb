package codingagent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/internal/semver"
)

// Port of packages/coding-agent/src/core/package-manager.ts. npm sources are
// fetched natively from the registry (tarball + integrity check) instead of
// shelling out to npm — pi-go runs without a Node toolchain (WP-360 scope).
// Declared package dependencies are installed with the npmCommand setting
// (default ["npm"], upstream getNpmCommand) after npm extraction and git
// clone/reconcile; a missing npm binary degrades to a warning so the package
// itself stays installed.

const (
	packageNetworkTimeout    = 10 * time.Second
	updateCheckConcurrency   = 4
	gitUpdateConcurrency     = 4
	packageManagerProjectDir = config.ConfigDirName
)

func isOfflineModeEnabled() bool {
	value := os.Getenv("PI_OFFLINE")
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	return value == "1" || lower == "true" || lower == "yes"
}

// PathMetadata mirrors upstream's resolved-resource metadata shape.
type PathMetadata struct {
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	Origin  string `json:"origin"`
	BaseDir string `json:"baseDir,omitempty"`
}

type ResolvedResource struct {
	Path     string       `json:"path"`
	Enabled  bool         `json:"enabled"`
	Metadata PathMetadata `json:"metadata"`
}

type ResolvedPaths struct {
	Extensions []ResolvedResource `json:"extensions"`
	Skills     []ResolvedResource `json:"skills"`
	Prompts    []ResolvedResource `json:"prompts"`
	Themes     []ResolvedResource `json:"themes"`
}

// MissingSourceAction answers the resolve-time "install missing?" prompt.
type MissingSourceAction string

const (
	MissingSourceInstall MissingSourceAction = "install"
	MissingSourceSkip    MissingSourceAction = "skip"
	MissingSourceError   MissingSourceAction = "error"
)

type ProgressEvent struct {
	Type    string `json:"type"`
	Action  string `json:"action"`
	Source  string `json:"source"`
	Message string `json:"message,omitempty"`
}

type ProgressCallback func(ProgressEvent)

type PackageUpdate struct {
	Source      string `json:"source"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
	Scope       string `json:"scope"`
}

type ConfiguredPackage struct {
	Source        string `json:"source"`
	Scope         string `json:"scope"`
	Filtered      bool   `json:"filtered"`
	InstalledPath string `json:"installedPath,omitempty"`
}

type npmSource struct {
	spec    string
	name    string
	version string
	rng     string
	pinned  bool
}

type localSource struct {
	path string
}

// parsedSource is exactly one of npm/git/local.
type parsedSource struct {
	npm   *npmSource
	git   *GitSource
	local *localSource
}

type configuredUpdateSource struct {
	source string
	scope  string
}

type PackageManagerOptions struct {
	CWD      string
	AgentDir string
	Settings *config.SettingsManager
}

type PackageManager struct {
	cwd      string
	agentDir string
	settings *config.SettingsManager

	progressMu sync.Mutex
	progress   ProgressCallback

	// Test seams.
	registryBaseURL string
	runCommand      func(spec execSpec) (string, error)

	registryOnce   sync.Once
	registryConfig npmRegistryConfig

	stdout io.Writer
	stderr io.Writer
}

func NewPackageManager(options PackageManagerOptions) *PackageManager {
	manager := &PackageManager{
		cwd:      pmResolvePath(options.CWD, ""),
		agentDir: pmResolvePath(options.AgentDir, ""),
		settings: options.Settings,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
	}
	manager.runCommand = manager.execCommand
	return manager
}

func pmHomeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// pmResolvePath mirrors upstream resolvePath(input, baseDir, {homeDir, trim}).
func pmResolvePath(input, baseDir string) string {
	normalized := strings.TrimSpace(input)
	if home := pmHomeDir(); home != "" {
		if normalized == "~" {
			normalized = home
		} else if strings.HasPrefix(normalized, "~/") {
			normalized = filepath.Join(home, normalized[2:])
		}
	}
	normalized = normalizeResourcePath(normalized)
	if filepath.IsAbs(normalized) {
		return filepath.Clean(normalized)
	}
	base := baseDir
	if base == "" {
		if cwd, err := os.Getwd(); err == nil {
			base = cwd
		}
	}
	return filepath.Clean(filepath.Join(normalizeResourcePath(base), normalized))
}

func (manager *PackageManager) SetProgressCallback(callback ProgressCallback) {
	manager.progressMu.Lock()
	defer manager.progressMu.Unlock()
	manager.progress = callback
}

func (manager *PackageManager) emitProgress(event ProgressEvent) {
	manager.progressMu.Lock()
	callback := manager.progress
	manager.progressMu.Unlock()
	if callback != nil {
		callback(event)
	}
}

func (manager *PackageManager) withProgress(action, source, message string, operation func() error) error {
	manager.emitProgress(ProgressEvent{Type: "start", Action: action, Source: source, Message: message})
	if err := operation(); err != nil {
		manager.emitProgress(ProgressEvent{Type: "error", Action: action, Source: source, Message: err.Error()})
		return err
	}
	manager.emitProgress(ProgressEvent{Type: "complete", Action: action, Source: source})
	return nil
}

// isLocalPathSource mirrors upstream isLocalPath: bare names, relative paths,
// and file: URLs are local; known package/URL prefixes are not.
func isLocalPathSource(value string) bool {
	trimmed := strings.TrimSpace(value)
	for _, prefix := range [...]string{"npm:", "git:", "github:", "http:", "https:", "ssh:"} {
		if strings.HasPrefix(trimmed, prefix) {
			return false
		}
	}
	return true
}

func isExactNpmVersion(version string) bool {
	return version != "" && semver.Valid(version)
}

func npmVersionRange(version string) string {
	if version == "" || !semver.ValidRange(version) {
		return ""
	}
	return version
}

func parseNpmSpec(spec string) (name, version string) {
	// Upstream regex: ^(@?[^@]+(?:\/[^@]+)?)(?:@(.+))?$
	if strings.HasPrefix(spec, "@") {
		rest := spec[1:]
		if at := strings.Index(rest, "@"); at >= 0 {
			return "@" + rest[:at], rest[at+1:]
		}
		return spec, ""
	}
	if at := strings.Index(spec, "@"); at >= 0 {
		return spec[:at], spec[at+1:]
	}
	return spec, ""
}

func (manager *PackageManager) parseSource(source string) parsedSource {
	if strings.HasPrefix(source, "npm:") {
		spec := strings.TrimSpace(source[len("npm:"):])
		name, version := parseNpmSpec(spec)
		return parsedSource{npm: &npmSource{
			spec:    spec,
			name:    name,
			version: version,
			rng:     npmVersionRange(version),
			pinned:  isExactNpmVersion(version),
		}}
	}
	if isLocalPathSource(source) {
		return parsedSource{local: &localSource{path: source}}
	}
	if git := ParseGitURL(source); git != nil {
		return parsedSource{git: git}
	}
	return parsedSource{local: &localSource{path: source}}
}

func (manager *PackageManager) getSourceMatchKeyForInput(source string) string {
	parsed := manager.parseSource(source)
	switch {
	case parsed.npm != nil:
		return "npm:" + parsed.npm.name
	case parsed.git != nil:
		return "git:" + parsed.git.Host + "/" + parsed.git.Path
	default:
		return "local:" + pmResolvePath(parsed.local.path, manager.cwd)
	}
}

func (manager *PackageManager) getSourceMatchKeyForSettings(source, scope string) string {
	parsed := manager.parseSource(source)
	switch {
	case parsed.npm != nil:
		return "npm:" + parsed.npm.name
	case parsed.git != nil:
		return "git:" + parsed.git.Host + "/" + parsed.git.Path
	default:
		baseDir, err := manager.getBaseDirForScope(scope)
		if err != nil {
			baseDir = manager.cwd
		}
		return "local:" + pmResolvePath(parsed.local.path, baseDir)
	}
}

func (manager *PackageManager) packageSourcesMatch(existing config.PackageSource, inputSource, scope string) bool {
	return manager.getSourceMatchKeyForSettings(existing.Source, scope) == manager.getSourceMatchKeyForInput(inputSource)
}

// getPackageIdentity ignores version/ref so the same package configured in
// global and project settings (or via SSH vs HTTPS URLs) dedupes to one entry.
func (manager *PackageManager) getPackageIdentity(source, scope string) string {
	parsed := manager.parseSource(source)
	switch {
	case parsed.npm != nil:
		return "npm:" + parsed.npm.name
	case parsed.git != nil:
		return "git:" + parsed.git.Host + "/" + parsed.git.Path
	default:
		if scope != "" {
			if baseDir, err := manager.getBaseDirForScope(scope); err == nil {
				return "local:" + pmResolvePath(parsed.local.path, baseDir)
			}
		}
		return "local:" + pmResolvePath(parsed.local.path, manager.cwd)
	}
}

func (manager *PackageManager) normalizePackageSourceForSettings(source, scope string) string {
	parsed := manager.parseSource(source)
	if parsed.local == nil {
		return source
	}
	baseDir, err := manager.getBaseDirForScope(scope)
	if err != nil {
		return source
	}
	resolved := pmResolvePath(parsed.local.path, manager.cwd)
	rel, err := filepath.Rel(baseDir, resolved)
	if err != nil || rel == "" {
		return "."
	}
	if rel == "." {
		return "."
	}
	return rel
}

// AddSourceToSettings adds or updates the package entry; reports change.
func (manager *PackageManager) AddSourceToSettings(source string, local bool) (bool, error) {
	scope := scopeForLocal(local)
	currentPackages := manager.scopedPackages(scope)
	normalizedSource := manager.normalizePackageSourceForSettings(source, scope)
	for index, existing := range currentPackages {
		if !manager.packageSourcesMatch(existing, source, scope) {
			continue
		}
		if existing.Source == normalizedSource {
			return false, nil
		}
		nextPackages := append([]config.PackageSource(nil), currentPackages...)
		nextPackages[index] = existing.WithSource(normalizedSource)
		return true, manager.writeScopedPackages(scope, nextPackages)
	}
	nextPackages := append(append([]config.PackageSource(nil), currentPackages...), config.PackageSource{Source: normalizedSource})
	return true, manager.writeScopedPackages(scope, nextPackages)
}

// RemoveSourceFromSettings removes matching entries; reports change.
func (manager *PackageManager) RemoveSourceFromSettings(source string, local bool) (bool, error) {
	scope := scopeForLocal(local)
	currentPackages := manager.scopedPackages(scope)
	nextPackages := make([]config.PackageSource, 0, len(currentPackages))
	for _, existing := range currentPackages {
		if !manager.packageSourcesMatch(existing, source, scope) {
			nextPackages = append(nextPackages, existing)
		}
	}
	if len(nextPackages) == len(currentPackages) {
		return false, nil
	}
	return true, manager.writeScopedPackages(scope, nextPackages)
}

func scopeForLocal(local bool) string {
	if local {
		return "project"
	}
	return "user"
}

func (manager *PackageManager) scopedPackages(scope string) []config.PackageSource {
	if scope == "project" {
		return manager.settings.GetProjectPackages()
	}
	return manager.settings.GetGlobalPackages()
}

func (manager *PackageManager) writeScopedPackages(scope string, packages []config.PackageSource) error {
	if scope == "project" {
		return manager.settings.SetProjectPackages(packages)
	}
	return manager.settings.SetPackages(packages)
}

// GetInstalledPath reports where the source is installed, or "".
func (manager *PackageManager) GetInstalledPath(source, scope string) string {
	parsed := manager.parseSource(source)
	switch {
	case parsed.npm != nil:
		path, err := manager.getNpmInstallPath(parsed.npm, scope)
		if err == nil && pathExists(path) {
			return path
		}
	case parsed.git != nil:
		path, err := manager.getGitInstallPath(parsed.git, scope)
		if err == nil && pathExists(path) {
			return path
		}
	default:
		baseDir, err := manager.getBaseDirForScope(scope)
		if err != nil {
			return ""
		}
		path := pmResolvePath(parsed.local.path, baseDir)
		if pathExists(path) {
			return path
		}
	}
	return ""
}

func (manager *PackageManager) ListConfiguredPackages() []ConfiguredPackage {
	configuredPackages := make([]ConfiguredPackage, 0)
	for _, pkg := range manager.settings.GetGlobalPackages() {
		configuredPackages = append(configuredPackages, ConfiguredPackage{
			Source: pkg.Source, Scope: "user", Filtered: pkg.IsObject,
			InstalledPath: manager.GetInstalledPath(pkg.Source, "user"),
		})
	}
	for _, pkg := range manager.settings.GetProjectPackages() {
		configuredPackages = append(configuredPackages, ConfiguredPackage{
			Source: pkg.Source, Scope: "project", Filtered: pkg.IsObject,
			InstalledPath: manager.GetInstalledPath(pkg.Source, "project"),
		})
	}
	return configuredPackages
}

func (manager *PackageManager) assertProjectTrustedForScope(scope string) error {
	if scope == "project" && !manager.settings.IsProjectTrusted() {
		return errors.New("Project is not trusted; refusing to access project package storage") //nolint:staticcheck // Upstream error text is observable.
	}
	return nil
}

// Install installs without persisting to settings.
func (manager *PackageManager) Install(source string, local bool) error {
	parsed := manager.parseSource(source)
	scope := scopeForLocal(local)
	if err := manager.assertProjectTrustedForScope(scope); err != nil {
		return err
	}
	return manager.withProgress("install", source, "Installing "+source+"...", func() error {
		switch {
		case parsed.npm != nil:
			return manager.installNpm(parsed.npm, scope, false)
		case parsed.git != nil:
			return manager.installGit(parsed.git, scope)
		default:
			resolved := pmResolvePath(parsed.local.path, manager.cwd)
			if !pathExists(resolved) {
				return fmt.Errorf("Path does not exist: %s", resolved) //nolint:staticcheck // Upstream error text is observable.
			}
			return nil
		}
	})
}

func (manager *PackageManager) InstallAndPersist(source string, local bool) error {
	if err := manager.Install(source, local); err != nil {
		return err
	}
	_, err := manager.AddSourceToSettings(source, local)
	return err
}

func (manager *PackageManager) Remove(source string, local bool) error {
	parsed := manager.parseSource(source)
	scope := scopeForLocal(local)
	if err := manager.assertProjectTrustedForScope(scope); err != nil {
		return err
	}
	return manager.withProgress("remove", source, "Removing "+source+"...", func() error {
		switch {
		case parsed.npm != nil:
			return manager.uninstallNpm(parsed.npm, scope)
		case parsed.git != nil:
			return manager.removeGit(parsed.git, scope)
		default:
			return nil
		}
	})
}

func (manager *PackageManager) RemoveAndPersist(source string, local bool) (bool, error) {
	if err := manager.Remove(source, local); err != nil {
		return false, err
	}
	return manager.RemoveSourceFromSettings(source, local)
}

func (manager *PackageManager) buildNoMatchingPackageMessage(source string, configuredPackages []config.PackageSource) string {
	suggestion := manager.findSuggestedConfiguredSource(source, configuredPackages)
	if suggestion == "" {
		return "No matching package found for " + source
	}
	return "No matching package found for " + source + ". Did you mean " + suggestion + "?"
}

func (manager *PackageManager) findSuggestedConfiguredSource(source string, configuredPackages []config.PackageSource) string {
	trimmedSource := strings.TrimSpace(source)
	for _, pkg := range configuredPackages {
		parsed := manager.parseSource(pkg.Source)
		switch {
		case parsed.npm != nil:
			if trimmedSource == parsed.npm.name || trimmedSource == parsed.npm.spec {
				return pkg.Source
			}
		case parsed.git != nil:
			shorthand := parsed.git.Host + "/" + parsed.git.Path
			if trimmedSource == shorthand {
				return pkg.Source
			}
			if parsed.git.Ref != "" && trimmedSource == shorthand+"@"+parsed.git.Ref {
				return pkg.Source
			}
		}
	}
	return ""
}

// Update updates all configured packages, or only those matching source.
func (manager *PackageManager) Update(source string) error {
	globalPackages := manager.settings.GetGlobalPackages()
	projectPackages := manager.settings.GetProjectPackages()
	identity := ""
	if source != "" {
		identity = manager.getPackageIdentity(source, "")
	}
	matched := false
	updateSources := make([]configuredUpdateSource, 0)

	for _, pkg := range globalPackages {
		if identity != "" && manager.getPackageIdentity(pkg.Source, "user") != identity {
			continue
		}
		matched = true
		updateSources = append(updateSources, configuredUpdateSource{source: pkg.Source, scope: "user"})
	}
	for _, pkg := range projectPackages {
		if identity != "" && manager.getPackageIdentity(pkg.Source, "project") != identity {
			continue
		}
		matched = true
		updateSources = append(updateSources, configuredUpdateSource{source: pkg.Source, scope: "project"})
	}

	if source != "" && !matched {
		return errors.New(manager.buildNoMatchingPackageMessage(source, append(append([]config.PackageSource(nil), globalPackages...), projectPackages...)))
	}
	return manager.updateConfiguredSources(updateSources)
}

func (manager *PackageManager) updateConfiguredSources(sources []configuredUpdateSource) error {
	if isOfflineModeEnabled() || len(sources) == 0 {
		return nil
	}

	type npmTarget struct {
		configuredUpdateSource
		parsed *npmSource
	}
	type gitTarget struct {
		configuredUpdateSource
		parsed *GitSource
	}
	npmCandidates := make([]npmTarget, 0)
	gitCandidates := make([]gitTarget, 0)
	for _, entry := range sources {
		parsed := manager.parseSource(entry.source)
		// Pinned npm versions are fixed. Pinned git refs are configured
		// checkout targets, so include them to reconcile an existing clone
		// when the configured ref changes.
		switch {
		case parsed.npm != nil:
			if !parsed.npm.pinned {
				npmCandidates = append(npmCandidates, npmTarget{configuredUpdateSource: entry, parsed: parsed.npm})
			}
		case parsed.git != nil:
			gitCandidates = append(gitCandidates, gitTarget{configuredUpdateSource: entry, parsed: parsed.git})
		}
	}

	shouldUpdate := runWithConcurrency(len(npmCandidates), updateCheckConcurrency, func(index int) bool {
		return manager.shouldUpdateNpmSource(npmCandidates[index].parsed, npmCandidates[index].scope)
	})
	updatesByScope := map[string][]npmTarget{}
	for index, entry := range npmCandidates {
		if shouldUpdate[index] {
			updatesByScope[entry.scope] = append(updatesByScope[entry.scope], entry)
		}
	}

	var updateErrors []error
	var wait sync.WaitGroup
	var errorsMu sync.Mutex
	appendError := func(err error) {
		if err != nil {
			errorsMu.Lock()
			updateErrors = append(updateErrors, err)
			errorsMu.Unlock()
		}
	}
	for _, scope := range [...]string{"user", "project"} {
		entries := updatesByScope[scope]
		if len(entries) == 0 {
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			sourceLabel := scope + " npm packages"
			message := "Updating " + scope + " npm packages..."
			if len(entries) == 1 {
				sourceLabel = entries[0].source
				message = "Updating " + entries[0].source + "..."
			}
			appendError(manager.withProgress("update", sourceLabel, message, func() error {
				var batchErrors []error
				for _, entry := range entries {
					batchErrors = append(batchErrors, manager.installNpm(entry.parsed, entry.scope, false))
				}
				return errors.Join(batchErrors...)
			}))
		}()
	}
	if len(gitCandidates) > 0 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results := runWithConcurrency(len(gitCandidates), gitUpdateConcurrency, func(index int) error {
				entry := gitCandidates[index]
				return manager.withProgress("update", entry.source, "Updating "+entry.source+"...", func() error {
					return manager.updateGit(entry.parsed, entry.scope)
				})
			})
			for _, err := range results {
				appendError(err)
			}
		}()
	}
	wait.Wait()
	return errors.Join(updateErrors...)
}

func (manager *PackageManager) shouldUpdateNpmSource(source *npmSource, scope string) bool {
	installedPath, err := manager.getManagedNpmInstallPath(source, scope)
	if err != nil {
		return false
	}
	installedVersion := ""
	if pathExists(installedPath) {
		installedVersion = getInstalledNpmVersion(installedPath)
	}
	if installedVersion == "" {
		return true
	}
	targetVersion, err := manager.getLatestNpmVersion(source)
	if err != nil {
		// Preserve existing update behavior when version lookup fails.
		return true
	}
	return targetVersion != installedVersion
}

// CheckForAvailableUpdates reports installed, unpinned packages with a newer
// version available.
func (manager *PackageManager) CheckForAvailableUpdates() []PackageUpdate {
	if isOfflineModeEnabled() {
		return nil
	}
	packageSources := manager.dedupePackages(manager.collectConfiguredPackages())
	results := runWithConcurrency(len(packageSources), updateCheckConcurrency, func(index int) *PackageUpdate {
		entry := packageSources[index]
		if entry.scope == "temporary" {
			return nil
		}
		parsed := manager.parseSource(entry.pkg.Source)
		switch {
		case parsed.npm != nil:
			if parsed.npm.pinned {
				return nil
			}
			installedPath, err := manager.getNpmInstallPath(parsed.npm, entry.scope)
			if err != nil || !pathExists(installedPath) {
				return nil
			}
			if !manager.npmHasAvailableUpdate(parsed.npm, installedPath) {
				return nil
			}
			return &PackageUpdate{Source: entry.pkg.Source, DisplayName: parsed.npm.name, Type: "npm", Scope: entry.scope}
		case parsed.git != nil:
			if parsed.git.Pinned {
				return nil
			}
			installedPath, err := manager.getGitInstallPath(parsed.git, entry.scope)
			if err != nil || !pathExists(installedPath) {
				return nil
			}
			if !manager.gitHasAvailableUpdate(installedPath) {
				return nil
			}
			return &PackageUpdate{Source: entry.pkg.Source, DisplayName: parsed.git.Host + "/" + parsed.git.Path, Type: "git", Scope: entry.scope}
		default:
			return nil
		}
	})
	updates := make([]PackageUpdate, 0)
	for _, result := range results {
		if result != nil {
			updates = append(updates, *result)
		}
	}
	return updates
}

func (manager *PackageManager) npmHasAvailableUpdate(source *npmSource, installedPath string) bool {
	if isOfflineModeEnabled() {
		return false
	}
	installedVersion := getInstalledNpmVersion(installedPath)
	if installedVersion == "" {
		return false
	}
	targetVersion, err := manager.getLatestNpmVersion(source)
	if err != nil {
		return false
	}
	return targetVersion != installedVersion
}

func (manager *PackageManager) installedNpmMatchesConfiguredVersion(source *npmSource, installedPath string) bool {
	installedVersion := getInstalledNpmVersion(installedPath)
	if installedVersion == "" {
		return false
	}
	if source.rng == "" {
		return true
	}
	return semver.Satisfies(installedVersion, source.rng)
}

func getInstalledNpmVersion(installedPath string) string {
	pkg, err := readPackageJSON(filepath.Join(installedPath, "package.json"))
	if err != nil {
		return ""
	}
	version, _ := pkg["version"].(string)
	return version
}

// npm dependency installation (upstream getNpmCommand / runNpmCommand /
// getGitDependencyInstallArgs).

func settingsNpmCommand(settings *config.SettingsManager) []string {
	if settings == nil {
		return nil
	}
	raw, _ := settings.GetSettings()["npmCommand"].([]any)
	if len(raw) == 0 {
		return nil
	}
	command := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			command = append(command, text)
		}
	}
	return command
}

// getNpmCommand mirrors upstream getNpmCommand: the argv-style npmCommand
// setting, defaulting to ["npm"].
func (manager *PackageManager) getNpmCommand() (string, []string, error) {
	configured := settingsNpmCommand(manager.settings)
	if len(configured) == 0 {
		return "npm", nil, nil
	}
	if configured[0] == "" {
		return "", nil, errors.New("Invalid npmCommand: first array entry must be a non-empty command") //nolint:staticcheck // Upstream error text is observable.
	}
	return configured[0], configured[1:], nil
}

// getDependencyInstallArgs mirrors upstream getGitDependencyInstallArgs:
// npm-specific production flags are skipped for custom npmCommand values.
func (manager *PackageManager) getDependencyInstallArgs() []string {
	if len(settingsNpmCommand(manager.settings)) > 0 {
		return []string{"install"}
	}
	return []string{"install", "--omit=dev"}
}

func declaredDependencies(packageJSONPath string) []string {
	pkg, err := readPackageJSON(packageJSONPath)
	if err != nil {
		return nil
	}
	dependencies, _ := pkg["dependencies"].(map[string]any)
	names := make([]string, 0, len(dependencies))
	for name := range dependencies {
		names = append(names, name)
	}
	return names
}

// installPackageDependencies runs the npmCommand install inside an installed
// package dir (upstream runNpmCommand(getGitDependencyInstallArgs())).
// Packages without declared dependencies — or shipping them bundled in
// node_modules — need no Node toolchain; a missing npm binary is a warning,
// not an install failure.
func (manager *PackageManager) installPackageDependencies(packageDir string) error {
	dependencies := declaredDependencies(filepath.Join(packageDir, "package.json"))
	if len(dependencies) == 0 {
		return nil
	}
	bundled := true
	for _, name := range dependencies {
		if !pathExists(filepath.Join(packageDir, "node_modules", name)) {
			bundled = false
			break
		}
	}
	if bundled {
		return nil
	}
	command, baseArgs, err := manager.getNpmCommand()
	if err != nil {
		return err
	}
	args := append(append([]string(nil), baseArgs...), manager.getDependencyInstallArgs()...)
	if _, err := manager.runCommand(execSpec{name: command, args: args, dir: packageDir, stream: true}); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			_, _ = fmt.Fprintf(manager.stderr, "Warning: %s not found; skipped installing dependencies for %s. Install npm or set the npmCommand setting.\n", command, packageDir)
			return nil
		}
		return err
	}
	return nil
}

// Git operations (system git binary; upstream behavior, minus npm install).

type execSpec struct {
	name    string
	args    []string
	dir     string
	env     []string
	timeout time.Duration
	stream  bool
}

func (manager *PackageManager) execCommand(spec execSpec) (string, error) {
	cmd := exec.Command(spec.name, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = append(os.Environ(), spec.env...)
	var output strings.Builder
	var stderr strings.Builder
	if spec.stream {
		cmd.Stdout = manager.stdout
		cmd.Stderr = manager.stderr
	} else {
		cmd.Stdout = &output
		cmd.Stderr = &stderr
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var timeoutCh <-chan time.Time
	if spec.timeout > 0 {
		timer := time.NewTimer(spec.timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}
	select {
	case err := <-done:
		if err != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = strings.TrimSpace(output.String())
			}
			return "", fmt.Errorf("%s %s failed: %s: %s", spec.name, strings.Join(spec.args, " "), err, detail)
		}
		return strings.TrimSpace(output.String()), nil
	case <-timeoutCh:
		_ = cmd.Process.Kill()
		<-done
		return "", fmt.Errorf("%s %s timed out after %s", spec.name, strings.Join(spec.args, " "), spec.timeout)
	}
}

func (manager *PackageManager) runGit(dir string, args ...string) error {
	_, err := manager.runCommand(execSpec{name: "git", args: args, dir: dir, stream: true})
	return err
}

func (manager *PackageManager) captureGit(dir string, timeout time.Duration, args ...string) (string, error) {
	return manager.runCommand(execSpec{name: "git", args: args, dir: dir, timeout: timeout})
}

func (manager *PackageManager) runGitRemoteCommand(installedPath string, args ...string) (string, error) {
	return manager.runCommand(execSpec{
		name: "git", args: args, dir: installedPath,
		timeout: packageNetworkTimeout,
		env:     []string{"GIT_TERMINAL_PROMPT=0"},
	})
}

type gitUpdateTarget struct {
	ref       string
	fetchArgs []string
}

func (manager *PackageManager) getLocalGitUpdateTarget(installedPath string) (gitUpdateTarget, error) {
	upstreamRef, err := manager.captureGit(installedPath, packageNetworkTimeout, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err == nil {
		trimmedUpstream := strings.TrimSpace(upstreamRef)
		if !strings.HasPrefix(trimmedUpstream, "origin/") {
			err = fmt.Errorf("Unsupported upstream remote: %s", trimmedUpstream) //nolint:staticcheck // Upstream error text is observable.
		} else if branch := strings.TrimPrefix(trimmedUpstream, "origin/"); branch == "" {
			err = errors.New("Missing upstream branch name") //nolint:staticcheck // Upstream error text is observable.
		} else if _, headErr := manager.captureGit(installedPath, packageNetworkTimeout, "rev-parse", "@{upstream}"); headErr != nil {
			err = headErr
		} else {
			return gitUpdateTarget{
				ref: "@{upstream}",
				fetchArgs: []string{
					"fetch", "-q", "--prune", "--no-tags", "origin",
					"+refs/heads/" + branch + ":refs/remotes/origin/" + branch,
				},
			}, nil
		}
	}
	if err != nil {
		_ = manager.runGit(installedPath, "remote", "set-head", "origin", "-a")
		if _, headErr := manager.captureGit(installedPath, packageNetworkTimeout, "rev-parse", "origin/HEAD"); headErr != nil {
			return gitUpdateTarget{}, headErr
		}
		originHeadRef, _ := manager.captureGit(installedPath, packageNetworkTimeout, "symbolic-ref", "refs/remotes/origin/HEAD")
		branch := strings.TrimPrefix(strings.TrimSpace(originHeadRef), "refs/remotes/origin/")
		if branch != "" && branch != strings.TrimSpace(originHeadRef) {
			return gitUpdateTarget{
				ref: "origin/HEAD",
				fetchArgs: []string{
					"fetch", "-q", "--prune", "--no-tags", "origin",
					"+refs/heads/" + branch + ":refs/remotes/origin/" + branch,
				},
			}, nil
		}
		return gitUpdateTarget{
			ref:       "origin/HEAD",
			fetchArgs: []string{"fetch", "-q", "--prune", "--no-tags", "origin", "+HEAD:refs/remotes/origin/HEAD"},
		}, nil
	}
	return gitUpdateTarget{}, err
}

func (manager *PackageManager) getGitUpstreamRef(installedPath string) string {
	upstreamRef, err := manager.captureGit(installedPath, packageNetworkTimeout, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		return ""
	}
	trimmed := strings.TrimSpace(upstreamRef)
	if !strings.HasPrefix(trimmed, "origin/") {
		return ""
	}
	branch := strings.TrimPrefix(trimmed, "origin/")
	if branch == "" {
		return ""
	}
	return "refs/heads/" + branch
}

func (manager *PackageManager) getRemoteGitHead(installedPath string) (string, error) {
	if upstreamRef := manager.getGitUpstreamRef(installedPath); upstreamRef != "" {
		remoteHead, err := manager.runGitRemoteCommand(installedPath, "ls-remote", "origin", upstreamRef)
		if err == nil {
			if hash := firstGitHash(remoteHead, ""); hash != "" {
				return hash, nil
			}
		}
	}
	remoteHead, err := manager.runGitRemoteCommand(installedPath, "ls-remote", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	if hash := firstGitHash(remoteHead, "HEAD"); hash != "" {
		return hash, nil
	}
	return "", errors.New("Failed to determine remote HEAD") //nolint:staticcheck // Upstream error text is observable.
}

func firstGitHash(output, requiredRef string) string {
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 1 || len(fields[0]) != 40 {
			continue
		}
		if !isHex40(fields[0]) {
			continue
		}
		if requiredRef != "" && (len(fields) < 2 || fields[1] != requiredRef) {
			continue
		}
		return fields[0]
	}
	return ""
}

func isHex40(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (manager *PackageManager) gitHasAvailableUpdate(installedPath string) bool {
	if isOfflineModeEnabled() {
		return false
	}
	localHead, err := manager.captureGit(installedPath, packageNetworkTimeout, "rev-parse", "HEAD")
	if err != nil {
		return false
	}
	remoteHead, err := manager.getRemoteGitHead(installedPath)
	if err != nil {
		return false
	}
	return strings.TrimSpace(localHead) != strings.TrimSpace(remoteHead)
}

func (manager *PackageManager) installGit(source *GitSource, scope string) error {
	targetDir, err := manager.getGitInstallPath(source, scope)
	if err != nil {
		return err
	}
	if pathExists(targetDir) {
		if source.Ref != "" {
			return manager.ensureGitRef(targetDir, []string{"fetch", "-q", "origin", source.Ref}, "FETCH_HEAD")
		}
		target, err := manager.getLocalGitUpdateTarget(targetDir)
		if err != nil {
			return err
		}
		return manager.ensureGitRef(targetDir, target.fetchArgs, target.ref)
	}
	gitRoot, err := manager.getGitInstallRoot(scope)
	if err != nil {
		return err
	}
	if gitRoot != "" {
		ensureGitIgnore(gitRoot)
	}
	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return err
	}
	if err := manager.runGit("", "clone", "-q", source.Repo, targetDir); err != nil {
		return err
	}
	if source.Ref != "" {
		if err := manager.runGit(targetDir, "-c", "advice.detachedHead=false", "checkout", "-q", source.Ref); err != nil {
			return err
		}
	}
	return manager.installPackageDependencies(targetDir)
}

func (manager *PackageManager) updateGit(source *GitSource, scope string) error {
	targetDir, err := manager.getGitInstallPath(source, scope)
	if err != nil {
		return err
	}
	if !pathExists(targetDir) {
		return manager.installGit(source, scope)
	}
	if source.Ref != "" {
		return manager.ensureGitRef(targetDir, []string{"fetch", "-q", "origin", source.Ref}, "FETCH_HEAD")
	}
	target, err := manager.getLocalGitUpdateTarget(targetDir)
	if err != nil {
		return err
	}
	return manager.ensureGitRef(targetDir, target.fetchArgs, target.ref)
}

func (manager *PackageManager) ensureGitRef(targetDir string, fetchArgs []string, ref string) error {
	// Fetch only the ref we will reset to, avoiding unrelated branch/tag noise.
	if err := manager.runGit(targetDir, fetchArgs...); err != nil {
		return err
	}
	localHead, err := manager.captureGit(targetDir, packageNetworkTimeout, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	commitRef := ref + "^{commit}"
	targetHead, err := manager.captureGit(targetDir, packageNetworkTimeout, "rev-parse", commitRef)
	if err != nil {
		return err
	}
	if strings.TrimSpace(localHead) == strings.TrimSpace(targetHead) {
		return nil
	}
	if err := manager.runGit(targetDir, "reset", "--hard", commitRef); err != nil {
		return err
	}
	// Clean untracked files (extensions should be pristine).
	if err := manager.runGit(targetDir, "clean", "-fdx"); err != nil {
		return err
	}
	return manager.installPackageDependencies(targetDir)
}

func (manager *PackageManager) refreshTemporaryGitSource(source *GitSource, sourceStr string) {
	if isOfflineModeEnabled() {
		return
	}
	// Keep the cached temporary checkout when the refresh fails.
	_ = manager.withProgress("pull", sourceStr, "Refreshing "+sourceStr+"...", func() error {
		return manager.updateGit(source, "temporary")
	})
}

func (manager *PackageManager) removeGit(source *GitSource, scope string) error {
	targetDir, err := manager.getGitInstallPath(source, scope)
	if err != nil {
		return err
	}
	if !pathExists(targetDir) {
		return nil
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return err
	}
	installRoot, err := manager.getGitInstallRoot(scope)
	if err != nil || installRoot == "" {
		return nil
	}
	pruneEmptyGitParents(targetDir, installRoot)
	return nil
}

func pruneEmptyGitParents(targetDir, installRoot string) {
	resolvedRoot := pmResolvePath(installRoot, "")
	current := filepath.Dir(targetDir)
	for strings.HasPrefix(current, resolvedRoot) && current != resolvedRoot {
		if !pathExists(current) {
			current = filepath.Dir(current)
			continue
		}
		entries, err := os.ReadDir(current)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.RemoveAll(current); err != nil {
			break
		}
		current = filepath.Dir(current)
	}
}

func ensureGitIgnore(dir string) {
	_ = os.MkdirAll(dir, 0o755)
	ignorePath := filepath.Join(dir, ".gitignore")
	if !pathExists(ignorePath) {
		_ = os.WriteFile(ignorePath, []byte("*\n!.gitignore\n"), 0o644)
	}
}

func (manager *PackageManager) ensureNpmProject(installRoot string) error {
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		return err
	}
	markPathIgnoredByCloudSync(installRoot)
	ensureGitIgnore(installRoot)
	packageJSONPath := filepath.Join(installRoot, "package.json")
	if !pathExists(packageJSONPath) {
		return os.WriteFile(packageJSONPath, []byte("{\n  \"name\": \"pi-extensions\",\n  \"private\": true\n}"), 0o644)
	}
	return nil
}

// markPathIgnoredByCloudSync excludes install roots from Dropbox-style sync
// (upstream utils/paths.ts); failures are ignored.
func markPathIgnoredByCloudSync(path string) {
	switch {
	case isDarwin():
		for _, attr := range [...]string{"com.dropbox.ignored", "com.apple.fileprovider.ignore#P"} {
			_ = exec.Command("xattr", "-w", attr, "1", path).Run()
		}
	case isLinux():
		_ = exec.Command("setfattr", "-n", "user.com.dropbox.ignored", "-v", "1", path).Run()
	}
}

// Install path layout.

func (manager *PackageManager) getNpmInstallRoot(scope string, temporary bool) (string, error) {
	if temporary {
		return manager.getTemporaryDir("npm", "")
	}
	if scope == "project" {
		if err := manager.assertProjectTrustedForScope(scope); err != nil {
			return "", err
		}
		return filepath.Join(manager.cwd, packageManagerProjectDir, "npm"), nil
	}
	return filepath.Join(manager.agentDir, "npm"), nil
}

func (manager *PackageManager) getManagedNpmInstallPath(source *npmSource, scope string) (string, error) {
	if scope == "temporary" {
		root, err := manager.getTemporaryDir("npm", "")
		if err != nil {
			return "", err
		}
		return resolveManagedPath(root, "node_modules", source.name)
	}
	if scope == "project" {
		if err := manager.assertProjectTrustedForScope(scope); err != nil {
			return "", err
		}
		return filepath.Join(manager.cwd, packageManagerProjectDir, "npm", "node_modules", source.name), nil
	}
	return filepath.Join(manager.agentDir, "npm", "node_modules", source.name), nil
}

// getNpmInstallPath: upstream also probes legacy npm/pnpm global roots for
// user scope; that migration path requires a Node toolchain and is not ported.
func (manager *PackageManager) getNpmInstallPath(source *npmSource, scope string) (string, error) {
	return manager.getManagedNpmInstallPath(source, scope)
}

func (manager *PackageManager) getGitInstallPath(source *GitSource, scope string) (string, error) {
	if scope == "temporary" {
		return manager.getTemporaryDir("git-"+source.Host, source.Path)
	}
	installRoot, err := manager.getGitInstallRoot(scope)
	if err != nil {
		return "", err
	}
	if installRoot == "" {
		return "", errors.New("Missing git install root") //nolint:staticcheck // Upstream error text is observable.
	}
	return resolveManagedPath(installRoot, source.Host, source.Path)
}

func (manager *PackageManager) getGitInstallRoot(scope string) (string, error) {
	if scope == "temporary" {
		return "", nil
	}
	if scope == "project" {
		if err := manager.assertProjectTrustedForScope(scope); err != nil {
			return "", err
		}
		return filepath.Join(manager.cwd, packageManagerProjectDir, "git"), nil
	}
	return filepath.Join(manager.agentDir, "git"), nil
}

// GetExtensionTempFolder creates the 0700 temp extension folder.
func GetExtensionTempFolder(agentDir string) (string, error) {
	tempFolder := filepath.Join(agentDir, "tmp", "extensions")
	if err := os.MkdirAll(tempFolder, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(tempFolder, 0o700); err != nil {
		return "", err
	}
	return tempFolder, nil
}

func (manager *PackageManager) getTemporaryDir(prefix, suffix string) (string, error) {
	tempFolder, err := GetExtensionTempFolder(manager.agentDir)
	if err != nil {
		return "", err
	}
	root, err := resolveManagedPath(tempFolder, prefix)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(prefix + "-" + suffix))
	hash := hex.EncodeToString(digest[:])[:8]
	return resolveManagedPath(root, hash, suffix)
}

func resolveManagedPath(root string, parts ...string) (string, error) {
	resolvedRoot := pmResolvePath(root, "")
	resolvedPath := filepath.Clean(filepath.Join(append([]string{resolvedRoot}, parts...)...))
	if resolvedPath != resolvedRoot && !strings.HasPrefix(resolvedPath, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("Refusing to use path outside package install root: %s", resolvedPath) //nolint:staticcheck // Upstream error text is observable.
	}
	return resolvedPath, nil
}

func (manager *PackageManager) getBaseDirForScope(scope string) (string, error) {
	switch scope {
	case "project":
		if err := manager.assertProjectTrustedForScope(scope); err != nil {
			return "", err
		}
		return filepath.Join(manager.cwd, packageManagerProjectDir), nil
	case "user":
		return manager.agentDir, nil
	default:
		return manager.cwd, nil
	}
}

// runWithConcurrency evaluates task(0..count-1) with a worker pool, keeping
// result order (upstream runWithConcurrency).
func runWithConcurrency[T any](count, limit int, task func(index int) T) []T {
	results := make([]T, count)
	if count == 0 {
		return results
	}
	workers := min(limit, count)
	if workers < 1 {
		workers = 1
	}
	var next int
	var mu sync.Mutex
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				mu.Lock()
				index := next
				next++
				mu.Unlock()
				if index >= count {
					return
				}
				results[index] = task(index)
			}
		}()
	}
	wait.Wait()
	return results
}

type scopedPackageSource struct {
	pkg   config.PackageSource
	scope string
}

func (manager *PackageManager) collectConfiguredPackages() []scopedPackageSource {
	allPackages := make([]scopedPackageSource, 0)
	for _, pkg := range manager.settings.GetProjectPackages() {
		allPackages = append(allPackages, scopedPackageSource{pkg: pkg, scope: "project"})
	}
	for _, pkg := range manager.settings.GetGlobalPackages() {
		allPackages = append(allPackages, scopedPackageSource{pkg: pkg, scope: "user"})
	}
	return allPackages
}

// dedupePackages keeps the project entry when the same identity appears in
// both scopes; a project entry with autoload=false is a delta over the global
// entry, so both are kept (delta first).
func (manager *PackageManager) dedupePackages(packages []scopedPackageSource) []scopedPackageSource {
	result := make([]scopedPackageSource, 0, len(packages))
	seen := make(map[string]int)
	for _, entry := range packages {
		identity := manager.getPackageIdentity(entry.pkg.Source, entry.scope)
		index, exists := seen[identity]
		if !exists {
			seen[identity] = len(result)
			result = append(result, entry)
			continue
		}
		existing := result[index]
		if existing.scope == "project" && entry.scope == "user" {
			if existing.pkg.IsObject && existing.pkg.Autoload != nil && !*existing.pkg.Autoload {
				result = append(result, entry)
			}
		} else if entry.scope == "project" {
			result[index] = entry
		}
	}
	return result
}

func (manager *PackageManager) findAutoloadDeltaBase(pkg config.PackageSource, scope string, sources []scopedPackageSource) *scopedPackageSource {
	if scope != "project" || !pkg.IsObject || pkg.Autoload == nil || *pkg.Autoload {
		return nil
	}
	identity := manager.getPackageIdentity(pkg.Source, scope)
	for _, entry := range sources {
		if entry.scope == "user" && manager.getPackageIdentity(entry.pkg.Source, "user") == identity {
			return &entry
		}
	}
	return nil
}
