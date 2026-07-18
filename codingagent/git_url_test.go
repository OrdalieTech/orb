package codingagent

import "testing"

func TestParseGitURL(t *testing.T) {
	cases := []struct {
		input string
		want  *GitSource
	}{
		// Protocol URLs work without the git: prefix.
		{"https://github.com/user/repo", &GitSource{Repo: "https://github.com/user/repo", Host: "github.com", Path: "user/repo"}},
		{"ssh://git@github.com/user/repo", &GitSource{Repo: "ssh://git@github.com/user/repo", Host: "github.com", Path: "user/repo"}},
		{"https://github.com/user/repo.git", &GitSource{Repo: "https://github.com/user/repo.git", Host: "github.com", Path: "user/repo"}},
		{"https://gitlab.com/user/repo", &GitSource{Repo: "https://gitlab.com/user/repo", Host: "gitlab.com", Path: "user/repo"}},
		{"https://bitbucket.org/user/repo", &GitSource{Repo: "https://bitbucket.org/user/repo", Host: "bitbucket.org", Path: "user/repo"}},
		{"https://codeberg.org/user/repo", &GitSource{Repo: "https://codeberg.org/user/repo", Host: "codeberg.org", Path: "user/repo"}},
		{"https://github.com/user/repo@v2.0", &GitSource{Repo: "https://github.com/user/repo", Host: "github.com", Path: "user/repo", Ref: "v2.0", Pinned: true}},
		// git: prefix accepts shorthand forms.
		{"git:github.com/user/repo", &GitSource{Repo: "https://github.com/user/repo", Host: "github.com", Path: "user/repo"}},
		{"git:git@github.com:user/repo", &GitSource{Repo: "git@github.com:user/repo", Host: "github.com", Path: "user/repo"}},
		{"git:git@github.com:user/repo@v1.0.0", &GitSource{Repo: "git@github.com:user/repo", Host: "github.com", Path: "user/repo", Ref: "v1.0.0", Pinned: true}},
		{"git:github.com/user/repo@v1", &GitSource{Repo: "https://github.com/user/repo", Host: "github.com", Path: "user/repo", Ref: "v1", Pinned: true}},
		{"git:https://github.com/user/repo", &GitSource{Repo: "https://github.com/user/repo", Host: "github.com", Path: "user/repo"}},
		// Unknown host shorthand goes through the generic parser.
		{"git:codeberg.org/user/repo@main", &GitSource{Repo: "https://codeberg.org/user/repo", Host: "codeberg.org", Path: "user/repo", Ref: "main", Pinned: true}},
		// Shorthand without git: prefix is not a git source.
		{"github.com/user/repo", nil},
		{"git@github.com:user/repo", nil},
		{"./relative/path", nil},
		{"/absolute/path", nil},
	}
	for _, c := range cases {
		got := ParseGitURL(c.input)
		if c.want == nil {
			if got != nil {
				t.Errorf("ParseGitURL(%q) = %+v, want nil", c.input, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("ParseGitURL(%q) = nil, want %+v", c.input, c.want)
			continue
		}
		if *got != *c.want {
			t.Errorf("ParseGitURL(%q) = %+v, want %+v", c.input, *got, *c.want)
		}
	}
}

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
