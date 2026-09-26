package agent

import (
	"regexp"
	"slices"
	"strings"
)

var changelogRelease = regexp.MustCompile(`^\[?\d+\.\d+\.\d+\]?`)

// FormatChangelog lists a changelog's release sections oldest first, so the
// newest one ends next to the prompt.
func FormatChangelog(content string) string {
	var releases []string
	for _, section := range strings.Split("\n"+content, "\n## ")[1:] {
		if changelogRelease.MatchString(section) {
			releases = append(releases, "## "+strings.TrimSpace(section))
		}
	}
	if len(releases) == 0 {
		return "No changelog entries found."
	}
	slices.Reverse(releases)
	return strings.Join(releases, "\n\n")
}
