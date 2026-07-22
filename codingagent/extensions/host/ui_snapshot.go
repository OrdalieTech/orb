package host

import (
	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

const wireThemeMarker = "\x00pigo-theme-text\x00"

var wireThemeForegrounds = []string{
	"accent", "border", "borderAccent", "borderMuted", "success", "error", "warning", "muted", "dim", "text",
	"thinkingText", "userMessageText", "customMessageText", "customMessageLabel", "toolTitle", "toolOutput",
	"mdHeading", "mdLink", "mdLinkUrl", "mdCode", "mdCodeBlock", "mdCodeBlockBorder", "mdQuote", "mdQuoteBorder",
	"mdHr", "mdListBullet", "toolDiffAdded", "toolDiffRemoved", "toolDiffContext", "syntaxComment", "syntaxKeyword",
	"syntaxFunction", "syntaxVariable", "syntaxString", "syntaxNumber", "syntaxType", "syntaxOperator", "syntaxPunctuation",
	"thinkingOff", "thinkingMinimal", "thinkingLow", "thinkingMedium", "thinkingHigh", "thinkingXhigh", "thinkingMax", "bashMode",
}

var wireThemeBackgrounds = []string{
	"selectedBg", "userMessageBg", "customMessageBg", "toolPendingBg", "toolSuccessBg", "toolErrorBg",
}

var wireThinkingLevels = []agent.ThinkingLevel{
	agent.ThinkingOff, agent.ThinkingMinimal, agent.ThinkingLow, agent.ThinkingMedium,
	agent.ThinkingHigh, agent.ThinkingXHigh, agent.ThinkingMax,
}

var wireKeybindingIDs = []string{
	"app.interrupt", "app.clear", "app.exit", "app.suspend", "app.thinking.cycle", "app.model.cycleForward",
	"app.model.cycleBackward", "app.model.select", "app.tools.expand", "app.thinking.toggle",
	"app.session.toggleNamedFilter", "app.editor.external", "app.message.copy", "app.message.followUp",
	"app.message.dequeue", "app.clipboard.pasteImage", "app.session.new", "app.session.tree", "app.session.fork",
	"app.session.resume", "app.tree.foldOrUp", "app.tree.unfoldOrDown", "app.tree.editLabel",
	"app.tree.toggleLabelTimestamp", "app.session.togglePath", "app.session.toggleSort", "app.session.rename",
	"app.session.delete", "app.session.deleteNoninvasive", "app.models.save", "app.models.enableAll",
	"app.models.clearAll", "app.models.toggleProvider", "app.models.reorderUp", "app.models.reorderDown",
}

type wireUISnapshot struct {
	EditorText    string          `json:"editorText"`
	ToolsExpanded bool            `json:"toolsExpanded"`
	Theme         *wireTheme      `json:"theme,omitempty"`
	Themes        []wireThemeInfo `json:"themes"`
}

type wireThemeInfo struct {
	Name  string     `json:"name"`
	Path  *string    `json:"path,omitempty"`
	Theme *wireTheme `json:"theme,omitempty"`
}

type wireTheme struct {
	FG             map[string]string `json:"fg"`
	BG             map[string]string `json:"bg"`
	Bold           string            `json:"bold"`
	Italic         string            `json:"italic"`
	Underline      string            `json:"underline"`
	Inverse        string            `json:"inverse"`
	Strikethrough  string            `json:"strikethrough"`
	FGANSI         map[string]string `json:"fgAnsi"`
	BGANSI         map[string]string `json:"bgAnsi"`
	ColorMode      string            `json:"colorMode"`
	ThinkingBorder map[string]string `json:"thinkingBorder"`
	BashModeBorder string            `json:"bashModeBorder"`
}

type wireKeybindings struct {
	Resolved map[string][]string `json:"resolved"`
}

type wireFooterData struct {
	GitBranch string            `json:"gitBranch,omitempty"`
	Statuses  map[string]string `json:"statuses,omitempty"`
}

type wireAutocompleteProvider struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

func snapshotUI(value extensions.Context) *wireUISnapshot {
	if value == nil || value.UI() == nil {
		return nil
	}
	ui := value.UI()
	result := &wireUISnapshot{
		EditorText:    ui.GetEditorText(),
		ToolsExpanded: ui.GetToolsExpanded(),
		Theme:         snapshotTheme(ui.Theme()),
		Themes:        []wireThemeInfo{},
	}
	for _, info := range ui.GetAllThemes() {
		entry := wireThemeInfo{Name: info.Name, Path: cloneUIString(info.Path)}
		entry.Theme = snapshotTheme(ui.GetTheme(info.Name))
		result.Themes = append(result.Themes, entry)
	}
	return result
}

func snapshotTheme(theme extensions.Theme) *wireTheme {
	if theme == nil {
		return nil
	}
	result := &wireTheme{
		FG:             make(map[string]string, len(wireThemeForegrounds)),
		BG:             make(map[string]string, len(wireThemeBackgrounds)),
		FGANSI:         make(map[string]string, len(wireThemeForegrounds)),
		BGANSI:         make(map[string]string, len(wireThemeBackgrounds)),
		ThinkingBorder: make(map[string]string, len(wireThinkingLevels)),
		ColorMode:      theme.ColorMode(),
	}
	for _, color := range wireThemeForegrounds {
		result.FG[color] = theme.FG(color, wireThemeMarker)
		result.FGANSI[color] = theme.FGANSI(color)
	}
	for _, color := range wireThemeBackgrounds {
		result.BG[color] = theme.BG(color, wireThemeMarker)
		result.BGANSI[color] = theme.BGANSI(color)
	}
	result.Bold = theme.Bold(wireThemeMarker)
	result.Italic = theme.Italic(wireThemeMarker)
	result.Underline = theme.Underline(wireThemeMarker)
	result.Inverse = theme.Inverse(wireThemeMarker)
	result.Strikethrough = theme.Strikethrough(wireThemeMarker)
	for _, level := range wireThinkingLevels {
		result.ThinkingBorder[string(level)] = theme.ThinkingBorderColor(level)(wireThemeMarker)
	}
	result.BashModeBorder = theme.BashModeBorderColor()(wireThemeMarker)
	return result
}

func snapshotKeybindings(keybindings extensions.Keybindings) *wireKeybindings {
	if keybindings == nil {
		return nil
	}
	resolved := make(map[string][]string)
	for binding, keys := range keybindings.ResolvedBindings() {
		resolved[binding] = append([]string(nil), keys...)
	}
	for _, binding := range wireKeybindingIDs {
		if _, exists := resolved[binding]; exists {
			continue
		}
		if keys := keybindings.Keys(binding); len(keys) > 0 {
			resolved[binding] = append([]string(nil), keys...)
		}
	}
	return &wireKeybindings{Resolved: resolved}
}

func snapshotFooterData(provider extensions.FooterDataProvider) *wireFooterData {
	if provider == nil {
		return nil
	}
	statuses := provider.Statuses()
	copied := make(map[string]string, len(statuses))
	for key, value := range statuses {
		copied[key] = value
	}
	return &wireFooterData{GitBranch: provider.GitBranch(), Statuses: copied}
}

func snapshotAutocompleteProvider(provider extensions.AutocompleteProvider) *wireAutocompleteProvider {
	if provider == nil {
		return nil
	}
	return &wireAutocompleteProvider{TriggerCharacters: append([]string(nil), provider.TriggerCharacters()...)}
}

func cloneUIString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
