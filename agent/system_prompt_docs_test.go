package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemPromptOmitsUnavailableDocsBundle(t *testing.T) {
	for _, test := range []struct {
		name   string
		assets int
	}{
		{name: "standalone-binary"},
		{name: "release-readme-only", assets: 1},
		{name: "docs-only", assets: 2},
		{name: "readme-and-docs", assets: 3},
		{name: "examples-only", assets: 4},
		{name: "readme-and-examples", assets: 5},
		{name: "docs-and-examples", assets: 6},
		{name: "complete-bundle", assets: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for index, name := range []string{"README.md", "docs", "examples"} {
				if test.assets&(1<<index) != 0 {
					writeSystemPromptDocAsset(t, root, name)
				}
			}
			prompt := BuildSystemPrompt(SystemPromptOptions{PackageDir: root, CWD: "/cwd"})
			if got, want := strings.Contains(prompt, "<docs>"), test.assets == 7; got != want {
				t.Fatalf("docs section present = %v, want %v: %s", got, want, prompt)
			}
			if test.assets != 7 {
				for _, name := range []string{"README.md", "docs", "examples"} {
					if strings.Contains(prompt, filepath.Join(root, name)) {
						t.Fatalf("incomplete docs bundle advertised %s: %s", name, prompt)
					}
				}
			}
		})
	}
}

func writeSystemPromptDocsBundle(t testing.TB, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"README.md", "docs", "examples"} {
		writeSystemPromptDocAsset(t, root, name)
	}
}

func writeSystemPromptDocAsset(t testing.TB, root, name string) {
	t.Helper()
	path := filepath.Join(root, name)
	if name == "README.md" {
		if err := os.WriteFile(path, []byte("Orb documentation"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
