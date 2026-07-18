package upstreamsync

import (
	"bufio"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

var (
	codeSpanPattern = regexp.MustCompile("`([^`]*)`")
	wpPattern       = regexp.MustCompile(`\bWP-[0-9]+\b`)
)

type mirrorMap struct {
	entries []mirrorEntry
}

type mirrorEntry struct {
	patterns []string
	targets  []string
	wp       string
}

func parseMirror(data []byte) (mirrorMap, error) {
	var result mirrorMap
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
			continue
		}
		cells := splitTableRow(line)
		if len(cells) < 2 {
			continue
		}
		patterns := codeSpans(cells[0])
		targets := codeSpans(cells[1])
		if len(patterns) == 0 || len(targets) == 0 {
			continue
		}
		patterns = filterSourcePatterns(patterns)
		if len(patterns) == 0 {
			continue
		}
		var wp string
		for _, cell := range cells[2:] {
			if match := wpPattern.FindString(cell); match != "" {
				wp = match
				break
			}
		}
		result.entries = append(result.entries, mirrorEntry{patterns: patterns, targets: targets, wp: wp})
	}
	if err := scanner.Err(); err != nil {
		return mirrorMap{}, fmt.Errorf("scan MIRROR.md: %w", err)
	}
	if len(result.entries) == 0 {
		return mirrorMap{}, fmt.Errorf("MIRROR.md contains no usable mappings")
	}
	return result, nil
}

func splitTableRow(line string) []string {
	line = strings.TrimPrefix(strings.TrimSuffix(line, "|"), "|")
	parts := strings.Split(line, "|")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func codeSpans(cell string) []string {
	matches := codeSpanPattern.FindAllStringSubmatch(cell, -1)
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		value := strings.TrimSpace(match[1])
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func filterSourcePatterns(patterns []string) []string {
	filtered := patterns[:0]
	for _, pattern := range patterns {
		if strings.Contains(pattern, "/") || strings.Contains(pattern, ".") {
			filtered = append(filtered, pattern)
		}
	}
	return filtered
}

func (mirror mirrorMap) lookup(paths ...string) (targets, wps []string) {
	var specific, baseline []mirrorEntry
	for _, entry := range mirror.entries {
		matched := false
		for _, candidate := range paths {
			if candidate != "" && entry.matches(candidate) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if entry.wp != "" {
			specific = append(specific, entry)
		} else {
			baseline = append(baseline, entry)
		}
	}
	selected := specific
	if len(selected) == 0 {
		selected = baseline
	}
	for _, entry := range selected {
		targets = append(targets, entry.targets...)
		if entry.wp != "" {
			wps = append(wps, entry.wp)
		}
	}
	return uniqueSorted(targets), uniqueSorted(wps)
}

func (entry mirrorEntry) matches(candidate string) bool {
	candidate = path.Clean(strings.TrimPrefix(candidate, "./"))
	for _, pattern := range entry.patterns {
		for _, expanded := range expandBraces(pattern) {
			if matchMirrorPattern(expanded, candidate) {
				return true
			}
		}
	}
	return false
}

func expandBraces(pattern string) []string {
	start := strings.IndexByte(pattern, '{')
	if start < 0 {
		return []string{pattern}
	}
	endOffset := strings.IndexByte(pattern[start+1:], '}')
	if endOffset < 0 {
		return []string{pattern}
	}
	end := start + 1 + endOffset
	choices := strings.Split(pattern[start+1:end], ",")
	var expanded []string
	for _, choice := range choices {
		next := pattern[:start] + strings.TrimSpace(choice) + pattern[end+1:]
		expanded = append(expanded, expandBraces(next)...)
	}
	return expanded
}

func matchMirrorPattern(pattern, candidate string) bool {
	pattern = strings.TrimSpace(strings.TrimPrefix(pattern, "./"))
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		return strings.Contains(pattern, ".") && path.Base(candidate) == pattern
	}
	if strings.HasSuffix(pattern, "/") {
		directoryPattern := strings.TrimSuffix(pattern, "/")
		patternParts := strings.Split(directoryPattern, "/")
		candidateParts := strings.Split(candidate, "/")
		if len(candidateParts) < len(patternParts) {
			return false
		}
		matched, err := path.Match(directoryPattern, strings.Join(candidateParts[:len(patternParts)], "/"))
		return err == nil && matched
	}
	matched, err := path.Match(pattern, candidate)
	return err == nil && matched
}

func classifyChange(current, old string) string {
	currentClass := classifyPath(current)
	oldClass := classifyPath(old)
	if classPriority(oldClass) > classPriority(currentClass) {
		return oldClass
	}
	return currentClass
}

func classifyPath(filename string) string {
	filename = strings.ToLower(path.Clean(filename))
	if filename == "." {
		return ClassFeature
	}
	if strings.HasSuffix(filename, ".md") || strings.Contains(filename, "/docs/") ||
		strings.HasSuffix(filename, "/readme") || strings.Contains(filename, "changelog") {
		return ClassDocs
	}
	wireFiles := []string{
		"packages/ai/src/types.ts",
		"packages/agent/src/types.ts",
		"packages/coding-agent/src/core/messages.ts",
		"packages/coding-agent/src/core/session-manager.ts",
		"packages/coding-agent/src/core/settings-manager.ts",
		"packages/coding-agent/src/core/auth-storage.ts",
		"packages/coding-agent/src/core/models-store.ts",
	}
	for _, wireFile := range wireFiles {
		if filename == wireFile {
			return ClassWire
		}
	}
	if strings.HasPrefix(filename, "packages/ai/src/api/") ||
		strings.Contains(filename, "packages/agent/src/harness/session/") ||
		filename == "packages/agent/src/harness/types.ts" ||
		strings.Contains(filename, "/modes/rpc") ||
		strings.Contains(filename, "/modes/json") ||
		strings.Contains(filename, "rpc-protocol") ||
		strings.Contains(filename, "session-format") {
		return ClassWire
	}
	apiFiles := []string{
		"packages/agent/src/agent-loop.ts",
		"packages/agent/src/agent.ts",
		"packages/agent/src/harness/agent-harness.ts",
		"packages/ai/src/models.ts",
		"packages/coding-agent/src/core/agent-session.ts",
	}
	for _, apiFile := range apiFiles {
		if filename == apiFile {
			return ClassAPI
		}
	}
	if strings.Contains(filename, "/src/auth/") ||
		strings.Contains(filename, "/src/providers/") ||
		strings.Contains(filename, "/core/extensions/types.ts") ||
		strings.HasSuffix(filename, "/src/index.ts") ||
		strings.Contains(filename, "/cli/args.ts") {
		return ClassAPI
	}
	return ClassFeature
}

func classPriority(classification string) int {
	switch classification {
	case ClassWire:
		return 4
	case ClassAPI:
		return 3
	case ClassDocs:
		return 2
	default:
		return 1
	}
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
