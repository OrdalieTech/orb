package modes

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/modes/theme"
	"github.com/OrdalieTech/pigo/tui"
)

type ConfigWriteScope string

const (
	ConfigWriteGlobal  ConfigWriteScope = "global"
	ConfigWriteProject ConfigWriteScope = "project"
)

type configResourceType string

const (
	configExtensions configResourceType = "extensions"
	configSkills     configResourceType = "skills"
	configPrompts    configResourceType = "prompts"
	configThemes     configResourceType = "themes"
)

var configResourceLabels = map[configResourceType]string{
	configExtensions: "Extensions",
	configSkills:     "Skills",
	configPrompts:    "Prompts",
	configThemes:     "Themes",
}

type ScopedResolvedPaths struct {
	Global  *codingagent.ResolvedPaths
	Project *codingagent.ResolvedPaths
}

type ConfigSelectorOptions struct {
	ResolvedPaths        ScopedResolvedPaths
	SettingsManager      *config.SettingsManager
	CWD                  string
	AgentDir             string
	WriteScope           ConfigWriteScope
	ProjectModeAvailable bool
	TerminalHeight       int
}

type projectOverrideState string

const (
	projectInherit projectOverrideState = "inherit"
	projectLoad    projectOverrideState = "load"
	projectUnload  projectOverrideState = "unload"
)

type configResourceItem struct {
	path         string
	enabled      bool
	metadata     codingagent.PathMetadata
	resourceType configResourceType
	displayName  string
}

type configResourceSubgroup struct {
	resourceType configResourceType
	label        string
	items        []*configResourceItem
}

type configResourceGroup struct {
	label     string
	scope     string
	origin    string
	source    string
	subgroups []*configResourceSubgroup
}

func formatConfigBaseDir(baseDir string) string {
	homeDir, _ := os.UserHomeDir()
	displayPath := filepath.ToSlash(baseDir)
	if homeDir != "" {
		switch {
		case baseDir == homeDir:
			displayPath = "~"
		case strings.HasPrefix(baseDir, homeDir):
			displayPath = "~" + filepath.ToSlash(baseDir[len(homeDir):])
		}
	}
	if !strings.HasSuffix(displayPath, "/") {
		displayPath += "/"
	}
	return displayPath
}

func configGroupLabel(metadata codingagent.PathMetadata, agentDir string) string {
	if metadata.Origin == "package" {
		return fmt.Sprintf("%s (%s)", metadata.Source, metadata.Scope)
	}
	if metadata.Source == "auto" {
		if metadata.BaseDir != "" {
			if metadata.Scope == "user" {
				return "User (" + formatConfigBaseDir(metadata.BaseDir) + ")"
			}
			return "Project (" + formatConfigBaseDir(metadata.BaseDir) + ")"
		}
		if metadata.Scope == "user" {
			return "User (" + formatConfigBaseDir(agentDir) + ")"
		}
		return "Project (" + config.ConfigDirName + "/)"
	}
	if metadata.Scope == "user" {
		return "User settings"
	}
	return "Project settings"
}

func compareConfigNames(left, right string) int {
	leftFolded, rightFolded := strings.ToLower(left), strings.ToLower(right)
	if leftFolded < rightFolded {
		return -1
	}
	if leftFolded > rightFolded {
		return 1
	}
	if left == right {
		return 0
	}
	if left == leftFolded {
		return -1
	}
	if right == rightFolded {
		return 1
	}
	return strings.Compare(left, right)
}

func buildConfigGroups(resolved *codingagent.ResolvedPaths, agentDir string) []*configResourceGroup {
	if resolved == nil {
		resolved = &codingagent.ResolvedPaths{}
	}
	groupsByKey := make(map[string]*configResourceGroup)
	addResources := func(resources []codingagent.ResolvedResource, resourceType configResourceType) {
		for _, resource := range resources {
			metadata := resource.Metadata
			groupKey := strings.Join([]string{metadata.Origin, metadata.Scope, metadata.Source, metadata.BaseDir}, ":")
			group := groupsByKey[groupKey]
			if group == nil {
				group = &configResourceGroup{
					label: configGroupLabel(metadata, agentDir), scope: metadata.Scope,
					origin: metadata.Origin, source: metadata.Source,
				}
				groupsByKey[groupKey] = group
			}
			var subgroup *configResourceSubgroup
			for _, candidate := range group.subgroups {
				if candidate.resourceType == resourceType {
					subgroup = candidate
					break
				}
			}
			if subgroup == nil {
				subgroup = &configResourceSubgroup{resourceType: resourceType, label: configResourceLabels[resourceType]}
				group.subgroups = append(group.subgroups, subgroup)
			}

			fileName := filepath.Base(resource.Path)
			parentFolder := filepath.Base(filepath.Dir(resource.Path))
			displayName := fileName
			if resourceType == configExtensions && parentFolder != "extensions" {
				displayName = filepath.Join(parentFolder, fileName)
			} else if resourceType == configSkills && fileName == "SKILL.md" {
				displayName = parentFolder
			}
			subgroup.items = append(subgroup.items, &configResourceItem{
				path: resource.Path, enabled: resource.Enabled, metadata: metadata, resourceType: resourceType,
				displayName: displayName,
			})
		}
	}

	addResources(resolved.Extensions, configExtensions)
	addResources(resolved.Skills, configSkills)
	addResources(resolved.Prompts, configPrompts)
	addResources(resolved.Themes, configThemes)

	groups := make([]*configResourceGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		groups = append(groups, group)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left, right := groups[i], groups[j]
		if left.origin != right.origin {
			return left.origin == "package"
		}
		if left.scope != right.scope {
			return left.scope == "user"
		}
		return compareConfigNames(left.source, right.source) < 0
	})
	typeOrder := map[configResourceType]int{configExtensions: 0, configSkills: 1, configPrompts: 2, configThemes: 3}
	for _, group := range groups {
		sort.SliceStable(group.subgroups, func(i, j int) bool {
			return typeOrder[group.subgroups[i].resourceType] < typeOrder[group.subgroups[j].resourceType]
		})
		for _, subgroup := range group.subgroups {
			sort.SliceStable(subgroup.items, func(i, j int) bool {
				return compareConfigNames(subgroup.items[i].displayName, subgroup.items[j].displayName) < 0
			})
		}
	}
	return groups
}

type configFlatEntry struct {
	kind     string
	group    *configResourceGroup
	subgroup *configResourceSubgroup
	item     *configResourceItem
}

type configSelectorHeader struct {
	mu                   sync.RWMutex
	writeScope           ConfigWriteScope
	projectModeAvailable bool
}

func (header *configSelectorHeader) setWriteScope(scope ConfigWriteScope) {
	header.mu.Lock()
	header.writeScope = scope
	header.mu.Unlock()
}

func (header *configSelectorHeader) Invalidate() {}

func (header *configSelectorHeader) Render(width int) []string {
	header.mu.RLock()
	scope, projectModeAvailable := header.writeScope, header.projectModeAvailable
	header.mu.RUnlock()
	title := theme.Bold("Global Resources")
	if scope == ConfigWriteProject {
		title = theme.Bold("Project Local Resources")
	}
	separator := theme.FG("muted", " · ")
	switchHint := ""
	if projectModeAvailable {
		switchHint = theme.FG("accent", KeyText("tui.input.tab")) + " switch mode" + separator
	}
	actionHint := "space toggle"
	if scope == ConfigWriteProject {
		actionHint = "space cycle inherit/+/-"
	}
	hint := switchHint + actionHint + separator + "esc close"
	spacing := max(1, width-tui.VisibleWidth(title)-tui.VisibleWidth(hint))
	scopeHint := theme.FG("muted", "~/"+config.ConfigDirName+"/agent/settings.json")
	if scope == ConfigWriteProject {
		scopeHint = theme.FG("muted", config.ConfigDirName+"/settings.json · inherited global resources are dimmed")
	}
	return []string{
		tui.TruncateToWidth(title+strings.Repeat(" ", spacing)+hint, width, "", false),
		tui.TruncateToWidth(scopeHint, width, "", false),
	}
}

type configResourceList struct {
	mu sync.Mutex

	groupsByScope map[ConfigWriteScope][]*configResourceGroup
	flatItems     []configFlatEntry
	filteredItems []configFlatEntry
	selectedIndex int
	searchInput   *tui.Input
	maxVisible    int
	settings      *config.SettingsManager
	cwd           string
	agentDir      string
	writeScope    ConfigWriteScope
	inherited     map[string]bool

	onCancel     func()
	onExit       func()
	onToggle     func()
	onSwitchMode func()
}

func newConfigResourceList(
	groupsByScope map[ConfigWriteScope][]*configResourceGroup,
	settings *config.SettingsManager,
	cwd, agentDir string,
	terminalHeight int,
	writeScope ConfigWriteScope,
) *configResourceList {
	if terminalHeight == 0 {
		terminalHeight = 24
	}
	list := &configResourceList{
		groupsByScope: groupsByScope, searchInput: tui.NewInput(), maxVisible: max(5, terminalHeight-8),
		settings: settings, cwd: cwd, agentDir: agentDir, writeScope: writeScope, inherited: make(map[string]bool),
	}
	for _, group := range groupsByScope[ConfigWriteGlobal] {
		for _, subgroup := range group.subgroups {
			for _, item := range subgroup.items {
				list.inherited[list.resourceItemKey(item)] = item.enabled
			}
		}
	}
	list.buildFlatListLocked()
	list.filteredItems = append([]configFlatEntry(nil), list.flatItems...)
	return list
}

func (list *configResourceList) SetFocused(focused bool) {
	list.mu.Lock()
	list.searchInput.SetFocused(focused)
	list.mu.Unlock()
}

func (list *configResourceList) Invalidate() {}

func (list *configResourceList) groupsLocked() []*configResourceGroup {
	return list.groupsByScope[list.writeScope]
}

func (list *configResourceList) setWriteScope(scope ConfigWriteScope) {
	list.mu.Lock()
	list.writeScope = scope
	list.buildFlatListLocked()
	list.filterItemsLocked(list.searchInput.GetValue())
	list.mu.Unlock()
}

func (list *configResourceList) buildFlatListLocked() {
	list.flatItems = nil
	for _, group := range list.groupsLocked() {
		list.flatItems = append(list.flatItems, configFlatEntry{kind: "group", group: group})
		for _, subgroup := range group.subgroups {
			list.flatItems = append(list.flatItems, configFlatEntry{kind: "subgroup", group: group, subgroup: subgroup})
			for _, item := range subgroup.items {
				list.flatItems = append(list.flatItems, configFlatEntry{kind: "item", item: item})
			}
		}
	}
	list.selectedIndex = firstConfigItem(list.flatItems)
}

func firstConfigItem(entries []configFlatEntry) int {
	for index, entry := range entries {
		if entry.kind == "item" {
			return index
		}
	}
	return 0
}

func (list *configResourceList) findNextItemLocked(fromIndex, direction int) int {
	for index := fromIndex + direction; index >= 0 && index < len(list.filteredItems); index += direction {
		if list.filteredItems[index].kind == "item" {
			return index
		}
	}
	return fromIndex
}

func (list *configResourceList) filterItemsLocked(query string) {
	if strings.TrimSpace(query) == "" {
		list.filteredItems = append([]configFlatEntry(nil), list.flatItems...)
		list.selectedIndex = firstConfigItem(list.filteredItems)
		return
	}
	lowerQuery := strings.ToLower(query)
	matchingItems := make(map[*configResourceItem]bool)
	matchingSubgroups := make(map[*configResourceSubgroup]bool)
	matchingGroups := make(map[*configResourceGroup]bool)
	for _, entry := range list.flatItems {
		if entry.kind != "item" {
			continue
		}
		item := entry.item
		if strings.Contains(strings.ToLower(item.displayName), lowerQuery) ||
			strings.Contains(strings.ToLower(string(item.resourceType)), lowerQuery) ||
			strings.Contains(strings.ToLower(item.path), lowerQuery) {
			matchingItems[item] = true
		}
	}
	for _, group := range list.groupsLocked() {
		for _, subgroup := range group.subgroups {
			for _, item := range subgroup.items {
				if matchingItems[item] {
					matchingSubgroups[subgroup], matchingGroups[group] = true, true
				}
			}
		}
	}
	list.filteredItems = nil
	for _, entry := range list.flatItems {
		switch entry.kind {
		case "group":
			if matchingGroups[entry.group] {
				list.filteredItems = append(list.filteredItems, entry)
			}
		case "subgroup":
			if matchingSubgroups[entry.subgroup] {
				list.filteredItems = append(list.filteredItems, entry)
			}
		case "item":
			if matchingItems[entry.item] {
				list.filteredItems = append(list.filteredItems, entry)
			}
		}
	}
	list.selectedIndex = firstConfigItem(list.filteredItems)
}

func (list *configResourceList) Render(width int) []string {
	list.mu.Lock()
	defer list.mu.Unlock()
	lines := append([]string(nil), list.searchInput.Render(width)...)
	lines = append(lines, "")
	if len(list.filteredItems) == 0 {
		return append(lines, theme.FG("muted", "  No resources found"))
	}
	startIndex := max(0, min(list.selectedIndex-list.maxVisible/2, len(list.filteredItems)-list.maxVisible))
	endIndex := min(startIndex+list.maxVisible, len(list.filteredItems))
	for index := startIndex; index < endIndex; index++ {
		entry := list.filteredItems[index]
		selected := index == list.selectedIndex
		switch entry.kind {
		case "group":
			inherited := list.writeScope == ConfigWriteProject && entry.group.scope == "user"
			label := entry.group.label
			if inherited {
				label += " · inherited global"
			}
			color := "accent"
			if inherited {
				color = "dim"
			}
			lines = append(lines, tui.TruncateToWidth("  "+theme.FG(color, theme.Bold(label)), width, "", false))
		case "subgroup":
			color := "muted"
			if list.writeScope == ConfigWriteProject && entry.group.scope == "user" {
				color = "dim"
			}
			lines = append(lines, tui.TruncateToWidth("    "+theme.FG(color, entry.subgroup.label), width, "", false))
		case "item":
			cursor := "  "
			if selected {
				cursor = "> "
			}
			dimmed := list.isDimmedItemLocked(entry.item)
			name := entry.item.displayName
			if selected && !dimmed {
				name = theme.Bold(name)
			}
			if dimmed {
				name = theme.FG("dim", name)
			}
			line := cursor + "    " + list.renderCheckboxLocked(entry.item) + " " + name + list.itemSuffixLocked(entry.item)
			lines = append(lines, tui.TruncateToWidth(line, width, "...", false))
		}
	}
	if startIndex > 0 || endIndex < len(list.filteredItems) {
		itemCount, currentItem := 0, 1
		for index, entry := range list.filteredItems {
			if entry.kind == "item" {
				itemCount++
				if index < list.selectedIndex {
					currentItem++
				}
			}
		}
		lines = append(lines, theme.FG("dim", fmt.Sprintf("  (%d/%d)", currentItem, itemCount)))
	}
	return lines
}

func (list *configResourceList) HandleInput(event tui.KeyEvent) {
	data := event.Raw
	keybindings := tui.GetKeybindings()

	list.mu.Lock()
	if tui.MatchesKey(data, "ctrl+c") {
		callback := list.onExit
		list.mu.Unlock()
		if callback != nil {
			callback()
		}
		return
	}
	if keybindings.Matches(data, "tui.select.cancel") {
		callback := list.onCancel
		list.mu.Unlock()
		if callback != nil {
			callback()
		}
		return
	}
	if keybindings.Matches(data, "tui.input.tab") {
		callback := list.onSwitchMode
		list.mu.Unlock()
		if callback != nil {
			callback()
		}
		return
	}
	if keybindings.Matches(data, "tui.select.up") {
		list.selectedIndex = list.findNextItemLocked(list.selectedIndex, -1)
		list.mu.Unlock()
		return
	}
	if keybindings.Matches(data, "tui.select.down") {
		list.selectedIndex = list.findNextItemLocked(list.selectedIndex, 1)
		list.mu.Unlock()
		return
	}
	if keybindings.Matches(data, "tui.select.pageUp") {
		target := max(0, list.selectedIndex-list.maxVisible)
		for target < len(list.filteredItems) && list.filteredItems[target].kind != "item" {
			target++
		}
		if target < len(list.filteredItems) {
			list.selectedIndex = target
		}
		list.mu.Unlock()
		return
	}
	if keybindings.Matches(data, "tui.select.pageDown") {
		target := min(len(list.filteredItems)-1, list.selectedIndex+list.maxVisible)
		for target >= 0 && list.filteredItems[target].kind != "item" {
			target--
		}
		if target >= 0 {
			list.selectedIndex = target
		}
		list.mu.Unlock()
		return
	}
	if data == " " || keybindings.Matches(data, "tui.select.confirm") {
		changed := false
		if list.selectedIndex >= 0 && list.selectedIndex < len(list.filteredItems) {
			entry := list.filteredItems[list.selectedIndex]
			if entry.kind == "item" && (list.writeScope == ConfigWriteProject || list.itemScope(entry.item) == "user") {
				if enabled, ok := list.toggleResourceLocked(entry.item); ok {
					list.updateItemLocked(entry.item, enabled)
					changed = true
				}
			}
		}
		callback := list.onToggle
		list.mu.Unlock()
		if changed && callback != nil {
			callback()
		}
		return
	}
	list.searchInput.HandleInput(event)
	list.filterItemsLocked(list.searchInput.GetValue())
	list.mu.Unlock()
}

func (list *configResourceList) updateItemLocked(item *configResourceItem, enabled bool) {
	item.enabled = enabled
	for _, group := range list.groupsLocked() {
		for _, subgroup := range group.subgroups {
			for _, candidate := range subgroup.items {
				if candidate.path == item.path && candidate.resourceType == item.resourceType {
					candidate.enabled = enabled
					return
				}
			}
		}
	}
}

func (list *configResourceList) toggleResourceLocked(item *configResourceItem) (bool, bool) {
	if list.writeScope == ConfigWriteProject {
		state := list.nextOverrideStateLocked(item)
		if !list.setProjectResourceOverrideLocked(item, state) {
			return false, false
		}
		if state == projectInherit {
			return list.inheritedEnabledLocked(item), true
		}
		return state == projectLoad, true
	}
	enabled := !item.enabled
	var err error
	if item.metadata.Origin == "top-level" {
		err = list.toggleTopLevelResourceLocked(item, enabled)
	} else {
		err = list.togglePackageResourceLocked(item, enabled)
	}
	return enabled, err == nil
}

func resourcePathsFromSettings(settings config.Settings, resourceType configResourceType) []string {
	raw := settings[string(resourceType)]
	switch values := raw.(type) {
	case []any:
		paths := make([]string, 0, len(values))
		for _, value := range values {
			if path, ok := value.(string); ok {
				paths = append(paths, path)
			}
		}
		return paths
	case []string:
		return append([]string(nil), values...)
	default:
		return nil
	}
}

func stripConfigPattern(entry string) string {
	if strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "+") || strings.HasPrefix(entry, "-") {
		return entry[1:]
	}
	return entry
}

func withoutConfigPattern(entries []string, pattern string) []string {
	updated := make([]string, 0, len(entries))
	for _, entry := range entries {
		if stripConfigPattern(entry) != pattern {
			updated = append(updated, entry)
		}
	}
	return updated
}

func (list *configResourceList) toggleTopLevelResourceLocked(item *configResourceItem, enabled bool) error {
	scope := list.itemScope(item)
	settings := list.settings.GetGlobalSettings()
	if scope == "project" {
		settings = list.settings.GetProjectSettings()
	}
	current := resourcePathsFromSettings(settings, item.resourceType)
	pattern := list.resourcePattern(item)
	updated := withoutConfigPattern(current, pattern)
	if enabled {
		updated = append(updated, "+"+pattern)
	} else {
		updated = append(updated, "-"+pattern)
	}
	return list.setResourcePaths(scope, item.resourceType, updated)
}

func packageResourceEntries(source config.PackageSource, resourceType configResourceType) []string {
	switch resourceType {
	case configExtensions:
		return source.Extensions
	case configSkills:
		return source.Skills
	case configPrompts:
		return source.Prompts
	default:
		return source.Themes
	}
}

func setPackageResourceEntries(source *config.PackageSource, resourceType configResourceType, entries []string) {
	switch resourceType {
	case configExtensions:
		source.Extensions = entries
	case configSkills:
		source.Skills = entries
	case configPrompts:
		source.Prompts = entries
	default:
		source.Themes = entries
	}
}

func packageHasFilters(source config.PackageSource) bool {
	return source.Extensions != nil || source.Skills != nil || source.Prompts != nil || source.Themes != nil
}

func (list *configResourceList) togglePackageResourceLocked(item *configResourceItem, enabled bool) error {
	scope := list.itemScope(item)
	packages := list.settings.GetGlobalPackages()
	if scope == "project" {
		packages = list.settings.GetProjectPackages()
	}
	packageIndex := -1
	for index, source := range packages {
		if source.Source == item.metadata.Source {
			packageIndex = index
			break
		}
	}
	if packageIndex < 0 {
		return nil
	}
	source := packages[packageIndex]
	source.IsObject = true
	pattern := list.packageResourcePattern(item)
	updated := withoutConfigPattern(packageResourceEntries(source, item.resourceType), pattern)
	if enabled {
		updated = append(updated, "+"+pattern)
	} else {
		updated = append(updated, "-"+pattern)
	}
	setPackageResourceEntries(&source, item.resourceType, updated)
	if !packageHasFilters(source) {
		source = config.PackageSource{Source: source.Source}
	}
	packages[packageIndex] = source
	if scope == "project" {
		return list.settings.SetProjectPackages(packages)
	}
	return list.settings.SetPackages(packages)
}

func (list *configResourceList) setResourcePaths(scope string, resourceType configResourceType, paths []string) error {
	if scope == "project" {
		switch resourceType {
		case configExtensions:
			return list.settings.SetProjectExtensionPaths(paths)
		case configSkills:
			return list.settings.SetProjectSkillPaths(paths)
		case configPrompts:
			return list.settings.SetProjectPromptTemplatePaths(paths)
		default:
			return list.settings.SetProjectThemePaths(paths)
		}
	}
	switch resourceType {
	case configExtensions:
		return list.settings.SetExtensionPaths(paths)
	case configSkills:
		return list.settings.SetSkillPaths(paths)
	case configPrompts:
		return list.settings.SetPromptTemplatePaths(paths)
	default:
		return list.settings.SetThemePaths(paths)
	}
}

func (list *configResourceList) renderCheckboxLocked(item *configResourceItem) string {
	if list.writeScope == ConfigWriteProject {
		switch list.projectOverrideStateLocked(item) {
		case projectLoad:
			return theme.FG("success", "[+]")
		case projectUnload:
			return theme.FG("warning", "[-]")
		default:
			if item.enabled {
				return theme.FG("dim", "[x]")
			}
			return theme.FG("dim", "[ ]")
		}
	}
	if item.enabled {
		return theme.FG("success", "[x]")
	}
	return theme.FG("dim", "[ ]")
}

func (list *configResourceList) itemSuffixLocked(item *configResourceItem) string {
	if list.writeScope != ConfigWriteProject {
		return ""
	}
	switch list.projectOverrideStateLocked(item) {
	case projectLoad:
		return theme.FG("muted", "  project load")
	case projectUnload:
		return theme.FG("muted", "  project unload")
	default:
		if list.isInheritedGlobalItemLocked(item) {
			return theme.FG("dim", "  inherited global")
		}
		return ""
	}
}

func (list *configResourceList) isDimmedItemLocked(item *configResourceItem) bool {
	return list.writeScope == ConfigWriteProject && list.isInheritedGlobalItemLocked(item) && list.projectOverrideStateLocked(item) == projectInherit
}

func (list *configResourceList) setProjectResourceOverrideLocked(item *configResourceItem, state projectOverrideState) bool {
	if item.metadata.Origin == "top-level" {
		return list.setProjectTopLevelOverrideLocked(item, state) == nil
	}
	return list.setProjectPackageOverrideLocked(item, state) == nil
}

func (list *configResourceList) setProjectTopLevelOverrideLocked(item *configResourceItem, state projectOverrideState) error {
	current := resourcePathsFromSettings(list.settings.GetProjectSettings(), item.resourceType)
	pattern := list.resourcePatternForScope(item, "project")
	if list.isInheritedGlobalItemLocked(item) {
		pattern = item.path
	}
	patterns := list.topLevelOverridePatterns(item, "project")
	updated := make([]string, 0, len(current)+2)
	for _, entry := range current {
		target := stripConfigPattern(entry)
		_, knownPattern := patterns[target]
		prefixed := strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "+") || strings.HasPrefix(entry, "-")
		if prefixed && knownPattern {
			continue
		}
		if state == projectInherit && list.isInheritedGlobalItemLocked(item) && target == pattern {
			continue
		}
		updated = append(updated, entry)
	}
	if state != projectInherit {
		if list.isInheritedGlobalItemLocked(item) && !containsConfigString(updated, pattern) {
			updated = append(updated, pattern)
		}
		prefix := "+"
		if state == projectUnload {
			prefix = "-"
		}
		updated = append(updated, prefix+pattern)
	}
	return list.setResourcePaths("project", item.resourceType, updated)
}

func containsConfigString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (list *configResourceList) setProjectPackageOverrideLocked(item *configResourceItem, state projectOverrideState) error {
	packages := list.settings.GetProjectPackages()
	packageIndex := -1
	for index, source := range packages {
		if list.packageSourceMatches(item.metadata.Source, list.itemScope(item), source.Source, "project") {
			packageIndex = index
			break
		}
	}
	if packageIndex < 0 {
		if state == projectInherit {
			return errors.New("cannot inherit a package without a project override")
		}
		packages = append(packages, list.createPackageOverrideSource(item))
		packageIndex = len(packages) - 1
	}
	source := packages[packageIndex]
	source.IsObject = true
	pattern := list.packageResourcePattern(item)
	updated := make([]string, 0, len(packageResourceEntries(source, item.resourceType))+1)
	for _, entry := range packageResourceEntries(source, item.resourceType) {
		if stripConfigPattern(entry) != pattern {
			updated = append(updated, entry)
		}
	}
	if state != projectInherit {
		prefix := "+"
		if state == projectUnload {
			prefix = "-"
		}
		updated = append(updated, prefix+pattern)
	}
	if len(updated) == 0 {
		updated = nil
	}
	setPackageResourceEntries(&source, item.resourceType, updated)
	if !packageHasFilters(source) {
		if source.Autoload != nil && !*source.Autoload {
			packages = append(packages[:packageIndex], packages[packageIndex+1:]...)
		} else {
			packages[packageIndex] = config.PackageSource{Source: source.Source}
		}
	} else {
		packages[packageIndex] = source
	}
	return list.settings.SetProjectPackages(packages)
}

func (list *configResourceList) nextOverrideStateLocked(item *configResourceItem) projectOverrideState {
	state := list.projectOverrideStateLocked(item)
	inheritedEnabled := list.inheritedEnabledLocked(item)
	switch state {
	case projectInherit:
		if inheritedEnabled {
			return projectUnload
		}
		return projectLoad
	case projectUnload:
		if inheritedEnabled {
			return projectLoad
		}
		return projectInherit
	default:
		if inheritedEnabled {
			return projectInherit
		}
		return projectUnload
	}
}

func (list *configResourceList) projectOverrideStateLocked(item *configResourceItem) projectOverrideState {
	if list.writeScope != ConfigWriteProject {
		return projectInherit
	}
	if item.metadata.Origin == "top-level" {
		return overrideStateFromEntries(
			resourcePathsFromSettings(list.settings.GetProjectSettings(), item.resourceType),
			list.topLevelOverridePatterns(item, "project"), false,
		)
	}
	source, found := list.matchingPackageSource(item, "project")
	if !found || !source.IsObject {
		return projectInherit
	}
	entries := packageResourceEntries(source, item.resourceType)
	if entries == nil {
		return projectInherit
	}
	emptyArrayIsUnload := source.Autoload == nil || *source.Autoload
	return overrideStateFromEntries(entries, map[string]bool{list.packageResourcePattern(item): true}, emptyArrayIsUnload)
}

func overrideStateFromEntries(entries []string, patterns map[string]bool, emptyArrayIsUnload bool) projectOverrideState {
	if len(entries) == 0 && entries != nil && emptyArrayIsUnload {
		return projectUnload
	}
	state := projectInherit
	for _, entry := range entries {
		if !patterns[stripConfigPattern(entry)] {
			continue
		}
		if strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "-") {
			state = projectUnload
		} else {
			state = projectLoad
		}
	}
	return state
}

func (list *configResourceList) inheritedEnabledLocked(item *configResourceItem) bool {
	if enabled, ok := list.inherited[list.resourceItemKey(item)]; ok {
		return enabled
	}
	if list.itemScope(item) == "user" {
		return item.enabled
	}
	return true
}

func (list *configResourceList) isInheritedGlobalItemLocked(item *configResourceItem) bool {
	if list.itemScope(item) == "user" {
		return true
	}
	_, ok := list.inherited[list.resourceItemKey(item)]
	return ok
}

func relativeConfigPath(base, target string) string {
	relative, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	if relative == "." {
		return ""
	}
	return relative
}

func (list *configResourceList) topLevelOverridePatterns(item *configResourceItem, scope string) map[string]bool {
	baseDir := list.topLevelBaseDir(scope)
	patterns := map[string]bool{
		list.resourcePatternForScope(item, scope): true,
		item.path:                              true,
		relativeConfigPath(baseDir, item.path): true,
	}
	if item.metadata.BaseDir != "" {
		patterns[relativeConfigPath(item.metadata.BaseDir, item.path)] = true
	}
	return patterns
}

func (list *configResourceList) resourcePatternForScope(item *configResourceItem, scope string) string {
	if scope != list.itemScope(item) {
		return item.path
	}
	baseDir := item.metadata.BaseDir
	if baseDir == "" {
		baseDir = list.topLevelBaseDir(scope)
	}
	return relativeConfigPath(baseDir, item.path)
}

func boolConfigPointer(value bool) *bool { return &value }

func (list *configResourceList) createPackageOverrideSource(item *configResourceItem) config.PackageSource {
	source := item.metadata.Source
	if !isConfigLocalPath(source) {
		return config.PackageSource{Source: source, Autoload: boolConfigPointer(false), IsObject: true}
	}
	sourcePath := resolveConfigPath(source, list.topLevelBaseDir(list.itemScope(item)))
	relative := relativeConfigPath(list.topLevelBaseDir("project"), sourcePath)
	if relative == "" {
		relative = "."
	}
	return config.PackageSource{Source: relative, Autoload: boolConfigPointer(false), IsObject: true}
}

func isConfigLocalPath(value string) bool {
	trimmed := strings.TrimSpace(value)
	for _, prefix := range [...]string{"npm:", "git:", "github:", "http:", "https:", "ssh:"} {
		if strings.HasPrefix(trimmed, prefix) {
			return false
		}
	}
	return true
}

func resolveConfigPath(value, baseDir string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "file://") {
		if parsed, err := url.Parse(value); err == nil && (parsed.Host == "" || strings.EqualFold(parsed.Host, "localhost")) {
			value = filepath.FromSlash(parsed.Path)
		}
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		if homeDir, err := os.UserHomeDir(); err == nil {
			if value == "~" {
				value = homeDir
			} else {
				value = filepath.Join(homeDir, value[2:])
			}
		}
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(baseDir, value)
	}
	return filepath.Clean(value)
}

func (list *configResourceList) packageSourceMatches(leftSource, leftScope, rightSource, rightScope string) bool {
	if leftSource == rightSource {
		return true
	}
	if !isConfigLocalPath(leftSource) || !isConfigLocalPath(rightSource) {
		return false
	}
	left := resolveConfigPath(leftSource, list.topLevelBaseDir(leftScope))
	right := resolveConfigPath(rightSource, list.topLevelBaseDir(rightScope))
	return left == right
}

func (list *configResourceList) matchingPackageSource(item *configResourceItem, targetScope string) (config.PackageSource, bool) {
	packages := list.settings.GetGlobalPackages()
	if targetScope == "project" {
		packages = list.settings.GetProjectPackages()
	}
	for _, source := range packages {
		if list.packageSourceMatches(item.metadata.Source, list.itemScope(item), source.Source, targetScope) {
			return source, true
		}
	}
	return config.PackageSource{}, false
}

func canonicalConfigPath(path string) string {
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		return canonical
	}
	return path
}

func (list *configResourceList) resourceItemKey(item *configResourceItem) string {
	return string(item.resourceType) + ":" + canonicalConfigPath(item.path)
}

func (list *configResourceList) itemScope(item *configResourceItem) string {
	if item.metadata.Scope == "project" {
		return "project"
	}
	return "user"
}

func (list *configResourceList) topLevelBaseDir(scope string) string {
	if scope == "project" {
		return filepath.Join(list.cwd, config.ConfigDirName)
	}
	return list.agentDir
}

func (list *configResourceList) resourcePattern(item *configResourceItem) string {
	baseDir := item.metadata.BaseDir
	if baseDir == "" {
		baseDir = list.topLevelBaseDir(list.itemScope(item))
	}
	return relativeConfigPath(baseDir, item.path)
}

func (list *configResourceList) packageResourcePattern(item *configResourceItem) string {
	baseDir := item.metadata.BaseDir
	if baseDir == "" {
		baseDir = filepath.Dir(item.path)
	}
	return relativeConfigPath(baseDir, item.path)
}

type ConfigSelector struct {
	tui.Container

	mu         sync.Mutex
	header     *configSelectorHeader
	resources  *configResourceList
	writeScope ConfigWriteScope
}

func NewConfigSelector(options ConfigSelectorOptions, onClose, onExit func(), requestRender func()) *ConfigSelector {
	writeScope := options.WriteScope
	if writeScope != ConfigWriteProject {
		writeScope = ConfigWriteGlobal
	}
	if writeScope == ConfigWriteProject && !options.ProjectModeAvailable {
		writeScope = ConfigWriteGlobal
	}
	groupsByScope := map[ConfigWriteScope][]*configResourceGroup{
		ConfigWriteGlobal:  buildConfigGroups(options.ResolvedPaths.Global, options.AgentDir),
		ConfigWriteProject: buildConfigGroups(options.ResolvedPaths.Project, options.AgentDir),
	}
	selector := &ConfigSelector{writeScope: writeScope}
	selector.header = &configSelectorHeader{writeScope: writeScope, projectModeAvailable: options.ProjectModeAvailable}
	selector.resources = newConfigResourceList(
		groupsByScope, options.SettingsManager, options.CWD, options.AgentDir, options.TerminalHeight, writeScope,
	)
	selector.resources.onCancel = onClose
	selector.resources.onExit = onExit
	selector.resources.onToggle = requestRender
	if options.ProjectModeAvailable {
		selector.resources.onSwitchMode = func() {
			selector.switchWriteScope()
			if requestRender != nil {
				requestRender()
			}
		}
	}
	selector.AddChild(tui.NewSpacer(1))
	selector.AddChild(NewDynamicBorder())
	selector.AddChild(tui.NewSpacer(1))
	selector.AddChild(selector.header)
	selector.AddChild(tui.NewSpacer(1))
	selector.AddChild(selector.resources)
	selector.AddChild(tui.NewSpacer(1))
	selector.AddChild(NewDynamicBorder())
	return selector
}

func (selector *ConfigSelector) switchWriteScope() {
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if selector.writeScope == ConfigWriteGlobal {
		selector.writeScope = ConfigWriteProject
	} else {
		selector.writeScope = ConfigWriteGlobal
	}
	selector.header.setWriteScope(selector.writeScope)
	selector.resources.setWriteScope(selector.writeScope)
}

func (selector *ConfigSelector) SetFocused(focused bool) { selector.resources.SetFocused(focused) }
func (selector *ConfigSelector) HandleInput(event tui.KeyEvent) {
	selector.resources.HandleInput(event)
}

func (selector *ConfigSelector) Render(width int) []string {
	selector.mu.Lock()
	defer selector.mu.Unlock()
	return selector.Container.Render(width)
}

func (selector *ConfigSelector) WriteScope() ConfigWriteScope {
	selector.mu.Lock()
	defer selector.mu.Unlock()
	return selector.writeScope
}

func RunConfigSelector(ctx context.Context, options ConfigSelectorOptions) error {
	return RunConfigSelectorWithTerminal(ctx, options, tui.NewProcessTerminal())
}

func RunConfigSelectorWithTerminal(ctx context.Context, options ConfigSelectorOptions, terminal tui.Terminal) error {
	if terminal == nil {
		return errors.New("config selector requires a terminal")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.SettingsManager == nil {
		return errors.New("config selector requires a settings manager")
	}
	if options.TerminalHeight == 0 {
		options.TerminalHeight = terminal.Rows()
	}

	previousTheme := theme.Current()
	registry := theme.Load(theme.LoadOptions{
		CWD: options.CWD, AgentDir: options.AgentDir, ProjectTrusted: options.ProjectModeAvailable,
		GlobalPaths: options.SettingsManager.GetGlobalThemePaths(), ProjectPaths: options.SettingsManager.GetProjectThemePaths(),
	})
	background := theme.DetectBackground(nil).Theme
	controller := theme.NewController(registry, options.SettingsManager.GetThemeSetting(), background, nil)
	theme.SetCurrent(controller.Current())
	defer theme.SetCurrent(previousTheme)

	uiApp := tui.NewTUI(terminal)
	done := make(chan struct{})
	var closeOnce sync.Once
	closeSelector := func() { closeOnce.Do(func() { close(done) }) }
	selector := NewConfigSelector(options, closeSelector, closeSelector, uiApp.RequestRender)
	uiApp.AddChild(selector)
	uiApp.SetFocus(selector)
	if err := uiApp.Start(); err != nil {
		return err
	}

	var waitErr error
	select {
	case <-done:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	return errors.Join(waitErr, uiApp.Stop())
}
