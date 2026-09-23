package nodepath

import (
	"runtime"
	"testing"
)

// Expected values are Node's url.fileURLToPath/pathToFileURL results on each
// platform, so both semantics are checked wherever the suite runs.

func TestFileURLToPathPosix(t *testing.T) {
	for raw, want := range map[string]string{
		"file:///tmp/a%20b":        "/tmp/a b",
		"file://localhost/tmp/x":   "/tmp/x",
		"file://LOCALHOST/tmp/x":   "/tmp/x",
		"file://C:/x":              "/C:/x",
		"file:///C|/x":             "/C:/x",
		"file:///a/./b/../c":       "/a/c",
		"file:///a/%2e%2E/c":       "/c",
		"file:///tmp\\foo":         "/tmp/foo",
		"file:///tmp/x?query#frag": "/tmp/x",
		" file:///tmp/a\tb\n ":     "/tmp/ab",
		"file://":                  "/",
		"file:///tmp/%C3%A9":       "/tmp/é",
	} {
		if got, err := fileURLToPath(raw, false); err != nil || got != want {
			t.Errorf("posix fileURLToPath(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for raw, want := range map[string]string{
		"file://server/share":         `File URL host must be "localhost" or empty on ` + runtime.GOOS,
		"file:///tmp/a%2Fb":           "File URL path must not include encoded / characters",
		"file:///%E0%A4%A":            "URI malformed",
		"file:///%FF":                 "URI malformed",
		"file://user@localhost/tmp/x": "Invalid URL",
		"file://localhost:80/tmp/x":   "Invalid URL",
		"file://[invalid/tmp":         "Invalid URL",
		"http://example.com/x":        "Invalid URL",
	} {
		if got, err := fileURLToPath(raw, false); err == nil || err.Error() != want {
			t.Errorf("posix fileURLToPath(%q) = %q, %v; want error %q", raw, got, err, want)
		}
	}
}

func TestFileURLToPathWindows(t *testing.T) {
	for raw, want := range map[string]string{
		"file:///C:/path/":            `C:\path\`,
		"file:///C:/Users/a%20b/x":    `C:\Users\a b\x`,
		"file://C:/x":                 `C:\x`,
		"file:///c|/x":                `c:\x`,
		"file://localhost/D:/x":       `D:\x`,
		"file://nas/foo.txt":          `\\nas\foo.txt`,
		"file://NAS/share/a%20b":      `\\nas\share\a b`,
		"file:///C:/a/../../b":        `C:\b`,
		"file:///C:\\Users\\x":        `C:\Users\x`,
		"file:///C:/x%23y?z#fragment": `C:\x#y`,
	} {
		if got, err := fileURLToPath(raw, true); err != nil || got != want {
			t.Errorf("win32 fileURLToPath(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for raw, want := range map[string]string{
		"file:///tmp/x":     "File URL path must be absolute",
		"file://":           "File URL path must be absolute",
		"file:///C:/a%2Fb":  `File URL path must not include encoded \ or / characters`,
		"file:///C:/a%5cb":  `File URL path must not include encoded \ or / characters`,
		"file:///C:/%E0%A4": "URI malformed",
		"file://a@b/x":      "Invalid URL",
	} {
		if got, err := fileURLToPath(raw, true); err == nil || err.Error() != want {
			t.Errorf("win32 fileURLToPath(%q) = %q, %v; want error %q", raw, got, err, want)
		}
	}
}

func TestPathToFileURL(t *testing.T) {
	for _, test := range []struct {
		path    string
		windows bool
		want    string
	}{
		{"/tmp/a b/c#1?.txt", false, "file:///tmp/a%20b/c%231%3F.txt"},
		{`/tmp/a\b%`, false, "file:///tmp/a%5Cb%25"},
		{"/tmp/é", false, "file:///tmp/%C3%A9"},
		{`C:\Users\a b\x.txt`, true, "file:///C:/Users/a%20b/x.txt"},
		{`\\Server\share\a b`, true, "file://server/share/a%20b"},
		{`\\?\UNC\server\share\x`, true, "file://server/share/x"},
	} {
		if got := pathToFileURL(test.path, test.windows); got != test.want {
			t.Errorf("pathToFileURL(%q, windows=%t) = %q, want %q", test.path, test.windows, got, test.want)
		}
		if back, err := fileURLToPath(test.want, test.windows); err != nil || (back != test.path && !test.windows) {
			t.Errorf("round trip %q = %q, %v", test.want, back, err)
		}
	}
}

func TestWindowsShellPath(t *testing.T) {
	for input, want := range map[string]string{
		"/c/Users/x":       `C:\Users\x`,
		"/C":               `C:\`,
		"/d/":              `D:\`,
		"/mnt/e/src/a.go":  `E:\src\a.go`,
		"/cygdrive/f":      `F:\`,
		"/MNT/g/x":         `G:\x`,
		"/usr/bin":         "/usr/bin",
		"/mnt/cc/x":        "/mnt/cc/x",
		"//server/share":   "//server/share",
		`/c/mixed\sep`:     `/c/mixed\sep`,
		"relative/c/x":     "relative/c/x",
		`C:\already\there`: `C:\already\there`,
	} {
		if got := windowsShellPath(input); got != want {
			t.Errorf("windowsShellPath(%q) = %q, want %q", input, got, want)
		}
	}
	if runtime.GOOS != "windows" && NormalizeShellPath("/c/x") != "/c/x" {
		t.Fatal("NormalizeShellPath rewrote a POSIX path off win32")
	}
}
