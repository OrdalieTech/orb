package tools

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestResolveToCwd(t *testing.T) {
	cwd := t.TempDir()
	want := filepath.Join(cwd, "relative", "file.txt")
	if got, err := ResolveToCwd("relative/file.txt", cwd); err != nil || got != want {
		t.Fatalf("ResolveToCwd relative = %q, want %q", got, want)
	}
	if got, err := ResolveToCwd("@~draft.md", cwd); err != nil || got != filepath.Join(cwd, "~draft.md") {
		t.Fatalf("ResolveToCwd tilde filename = %q", got)
	}
	absolute := filepath.Join(t.TempDir(), "file.txt")
	if got, err := ResolveToCwd(absolute, cwd); err != nil || got != absolute {
		t.Fatalf("ResolveToCwd absolute = %q, want %q", got, absolute)
	}
}

func TestResolveReadPathVariants(t *testing.T) {
	tests := []struct {
		name     string
		created  string
		provided string
	}{
		{name: "nfd", created: "filee\u0301.txt", provided: "file\u00e9.txt"},
		{name: "curly quote", created: "Capture d\u2019cran.txt", provided: "Capture d'cran.txt"},
		{name: "combined", created: "Capture d\u2019e\u0301cran.txt", provided: "Capture d'\u00e9cran.txt"},
		{name: "screenshot", created: "Screenshot at 10.00.00\u202fAM.png", provided: "Screenshot at 10.00.00 AM.png"},
		{name: "lowercase screenshot", created: "Screenshot at 10.00.00\u202fam.png", provided: "Screenshot at 10.00.00 am.png"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			created := filepath.Join(dir, test.created)
			if err := os.WriteFile(created, []byte("content"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ResolveReadPath(test.provided, dir)
			if err != nil || norm.NFC.String(got) != norm.NFC.String(created) {
				t.Fatalf("ResolveReadPath = %q, %v; want canonically equivalent to %q", got, err, created)
			}
			if _, err := os.Stat(got); err != nil {
				t.Fatalf("resolved path does not exist: %v", err)
			}
		})
	}
}
