package exporthtml

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	modetheme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
	"github.com/OrdalieTech/pigo/codingagent/session"
)

func TestExportMatchesPinnedUpstreamHTML(t *testing.T) {
	fixture := fixturePath(t)
	tests := []struct {
		name  string
		theme string
		hash  string
	}{
		{"dark", "dark", "738870714620acd4fda1b41eba41de3f31187c826d35f5d5dc509aaeb1b5d363"},
		{"light", "light", "06397f1a46c74fcfab93261e191e8636867c58bef1013ad7b7741965e09477cc"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "session.html")
			path, err := ExportFromFile(fixture, Options{OutputPath: output, ThemeName: test.theme})
			if err != nil {
				t.Fatal(err)
			}
			if path != output {
				t.Fatalf("export path = %q, want %q", path, output)
			}
			contents, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if got := sha256Hex(contents); got != test.hash {
				t.Fatalf("%s HTML sha256 = %s, want pinned-upstream %s", test.name, got, test.hash)
			}
			if test.theme == "dark" {
				assertEmbeddedPayload(t, string(contents))
				assertSelfContainedRenderer(t, string(contents))
			}
		})
	}
}

func TestEmbeddedAssetsMatchPinnedUpstream(t *testing.T) {
	t.Parallel()
	assets := map[string]struct {
		contents string
		hash     string
	}{
		"template.html":    {templateHTML, "916782b1184a9597527605ad751e2b3af30fcea23ba2194002969cd217a06881"},
		"template.css":     {templateCSS, "28c16e3827c23a62eef8283cac316b478f946e023093adad25ff9c9b891d41af"},
		"template.js":      {templateJS, "1893cdb77587f592eef5717905391269886d6d7a4dc6a488417a73da374d9226"},
		"marked.min.js":    {markedJS, "d5487edc7258b404bfa74c393d74a6393155f02517bd5e7e77cd64f8187f39a0"},
		"highlight.min.js": {highlightJS, "837a6fa5b0c736b52bbde2b2b6190f305da3fc9ed41681db5321507057b5c846"},
	}
	for name, asset := range assets {
		if got := sha256Hex([]byte(asset.contents)); got != asset.hash {
			t.Errorf("%s sha256 = %s, want %s", name, got, asset.hash)
		}
	}
}

func TestThemeSelectionMatchesUpstreamCOLORFGBG(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":                 "dark",
		"0":                "dark",
		"7":                "light",
		"8":                "dark",
		"15;0":             "dark",
		"0;15":             "light",
		"0;15garbage":      "light",
		"0;\ufeff15\ufeff": "light",
		"15;999":           "light",
		"15;not-a-color":   "light",
	}
	for value, want := range tests {
		if got := defaultThemeName(value); got != want {
			t.Errorf("defaultThemeName(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestDefaultThemeUsesCOLORFGBG(t *testing.T) {
	t.Setenv("COLORFGBG", "0;15")
	output := filepath.Join(t.TempDir(), "session.html")
	if _, err := ExportFromFile(fixturePath(t), Options{OutputPath: output}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256Hex(contents); got != "06397f1a46c74fcfab93261e191e8636867c58bef1013ad7b7741965e09477cc" {
		t.Fatalf("COLORFGBG light export sha256 = %s", got)
	}
}

func TestExportUsesPinnedUpstreamCustomTheme(t *testing.T) {
	agentDir := filepath.Join(t.TempDir(), "agent")
	themeDir := filepath.Join(agentDir, "themes")
	if err := os.MkdirAll(themeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "modes", "theme", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.Replace(string(source), `"name": "dark"`, `"name": "custom-export"`, 1)
	custom = strings.Replace(custom, `"userMsgBg": "#343541"`, `"userMsgBg": "#204060", "pageDeep": "#112233", "pageAlias": "pageDeep", "cardIndex": 24`, 1)
	custom = strings.Replace(custom, `"export": { "pageBg": "#18181e", "cardBg": "#1e1e24", "infoBg": "#3c3728" }`, `"export": { "pageBg": "pageAlias", "cardBg": "cardIndex", "infoBg": "" }`, 1)
	if err := os.WriteFile(filepath.Join(themeDir, "custom-export.json"), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)

	output := filepath.Join(t.TempDir(), "session.html")
	if _, err := ExportFromFile(fixturePath(t), Options{OutputPath: output, ThemeName: "custom-export"}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256Hex(contents); got != "23a0dba3ddb7c5a584aadad888a2d1c2b466a534d5dfc1b4fa2ba7ad19e951ee" {
		t.Fatalf("custom-theme HTML sha256 = %s, want pinned-upstream fixture", got)
	}
	for _, want := range []string{
		"--userMessageBg: #204060;",
		"--exportPageBg: #112233;",
		"--exportCardBg: #005f87;",
		"--exportInfoBg: rgb(52, 79, 96);",
		"--body-bg: #112233;",
		"--container-bg: #005f87;",
		"--info-bg: rgb(52, 79, 96);",
	} {
		if !strings.Contains(string(contents), want) {
			t.Errorf("custom-theme export is missing %q", want)
		}
	}
}

func TestRegisteredCustomThemeExportReloadsSource(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "modes", "theme", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.Replace(string(source), `"name": "dark"`, `"name": "custom-reload"`, 1)
	path := filepath.Join(t.TempDir(), "custom-reload.json")
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	selected, err := modetheme.Parse(path, []byte(custom), modetheme.TrueColor)
	if err != nil {
		t.Fatal(err)
	}
	selected.SourcePath = path
	updated := strings.Replace(custom, `"pageBg": "#18181e"`, `"pageBg": "#123456"`, 1)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveExportTheme("custom-reload", selected)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.pageBg != "#123456" {
		t.Fatalf("page background after source edit = %q", resolved.pageBg)
	}
}

func TestRegisteredCustomThemeExportRequiresSourcePath(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "modes", "theme", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.Replace(string(source), `"name": "dark"`, `"name": "memory-only"`, 1)
	selected, err := modetheme.Parse("memory-only", []byte(custom), modetheme.TrueColor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveExportTheme("memory-only", selected)
	if want := `Theme "memory-only" does not have a source path for export`; err == nil || err.Error() != want {
		t.Fatalf("source-path error = %v, want %q", err, want)
	}
}

func TestExportPathNormalizationAndDefaultNames(t *testing.T) {
	fixturePath := fixturePath(t)
	root := t.TempDir()
	t.Chdir(root)
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lower.jsonl", "upper.JSONL", "session.data"} {
		if err := os.WriteFile(filepath.Join(root, name), fixture, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := map[string]string{
		"lower.jsonl":  "pi-session-lower.html",
		"upper.JSONL":  "pi-session-upper.JSONL.html",
		"session.data": "pi-session-session.data.html",
	}
	for input, want := range tests {
		got, exportErr := ExportFromFile(filepath.Join(root, input), Options{ThemeName: "dark"})
		if exportErr != nil {
			t.Fatal(exportErr)
		}
		if got != want {
			t.Errorf("default output for %q = %q, want %q", input, got, want)
		}
	}

	inputURL := (&url.URL{Scheme: "file", Path: filepath.Join(root, "lower.jsonl")}).String()
	fileOutput := filepath.Join(root, "file url output.html")
	outputURL := (&url.URL{Scheme: "file", Path: fileOutput}).String()
	got, err := ExportFromFile(inputURL, Options{OutputPath: outputURL, ThemeName: "dark"})
	if err != nil {
		t.Fatal(err)
	}
	if got != fileOutput {
		t.Fatalf("file URL output = %q, want %q", got, fileOutput)
	}
	if _, err := os.Stat(fileOutput); err != nil {
		t.Fatal(err)
	}
}

func TestExportErrorsMatchUpstream(t *testing.T) {
	root := t.TempDir()
	inMemory, err := session.InMemory(root, session.WithSessionID("memory-session"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExportSession(inMemory, Options{}); err == nil || err.Error() != "Cannot export in-memory session to HTML" {
		t.Fatalf("in-memory error = %v", err)
	}

	persisted, err := session.Create(root, filepath.Join(root, "sessions"), session.WithSessionID("empty-session"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExportSession(persisted, Options{}); err == nil || err.Error() != "Nothing to export yet - start a conversation first" {
		t.Fatalf("empty-session error = %v", err)
	}

	missing := filepath.Join(root, "missing.jsonl")
	if _, err := ExportFromFile(missing, Options{}); err == nil || err.Error() != "File not found: "+missing {
		t.Fatalf("missing-file error = %v", err)
	}

	output := filepath.Join(root, "invalid-theme.html")
	if _, err := ExportFromFile(fixturePath(t), Options{OutputPath: output, ThemeName: "custom-deferred"}); err == nil || err.Error() != "Theme not found: custom-deferred" {
		t.Fatalf("theme error = %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid theme created output: %v", err)
	}

	if _, err := ExportFromFile("file://remote/tmp/session.jsonl", Options{}); err == nil || err.Error() != `file URL has unsupported host "remote"` {
		t.Fatalf("remote file URL error = %v", err)
	}
	if _, err := ExportFromFile(fixturePath(t), Options{OutputPath: "file://remote/tmp/output.html", ThemeName: "missing"}); err == nil || err.Error() != "Theme not found: missing" {
		t.Fatalf("theme/path error precedence = %v", err)
	}
}

func TestReplaceOnceMatchesJavaScriptStringReplace(t *testing.T) {
	t.Parallel()
	got := replaceOnce("aXbXc", "X", "$$-$&-$`-$'-$1")
	if want := "a$-X-a-bXc-$1bXc"; got != want {
		t.Fatalf("replaceOnce = %q, want %q", got, want)
	}
}

func assertEmbeddedPayload(t *testing.T, html string) {
	t.Helper()
	match := regexp.MustCompile(`<script id="session-data" type="application/json">([^<]+)</script>`).FindStringSubmatch(html)
	if len(match) != 2 {
		t.Fatal("session data script was not found")
	}
	if !strings.HasSuffix(match[1], "==") {
		t.Fatalf("session payload is not standard padded base64: %q", match[1][len(match[1])-8:])
	}
	if got := sha256Hex([]byte(match[1])); got != "a6fae2837f472947a8a414cf329741454e2506d3e7ed3b46100a3008d4941431" {
		t.Fatalf("base64 payload sha256 = %s", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(match[1])
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256Hex(decoded); got != "05395fdf655d4cd1c1f2b7c1e0bb44d81f3c98c1aad8378ddd6ea48eeb9cf49c" {
		t.Fatalf("decoded payload sha256 = %s", got)
	}
	for _, want := range []string{`"big":9007199254740992`, `"text":"answer <& \ud800"`, `"leafId":"00000008"`} {
		if !strings.Contains(string(decoded), want) {
			t.Errorf("normalized payload is missing %q", want)
		}
	}
}

func assertSelfContainedRenderer(t *testing.T, html string) {
	t.Helper()
	for _, placeholder := range []string{"{{CSS}}", "{{JS}}", "{{SESSION_DATA}}", "{{MARKED_JS}}"} {
		if strings.Contains(html, placeholder) {
			t.Errorf("export retained placeholder %s", placeholder)
		}
	}
	// highlight.min.js contains $`, so upstream String.replace copies the
	// preceding HTML and leaves one copied placeholder behind.
	if count := strings.Count(html, "{{HIGHLIGHT_JS}}"); count != 1 {
		t.Errorf("highlight placeholder count = %d, want upstream count 1", count)
	}
	if strings.Contains(html, "<script src=") || strings.Contains(html, "<link rel=") {
		t.Error("export contains an external asset reference")
	}
	for _, securityPattern := range []string{
		"sanitizeMarkdownUrl", "escapeHtml(href)", "parseSkillBlock", "replace(/[\\x00-\\x1f\\x7f]/g, '')",
	} {
		if !strings.Contains(html, securityPattern) {
			t.Errorf("vendored export renderer is missing %q", securityPattern)
		}
	}
	whitespace := regexp.MustCompile(`(?s)\.output-preview > div:not\(\.expand-hint\),\s*\.output-full > div:not\(\.expand-hint\) \{.*?white-space:\s*pre-wrap;`)
	ansiWhitespace := regexp.MustCompile(`(?s)\.ansi-line\s*\{\s*white-space:\s*pre;`)
	if !whitespace.MatchString(html) || !ansiWhitespace.MatchString(html) {
		t.Error("vendored renderer lost upstream output whitespace rules")
	}
}

func fixturePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func sha256Hex(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}
