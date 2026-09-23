package tools

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/OrdalieTech/orb/internal/nodepath"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ExpandPath("~"); err != nil || got != home {
		t.Fatalf("ExpandPath(~) = %q, want %q", got, home)
	}
	if got, err := ExpandPath("~/Documents/file.txt"); err != nil || got != filepath.Join(home, "Documents", "file.txt") {
		t.Fatalf("ExpandPath(~/...) = %q", got)
	}
	if got, err := ExpandPath("@~draft.md"); err != nil || got != "~draft.md" {
		t.Fatalf("ExpandPath(@~draft.md) = %q", got)
	}
	if got, err := ExpandPath("file\u00a0name.txt"); err != nil || got != "file name.txt" {
		t.Fatalf("ExpandPath unicode space = %q", got)
	}
}

func TestExpandPathFallsBackToAccountHome(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	if got, err := ExpandPath("~/file.txt"); err != nil || got != filepath.Join(current.HomeDir, "file.txt") {
		t.Fatalf("ExpandPath without HOME = %q, %v; want account home %q", got, err, current.HomeDir)
	}
}

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

func TestResolveToCwdAcceptsAndRejectsFileURLsLikeNode(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "file with spaces.txt")
	fileURL := nodepath.PathToFileURL(want)
	if got, err := ResolveToCwd(fileURL, filepath.Join(dir, "base")); err != nil || got != want {
		t.Fatalf("ResolveToCwd file URL = %q, %v; want %q", got, err, want)
	}
	// urlRoot and native give the same absolute path in URL and native form:
	// win32 fileURLToPath needs a drive letter where POSIX takes "/".
	urlRoot, native := "", func(path string) string { return path }
	invalid := []string{"file:///%E0%A4%A", "file://[invalid", "file:///tmp/a%2Fb", "file://user@localhost/tmp/x", "file://localhost:80/tmp/x", "file://%25/tmp", "file://local\u200dhost/tmp/x"}
	if runtime.GOOS == "windows" {
		urlRoot, native = "/C:", func(path string) string { return "C:" + filepath.FromSlash(path) }
		invalid = append(invalid, "file:///tmp/x", "file:///C:/a%5Cb", "file://", "file://localhost")
		if got, err := ResolveToCwd("file://server/share", dir); err != nil || got != `\\server\share` {
			t.Fatalf("ResolveToCwd UNC file URL = %q, %v", got, err)
		}
		if got, err := ResolveToCwd("file://C:/tmp/x", dir); err != nil || got != `C:\tmp\x` {
			t.Fatalf("ResolveToCwd drive-letter host = %q, %v", got, err)
		}
	} else {
		invalid = append(invalid, "file://server/share")
		for _, rootURL := range []string{"file://", "file://localhost"} {
			if got, err := ResolveToCwd(rootURL, dir); err != nil || got != string(filepath.Separator) {
				t.Fatalf("ResolveToCwd(%q) = %q, %v; want root", rootURL, got, err)
			}
		}
	}
	for _, input := range invalid {
		if _, err := ResolveToCwd(input, dir); err == nil {
			t.Fatalf("ResolveToCwd(%q) accepted invalid file URL", input)
		}
	}
	for _, suffix := range []string{" \t", "\v", "\f", "\x00"} {
		if got, err := ResolveToCwd("file://"+urlRoot+"/tmp/trailing"+suffix, dir); err != nil || got != native("/tmp/trailing") {
			t.Fatalf("trailing URL whitespace %q = %q, %v", suffix, got, err)
		}
	}
	if _, err := ResolveToCwd(native("/absolute"), "file:///%E0%A4%A"); err == nil || err.Error() != "URI malformed" {
		t.Fatalf("invalid base URL error = %v", err)
	}
	for input, want := range map[string]string{
		"file://" + urlRoot + "/tmp\\foo":               "/tmp/foo",
		"file://%6cocalhost" + urlRoot + "/tmp/x":       "/tmp/x",
		"file://local%68ost" + urlRoot + "/tmp/x":       "/tmp/x",
		"file://%EF%BD%8Cocalhost" + urlRoot + "/tmp/x": "/tmp/x",
		"file://local\u00adhost" + urlRoot + "/tmp/x":   "/tmp/x",
		"file://local\u034fhost" + urlRoot + "/tmp/x":   "/tmp/x",
		"file://local\ufe0fhost" + urlRoot + "/tmp/x":   "/tmp/x",
		"file://" + urlRoot + "/tmp/a\tb\nc\rd":         "/tmp/abcd",
		"file://" + urlRoot + "/tmp/a\x7fb":             "/tmp/a\x7fb",
		"file://" + urlRoot + "/tmp/a\x00b\vc\fd":       "/tmp/a\x00b\vc\fd",
	} {
		if got, err := ResolveToCwd(input, dir); err != nil || got != native(want) {
			t.Fatalf("ResolveToCwd(%q) = %q, %v; want %q", input, got, err, native(want))
		}
	}
}

func TestResolveToCwdRootsDrivelessAbsolutePathsLikeNode(t *testing.T) {
	cwd := t.TempDir()
	want := filepath.Join(filepath.VolumeName(cwd)+string(filepath.Separator), "rooted", "file.txt")
	for _, input := range []string{"/rooted/file.txt", string(filepath.Separator) + filepath.Join("rooted", "file.txt")} {
		if got, err := ResolveToCwd(input, cwd); err != nil || got != want {
			t.Fatalf("ResolveToCwd(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestResolveToCwdDoesNotApplyInputNormalizationToBase(t *testing.T) {
	parent := t.TempDir()
	for _, baseName := range []string{"@workspace", "non\u00a0breaking"} {
		base := filepath.Join(parent, baseName)
		got, err := ResolveToCwd("file.txt", base)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(base, "file.txt"); got != want {
			t.Fatalf("ResolveToCwd base %q = %q, want %q", baseName, got, want)
		}
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

func TestResolveReadPathReturnsNormalizedMissingPath(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolveReadPath("@missing\u00a0file.txt", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "missing file.txt") {
		t.Fatalf("ResolveReadPath missing = %q", got)
	}
}
