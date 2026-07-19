package codingagent

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

type changelogEntry struct {
	Major   int
	Minor   int
	Patch   int
	Content string
}

func (entry changelogEntry) version() string {
	return fmt.Sprintf("%d.%d.%d", entry.Major, entry.Minor, entry.Patch)
}

const (
	changelogRepoURL      = "https://github.com/earendil-works/pi"
	changelogLinkBasePath = "packages/coding-agent"
	changelogJSWhitespace = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`
)

var (
	changelogVersionHeader = regexp.MustCompile(`##[` + changelogJSWhitespace + `]+\[?(\d+)\.(\d+)\.(\d+)\]?`)
	changelogURLScheme     = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:`)
	changelogInlineLink    = regexp.MustCompile(`(!?\[[^\]\n]+\]\()([^` + changelogJSWhitespace + `)]+)((?:[` + changelogJSWhitespace + `]+[^)]*)?\))`)
)

// FormatChangelog parses, oldest-first reverses, and tag-pins links exactly as
// the interactive upstream changelog command does.
func FormatChangelog(content string) string {
	entries := parseChangelog(content)
	if len(entries) == 0 {
		return "No changelog entries found."
	}
	formatted := make([]string, 0, len(entries))
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		formatted = append(formatted, normalizeChangelogLinks(entry.Content, entry.version()))
	}
	return strings.Join(formatted, "\n\n")
}

func parseChangelog(content string) []changelogEntry {
	entries := make([]changelogEntry, 0)
	var current *changelogEntry
	var lines []string
	flush := func() {
		if current == nil || len(lines) == 0 {
			return
		}
		entry := *current
		entry.Content = strings.TrimFunc(strings.Join(lines, "\n"), isJSTrimSpace)
		entries = append(entries, entry)
	}
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "## ") {
			if current != nil {
				lines = append(lines, line)
			}
			continue
		}
		flush()
		match := changelogVersionHeader.FindStringSubmatch(line)
		if match == nil {
			current, lines = nil, nil
			continue
		}
		major, _ := strconv.Atoi(match[1])
		minor, _ := strconv.Atoi(match[2])
		patchVersion, _ := strconv.Atoi(match[3])
		current = &changelogEntry{Major: major, Minor: minor, Patch: patchVersion}
		lines = []string{line}
	}
	flush()
	return entries
}

func normalizeChangelogLinks(markdown, version string) string {
	tag := version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return changelogInlineLink.ReplaceAllStringFunc(markdown, func(match string) string {
		groups := changelogInlineLink.FindStringSubmatch(match)
		return groups[1] + normalizeChangelogLinkTarget(groups[2], tag) + groups[3]
	})
}

func normalizeChangelogLinkTarget(target, tag string) string {
	canonical := target
	for _, legacy := range []string{"https://github.com/badlogic/pi-mono", "https://github.com/earendil-works/pi-mono"} {
		if canonical == legacy || strings.HasPrefix(canonical, legacy+"/") {
			canonical = changelogRepoURL + canonical[len(legacy):]
			break
		}
	}
	for _, route := range []string{"blob", "tree"} {
		for _, branch := range []string{"main", "master"} {
			prefix := changelogRepoURL + "/" + route + "/" + branch + "/"
			if strings.HasPrefix(canonical, prefix) {
				canonical = changelogRepoURL + "/" + route + "/" + tag + "/" + canonical[len(prefix):]
			}
		}
	}
	if strings.HasPrefix(canonical, "#") || strings.HasPrefix(canonical, "//") || changelogURLScheme.MatchString(canonical) {
		return canonical
	}
	fragment, localPath, query := splitChangelogTarget(canonical)
	if localPath == "" {
		return canonical
	}
	repositoryPath, ok := resolveChangelogRepositoryPath(localPath)
	if !ok {
		return canonical
	}
	route := "blob"
	if strings.HasSuffix(localPath, "/") || !strings.Contains(path.Base(repositoryPath), ".") {
		route = "tree"
	}
	return changelogRepoURL + "/" + route + "/" + tag + "/" + encodeChangelogURI(repositoryPath) + query + fragment
}

func splitChangelogTarget(target string) (fragment, localPath, query string) {
	beforeHash := target
	if index := strings.IndexByte(target, '#'); index >= 0 {
		beforeHash, fragment = target[:index], target[index:]
	}
	if index := strings.IndexByte(beforeHash, '?'); index >= 0 {
		return fragment, beforeHash[:index], beforeHash[index:]
	}
	return fragment, beforeHash, ""
}

func resolveChangelogRepositoryPath(target string) (string, bool) {
	target = strings.ReplaceAll(target, "\\", "/")
	joined := ""
	if strings.HasPrefix(target, "/") {
		joined = normalizeChangelogPosixPath(strings.TrimLeft(target, "/"))
	} else {
		joined = normalizeChangelogPosixPath(changelogLinkBasePath + "/" + target)
	}
	if joined == "." || joined == ".." || strings.HasPrefix(joined, "../") {
		return "", false
	}
	return joined, true
}

func normalizeChangelogPosixPath(value string) string {
	cleaned := path.Clean(value)
	if strings.HasSuffix(value, "/") && cleaned != "/" && cleaned != "." && cleaned != ".." {
		cleaned += "/"
	}
	return cleaned
}

func encodeChangelogURI(value string) string {
	const unescaped = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789;,/?:@&=+$-_.!~*'()#"
	var encoded strings.Builder
	for _, character := range []byte(value) {
		if strings.IndexByte(unescaped, character) >= 0 {
			encoded.WriteByte(character)
		} else {
			fmt.Fprintf(&encoded, "%%%02X", character)
		}
	}
	return encoded.String()
}
