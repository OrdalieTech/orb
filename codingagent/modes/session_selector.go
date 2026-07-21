package modes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/modes/theme"
	"github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"
)

type SessionSelectorLoader func(session.SessionListProgress) []session.SessionInfo

type SessionDeleteMethod string

const (
	SessionDeleteTrash  SessionDeleteMethod = "trash"
	SessionDeleteUnlink SessionDeleteMethod = "unlink"
)

type SessionSelectorOptions struct {
	CurrentSessions    SessionSelectorLoader
	AllSessions        SessionSelectorLoader
	CurrentSessionPath string
	Keybindings        *tui.KeybindingsManager
	RequestRender      func()
	Now                func() time.Time
	DeleteSession      func(string) (SessionDeleteMethod, error)
}

type sessionSelectorScope string

const (
	sessionScopeCurrent sessionSelectorScope = "current"
	sessionScopeAll     sessionSelectorScope = "all"
)

type sessionSelectorSort string

const (
	sessionSortThreaded  sessionSelectorSort = "threaded"
	sessionSortRecent    sessionSelectorSort = "recent"
	sessionSortRelevance sessionSelectorSort = "relevance"
)

type sessionSelectorNameFilter string

const (
	sessionNamesAll   sessionSelectorNameFilter = "all"
	sessionNamesNamed sessionSelectorNameFilter = "named"
)

type flatSessionNode struct {
	session           session.SessionInfo
	depth             int
	isLast            bool
	ancestorContinues []bool
}

type sessionTreeNode struct {
	session        session.SessionInfo
	children       []*sessionTreeNode
	latestActivity time.Time
}

type selectorStatus struct {
	kind    string
	message string
}

// SessionSelectorComponent mirrors the startup and interactive session picker
// used by upstream. Loaders may block; scope loads run away from the TUI input
// loop and publish progress through RequestRender.
type SessionSelectorComponent struct {
	mu sync.Mutex

	currentLoader SessionSelectorLoader
	allLoader     SessionSelectorLoader
	keybindings   *tui.KeybindingsManager
	requestRender func()
	now           func() time.Time
	deleteSession func(string) (SessionDeleteMethod, error)

	currentSessions []session.SessionInfo
	allSessions     []session.SessionInfo
	allLoaded       bool
	currentLoading  bool
	allLoading      bool
	allLoadSeq      int

	scope      sessionSelectorScope
	sortMode   sessionSelectorSort
	nameFilter sessionSelectorNameFilter
	showPath   bool
	selected   int
	maxVisible int
	filtered   []flatSessionNode
	search     *tui.Input
	focused    bool

	confirmingDelete string
	status           *selectorStatus
	loadProgress     string
	statusTimer      *time.Timer
	currentPath      string

	onSelect func(string)
	onCancel func()
}

func NewSessionSelectorComponent(options SessionSelectorOptions, onSelect func(string), onCancel func()) *SessionSelectorComponent {
	if options.CurrentSessions == nil {
		options.CurrentSessions = func(session.SessionListProgress) []session.SessionInfo { return nil }
	}
	if options.AllSessions == nil {
		options.AllSessions = func(session.SessionListProgress) []session.SessionInfo { return nil }
	}
	if options.Keybindings == nil {
		options.Keybindings = NewAppKeybindings(nil)
	}
	if options.RequestRender == nil {
		options.RequestRender = func() {}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.DeleteSession == nil {
		options.DeleteSession = deleteSessionFile
	}
	selector := &SessionSelectorComponent{
		currentLoader: options.CurrentSessions,
		allLoader:     options.AllSessions,
		keybindings:   options.Keybindings,
		requestRender: options.RequestRender,
		now:           options.Now,
		deleteSession: options.DeleteSession,
		scope:         sessionScopeCurrent,
		sortMode:      sessionSortThreaded,
		nameFilter:    sessionNamesAll,
		maxVisible:    10,
		search:        tui.NewInput(),
		currentPath:   canonicalSessionPath(options.CurrentSessionPath),
		onSelect:      onSelect,
		onCancel:      onCancel,
	}
	selector.filterLocked("")
	go selector.loadScope(sessionScopeCurrent)
	return selector
}

func (selector *SessionSelectorComponent) SetFocused(focused bool) {
	selector.mu.Lock()
	selector.focused = focused
	selector.search.SetFocused(focused)
	selector.mu.Unlock()
}

func (selector *SessionSelectorComponent) Invalidate() {}

func (selector *SessionSelectorComponent) loadScope(scope sessionSelectorScope) {
	selector.mu.Lock()
	var loader SessionSelectorLoader
	seq := 0
	if scope == sessionScopeCurrent {
		selector.currentLoading = true
		loader = selector.currentLoader
	} else {
		selector.allLoading = true
		selector.allLoadSeq++
		seq = selector.allLoadSeq
		loader = selector.allLoader
	}
	selector.loadProgress = ""
	selector.mu.Unlock()
	selector.requestRender()

	progress := func(loaded, total int) {
		selector.mu.Lock()
		if scope == selector.scope && (scope != sessionScopeAll || seq == selector.allLoadSeq) {
			selector.loadProgress = fmt.Sprintf("%d/%d", loaded, total)
		}
		selector.mu.Unlock()
		selector.requestRender()
	}
	sessions := loader(progress)

	selector.mu.Lock()
	if scope == sessionScopeCurrent {
		selector.currentSessions = append([]session.SessionInfo(nil), sessions...)
		selector.currentLoading = false
	} else {
		selector.allSessions = append([]session.SessionInfo(nil), sessions...)
		selector.allLoaded = true
		selector.allLoading = false
	}
	if scope == selector.scope && (scope != sessionScopeAll || seq == selector.allLoadSeq) {
		selector.loadProgress = ""
		selector.filterLocked(selector.search.GetValue())
	}
	selector.mu.Unlock()
	selector.requestRender()
}

func (selector *SessionSelectorComponent) filterLocked(query string) {
	var sessions []session.SessionInfo
	if selector.scope == sessionScopeAll {
		sessions = selector.allSessions
	} else {
		sessions = selector.currentSessions
	}
	if selector.nameFilter == sessionNamesNamed {
		named := make([]session.SessionInfo, 0, len(sessions))
		for _, info := range sessions {
			if info.Name != nil && strings.TrimSpace(*info.Name) != "" {
				named = append(named, info)
			}
		}
		sessions = named
	}
	if selector.sortMode == sessionSortThreaded && strings.TrimSpace(query) == "" {
		selector.filtered = flattenSessionTree(buildSessionTree(sessions))
	} else {
		filtered := filterAndSortSelectorSessions(sessions, query, selector.sortMode)
		selector.filtered = make([]flatSessionNode, len(filtered))
		for index, info := range filtered {
			selector.filtered[index] = flatSessionNode{session: info, isLast: true}
		}
	}
	selector.selected = max(0, min(selector.selected, max(0, len(selector.filtered)-1)))
}

func canonicalSessionPath(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func buildSessionTree(sessions []session.SessionInfo) []*sessionTreeNode {
	byPath := make(map[string]*sessionTreeNode, len(sessions))
	for _, info := range sessions {
		byPath[canonicalSessionPath(info.Path)] = &sessionTreeNode{session: info, latestActivity: info.Modified}
	}
	roots := make([]*sessionTreeNode, 0, len(sessions))
	for _, info := range sessions {
		node := byPath[canonicalSessionPath(info.Path)]
		parentPath := ""
		if info.ParentSessionPath != nil {
			parentPath = canonicalSessionPath(*info.ParentSessionPath)
		}
		if parent := byPath[parentPath]; parent != nil {
			parent.children = append(parent.children, node)
		} else {
			roots = append(roots, node)
		}
	}
	var updateLatest func(*sessionTreeNode) time.Time
	updateLatest = func(node *sessionTreeNode) time.Time {
		latest := node.session.Modified
		for _, child := range node.children {
			if candidate := updateLatest(child); candidate.After(latest) {
				latest = candidate
			}
		}
		node.latestActivity = latest
		return latest
	}
	var sortNodes func([]*sessionTreeNode)
	sortNodes = func(nodes []*sessionTreeNode) {
		sort.SliceStable(nodes, func(left, right int) bool { return nodes[left].latestActivity.After(nodes[right].latestActivity) })
		for _, node := range nodes {
			sortNodes(node.children)
		}
	}
	for _, root := range roots {
		updateLatest(root)
	}
	sortNodes(roots)
	return roots
}

func flattenSessionTree(roots []*sessionTreeNode) []flatSessionNode {
	result := make([]flatSessionNode, 0)
	var walk func(*sessionTreeNode, int, []bool, bool)
	walk = func(node *sessionTreeNode, depth int, ancestorContinues []bool, isLast bool) {
		result = append(result, flatSessionNode{session: node.session, depth: depth, isLast: isLast, ancestorContinues: ancestorContinues})
		for index, child := range node.children {
			childLast := index == len(node.children)-1
			continues := depth > 0 && !isLast
			walk(child, depth+1, append(append([]bool(nil), ancestorContinues...), continues), childLast)
		}
	}
	for index, root := range roots {
		walk(root, 0, nil, index == len(roots)-1)
	}
	return result
}

type selectorSearchToken struct {
	kind  string
	value string
}

type selectorSearchQuery struct {
	regex   *regexp.Regexp
	tokens  []selectorSearchToken
	invalid bool
}

func parseSelectorSearch(query string) selectorSearchQuery {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return selectorSearchQuery{}
	}
	if strings.HasPrefix(trimmed, "re:") {
		pattern := strings.TrimSpace(strings.TrimPrefix(trimmed, "re:"))
		if pattern == "" {
			return selectorSearchQuery{invalid: true}
		}
		compiled, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return selectorSearchQuery{invalid: true}
		}
		return selectorSearchQuery{regex: compiled}
	}
	var tokens []selectorSearchToken
	var buffer strings.Builder
	inQuote := false
	flush := func(kind string) {
		value := strings.TrimSpace(buffer.String())
		buffer.Reset()
		if value != "" {
			tokens = append(tokens, selectorSearchToken{kind: kind, value: value})
		}
	}
	for _, char := range trimmed {
		switch {
		case char == '"':
			if inQuote {
				flush("phrase")
				inQuote = false
			} else {
				flush("fuzzy")
				inQuote = true
			}
		case !inQuote && isSelectorWhitespace(char):
			flush("fuzzy")
		default:
			buffer.WriteRune(char)
		}
	}
	if inQuote {
		fields := strings.Fields(trimmed)
		tokens = make([]selectorSearchToken, len(fields))
		for index, field := range fields {
			tokens[index] = selectorSearchToken{kind: "fuzzy", value: field}
		}
		return selectorSearchQuery{tokens: tokens}
	}
	flush("fuzzy")
	return selectorSearchQuery{tokens: tokens}
}

func isSelectorWhitespace(char rune) bool {
	return unicode.IsSpace(char) || char == '\ufeff'
}

func selectorSearchText(info session.SessionInfo) string {
	name := ""
	if info.Name != nil {
		name = *info.Name
	}
	return fmt.Sprintf("%s %s %s %s", info.ID, name, info.AllMessagesText, info.CWD)
}

func normalizeSelectorPhrase(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func matchSelectorSession(info session.SessionInfo, parsed selectorSearchQuery) (bool, float64) {
	if parsed.invalid {
		return false, 0
	}
	text := selectorSearchText(info)
	if parsed.regex != nil {
		location := parsed.regex.FindStringIndex(text)
		if location == nil {
			return false, 0
		}
		return true, float64(selectorUTF16Length(text[:location[0]])) * 0.1
	}
	if len(parsed.tokens) == 0 {
		return true, 0
	}
	total := 0.0
	normalized := ""
	for _, token := range parsed.tokens {
		if token.kind == "phrase" {
			if normalized == "" {
				normalized = normalizeSelectorPhrase(text)
			}
			phrase := normalizeSelectorPhrase(token.value)
			index := strings.Index(normalized, phrase)
			if phrase == "" {
				continue
			}
			if index < 0 {
				return false, 0
			}
			total += float64(selectorUTF16Length(normalized[:index])) * 0.1
			continue
		}
		match := tui.FuzzyMatchScore(token.value, text)
		if !match.Matches {
			return false, 0
		}
		total += match.Score
	}
	return true, total
}

func selectorUTF16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func filterAndSortSelectorSessions(sessions []session.SessionInfo, query string, mode sessionSelectorSort) []session.SessionInfo {
	if strings.TrimSpace(query) == "" {
		return append([]session.SessionInfo(nil), sessions...)
	}
	parsed := parseSelectorSearch(query)
	if parsed.invalid {
		return nil
	}
	type scoredSession struct {
		info  session.SessionInfo
		score float64
	}
	scored := make([]scoredSession, 0, len(sessions))
	for _, info := range sessions {
		matches, score := matchSelectorSession(info, parsed)
		if matches {
			scored = append(scored, scoredSession{info: info, score: score})
		}
	}
	if mode != sessionSortRecent {
		sort.SliceStable(scored, func(left, right int) bool {
			if scored[left].score != scored[right].score {
				return scored[left].score < scored[right].score
			}
			return scored[left].info.Modified.After(scored[right].info.Modified)
		})
	}
	result := make([]session.SessionInfo, len(scored))
	for index, value := range scored {
		result[index] = value.info
	}
	return result
}

func (selector *SessionSelectorComponent) loadingLocked() bool {
	if selector.scope == sessionScopeAll {
		return selector.allLoading
	}
	return selector.currentLoading
}

func (selector *SessionSelectorComponent) headerLinesLocked(width int) []string {
	title := "Resume Session (Current Folder)"
	if selector.scope == sessionScopeAll {
		title = "Resume Session (All)"
	}
	left := theme.Bold(title)
	sortLabel := "Threaded"
	switch selector.sortMode {
	case sessionSortRecent:
		sortLabel = "Recent"
	case sessionSortRelevance:
		sortLabel = "Fuzzy"
	}
	nameLabel := "All"
	if selector.nameFilter == sessionNamesNamed {
		nameLabel = "Named"
	}
	scopeText := ""
	if selector.loadingLocked() {
		progress := "..."
		if selector.loadProgress != "" {
			progress = selector.loadProgress
		}
		scopeText = theme.FG("muted", "○ Current Folder | ") + theme.FG("accent", "Loading "+progress)
	} else if selector.scope == sessionScopeCurrent {
		scopeText = theme.FG("accent", "◉ Current Folder") + theme.FG("muted", " | ○ All")
	} else {
		scopeText = theme.FG("muted", "○ Current Folder | ") + theme.FG("accent", "◉ All")
	}
	right := scopeText + "  " + theme.FG("muted", "Name: ") + theme.FG("accent", nameLabel) +
		"  " + theme.FG("muted", "Sort: ") + theme.FG("accent", sortLabel)
	right = tui.TruncateToWidth(right, width, "", false)
	availableLeft := max(0, width-tui.VisibleWidth(right)-1)
	left = tui.TruncateToWidth(left, availableLeft, "", false)
	spacing := max(0, width-tui.VisibleWidth(left)-tui.VisibleWidth(right))
	first := left + strings.Repeat(" ", spacing) + right
	if selector.confirmingDelete != "" {
		hint := "Delete session? " + selectorKeyHint("tui.select.confirm", "confirm") + " · " + selectorKeyHint("tui.select.cancel", "cancel")
		return []string{first, theme.FG("error", tui.TruncateToWidth(hint, width, "…", false)), ""}
	}
	if selector.status != nil {
		color := "accent"
		if selector.status.kind == "error" {
			color = "error"
		}
		return []string{first, theme.FG(color, tui.TruncateToWidth(selector.status.message, width, "…", false)), ""}
	}
	pathState := "off"
	if selector.showPath {
		pathState = "on"
	}
	return []string{
		first,
		tui.TruncateToWidth(selectorKeyHint("tui.input.tab", "scope")+theme.FG("muted", " · re:<pattern> regex · \"phrase\" exact"), width, "…", false),
		tui.TruncateToWidth(selectorKeyHint("app.session.toggleSort", "sort")+theme.FG("muted", " · ")+
			selectorKeyHint("app.session.toggleNamedFilter", "named")+theme.FG("muted", " · ")+
			selectorKeyHint("app.session.delete", "delete")+theme.FG("muted", " · ")+
			selectorKeyHint("app.session.togglePath", "path ("+pathState+")"), width, "…", false),
	}
}

func selectorKeyHint(binding, label string) string {
	return theme.FG("accent", KeyText(binding)) + " " + theme.FG("muted", label)
}

func (selector *SessionSelectorComponent) Render(width int) []string {
	selector.mu.Lock()
	defer selector.mu.Unlock()
	border := theme.FG("accent", strings.Repeat("─", max(0, width)))
	lines := []string{"", border, ""}
	lines = append(lines, selector.headerLinesLocked(width)...)
	lines = append(lines, "")
	lines = append(lines, selector.search.Render(width)...)
	lines = append(lines, "")
	lines = append(lines, selector.listLinesLocked(width)...)
	lines = append(lines, "", border)
	return lines
}

func (selector *SessionSelectorComponent) listLinesLocked(width int) []string {
	if len(selector.filtered) == 0 {
		message := "  No sessions in current folder. Press Tab to view all."
		if selector.nameFilter == sessionNamesNamed {
			if selector.scope == sessionScopeAll {
				message = "  No named sessions found. Press " + KeyText("app.session.toggleNamedFilter") + " to show all."
			} else {
				message = "  No named sessions in current folder. Press " + KeyText("app.session.toggleNamedFilter") + " to show all, or Tab to view all."
			}
		} else if selector.scope == sessionScopeAll {
			message = "  No sessions found"
		}
		return []string{tui.TruncateToWidth(message, width, "…", false)}
	}
	start := max(0, min(selector.selected-selector.maxVisible/2, len(selector.filtered)-selector.maxVisible))
	end := min(start+selector.maxVisible, len(selector.filtered))
	lines := make([]string, 0, end-start+1)
	for index := start; index < end; index++ {
		lines = append(lines, selector.renderSessionLineLocked(selector.filtered[index], index == selector.selected, width))
	}
	if start > 0 || end < len(selector.filtered) {
		lines = append(lines, fmt.Sprintf("  (%d/%d)", selector.selected+1, len(selector.filtered)))
	}
	return lines
}

func (selector *SessionSelectorComponent) renderSessionLineLocked(node flatSessionNode, selected bool, width int) string {
	info := node.session
	prefix := ""
	if node.depth > 0 {
		var builder strings.Builder
		for _, continues := range node.ancestorContinues {
			if continues {
				builder.WriteString("│  ")
			} else {
				builder.WriteString("   ")
			}
		}
		if node.isLast {
			builder.WriteString("└─ ")
		} else {
			builder.WriteString("├─ ")
		}
		prefix = builder.String()
	}
	display := info.FirstMessage
	if info.Name != nil {
		display = *info.Name
	}
	display = strings.TrimSpace(strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return ' '
		}
		return char
	}, display))
	right := fmt.Sprintf("%d %s", info.MessageCount, formatSessionAge(selector.now(), info.Modified))
	if selector.scope == sessionScopeAll && info.CWD != "" {
		right = shortenSessionPath(info.CWD) + " " + right
	}
	if selector.showPath {
		right = shortenSessionPath(info.Path) + " " + right
	}
	cursor := "  "
	if selected {
		cursor = theme.FG("accent", "› ")
	}
	available := width - 2 - tui.VisibleWidth(prefix) - tui.VisibleWidth(right) - 2
	display = tui.TruncateToWidth(display, max(10, available), "…", false)
	styledDisplay := display
	confirming := info.Path == selector.confirmingDelete
	isCurrent := selector.currentPath != "" && canonicalSessionPath(info.Path) == selector.currentPath
	switch {
	case confirming:
		styledDisplay = theme.FG("error", styledDisplay)
	case isCurrent:
		styledDisplay = theme.FG("accent", styledDisplay)
	case info.Name != nil && *info.Name != "":
		styledDisplay = theme.FG("warning", styledDisplay)
	}
	if selected {
		styledDisplay = theme.Bold(styledDisplay)
	}
	left := cursor + theme.FG("dim", prefix) + styledDisplay
	spacing := max(1, width-tui.VisibleWidth(left)-tui.VisibleWidth(right))
	styledRight := theme.FG("dim", right)
	if confirming {
		styledRight = theme.FG("error", right)
	}
	line := left + strings.Repeat(" ", spacing) + styledRight
	if selected {
		line = theme.BG("selectedBg", line)
	}
	return tui.TruncateToWidth(line, width, "", false)
}

func shortenSessionPath(path string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func formatSessionAge(now, modified time.Time) string {
	difference := now.Sub(modified)
	minutes := int(difference / time.Minute)
	hours := int(difference / time.Hour)
	days := int(difference / (24 * time.Hour))
	switch {
	case minutes < 1:
		return "now"
	case minutes < 60:
		return fmt.Sprintf("%dm", minutes)
	case hours < 24:
		return fmt.Sprintf("%dh", hours)
	case days < 7:
		return fmt.Sprintf("%dd", days)
	case days < 30:
		return fmt.Sprintf("%dw", days/7)
	case days < 365:
		return fmt.Sprintf("%dmo", days/30)
	default:
		return fmt.Sprintf("%dy", days/365)
	}
}

func (selector *SessionSelectorComponent) HandleInput(event tui.KeyEvent) {
	data := event.Raw
	selector.mu.Lock()
	if selector.confirmingDelete != "" {
		switch {
		case tui.GetKeybindings().Matches(data, "tui.select.confirm"):
			path := selector.confirmingDelete
			selector.confirmingDelete = ""
			selector.mu.Unlock()
			go selector.removeSession(path)
		case tui.GetKeybindings().Matches(data, "tui.select.cancel"):
			selector.confirmingDelete = ""
			selector.mu.Unlock()
			selector.requestRender()
		default:
			selector.mu.Unlock()
		}
		return
	}
	switch {
	case tui.GetKeybindings().Matches(data, "tui.input.tab"):
		selector.toggleScopeLocked()
		selector.mu.Unlock()
		selector.requestRender()
		return
	case selector.keybindings.Matches(data, "app.session.toggleSort"):
		switch selector.sortMode {
		case sessionSortThreaded:
			selector.sortMode = sessionSortRecent
		case sessionSortRecent:
			selector.sortMode = sessionSortRelevance
		default:
			selector.sortMode = sessionSortThreaded
		}
		selector.filterLocked(selector.search.GetValue())
		selector.mu.Unlock()
		selector.requestRender()
		return
	case selector.keybindings.Matches(data, "app.session.toggleNamedFilter"):
		if selector.nameFilter == sessionNamesAll {
			selector.nameFilter = sessionNamesNamed
		} else {
			selector.nameFilter = sessionNamesAll
		}
		selector.filterLocked(selector.search.GetValue())
		selector.mu.Unlock()
		selector.requestRender()
		return
	case selector.keybindings.Matches(data, "app.session.togglePath"):
		selector.showPath = !selector.showPath
		selector.mu.Unlock()
		selector.requestRender()
		return
	case selector.keybindings.Matches(data, "app.session.delete"):
		selector.startDeleteLocked()
		selector.mu.Unlock()
		selector.requestRender()
		return
	case selector.keybindings.Matches(data, "app.session.deleteNoninvasive") && selector.search.GetValue() == "":
		selector.startDeleteLocked()
		selector.mu.Unlock()
		selector.requestRender()
		return
	case tui.GetKeybindings().Matches(data, "tui.select.up"):
		selector.selected = max(0, selector.selected-1)
		selector.mu.Unlock()
		selector.requestRender()
		return
	case tui.GetKeybindings().Matches(data, "tui.select.down"):
		if len(selector.filtered) > 0 {
			selector.selected = min(len(selector.filtered)-1, selector.selected+1)
		}
		selector.mu.Unlock()
		selector.requestRender()
		return
	case tui.GetKeybindings().Matches(data, "tui.select.pageUp"):
		selector.selected = max(0, selector.selected-selector.maxVisible)
		selector.mu.Unlock()
		selector.requestRender()
		return
	case tui.GetKeybindings().Matches(data, "tui.select.pageDown"):
		if len(selector.filtered) > 0 {
			selector.selected = min(len(selector.filtered)-1, selector.selected+selector.maxVisible)
		}
		selector.mu.Unlock()
		selector.requestRender()
		return
	case tui.GetKeybindings().Matches(data, "tui.select.confirm"):
		callback := selector.onSelect
		path := ""
		if selector.selected >= 0 && selector.selected < len(selector.filtered) {
			path = selector.filtered[selector.selected].session.Path
		}
		if path != "" {
			selector.clearStatusLocked()
		}
		selector.mu.Unlock()
		if callback != nil && path != "" {
			callback(path)
		}
		return
	case tui.GetKeybindings().Matches(data, "tui.select.cancel"):
		callback := selector.onCancel
		selector.clearStatusLocked()
		selector.mu.Unlock()
		if callback != nil {
			callback()
		}
		return
	}
	selector.mu.Unlock()
	selector.search.HandleInput(event)
	selector.mu.Lock()
	selector.filterLocked(selector.search.GetValue())
	selector.mu.Unlock()
	selector.requestRender()
}

func (selector *SessionSelectorComponent) toggleScopeLocked() {
	if selector.scope == sessionScopeCurrent {
		selector.scope = sessionScopeAll
		if selector.allLoaded {
			selector.filterLocked(selector.search.GetValue())
		} else if !selector.allLoading {
			go selector.loadScope(sessionScopeAll)
		}
		return
	}
	selector.scope = sessionScopeCurrent
	selector.filterLocked(selector.search.GetValue())
}

func (selector *SessionSelectorComponent) startDeleteLocked() {
	if selector.selected < 0 || selector.selected >= len(selector.filtered) {
		return
	}
	path := selector.filtered[selector.selected].session.Path
	if selector.currentPath != "" && canonicalSessionPath(path) == selector.currentPath {
		selector.setStatusLocked("error", "Cannot delete the currently active session", 3*time.Second)
		return
	}
	selector.confirmingDelete = path
}

func (selector *SessionSelectorComponent) setStatusLocked(kind, message string, duration time.Duration) {
	selector.clearStatusLocked()
	selector.status = &selectorStatus{kind: kind, message: message}
	if duration <= 0 {
		return
	}
	selector.statusTimer = time.AfterFunc(duration, func() {
		selector.mu.Lock()
		selector.status = nil
		selector.statusTimer = nil
		selector.mu.Unlock()
		selector.requestRender()
	})
}

func (selector *SessionSelectorComponent) clearStatusLocked() {
	if selector.statusTimer != nil {
		selector.statusTimer.Stop()
		selector.statusTimer = nil
	}
	selector.status = nil
}

func (selector *SessionSelectorComponent) clearStatus() {
	selector.mu.Lock()
	selector.clearStatusLocked()
	selector.mu.Unlock()
}

func (selector *SessionSelectorComponent) removeSession(path string) {
	method, err := selector.deleteSession(path)
	selector.mu.Lock()
	if err != nil {
		selector.setStatusLocked("error", "Failed to delete: "+err.Error(), 3*time.Second)
		selector.mu.Unlock()
		selector.requestRender()
		return
	}
	remove := func(values []session.SessionInfo) []session.SessionInfo {
		result := values[:0]
		for _, info := range values {
			if info.Path != path {
				result = append(result, info)
			}
		}
		return result
	}
	selector.currentSessions = remove(selector.currentSessions)
	selector.allSessions = remove(selector.allSessions)
	message := "Session deleted"
	if method == SessionDeleteTrash {
		message = "Session moved to trash"
	}
	selector.setStatusLocked("info", message, 2*time.Second)
	selector.filterLocked(selector.search.GetValue())
	scope := selector.scope
	selector.mu.Unlock()
	selector.requestRender()
	selector.loadScope(scope)
}

func deleteSessionFile(path string) (SessionDeleteMethod, error) {
	arguments := []string{path}
	if strings.HasPrefix(path, "-") {
		arguments = []string{"--", path}
	}
	trashErr := exec.Command("trash", arguments...).Run()
	if trashErr == nil {
		return SessionDeleteTrash, nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return SessionDeleteTrash, nil
	}
	if err := os.Remove(path); err != nil {
		return SessionDeleteUnlink, err
	}
	return SessionDeleteUnlink, nil
}

func RunSessionSelector(ctx context.Context, current, all SessionSelectorLoader) (string, bool, error) {
	return RunSessionSelectorWithTerminal(ctx, current, all, tui.NewProcessTerminal())
}

func RunSessionSelectorWithTerminal(ctx context.Context, current, all SessionSelectorLoader, terminal tui.Terminal) (string, bool, error) {
	if terminal == nil {
		return "", false, errors.New("session selector requires a terminal")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	bindings := NewAppKeybindings(nil)
	if agentDir, err := config.GetAgentDir(); err == nil {
		bindings = NewAppKeybindings(tui.LoadKeybindingsFile(filepath.Join(agentDir, "keybindings.json")))
	}
	tui.SetKeybindings(bindings)
	uiApp := tui.NewTUI(terminal)
	type result struct {
		path      string
		cancelled bool
	}
	resolved := make(chan result, 1)
	var once sync.Once
	resolve := func(value result) { once.Do(func() { resolved <- value }) }
	selector := NewSessionSelectorComponent(SessionSelectorOptions{
		CurrentSessions: current,
		AllSessions:     all,
		Keybindings:     bindings,
		RequestRender:   uiApp.RequestRender,
	}, func(path string) { resolve(result{path: path}) }, func() { resolve(result{cancelled: true}) })
	uiApp.AddChild(selector)
	uiApp.SetFocus(selector)
	if err := uiApp.Start(); err != nil {
		selector.clearStatus()
		return "", false, err
	}
	var selected result
	var waitErr error
	select {
	case selected = <-resolved:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	selector.clearStatus()
	stopErr := uiApp.Stop()
	if err := errors.Join(waitErr, stopErr); err != nil {
		return "", false, err
	}
	return selected.path, !selected.cancelled, nil
}
