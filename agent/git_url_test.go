package agent

import "testing"

func TestParseGitURLRejectsUnsafeInstallParts(t *testing.T) {
	unsafe := []string{
		"git:github.com/../repo",
		"git:github.com/user/repo/../../etc",
		"https://github.com/user/%2e%2e",
	}
	for _, input := range unsafe {
		if got := ParseGitURL(input); got != nil {
			t.Errorf("ParseGitURL(%q) accepted unsafe input: %+v", input, got)
		}
	}
}
