package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/OrdalieTech/pigo/codingagent/modes/theme"
	"github.com/OrdalieTech/pigo/conformance/runner"
	"github.com/OrdalieTech/pigo/tui"
)

type f12ThemeFixture struct {
	SchemaVersion int               `json:"schemaVersion"`
	Themes        []f12ThemeCase    `json:"themes"`
	Discovery     f12ThemeDiscovery `json:"discovery"`
	Validation    struct {
		TrailingDocumentAccepted bool `json:"trailingDocumentAccepted"`
		UnknownForegroundThrows  bool `json:"unknownForegroundThrows"`
	} `json:"validation"`
}

type f12ThemeCase struct {
	Name        string            `json:"name"`
	Mode        string            `json:"mode"`
	Foreground  map[string]string `json:"foreground"`
	Background  map[string]string `json:"background"`
	Sample      []string          `json:"sample"`
	Highlighted []string          `json:"highlighted"`
	Highlights  []struct {
		Name     string   `json:"name"`
		Language string   `json:"language"`
		Code     string   `json:"code"`
		Expected []string `json:"expected"`
	} `json:"highlights"`
	Fallback []string          `json:"fallback"`
	Resolved map[string]string `json:"resolved"`
	Export   map[string]string `json:"export"`
}

type f12ThemeDiscovery struct {
	ProjectOverUser f12ThemeDiscoveryCase `json:"projectOverUser"`
	ExtendFirstWins f12ThemeDiscoveryCase `json:"extendFirstWins"`
}

type f12ThemeDiscoveryCase struct {
	Selected    string               `json:"selected"`
	Diagnostics []f12ThemeDiagnostic `json:"diagnostics"`
}

type f12ThemeDiagnostic struct {
	Type      string             `json:"type"`
	Message   string             `json:"message"`
	Path      string             `json:"path,omitempty"`
	Collision *f12ThemeCollision `json:"collision,omitempty"`
}

type f12ThemeCollision struct {
	ResourceType string `json:"resourceType"`
	Name         string `json:"name"`
	WinnerPath   string `json:"winnerPath"`
	LoserPath    string `json:"loserPath"`
}

func TestF12BuiltInThemesMatchUpstream(t *testing.T) {
	var fixture f12ThemeFixture
	runner.LoadJSON(t, "F12", "themes.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Themes) != 2 {
		t.Fatalf("F12 themes header = version %d, themes %d", fixture.SchemaVersion, len(fixture.Themes))
	}
	registry := theme.Load(theme.LoadOptions{AgentDir: t.TempDir(), CWD: t.TempDir(), Mode: theme.TrueColor})
	for _, fixtureCase := range fixture.Themes {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			selected, ok := registry.Get(fixtureCase.Name)
			if !ok {
				t.Fatalf("theme %q is unavailable", fixtureCase.Name)
			}
			if string(selected.ColorMode()) != fixtureCase.Mode {
				t.Fatalf("color mode = %q, want %q", selected.ColorMode(), fixtureCase.Mode)
			}
			for name, expected := range fixtureCase.Foreground {
				actual, err := selected.ForegroundANSI(name)
				if err != nil || actual != expected {
					t.Fatalf("foreground %s = %q, %v; want %q", name, actual, err, expected)
				}
			}
			for name, expected := range fixtureCase.Background {
				actual, err := selected.BackgroundANSI(name)
				if err != nil || actual != expected {
					t.Fatalf("background %s = %q, %v; want %q", name, actual, err, expected)
				}
			}

			const sample = "# Theme sample\n\n> quote with **bold** and `code`\n\n```typescript\nconst answer: number = 42; // value\n```"
			component := tui.NewMarkdown(sample, 0, 0, selected.Markdown(">>"), nil, nil)
			if diff := linesDiff(fixtureCase.Sample, component.Render(72)); diff != "" {
				t.Fatal(diff)
			}
			if actual := theme.Highlight("const answer: number = 42; // value", "typescript", selected); !reflect.DeepEqual(actual, fixtureCase.Highlighted) {
				t.Fatalf("highlighted = %#v\nwant %#v", actual, fixtureCase.Highlighted)
			}
			for _, highlightCase := range fixtureCase.Highlights {
				if actual := theme.Highlight(highlightCase.Code, highlightCase.Language, selected); !reflect.DeepEqual(actual, highlightCase.Expected) {
					t.Fatalf("highlight %s = %#v\nwant %#v", highlightCase.Name, actual, highlightCase.Expected)
				}
			}
			if actual := theme.Highlight("plain text", "definitely-not-a-language", selected); !reflect.DeepEqual(actual, fixtureCase.Fallback) {
				t.Fatalf("fallback = %#v\nwant %#v", actual, fixtureCase.Fallback)
			}
			if actual := selected.ResolvedColors(fixtureCase.Name == "light"); !reflect.DeepEqual(actual, fixtureCase.Resolved) {
				t.Fatalf("resolved colors differ\ngot:  %#v\nwant: %#v", actual, fixtureCase.Resolved)
			}
			if actual := selected.ExportColors(); !reflect.DeepEqual(actual, fixtureCase.Export) {
				t.Fatalf("export colors differ\ngot:  %#v\nwant: %#v", actual, fixtureCase.Export)
			}
		})
	}

	dark, ok := registry.Get("dark")
	if !ok {
		t.Fatal("dark theme missing for validation")
	}
	trailing := append(f12ThemeDocument(t, "trailing", dark.ResolvedColors(false)), []byte("\n{}")...)
	_, parseErr := theme.Parse("trailing", trailing, theme.TrueColor)
	if accepted := parseErr == nil; accepted != fixture.Validation.TrailingDocumentAccepted {
		t.Fatalf("trailing theme document accepted = %v, want %v", accepted, fixture.Validation.TrailingDocumentAccepted)
	}
	if throws := f12ThemeForegroundThrows(dark); throws != fixture.Validation.UnknownForegroundThrows {
		t.Fatalf("unknown foreground throws = %v, want %v", throws, fixture.Validation.UnknownForegroundThrows)
	}

	root := t.TempDir()
	agentDir, cwd := filepath.Join(root, "agent"), filepath.Join(root, "project")
	userTheme := filepath.Join(agentDir, "themes", "user.json")
	projectTheme := filepath.Join(cwd, ".pi", "themes", "project.json")
	f12WriteTheme(t, userTheme, "project-over-user", dark.ResolvedColors(false))
	f12WriteTheme(t, projectTheme, "project-over-user", dark.ResolvedColors(false))
	projectRegistry := theme.Load(theme.LoadOptions{CWD: cwd, AgentDir: agentDir, ProjectTrusted: true, Mode: theme.TrueColor})
	actualProject := f12SummarizeThemeDiscovery(projectRegistry, "project-over-user", map[string]string{
		projectTheme: "<project-theme>",
		userTheme:    "<user-theme>",
	})
	if !reflect.DeepEqual(actualProject, fixture.Discovery.ProjectOverUser) {
		t.Fatalf("project-over-user discovery = %#v\nwant %#v", actualProject, fixture.Discovery.ProjectOverUser)
	}

	firstTheme, secondTheme := filepath.Join(root, "first.json"), filepath.Join(root, "second.json")
	f12WriteTheme(t, firstTheme, "extend-first-wins", dark.ResolvedColors(false))
	f12WriteTheme(t, secondTheme, "extend-first-wins", dark.ResolvedColors(false))
	extendRegistry := theme.Load(theme.LoadOptions{CWD: filepath.Join(root, "extend-project"), AgentDir: filepath.Join(root, "extend-agent"), Mode: theme.TrueColor, AdditionalPaths: []string{firstTheme}})
	extendRegistry.Extend([]string{secondTheme})
	actualExtend := f12SummarizeThemeDiscovery(extendRegistry, "extend-first-wins", map[string]string{
		firstTheme:  "<first-theme>",
		secondTheme: "<second-theme>",
	})
	if !reflect.DeepEqual(actualExtend, fixture.Discovery.ExtendFirstWins) {
		t.Fatalf("extend-first-wins discovery = %#v\nwant %#v", actualExtend, fixture.Discovery.ExtendFirstWins)
	}
}

func f12ThemeDocument(t *testing.T, name string, colors map[string]string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"name": name, "colors": colors})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func f12WriteTheme(t *testing.T, path, name string, colors map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, f12ThemeDocument(t, name, colors), 0o644); err != nil {
		t.Fatal(err)
	}
}

func f12ThemeForegroundThrows(selected *theme.Theme) (threw bool) {
	defer func() { threw = recover() != nil }()
	_ = selected.Foreground("__pi_go_unknown__", "value")
	return false
}

func f12SummarizeThemeDiscovery(registry *theme.Registry, name string, labels map[string]string) f12ThemeDiscoveryCase {
	label := func(path string) string {
		if replacement, ok := labels[path]; ok {
			return replacement
		}
		return path
	}
	result := f12ThemeDiscoveryCase{Diagnostics: []f12ThemeDiagnostic{}}
	if selected, ok := registry.Get(name); ok {
		result.Selected = label(selected.SourcePath)
	}
	for _, diagnostic := range registry.Diagnostics() {
		converted := f12ThemeDiagnostic{Type: diagnostic.Type, Message: diagnostic.Message, Path: label(diagnostic.Path)}
		if diagnostic.Collision != nil {
			converted.Collision = &f12ThemeCollision{
				ResourceType: diagnostic.Collision.ResourceType,
				Name:         diagnostic.Collision.Name,
				WinnerPath:   label(diagnostic.Collision.WinnerPath),
				LoserPath:    label(diagnostic.Collision.LoserPath),
			}
		}
		result.Diagnostics = append(result.Diagnostics, converted)
	}
	return result
}
