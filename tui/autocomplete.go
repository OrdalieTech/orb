package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

type AutocompleteItem struct {
	Value       string
	Label       string
	Description string
}

type AutocompleteSuggestions struct {
	Items []AutocompleteItem
	// Prefix is what the suggestions were matched against (e.g. "/" or "src/").
	Prefix string
}

// CompletionResult is the new editor text and cursor after a completion.
type CompletionResult struct {
	Lines      []string
	CursorLine int
	CursorCol  int
}

// AutocompleteProvider supplies and applies completions. GetSuggestions runs
// off the input goroutine; ctx is cancelled when the request becomes stale.
// A nil result means no suggestions.
type AutocompleteProvider interface {
	GetSuggestions(ctx context.Context, lines []string, cursorLine, cursorCol int, force bool) *AutocompleteSuggestions
	ApplyCompletion(lines []string, cursorLine, cursorCol int, item AutocompleteItem, prefix string) CompletionResult
}

// FileCompletionGate optionally vetoes explicit-Tab file completion.
type FileCompletionGate interface {
	ShouldTriggerFileCompletion(lines []string, cursorLine, cursorCol int) bool
}

// TriggerCharacterProvider optionally contributes extra characters that
// trigger completion at token boundaries (stacked onto the built-in "@"/"#").
type TriggerCharacterProvider interface {
	TriggerCharacters() []string
}

// SlashCommand describes a command offered by slash completion.
type SlashCommand struct {
	Name         string
	Description  string
	ArgumentHint string
	// GetArgumentCompletions returns completions for the command's argument
	// text, or nil when unavailable.
	GetArgumentCompletions func(argumentPrefix string) []AutocompleteItem
}

const pathDelimiters = " \t\"'="

func isPathDelimiter(r rune) bool { return strings.ContainsRune(pathDelimiters, r) }

func toDisplayPath(value string) string { return strings.ReplaceAll(value, `\`, "/") }

// escapeFdRegex escapes JS regex metacharacters; fd receives the same
// pattern string upstream builds.
func escapeFdRegex(value string) string {
	var result strings.Builder
	for _, r := range value {
		if strings.ContainsRune(`.*+?^${}()|[]\`, r) {
			result.WriteByte('\\')
		}
		result.WriteRune(r)
	}
	return result.String()
}

func buildFdPathQuery(query string) string {
	normalized := toDisplayPath(query)
	if !strings.Contains(normalized, "/") {
		return normalized
	}
	hasTrailingSeparator := strings.HasSuffix(normalized, "/")
	trimmed := strings.Trim(normalized, "/")
	if trimmed == "" {
		return normalized
	}
	separatorPattern := `[\\/]`
	var segments []string
	for _, part := range strings.Split(trimmed, "/") {
		if part != "" {
			segments = append(segments, escapeFdRegex(part))
		}
	}
	if len(segments) == 0 {
		return normalized
	}
	pattern := strings.Join(segments, separatorPattern)
	if hasTrailingSeparator {
		pattern += separatorPattern
	}
	return pattern
}

func findLastDelimiter(text string) int {
	runes := []rune(text)
	for i := len(runes) - 1; i >= 0; i-- {
		if isPathDelimiter(runes[i]) {
			return i
		}
	}
	return -1
}

func findUnclosedQuoteStart(text string) int {
	inQuotes := false
	quoteStart := -1
	for i, r := range []rune(text) {
		if r == '"' {
			inQuotes = !inQuotes
			if inQuotes {
				quoteStart = i
			}
		}
	}
	if inQuotes {
		return quoteStart
	}
	return -1
}

func isTokenStart(runes []rune, index int) bool {
	return index == 0 || (index-1 < len(runes) && isPathDelimiter(runes[index-1]))
}

func extractQuotedPrefix(text string) string {
	quoteStart := findUnclosedQuoteStart(text)
	if quoteStart < 0 {
		return ""
	}
	runes := []rune(text)
	if quoteStart > 0 && runes[quoteStart-1] == '@' {
		if !isTokenStart(runes, quoteStart-1) {
			return ""
		}
		return string(runes[quoteStart-1:])
	}
	if !isTokenStart(runes, quoteStart) {
		return ""
	}
	return string(runes[quoteStart:])
}

type parsedPathPrefix struct {
	rawPrefix      string
	isAtPrefix     bool
	isQuotedPrefix bool
}

func parsePathPrefix(prefix string) parsedPathPrefix {
	switch {
	case strings.HasPrefix(prefix, `@"`):
		return parsedPathPrefix{rawPrefix: prefix[2:], isAtPrefix: true, isQuotedPrefix: true}
	case strings.HasPrefix(prefix, `"`):
		return parsedPathPrefix{rawPrefix: prefix[1:], isQuotedPrefix: true}
	case strings.HasPrefix(prefix, "@"):
		return parsedPathPrefix{rawPrefix: prefix[1:], isAtPrefix: true}
	default:
		return parsedPathPrefix{rawPrefix: prefix}
	}
}

func buildCompletionValue(path string, isAtPrefix, isQuotedPrefix bool) string {
	needsQuotes := isQuotedPrefix || strings.Contains(path, " ")
	prefix := ""
	if isAtPrefix {
		prefix = "@"
	}
	if !needsQuotes {
		return prefix + path
	}
	return prefix + `"` + path + `"`
}

type fdEntry struct {
	path        string
	isDirectory bool
}

// walkDirectoryWithFd walks the tree via fd (fast, respects .gitignore).
func walkDirectoryWithFd(ctx context.Context, baseDir, fdPath, query string, maxResults int) []fdEntry {
	args := []string{
		"--base-directory", baseDir,
		"--max-results", strconv.Itoa(maxResults),
		"--type", "f", "--type", "d",
		"--follow", "--hidden",
		"--exclude", ".git", "--exclude", ".git/*", "--exclude", ".git/**",
	}
	if strings.Contains(toDisplayPath(query), "/") {
		args = append(args, "--full-path")
	}
	if query != "" {
		args = append(args, buildFdPathQuery(query))
	}
	if ctx.Err() != nil {
		return nil
	}
	output, err := exec.CommandContext(ctx, fdPath, args...).Output()
	if ctx.Err() != nil || err != nil || len(output) == 0 {
		return nil
	}
	var results []fdEntry
	for _, line := range strings.Split(trimWhitespace(string(output)), "\n") {
		if line == "" {
			continue
		}
		displayLine := toDisplayPath(line)
		hasTrailingSeparator := strings.HasSuffix(displayLine, "/")
		normalizedPath := strings.TrimSuffix(displayLine, "/")
		if normalizedPath == ".git" || strings.HasPrefix(normalizedPath, ".git/") || strings.Contains(normalizedPath, "/.git/") {
			continue
		}
		results = append(results, fdEntry{path: displayLine, isDirectory: hasTrailingSeparator})
	}
	return results
}

// CombinedAutocompleteProvider stacks slash-command, command-argument, "@"
// fuzzy-file, and path completion, mirroring upstream provider stacking.
type CombinedAutocompleteProvider struct {
	commands []SlashCommand
	basePath string
	fdPath   string // empty disables fd-backed fuzzy search
}

func NewCombinedAutocompleteProvider(commands []SlashCommand, basePath, fdPath string) *CombinedAutocompleteProvider {
	return &CombinedAutocompleteProvider{commands: commands, basePath: basePath, fdPath: fdPath}
}

func (provider *CombinedAutocompleteProvider) GetSuggestions(ctx context.Context, lines []string, cursorLine, cursorCol int, force bool) *AutocompleteSuggestions {
	currentLine := ""
	if cursorLine >= 0 && cursorLine < len(lines) {
		currentLine = lines[cursorLine]
	}
	textBeforeCursor := runeSlice(currentLine, 0, cursorCol)

	if atPrefix := provider.extractAtPrefix(textBeforeCursor); atPrefix != "" {
		parsed := parsePathPrefix(atPrefix)
		suggestions := provider.getFuzzyFileSuggestions(ctx, parsed.rawPrefix, parsed.isQuotedPrefix)
		if len(suggestions) == 0 {
			return nil
		}
		return &AutocompleteSuggestions{Items: suggestions, Prefix: atPrefix}
	}

	if !force && strings.HasPrefix(textBeforeCursor, "/") {
		spaceIndex := strings.IndexByte(textBeforeCursor, ' ')
		if spaceIndex == -1 {
			prefix := textBeforeCursor[1:]
			type commandItem struct {
				name        string
				label       string
				description string
			}
			commandItems := make([]commandItem, len(provider.commands))
			for index, command := range provider.commands {
				fullDescription := command.Description
				if command.ArgumentHint != "" {
					if command.Description != "" {
						fullDescription = command.ArgumentHint + " — " + command.Description
					} else {
						fullDescription = command.ArgumentHint
					}
				}
				commandItems[index] = commandItem{name: command.Name, label: command.Name, description: fullDescription}
			}
			filtered := FuzzyFilter(commandItems, prefix, func(item commandItem) string { return item.name })
			if len(filtered) == 0 {
				return nil
			}
			items := make([]AutocompleteItem, len(filtered))
			for index, item := range filtered {
				items[index] = AutocompleteItem{Value: item.name, Label: item.label, Description: item.description}
			}
			return &AutocompleteSuggestions{Items: items, Prefix: textBeforeCursor}
		}

		commandName := textBeforeCursor[1:spaceIndex]
		argumentText := textBeforeCursor[spaceIndex+1:]
		for _, command := range provider.commands {
			if command.Name != commandName {
				continue
			}
			if command.GetArgumentCompletions == nil {
				return nil
			}
			argumentSuggestions := command.GetArgumentCompletions(argumentText)
			if len(argumentSuggestions) == 0 {
				return nil
			}
			return &AutocompleteSuggestions{Items: argumentSuggestions, Prefix: argumentText}
		}
		return nil
	}

	pathMatch, ok := provider.extractPathPrefix(textBeforeCursor, force)
	if !ok {
		return nil
	}
	suggestions := provider.getFileSuggestions(pathMatch)
	if len(suggestions) == 0 {
		return nil
	}
	return &AutocompleteSuggestions{Items: suggestions, Prefix: pathMatch}
}

func (provider *CombinedAutocompleteProvider) ApplyCompletion(lines []string, cursorLine, cursorCol int, item AutocompleteItem, prefix string) CompletionResult {
	currentLine := ""
	if cursorLine >= 0 && cursorLine < len(lines) {
		currentLine = lines[cursorLine]
	}
	beforePrefix := runeSlice(currentLine, 0, cursorCol-runeLen(prefix))
	afterCursor := runeSliceFrom(currentLine, cursorCol)
	isQuotedPrefix := strings.HasPrefix(prefix, `"`) || strings.HasPrefix(prefix, `@"`)
	hasLeadingQuoteAfterCursor := strings.HasPrefix(afterCursor, `"`)
	hasTrailingQuoteInItem := strings.HasSuffix(item.Value, `"`)
	adjustedAfterCursor := afterCursor
	if isQuotedPrefix && hasTrailingQuoteInItem && hasLeadingQuoteAfterCursor {
		adjustedAfterCursor = afterCursor[1:]
	}

	newLines := append([]string(nil), lines...)
	setLine := func(line string) {
		if cursorLine >= 0 && cursorLine < len(newLines) {
			newLines[cursorLine] = line
		}
	}

	// Slash-command completion: at line start, no path separators after "/".
	isSlashCommand := strings.HasPrefix(prefix, "/") && trimWhitespace(beforePrefix) == "" && !strings.Contains(prefix[1:], "/")
	if isSlashCommand {
		setLine(beforePrefix + "/" + item.Value + " " + adjustedAfterCursor)
		return CompletionResult{Lines: newLines, CursorLine: cursorLine, CursorCol: runeLen(beforePrefix) + runeLen(item.Value) + 2}
	}

	// File attachment: no trailing space after directories so completion can
	// continue.
	if strings.HasPrefix(prefix, "@") {
		isDirectory := strings.HasSuffix(item.Label, "/")
		suffix := " "
		if isDirectory {
			suffix = ""
		}
		setLine(beforePrefix + item.Value + suffix + adjustedAfterCursor)
		cursorOffset := runeLen(item.Value)
		if isDirectory && hasTrailingQuoteInItem {
			cursorOffset = runeLen(item.Value) - 1
		}
		return CompletionResult{Lines: newLines, CursorLine: cursorLine, CursorCol: runeLen(beforePrefix) + cursorOffset + runeLen(suffix)}
	}

	setLine(beforePrefix + item.Value + adjustedAfterCursor)
	isDirectory := strings.HasSuffix(item.Label, "/")
	cursorOffset := runeLen(item.Value)
	if isDirectory && hasTrailingQuoteInItem {
		cursorOffset = runeLen(item.Value) - 1
	}
	return CompletionResult{Lines: newLines, CursorLine: cursorLine, CursorCol: runeLen(beforePrefix) + cursorOffset}
}

func (provider *CombinedAutocompleteProvider) extractAtPrefix(text string) string {
	quotedPrefix := extractQuotedPrefix(text)
	if strings.HasPrefix(quotedPrefix, `@"`) {
		return quotedPrefix
	}
	runes := []rune(text)
	tokenStart := findLastDelimiter(text) + 1
	if tokenStart < len(runes) && runes[tokenStart] == '@' {
		return string(runes[tokenStart:])
	}
	return ""
}

func (provider *CombinedAutocompleteProvider) extractPathPrefix(text string, forceExtract bool) (string, bool) {
	if quotedPrefix := extractQuotedPrefix(text); quotedPrefix != "" {
		return quotedPrefix, true
	}
	runes := []rune(text)
	pathPrefix := string(runes[findLastDelimiter(text)+1:])

	if forceExtract {
		return pathPrefix, true
	}
	if strings.Contains(pathPrefix, "/") || strings.HasPrefix(pathPrefix, ".") || strings.HasPrefix(pathPrefix, "~/") {
		return pathPrefix, true
	}
	// An empty prefix only triggers after a space; empty text is reserved
	// for forced Tab completion.
	if pathPrefix == "" && strings.HasSuffix(text, " ") {
		return pathPrefix, true
	}
	return "", false
}

func (provider *CombinedAutocompleteProvider) expandHomePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, "~/") {
		expanded := filepath.Join(home, path[2:])
		if strings.HasSuffix(path, "/") && !strings.HasSuffix(expanded, "/") {
			return expanded + "/"
		}
		return expanded
	}
	if path == "~" {
		return home
	}
	return path
}

type scopedFuzzyQuery struct {
	baseDir     string
	query       string
	displayBase string
}

func (provider *CombinedAutocompleteProvider) resolveScopedFuzzyQuery(rawQuery string) *scopedFuzzyQuery {
	normalizedQuery := toDisplayPath(rawQuery)
	slashIndex := strings.LastIndex(normalizedQuery, "/")
	if slashIndex == -1 {
		return nil
	}
	displayBase := normalizedQuery[:slashIndex+1]
	query := normalizedQuery[slashIndex+1:]

	var baseDir string
	switch {
	case strings.HasPrefix(displayBase, "~/"):
		baseDir = provider.expandHomePath(displayBase)
	case strings.HasPrefix(displayBase, "/"):
		baseDir = displayBase
	default:
		baseDir = filepath.Join(provider.basePath, displayBase)
	}

	info, err := os.Stat(baseDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	return &scopedFuzzyQuery{baseDir: baseDir, query: query, displayBase: displayBase}
}

func scopedPathForDisplay(displayBase, relativePath string) string {
	normalizedRelativePath := toDisplayPath(relativePath)
	if displayBase == "/" {
		return "/" + normalizedRelativePath
	}
	return toDisplayPath(displayBase) + normalizedRelativePath
}

func autocompleteCollationLanguage() language.Tag {
	locale := ""
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(name); value != "" {
			locale = value
			break
		}
	}
	locale = strings.SplitN(locale, ".", 2)[0]
	locale = strings.SplitN(locale, "@", 2)[0]
	if locale == "" || locale == "C" || locale == "POSIX" {
		return language.AmericanEnglish
	}
	tag, err := language.Parse(strings.ReplaceAll(locale, "_", "-"))
	if err != nil {
		return language.AmericanEnglish
	}
	return tag
}

func (provider *CombinedAutocompleteProvider) getFileSuggestions(prefix string) []AutocompleteItem {
	parsed := parsePathPrefix(prefix)
	rawPrefix := parsed.rawPrefix
	expandedPrefix := rawPrefix
	if strings.HasPrefix(expandedPrefix, "~") {
		expandedPrefix = provider.expandHomePath(expandedPrefix)
	}

	isRootPrefix := rawPrefix == "" || rawPrefix == "./" || rawPrefix == "../" || rawPrefix == "~" || rawPrefix == "~/" || rawPrefix == "/"

	var searchDir, searchPrefix string
	switch {
	case isRootPrefix:
		if strings.HasPrefix(rawPrefix, "~") || strings.HasPrefix(expandedPrefix, "/") {
			searchDir = expandedPrefix
		} else {
			searchDir = filepath.Join(provider.basePath, expandedPrefix)
		}
	case strings.HasSuffix(rawPrefix, "/"):
		if strings.HasPrefix(rawPrefix, "~") || strings.HasPrefix(expandedPrefix, "/") {
			searchDir = expandedPrefix
		} else {
			searchDir = filepath.Join(provider.basePath, expandedPrefix)
		}
	default:
		dir := filepath.Dir(expandedPrefix)
		file := filepath.Base(expandedPrefix)
		if strings.HasPrefix(rawPrefix, "~") || strings.HasPrefix(expandedPrefix, "/") {
			searchDir = dir
		} else {
			searchDir = filepath.Join(provider.basePath, dir)
		}
		searchPrefix = file
	}

	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return nil
	}

	var suggestions []AutocompleteItem
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(searchPrefix)) {
			continue
		}

		isDirectory := entry.IsDir()
		if !isDirectory && entry.Type()&os.ModeSymlink != 0 {
			if info, statErr := os.Stat(filepath.Join(searchDir, name)); statErr == nil {
				isDirectory = info.IsDir()
			}
		}

		displayPrefix := rawPrefix
		var relativePath string
		switch {
		case strings.HasSuffix(displayPrefix, "/"):
			relativePath = displayPrefix + name
		case strings.Contains(displayPrefix, "/") || strings.Contains(displayPrefix, `\`):
			switch {
			case strings.HasPrefix(displayPrefix, "~/"):
				homeRelativeDir := displayPrefix[2:]
				dir := filepath.Dir(homeRelativeDir)
				if dir == "." {
					relativePath = "~/" + name
				} else {
					relativePath = "~/" + filepath.Join(dir, name)
				}
			case strings.HasPrefix(displayPrefix, "/"):
				dir := filepath.Dir(displayPrefix)
				if dir == "/" {
					relativePath = "/" + name
				} else {
					relativePath = dir + "/" + name
				}
			default:
				relativePath = filepath.Join(filepath.Dir(displayPrefix), name)
				if strings.HasPrefix(displayPrefix, "./") && !strings.HasPrefix(relativePath, "./") {
					relativePath = "./" + relativePath
				}
			}
		default:
			if strings.HasPrefix(displayPrefix, "~") {
				relativePath = "~/" + name
			} else {
				relativePath = name
			}
		}

		relativePath = toDisplayPath(relativePath)
		pathValue := relativePath
		if isDirectory {
			pathValue += "/"
		}
		value := buildCompletionValue(pathValue, parsed.isAtPrefix, parsed.isQuotedPrefix)
		label := name
		if isDirectory {
			label += "/"
		}
		suggestions = append(suggestions, AutocompleteItem{Value: value, Label: label})
	}

	sortAutocompleteSuggestions(suggestions)
	return suggestions
}

func sortAutocompleteSuggestions(suggestions []AutocompleteItem) {
	collator := collate.New(autocompleteCollationLanguage())
	sort.SliceStable(suggestions, func(a, b int) bool {
		aIsDir := strings.HasSuffix(suggestions[a].Value, "/")
		bIsDir := strings.HasSuffix(suggestions[b].Value, "/")
		if aIsDir != bIsDir {
			return aIsDir
		}
		return collator.CompareString(suggestions[a].Label, suggestions[b].Label) < 0
	})
}

// scoreEntry ranks an entry against the query (higher is better); folders
// get a bonus so they appear first.
func scoreEntry(filePath, query string, isDirectory bool) int {
	fileName := filepath.Base(filePath)
	lowerFileName := strings.ToLower(fileName)
	lowerQuery := strings.ToLower(query)

	score := 0
	switch {
	case lowerFileName == lowerQuery:
		score = 100
	case strings.HasPrefix(lowerFileName, lowerQuery):
		score = 80
	case strings.Contains(lowerFileName, lowerQuery):
		score = 50
	case strings.Contains(strings.ToLower(filePath), lowerQuery):
		score = 30
	}
	if isDirectory && score > 0 {
		score += 10
	}
	return score
}

func (provider *CombinedAutocompleteProvider) getFuzzyFileSuggestions(ctx context.Context, query string, isQuotedPrefix bool) []AutocompleteItem {
	if provider.fdPath == "" || ctx.Err() != nil {
		return nil
	}
	scopedQuery := provider.resolveScopedFuzzyQuery(query)
	fdBaseDir, fdQuery := provider.basePath, query
	if scopedQuery != nil {
		fdBaseDir, fdQuery = scopedQuery.baseDir, scopedQuery.query
	}
	entries := walkDirectoryWithFd(ctx, fdBaseDir, provider.fdPath, fdQuery, 100)
	if ctx.Err() != nil {
		return nil
	}

	type scoredEntry struct {
		fdEntry
		score int
	}
	scoredEntries := make([]scoredEntry, 0, len(entries))
	for _, entry := range entries {
		score := 1
		if fdQuery != "" {
			score = scoreEntry(entry.path, fdQuery, entry.isDirectory)
		}
		if score > 0 {
			scoredEntries = append(scoredEntries, scoredEntry{fdEntry: entry, score: score})
		}
	}
	sort.SliceStable(scoredEntries, func(a, b int) bool { return scoredEntries[a].score > scoredEntries[b].score })
	if len(scoredEntries) > 20 {
		scoredEntries = scoredEntries[:20]
	}

	suggestions := make([]AutocompleteItem, 0, len(scoredEntries))
	for _, entry := range scoredEntries {
		pathWithoutSlash := strings.TrimSuffix(entry.path, "/")
		displayPath := pathWithoutSlash
		if scopedQuery != nil {
			displayPath = scopedPathForDisplay(scopedQuery.displayBase, pathWithoutSlash)
		}
		entryName := filepath.Base(pathWithoutSlash)
		completionPath := displayPath
		label := entryName
		if entry.isDirectory {
			completionPath += "/"
			label += "/"
		}
		suggestions = append(suggestions, AutocompleteItem{
			Value:       buildCompletionValue(completionPath, true, isQuotedPrefix),
			Label:       label,
			Description: displayPath,
		})
	}
	return suggestions
}

// ShouldTriggerFileCompletion vetoes Tab file completion while typing a
// slash command name at line start.
func (provider *CombinedAutocompleteProvider) ShouldTriggerFileCompletion(lines []string, cursorLine, cursorCol int) bool {
	currentLine := ""
	if cursorLine >= 0 && cursorLine < len(lines) {
		currentLine = lines[cursorLine]
	}
	textBeforeCursor := trimWhitespace(runeSlice(currentLine, 0, cursorCol))
	if strings.HasPrefix(textBeforeCursor, "/") && !strings.Contains(textBeforeCursor, " ") {
		return false
	}
	return true
}
