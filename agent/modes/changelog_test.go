package modes

import (
	"strings"
	"testing"
)

// /changelog shows Orb's own releases, oldest first, without unreleased notes.
func TestChangelogShowsOrbReleases(t *testing.T) {
	got := bundledChangelog()
	first, latest := strings.Index(got, "## [0.1.0]"), strings.Index(got, "## [0.12.0]")
	if first < 0 || latest < first || strings.Contains(got, "[Unreleased]") {
		t.Fatalf("changelog = %.300s", got)
	}
}
