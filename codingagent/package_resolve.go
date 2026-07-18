package codingagent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/bmatcuk/doublestar/v4"
)

// Resource resolution half of the package-manager port: package manifests,
// convention directories, settings filters, and auto-discovery.

var packageResourceTypes = [...]string{"extensions", "skills", "prompts", "themes"}

func resourceFileMatches(resourceType, name string) bool {
	switch resourceType {
	case "extensions":
		return strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".js")
	case "skills", "prompts":
		return strings.HasSuffix(name, ".md")
	default:
		return strings.HasSuffix(name, ".json")
	}
}

type resolvedEntryKind int

const (
	entryKindMissing resolvedEntryKind = iota
	entryKindFile
	entryKindDir
)

// statEntryKind follows symlinks like upstream's statSync-based checks.
func statEntryKind(path string) resolvedEntryKind {
	info, err := os.Stat(path)
	if err != nil {
		return entryKindMissing
	}
	if info.IsDir() {
		return entryKindDir
	}
	return entryKindFile
}

type dirEntryInfo struct {
	name   string
	isDir  bool
	isFile bool
}

// readPackageDirEntries resolves symlinked entries and drops dead links.
func readPackageDirEntries(dir string) []dirEntryInfo {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	infos := make([]dirEntryInfo, 0, len(entries))
	for _, entry := range entries {
		info := dirEntryInfo{name: entry.Name(), isDir: entry.IsDir(), isFile: entry.Type().IsRegular()}
		if entry.Type()&fs.ModeSymlink != 0 {
			resolved, err := os.Stat(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			info.isDir = resolved.IsDir()
			info.isFile = resolved.Mode().IsRegular()
		}
		infos = append(infos, info)
	}
	return infos
}

func relPosix(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func collectFiles(dir string, resourceType string, matcher *skillIgnoreMatcher, rootDir string) []string {
	files := make([]string, 0)
	if !pathExists(dir) {
		return files
	}
	root := rootDir
	if root == "" {
		root = dir
	}
	if matcher == nil {
		matcher = &skillIgnoreMatcher{}
	}
	matcher.addDirectoryRules(dir, root)

	for _, entry := range readPackageDirEntries(dir) {
		if strings.HasPrefix(entry.name, ".") || entry.name == "node_modules" {
			continue
		}
		fullPath := filepath.Join(dir, entry.name)
		relPath := relPosix(root, fullPath)
		if entry.isDir {
			if matcher.ignores(relPath+"/", true) {
				continue
			}
			files = append(files, collectFiles(fullPath, resourceType, matcher, root)...)
		} else if entry.isFile && resourceFileMatches(resourceType, entry.name) {
			if matcher.ignores(relPath, false) {
				continue
			}
			files = append(files, fullPath)
		}
	}
	return files
}

// collectSkillEntries: a SKILL.md folder contributes only that file; "pi" mode
// additionally loads root-level .md files as single-file skills.
func collectSkillEntries(dir, mode string, matcher *skillIgnoreMatcher, rootDir string) []string {
	entries := make([]string, 0)
	if !pathExists(dir) {
		return entries
	}
	root := rootDir
	if root == "" {
		root = dir
	}
	if matcher == nil {
		matcher = &skillIgnoreMatcher{}
	}
	matcher.addDirectoryRules(dir, root)

	dirEntries := readPackageDirEntries(dir)
	for _, entry := range dirEntries {
		if entry.name != "SKILL.md" {
			continue
		}
		fullPath := filepath.Join(dir, entry.name)
		if entry.isFile && !matcher.ignores(relPosix(root, fullPath), false) {
			return append(entries, fullPath)
		}
	}

	for _, entry := range dirEntries {
		if strings.HasPrefix(entry.name, ".") || entry.name == "node_modules" {
			continue
		}
		fullPath := filepath.Join(dir, entry.name)
		relPath := relPosix(root, fullPath)
		if mode == "pi" && dir == root && entry.isFile && strings.HasSuffix(entry.name, ".md") && !matcher.ignores(relPath, false) {
			entries = append(entries, fullPath)
			continue
		}
		if !entry.isDir || matcher.ignores(relPath+"/", true) {
			continue
		}
		entries = append(entries, collectSkillEntries(fullPath, mode, matcher, root)...)
	}
	return entries
}

// collectAutoFlatEntries covers prompts (.md) and themes (.json): top-level
// files only, honoring ignore files.
func collectAutoFlatEntries(dir, suffix string) []string {
	entries := make([]string, 0)
	if !pathExists(dir) {
		return entries
	}
	matcher := &skillIgnoreMatcher{}
	matcher.addDirectoryRules(dir, dir)
	for _, entry := range readPackageDirEntries(dir) {
		if strings.HasPrefix(entry.name, ".") || entry.name == "node_modules" {
			continue
		}
		fullPath := filepath.Join(dir, entry.name)
		if matcher.ignores(relPosix(dir, fullPath), false) {
			continue
		}
		if entry.isFile && strings.HasSuffix(entry.name, suffix) {
			entries = append(entries, fullPath)
		}
	}
	return entries
}

type piManifest struct {
	entries map[string][]string
}

func readPiManifestFile(packageJSONPath string) *piManifest {
	pkg, err := readPackageJSON(packageJSONPath)
	if err != nil {
		return nil
	}
	pi, ok := pkg["pi"].(map[string]any)
	if !ok {
		return nil
	}
	manifest := &piManifest{entries: map[string][]string{}}
	for _, resourceType := range packageResourceTypes {
		raw, exists := pi[resourceType]
		if !exists {
			continue
		}
		values, ok := raw.([]any)
		if !ok {
			continue
		}
		list := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				list = append(list, text)
			}
		}
		manifest.entries[resourceType] = list
	}
	return manifest
}

func readPiManifest(packageRoot string) *piManifest {
	packageJSONPath := filepath.Join(packageRoot, "package.json")
	if !pathExists(packageJSONPath) {
		return nil
	}
	return readPiManifestFile(packageJSONPath)
}

func (manifest *piManifest) list(resourceType string) ([]string, bool) {
	if manifest == nil {
		return nil, false
	}
	list, exists := manifest.entries[resourceType]
	return list, exists
}

// resolveExtensionEntries: explicit pi.extensions manifest entries, else
// index.ts/index.js.
func resolveExtensionEntries(dir string) []string {
	packageJSONPath := filepath.Join(dir, "package.json")
	if pathExists(packageJSONPath) {
		manifest := readPiManifestFile(packageJSONPath)
		if list, exists := manifest.list("extensions"); exists && len(list) > 0 {
			entries := make([]string, 0, len(list))
			for _, extPath := range list {
				resolved := filepath.Clean(filepath.Join(dir, extPath))
				if pathExists(resolved) {
					entries = append(entries, resolved)
				}
			}
			if len(entries) > 0 {
				return entries
			}
		}
	}
	if indexTS := filepath.Join(dir, "index.ts"); pathExists(indexTS) {
		return []string{indexTS}
	}
	if indexJS := filepath.Join(dir, "index.js"); pathExists(indexJS) {
		return []string{indexJS}
	}
	return nil
}

func collectAutoExtensionEntries(dir string) []string {
	entries := make([]string, 0)
	if !pathExists(dir) {
		return entries
	}
	if rootEntries := resolveExtensionEntries(dir); rootEntries != nil {
		return rootEntries
	}
	matcher := &skillIgnoreMatcher{}
	matcher.addDirectoryRules(dir, dir)
	for _, entry := range readPackageDirEntries(dir) {
		if strings.HasPrefix(entry.name, ".") || entry.name == "node_modules" {
			continue
		}
		fullPath := filepath.Join(dir, entry.name)
		relPath := relPosix(dir, fullPath)
		if entry.isDir {
			if matcher.ignores(relPath+"/", true) {
				continue
			}
			if resolvedEntries := resolveExtensionEntries(fullPath); resolvedEntries != nil {
				entries = append(entries, resolvedEntries...)
			}
		} else if entry.isFile && (strings.HasSuffix(entry.name, ".ts") || strings.HasSuffix(entry.name, ".js")) {
			if matcher.ignores(relPath, false) {
				continue
			}
			entries = append(entries, fullPath)
		}
	}
	return entries
}

func collectResourceFiles(dir, resourceType string) []string {
	switch resourceType {
	case "skills":
		return collectSkillEntries(dir, "pi", nil, "")
	case "extensions":
		return collectAutoExtensionEntries(dir)
	default:
		return collectFiles(dir, resourceType, nil, "")
	}
}

// Pattern helpers (upstream minimatch usage → doublestar).

func isFilterPattern(entry string) bool {
	return strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "+") || strings.HasPrefix(entry, "-") ||
		strings.Contains(entry, "*") || strings.Contains(entry, "?")
}

func isOverridePattern(entry string) bool {
	return strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "+") || strings.HasPrefix(entry, "-")
}

func hasGlobPattern(entry string) bool {
	return strings.Contains(entry, "*") || strings.Contains(entry, "?")
}

func splitFilterPatterns(entries []string) (plain, patterns []string) {
	for _, entry := range entries {
		if isFilterPattern(entry) {
			patterns = append(patterns, entry)
		} else {
			plain = append(plain, entry)
		}
	}
	return plain, patterns
}

func globMatch(pattern, value string) bool {
	matched, err := doublestar.Match(pattern, value)
	return err == nil && matched
}

func matchesAnyPattern(filePath string, patterns []string, baseDir string) bool {
	rel := relPosix(baseDir, filePath)
	name := filepath.Base(filePath)
	filePathPosix := filepath.ToSlash(filePath)
	isSkillFile := name == "SKILL.md"
	parentRel, parentName, parentPosix := "", "", ""
	if isSkillFile {
		parentDir := filepath.Dir(filePath)
		parentRel = relPosix(baseDir, parentDir)
		parentName = filepath.Base(parentDir)
		parentPosix = filepath.ToSlash(parentDir)
	}
	for _, pattern := range patterns {
		normalizedPattern := filepath.ToSlash(pattern)
		if globMatch(normalizedPattern, rel) || globMatch(normalizedPattern, name) || globMatch(normalizedPattern, filePathPosix) {
			return true
		}
		if !isSkillFile {
			continue
		}
		if globMatch(normalizedPattern, parentRel) || globMatch(normalizedPattern, parentName) || globMatch(normalizedPattern, parentPosix) {
			return true
		}
	}
	return false
}

func normalizeExactPattern(pattern string) string {
	if strings.HasPrefix(pattern, "./") || strings.HasPrefix(pattern, ".\\") {
		pattern = pattern[2:]
	}
	return filepath.ToSlash(pattern)
}

func matchesAnyExactPattern(filePath string, patterns []string, baseDir string) bool {
	if len(patterns) == 0 {
		return false
	}
	rel := relPosix(baseDir, filePath)
	name := filepath.Base(filePath)
	filePathPosix := filepath.ToSlash(filePath)
	isSkillFile := name == "SKILL.md"
	parentRel, parentPosix := "", ""
	if isSkillFile {
		parentDir := filepath.Dir(filePath)
		parentRel = relPosix(baseDir, parentDir)
		parentPosix = filepath.ToSlash(parentDir)
	}
	for _, pattern := range patterns {
		normalized := normalizeExactPattern(pattern)
		if normalized == rel || normalized == filePathPosix {
			return true
		}
		if isSkillFile && (normalized == parentRel || normalized == parentPosix) {
			return true
		}
	}
	return false
}

func isEnabledByOverrides(filePath string, patterns []string, baseDir string) bool {
	excludes := make([]string, 0)
	forceIncludes := make([]string, 0)
	forceExcludes := make([]string, 0)
	for _, pattern := range patterns {
		switch {
		case strings.HasPrefix(pattern, "!"):
			excludes = append(excludes, pattern[1:])
		case strings.HasPrefix(pattern, "+"):
			forceIncludes = append(forceIncludes, pattern[1:])
		case strings.HasPrefix(pattern, "-"):
			forceExcludes = append(forceExcludes, pattern[1:])
		}
	}
	enabled := len(excludes) == 0 || !matchesAnyPattern(filePath, excludes, baseDir)
	if len(forceIncludes) > 0 && matchesAnyExactPattern(filePath, forceIncludes, baseDir) {
		enabled = true
	}
	if len(forceExcludes) > 0 && matchesAnyExactPattern(filePath, forceExcludes, baseDir) {
		enabled = false
	}
	return enabled
}

// applyPatterns: includes (or all), minus !excludes, plus +force-includes,
// minus -force-excludes.
func applyPatterns(allPaths, patterns []string, baseDir string) map[string]bool {
	includes := make([]string, 0)
	excludes := make([]string, 0)
	forceIncludes := make([]string, 0)
	forceExcludes := make([]string, 0)
	for _, pattern := range patterns {
		switch {
		case strings.HasPrefix(pattern, "+"):
			forceIncludes = append(forceIncludes, pattern[1:])
		case strings.HasPrefix(pattern, "-"):
			forceExcludes = append(forceExcludes, pattern[1:])
		case strings.HasPrefix(pattern, "!"):
			excludes = append(excludes, pattern[1:])
		default:
			includes = append(includes, pattern)
		}
	}

	result := make([]string, 0, len(allPaths))
	if len(includes) == 0 {
		result = append(result, allPaths...)
	} else {
		for _, filePath := range allPaths {
			if matchesAnyPattern(filePath, includes, baseDir) {
				result = append(result, filePath)
			}
		}
	}
	if len(excludes) > 0 {
		filtered := result[:0]
		for _, filePath := range result {
			if !matchesAnyPattern(filePath, excludes, baseDir) {
				filtered = append(filtered, filePath)
			}
		}
		result = filtered
	}
	if len(forceIncludes) > 0 {
		present := make(map[string]bool, len(result))
		for _, filePath := range result {
			present[filePath] = true
		}
		for _, filePath := range allPaths {
			if !present[filePath] && matchesAnyExactPattern(filePath, forceIncludes, baseDir) {
				result = append(result, filePath)
			}
		}
	}
	if len(forceExcludes) > 0 {
		filtered := result[:0]
		for _, filePath := range result {
			if !matchesAnyExactPattern(filePath, forceExcludes, baseDir) {
				filtered = append(filtered, filePath)
			}
		}
		result = filtered
	}
	enabled := make(map[string]bool, len(result))
	for _, filePath := range result {
		enabled[filePath] = true
	}
	return enabled
}

type autoloadDecision struct {
	path    string
	enabled bool
}

func applyAutoloadDisabledPatterns(allPaths, patterns []string, baseDir string) []autoloadDecision {
	decided := make(map[string]int)
	decisions := make([]autoloadDecision, 0)
	for _, pattern := range patterns {
		target := pattern
		if strings.HasPrefix(pattern, "+") || strings.HasPrefix(pattern, "-") || strings.HasPrefix(pattern, "!") {
			target = pattern[1:]
		}
		enabled := !strings.HasPrefix(pattern, "-") && !strings.HasPrefix(pattern, "!")
		exact := strings.HasPrefix(pattern, "+") || strings.HasPrefix(pattern, "-")
		for _, filePath := range allPaths {
			matched := false
			if exact {
				matched = matchesAnyExactPattern(filePath, []string{target}, baseDir)
			} else {
				matched = matchesAnyPattern(filePath, []string{target}, baseDir)
			}
			if !matched {
				continue
			}
			if index, exists := decided[filePath]; exists {
				decisions[index].enabled = enabled
			} else {
				decided[filePath] = len(decisions)
				decisions = append(decisions, autoloadDecision{path: filePath, enabled: enabled})
			}
		}
	}
	return decisions
}

// Accumulator with insertion order and first-wins semantics.

type resourceEntry struct {
	path     string
	metadata PathMetadata
	enabled  bool
}

type resourceMap struct {
	order   []int
	indexOf map[string]int
	entries []resourceEntry
}

func newResourceMap() *resourceMap {
	return &resourceMap{indexOf: map[string]int{}}
}

func (m *resourceMap) add(path string, metadata PathMetadata, enabled bool) {
	if path == "" {
		return
	}
	if _, exists := m.indexOf[path]; exists {
		return
	}
	m.indexOf[path] = len(m.entries)
	m.order = append(m.order, len(m.entries))
	m.entries = append(m.entries, resourceEntry{path: path, metadata: metadata, enabled: enabled})
}

type resourceAccumulator struct {
	maps map[string]*resourceMap
}

func newResourceAccumulator() *resourceAccumulator {
	accumulator := &resourceAccumulator{maps: map[string]*resourceMap{}}
	for _, resourceType := range packageResourceTypes {
		accumulator.maps[resourceType] = newResourceMap()
	}
	return accumulator
}

// resourcePrecedenceRank: lower = higher precedence for canonical-path dedup.
func resourcePrecedenceRank(metadata PathMetadata) int {
	if metadata.Origin == "package" {
		return 4
	}
	scopeBase := 2
	if metadata.Scope == "project" {
		scopeBase = 0
	}
	if metadata.Source == "local" {
		return scopeBase
	}
	return scopeBase + 1
}

func (accumulator *resourceAccumulator) toResolvedPaths() *ResolvedPaths {
	convert := func(m *resourceMap) []ResolvedResource {
		resolved := make([]ResolvedResource, 0, len(m.entries))
		for _, index := range m.order {
			entry := m.entries[index]
			resolved = append(resolved, ResolvedResource{Path: entry.path, Enabled: entry.enabled, Metadata: entry.metadata})
		}
		sort.SliceStable(resolved, func(a, b int) bool {
			return resourcePrecedenceRank(resolved[a].Metadata) < resourcePrecedenceRank(resolved[b].Metadata)
		})
		seen := make(map[string]bool, len(resolved))
		deduped := make([]ResolvedResource, 0, len(resolved))
		for _, entry := range resolved {
			canonicalPath := canonicalResourcePath(entry.Path)
			if seen[canonicalPath] {
				continue
			}
			seen[canonicalPath] = true
			deduped = append(deduped, entry)
		}
		return deduped
	}
	return &ResolvedPaths{
		Extensions: convert(accumulator.maps["extensions"]),
		Skills:     convert(accumulator.maps["skills"]),
		Prompts:    convert(accumulator.maps["prompts"]),
		Themes:     convert(accumulator.maps["themes"]),
	}
}

// Resolve resolves all configured packages, settings entries, and
// auto-discovered resources. onMissing (nil = install) answers what to do for
// configured-but-uninstalled sources.
func (manager *PackageManager) Resolve(onMissing func(source string) (MissingSourceAction, error)) (*ResolvedPaths, error) {
	accumulator := newResourceAccumulator()
	packageSources := manager.dedupePackages(manager.collectConfiguredPackages())
	if err := manager.resolvePackageSources(packageSources, accumulator, onMissing); err != nil {
		return nil, err
	}

	globalBaseDir := manager.agentDir
	projectBaseDir := filepath.Join(manager.cwd, packageManagerProjectDir)

	for _, resourceType := range packageResourceTypes {
		target := accumulator.maps[resourceType]
		globalEntries := manager.topLevelEntries(config.GlobalSettings, resourceType)
		projectEntries := manager.topLevelEntries(config.ProjectSettings, resourceType)
		manager.resolveLocalEntries(projectEntries, resourceType, target,
			PathMetadata{Source: "local", Scope: "project", Origin: "top-level"}, projectBaseDir)
		manager.resolveLocalEntries(globalEntries, resourceType, target,
			PathMetadata{Source: "local", Scope: "user", Origin: "top-level"}, globalBaseDir)
	}

	manager.addAutoDiscoveredResources(accumulator, globalBaseDir, projectBaseDir)
	return accumulator.toResolvedPaths(), nil
}

// ResolveExtensionSources resolves ad-hoc sources (the -e flag path).
func (manager *PackageManager) ResolveExtensionSources(sources []string, local, temporary bool) (*ResolvedPaths, error) {
	accumulator := newResourceAccumulator()
	scope := "user"
	if temporary {
		scope = "temporary"
	} else if local {
		scope = "project"
	}
	packageSources := make([]scopedPackageSource, 0, len(sources))
	for _, source := range sources {
		packageSources = append(packageSources, scopedPackageSource{pkg: config.PackageSource{Source: source}, scope: scope})
	}
	if err := manager.resolvePackageSources(packageSources, accumulator, nil); err != nil {
		return nil, err
	}
	return accumulator.toResolvedPaths(), nil
}

func (manager *PackageManager) topLevelEntries(scope config.SettingsScope, resourceType string) []string {
	switch resourceType {
	case "extensions":
		if scope == config.ProjectSettings {
			return manager.settings.GetProjectExtensionPaths()
		}
		return manager.settings.GetGlobalExtensionPaths()
	case "skills":
		if scope == config.ProjectSettings {
			return manager.settings.GetProjectSkillPaths()
		}
		return manager.settings.GetGlobalSkillPaths()
	case "prompts":
		if scope == config.ProjectSettings {
			return manager.settings.GetProjectPromptTemplatePaths()
		}
		return manager.settings.GetGlobalPromptTemplatePaths()
	default:
		if scope == config.ProjectSettings {
			return manager.settings.GetProjectThemePaths()
		}
		return manager.settings.GetGlobalThemePaths()
	}
}

func (manager *PackageManager) resolvePackageSources(
	sources []scopedPackageSource,
	accumulator *resourceAccumulator,
	onMissing func(source string) (MissingSourceAction, error),
) error {
	for _, entry := range sources {
		sourceStr := entry.pkg.Source
		var filter *config.PackageSource
		if entry.pkg.IsObject {
			pkgCopy := entry.pkg
			filter = &pkgCopy
		}
		deltaBase := manager.findAutoloadDeltaBase(entry.pkg, entry.scope, sources)
		resolvedSource := sourceStr
		resolvedScope := entry.scope
		if deltaBase != nil {
			resolvedSource = deltaBase.pkg.Source
			resolvedScope = deltaBase.scope
		}
		parsed := manager.parseSource(resolvedSource)
		metadata := PathMetadata{Source: sourceStr, Scope: entry.scope, Origin: "package"}

		if parsed.local != nil {
			baseDir, err := manager.getBaseDirForScope(resolvedScope)
			if err != nil {
				return err
			}
			manager.resolveLocalExtensionSource(parsed.local, accumulator, filter, metadata, baseDir)
			continue
		}

		installMissing := func() (bool, error) {
			if isOfflineModeEnabled() {
				return false, nil
			}
			if onMissing == nil {
				return true, manager.installParsedSource(parsed, resolvedScope)
			}
			action, err := onMissing(resolvedSource)
			if err != nil {
				return false, err
			}
			if action == MissingSourceSkip {
				return false, nil
			}
			if action == MissingSourceError {
				return false, fmt.Errorf("Missing source: %s", resolvedSource) //nolint:staticcheck // Upstream error text is observable.
			}
			return true, manager.installParsedSource(parsed, resolvedScope)
		}

		if parsed.npm != nil {
			installedPath, err := manager.getNpmInstallPath(parsed.npm, resolvedScope)
			if err != nil {
				return err
			}
			needsInstall := !pathExists(installedPath) || !manager.installedNpmMatchesConfiguredVersion(parsed.npm, installedPath)
			if needsInstall {
				installed, err := installMissing()
				if err != nil {
					return err
				}
				if !installed {
					continue
				}
				installedPath, err = manager.getNpmInstallPath(parsed.npm, resolvedScope)
				if err != nil {
					return err
				}
			}
			metadata.BaseDir = installedPath
			manager.collectPackageResources(installedPath, accumulator, filter, metadata)
			continue
		}

		if parsed.git != nil {
			installedPath, err := manager.getGitInstallPath(parsed.git, resolvedScope)
			if err != nil {
				return err
			}
			if !pathExists(installedPath) {
				installed, err := installMissing()
				if err != nil {
					return err
				}
				if !installed {
					continue
				}
			} else if resolvedScope == "temporary" && !parsed.git.Pinned && !isOfflineModeEnabled() {
				manager.refreshTemporaryGitSource(parsed.git, resolvedSource)
			}
			metadata.BaseDir = installedPath
			manager.collectPackageResources(installedPath, accumulator, filter, metadata)
		}
	}
	return nil
}

func (manager *PackageManager) installParsedSource(parsed parsedSource, scope string) error {
	switch {
	case parsed.npm != nil:
		return manager.installNpm(parsed.npm, scope, scope == "temporary")
	case parsed.git != nil:
		return manager.installGit(parsed.git, scope)
	default:
		return nil
	}
}

func (manager *PackageManager) resolveLocalExtensionSource(
	source *localSource,
	accumulator *resourceAccumulator,
	filter *config.PackageSource,
	metadata PathMetadata,
	baseDir string,
) {
	resolved := pmResolvePath(source.path, baseDir)
	switch statEntryKind(resolved) {
	case entryKindFile:
		metadata.BaseDir = filepath.Dir(resolved)
		accumulator.maps["extensions"].add(resolved, metadata, true)
	case entryKindDir:
		metadata.BaseDir = resolved
		if !manager.collectPackageResources(resolved, accumulator, filter, metadata) {
			accumulator.maps["extensions"].add(resolved, metadata, true)
		}
	default:
	}
}

func filterList(filter *config.PackageSource, resourceType string) ([]string, bool) {
	if filter == nil {
		return nil, false
	}
	switch resourceType {
	case "extensions":
		return filter.Extensions, filter.Extensions != nil
	case "skills":
		return filter.Skills, filter.Skills != nil
	case "prompts":
		return filter.Prompts, filter.Prompts != nil
	default:
		return filter.Themes, filter.Themes != nil
	}
}

func (manager *PackageManager) collectPackageResources(
	packageRoot string,
	accumulator *resourceAccumulator,
	filter *config.PackageSource,
	metadata PathMetadata,
) bool {
	if filter != nil {
		for _, resourceType := range packageResourceTypes {
			target := accumulator.maps[resourceType]
			patterns, hasPatterns := filterList(filter, resourceType)
			if filter.Autoload != nil && !*filter.Autoload {
				manager.applyPackageDeltaFilter(packageRoot, patterns, resourceType, target, metadata)
			} else if hasPatterns {
				manager.applyPackageFilter(packageRoot, patterns, resourceType, target, metadata)
			} else {
				manager.collectDefaultResources(packageRoot, resourceType, target, metadata)
			}
		}
		return true
	}

	manifest := readPiManifest(packageRoot)
	if manifest != nil {
		for _, resourceType := range packageResourceTypes {
			entries, _ := manifest.list(resourceType)
			manager.addManifestEntries(entries, packageRoot, resourceType, accumulator.maps[resourceType], metadata)
		}
		return true
	}

	hasAnyDir := false
	for _, resourceType := range packageResourceTypes {
		dir := filepath.Join(packageRoot, resourceType)
		if !pathExists(dir) {
			continue
		}
		for _, file := range collectResourceFiles(dir, resourceType) {
			accumulator.maps[resourceType].add(file, metadata, true)
		}
		hasAnyDir = true
	}
	return hasAnyDir
}

func (manager *PackageManager) collectDefaultResources(
	packageRoot, resourceType string,
	target *resourceMap,
	metadata PathMetadata,
) {
	manifest := readPiManifest(packageRoot)
	if entries, exists := manifest.list(resourceType); exists {
		manager.addManifestEntries(entries, packageRoot, resourceType, target, metadata)
		return
	}
	dir := filepath.Join(packageRoot, resourceType)
	if !pathExists(dir) {
		return
	}
	for _, file := range collectResourceFiles(dir, resourceType) {
		target.add(file, metadata, true)
	}
}

func (manager *PackageManager) applyPackageFilter(
	packageRoot string,
	userPatterns []string,
	resourceType string,
	target *resourceMap,
	metadata PathMetadata,
) {
	allFiles := manager.collectManifestFiles(packageRoot, resourceType)
	if len(userPatterns) == 0 {
		// An explicit empty array disables all resources of this type.
		for _, file := range allFiles {
			target.add(file, metadata, false)
		}
		return
	}
	enabledByUser := applyPatterns(allFiles, userPatterns, packageRoot)
	for _, file := range allFiles {
		target.add(file, metadata, enabledByUser[file])
	}
}

func (manager *PackageManager) applyPackageDeltaFilter(
	packageRoot string,
	userPatterns []string,
	resourceType string,
	target *resourceMap,
	metadata PathMetadata,
) {
	if len(userPatterns) == 0 {
		return
	}
	allFiles := manager.collectManifestFiles(packageRoot, resourceType)
	for _, decision := range applyAutoloadDisabledPatterns(allFiles, userPatterns, packageRoot) {
		target.add(decision.path, metadata, decision.enabled)
	}
}

// collectManifestFiles returns the files enabled by the package's own
// manifest patterns (or all convention-directory files).
func (manager *PackageManager) collectManifestFiles(packageRoot, resourceType string) []string {
	manifest := readPiManifest(packageRoot)
	if entries, exists := manifest.list(resourceType); exists && len(entries) > 0 {
		allFiles := manager.collectFilesFromManifestEntries(entries, packageRoot, resourceType)
		manifestPatterns := make([]string, 0)
		for _, entry := range entries {
			if isOverridePattern(entry) {
				manifestPatterns = append(manifestPatterns, entry)
			}
		}
		if len(manifestPatterns) == 0 {
			return allFiles
		}
		enabled := applyPatterns(allFiles, manifestPatterns, packageRoot)
		filtered := make([]string, 0, len(allFiles))
		for _, file := range allFiles {
			if enabled[file] {
				filtered = append(filtered, file)
			}
		}
		return filtered
	}

	conventionDir := filepath.Join(packageRoot, resourceType)
	if !pathExists(conventionDir) {
		return nil
	}
	return collectResourceFiles(conventionDir, resourceType)
}

func (manager *PackageManager) addManifestEntries(
	entries []string,
	root, resourceType string,
	target *resourceMap,
	metadata PathMetadata,
) {
	if entries == nil {
		return
	}
	allFiles := manager.collectFilesFromManifestEntries(entries, root, resourceType)
	patterns := make([]string, 0)
	for _, entry := range entries {
		if isOverridePattern(entry) {
			patterns = append(patterns, entry)
		}
	}
	enabledPaths := applyPatterns(allFiles, patterns, root)
	for _, file := range allFiles {
		if enabledPaths[file] {
			target.add(file, metadata, true)
		}
	}
}

func (manager *PackageManager) collectFilesFromManifestEntries(entries []string, root, resourceType string) []string {
	resolved := make([]string, 0, len(entries))
	for _, entry := range entries {
		if isOverridePattern(entry) {
			continue
		}
		if !hasGlobPattern(entry) {
			resolved = append(resolved, filepath.Clean(filepath.Join(root, entry)))
			continue
		}
		pattern := filepath.ToSlash(entry)
		for strings.HasPrefix(pattern, "./") {
			pattern = pattern[2:]
		}
		matches, err := doublestar.Glob(os.DirFS(root), pattern)
		if err != nil {
			continue
		}
		sort.Strings(matches)
		for _, match := range matches {
			// node-glob dot:false — wildcard matches never include dotfiles.
			if hasHiddenSegment(match) {
				continue
			}
			resolved = append(resolved, filepath.Join(root, filepath.FromSlash(match)))
		}
	}
	return manager.collectFilesFromPaths(resolved, resourceType)
}

func hasHiddenSegment(relPath string) bool {
	for segment := range strings.SplitSeq(filepath.ToSlash(relPath), "/") {
		if strings.HasPrefix(segment, ".") && segment != "." && segment != ".." {
			return true
		}
	}
	return false
}

func (manager *PackageManager) resolveLocalEntries(
	entries []string,
	resourceType string,
	target *resourceMap,
	metadata PathMetadata,
	baseDir string,
) {
	if len(entries) == 0 {
		return
	}
	plain, patterns := splitFilterPatterns(entries)
	resolvedPlain := make([]string, 0, len(plain))
	for _, entry := range plain {
		resolvedPlain = append(resolvedPlain, pmResolvePath(entry, baseDir))
	}
	allFiles := manager.collectFilesFromPaths(resolvedPlain, resourceType)
	enabledPaths := applyPatterns(allFiles, patterns, baseDir)
	for _, file := range allFiles {
		target.add(file, metadata, enabledPaths[file])
	}
}

func (manager *PackageManager) collectFilesFromPaths(paths []string, resourceType string) []string {
	files := make([]string, 0, len(paths))
	for _, path := range paths {
		switch statEntryKind(path) {
		case entryKindFile:
			files = append(files, path)
		case entryKindDir:
			files = append(files, collectResourceFiles(path, resourceType)...)
		default:
		}
	}
	return files
}

func (manager *PackageManager) addAutoDiscoveredResources(accumulator *resourceAccumulator, globalBaseDir, projectBaseDir string) {
	userMetadata := PathMetadata{Source: "auto", Scope: "user", Origin: "top-level", BaseDir: globalBaseDir}
	projectMetadata := PathMetadata{Source: "auto", Scope: "project", Origin: "top-level", BaseDir: projectBaseDir}

	userOverrides := map[string][]string{}
	projectOverrides := map[string][]string{}
	for _, resourceType := range packageResourceTypes {
		userOverrides[resourceType] = manager.topLevelEntries(config.GlobalSettings, resourceType)
		projectOverrides[resourceType] = manager.topLevelEntries(config.ProjectSettings, resourceType)
	}

	userAgentsSkillsDir := filepath.Join(pmHomeDir(), ".agents", "skills")
	projectTrusted := manager.settings.IsProjectTrusted()
	projectAgentsSkillDirs := make([]string, 0)
	if projectTrusted {
		for _, dir := range ancestorAgentsSkillDirs(manager.cwd) {
			if resolveResourcePath(dir) != resolveResourcePath(userAgentsSkillsDir) {
				projectAgentsSkillDirs = append(projectAgentsSkillDirs, dir)
			}
		}
	}

	addResources := func(resourceType string, paths []string, metadata PathMetadata, overrides []string, baseDir string) {
		target := accumulator.maps[resourceType]
		for _, path := range paths {
			target.add(path, metadata, isEnabledByOverrides(path, overrides, baseDir))
		}
	}

	if projectTrusted {
		addResources("extensions", collectAutoExtensionEntries(filepath.Join(projectBaseDir, "extensions")),
			projectMetadata, projectOverrides["extensions"], projectBaseDir)
		addResources("skills", collectSkillEntries(filepath.Join(projectBaseDir, "skills"), "pi", nil, ""),
			projectMetadata, projectOverrides["skills"], projectBaseDir)
	}

	for _, agentsSkillsDir := range projectAgentsSkillDirs {
		agentsBaseDir := filepath.Dir(agentsSkillsDir)
		agentsMetadata := projectMetadata
		agentsMetadata.BaseDir = agentsBaseDir
		addResources("skills", collectSkillEntries(agentsSkillsDir, "agents", nil, ""),
			agentsMetadata, projectOverrides["skills"], agentsBaseDir)
	}

	if projectTrusted {
		addResources("prompts", collectAutoFlatEntries(filepath.Join(projectBaseDir, "prompts"), ".md"),
			projectMetadata, projectOverrides["prompts"], projectBaseDir)
		addResources("themes", collectAutoFlatEntries(filepath.Join(projectBaseDir, "themes"), ".json"),
			projectMetadata, projectOverrides["themes"], projectBaseDir)
	}

	addResources("extensions", collectAutoExtensionEntries(filepath.Join(globalBaseDir, "extensions")),
		userMetadata, userOverrides["extensions"], globalBaseDir)
	addResources("skills", collectSkillEntries(filepath.Join(globalBaseDir, "skills"), "pi", nil, ""),
		userMetadata, userOverrides["skills"], globalBaseDir)

	userAgentsBaseDir := filepath.Dir(userAgentsSkillsDir)
	userAgentsMetadata := userMetadata
	userAgentsMetadata.BaseDir = userAgentsBaseDir
	addResources("skills", collectSkillEntries(userAgentsSkillsDir, "agents", nil, ""),
		userAgentsMetadata, userOverrides["skills"], userAgentsBaseDir)

	addResources("prompts", collectAutoFlatEntries(filepath.Join(globalBaseDir, "prompts"), ".md"),
		userMetadata, userOverrides["prompts"], globalBaseDir)
	addResources("themes", collectAutoFlatEntries(filepath.Join(globalBaseDir, "themes"), ".json"),
		userMetadata, userOverrides["themes"], globalBaseDir)
}
