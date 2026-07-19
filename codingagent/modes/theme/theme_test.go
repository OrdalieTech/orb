package theme

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	chroma "github.com/alecthomas/chroma/v2"
)

func TestBuiltinsAndColorModes(t *testing.T) {
	trueColor := Load(LoadOptions{AgentDir: t.TempDir(), CWD: t.TempDir(), Mode: TrueColor})
	dark, ok := trueColor.Get("dark")
	if !ok {
		t.Fatal("dark theme missing")
	}
	if ansi, _ := dark.ForegroundANSI("accent"); ansi != "\x1b[38;2;138;190;183m" {
		t.Fatalf("truecolor accent = %q", ansi)
	}
	indexed := Load(LoadOptions{AgentDir: t.TempDir(), CWD: t.TempDir(), Mode: Color256})
	dark, _ = indexed.Get("dark")
	if ansi, _ := dark.ForegroundANSI("accent"); !strings.HasPrefix(ansi, "\x1b[38;5;") {
		t.Fatalf("256-color accent = %q", ansi)
	}
	if got := indexed.Available(); !reflect.DeepEqual(got, []string{"dark", "light"}) {
		t.Fatalf("available = %#v", got)
	}
}

func TestRegistryDiscoveryTrustAndContentNames(t *testing.T) {
	agentDir, cwd := t.TempDir(), t.TempDir()
	writeTestTheme(t, filepath.Join(agentDir, "themes", "filename.json"), "user-name", "#112233")
	projectPath := filepath.Join(cwd, ".pi", "themes", "project.json")
	userPath := filepath.Join(agentDir, "themes", "filename.json")
	writeTestTheme(t, projectPath, "user-name", "#223344")

	untrusted := Load(LoadOptions{AgentDir: agentDir, CWD: cwd, ProjectTrusted: false, Mode: TrueColor})
	if diagnostics := untrusted.Diagnostics(); len(diagnostics) != 0 {
		t.Fatalf("default discovery diagnostics = %#v", diagnostics)
	}
	if _, ok := untrusted.Get("user-name"); !ok {
		t.Fatal("user theme missing")
	}
	if _, ok := untrusted.Get("filename"); ok {
		t.Fatal("filename was used instead of the theme content name")
	}
	user, _ := untrusted.Get("user-name")
	if user.SourcePath != userPath {
		t.Fatalf("untrusted source = %q", user.SourcePath)
	}

	trusted := Load(LoadOptions{AgentDir: agentDir, CWD: cwd, ProjectTrusted: true, Mode: TrueColor})
	project, ok := trusted.Get("user-name")
	if !ok {
		t.Fatal("trusted project theme missing")
	}
	if project.SourcePath != projectPath {
		t.Fatalf("project source = %q", project.SourcePath)
	}
	diagnostics := trusted.Diagnostics()
	if len(diagnostics) != 1 || diagnostics[0].Type != "collision" || diagnostics[0].Path != userPath || diagnostics[0].Collision == nil {
		t.Fatalf("trusted diagnostics = %#v", diagnostics)
	}
	if collision := diagnostics[0].Collision; collision.ResourceType != "theme" || collision.Name != "user-name" || collision.WinnerPath != projectPath || collision.LoserPath != userPath {
		t.Fatalf("trusted collision = %#v", collision)
	}
}

func TestRegistryThemePrecedenceIsFirstWins(t *testing.T) {
	root := t.TempDir()
	agentDir, cwd := filepath.Join(root, "agent"), filepath.Join(root, "project")
	projectSettings := filepath.Join(cwd, ".pi", "configured.json")
	projectAuto := filepath.Join(cwd, ".pi", "themes", "auto.json")
	userSettings := filepath.Join(agentDir, "configured.json")
	userAuto := filepath.Join(agentDir, "themes", "auto.json")
	packagePath := filepath.Join(root, "package.json")
	additionalPath := filepath.Join(root, "additional.json")
	resourcePath := filepath.Join(root, "resource.json")
	paths := []string{projectSettings, projectAuto, userSettings, userAuto, packagePath, additionalPath, resourcePath}
	for index, path := range paths {
		writeTestTheme(t, path, "shared", fmt.Sprintf("#00000%d", index+1))
	}
	registry := Load(LoadOptions{
		AgentDir: agentDir, CWD: cwd, ProjectTrusted: true, Mode: TrueColor,
		ProjectPaths: []string{"configured.json"}, GlobalPaths: []string{"configured.json"},
		PackagePaths: []string{packagePath}, AdditionalPaths: []string{additionalPath}, ResourceDiscoverPath: []string{resourcePath},
	})
	assertSelectedTheme(t, registry, "shared", projectSettings)
	diagnostics := registry.Diagnostics()
	if len(diagnostics) != len(paths)-1 {
		t.Fatalf("collision diagnostics = %#v", diagnostics)
	}
	for index, diagnostic := range diagnostics {
		loser := paths[index+1]
		if diagnostic.Type != "collision" || diagnostic.Message != `name "shared" collision` || diagnostic.Path != loser || diagnostic.Collision == nil {
			t.Fatalf("diagnostic %d = %#v", index, diagnostic)
		}
		if diagnostic.Collision.WinnerPath != projectSettings || diagnostic.Collision.LoserPath != loser {
			t.Fatalf("collision %d = %#v", index, diagnostic.Collision)
		}
	}
}

func TestRegistryAdditionalExtendAndBuiltinSemantics(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	first, second := filepath.Join(root, "first.json"), filepath.Join(root, "second.json")
	writeTestTheme(t, first, "shared", "#111111")
	writeTestTheme(t, second, "shared", "#222222")
	writeTestTheme(t, filepath.Join(cwd, "custom-dark.json"), "dark", "#333333")
	registry := Load(LoadOptions{AgentDir: t.TempDir(), CWD: cwd, Mode: TrueColor, AdditionalPaths: []string{first, "custom-dark.json"}})
	registry.Extend([]string{second})
	assertSelectedTheme(t, registry, "shared", first)
	assertSelectedTheme(t, registry, "dark", filepath.Join(cwd, "custom-dark.json"))
	if diagnostics := registry.Diagnostics(); len(diagnostics) != 1 || diagnostics[0].Collision == nil || diagnostics[0].Collision.WinnerPath != first || diagnostics[0].Collision.LoserPath != second {
		t.Fatalf("extend diagnostics = %#v", diagnostics)
	}

	replacement, err := Parse("replacement", mustThemeJSON(t, "shared", "#abcdef"), TrueColor)
	if err != nil {
		t.Fatal(err)
	}
	replacement.SourcePath = "<replacement>"
	if err := registry.Register(replacement); err != nil {
		t.Fatal(err)
	}
	assertSelectedTheme(t, registry, "shared", "<replacement>")
}

func TestRegistryDiagnosticsPlaceLoadWarningsBeforeCollisions(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.json")
	invalid := filepath.Join(root, "not-json.txt")
	second := filepath.Join(root, "second.json")
	missing := filepath.Join(root, "missing.json")
	writeTestTheme(t, first, "shared", "#111111")
	writeTestTheme(t, second, "shared", "#222222")
	if err := os.WriteFile(invalid, []byte("not a theme"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := Load(LoadOptions{
		AgentDir: t.TempDir(), CWD: root, NoThemes: true, Mode: TrueColor,
		AdditionalPaths: []string{first, invalid, second, missing},
	})
	loaded := registry.Loaded()
	if len(loaded) != 1 || loaded[0].Name != "shared" || loaded[0].SourcePath != first {
		t.Fatalf("loaded themes = %#v", loaded)
	}
	diagnostics := registry.Diagnostics()
	if len(diagnostics) != 3 || diagnostics[0].Path != invalid || diagnostics[1].Path != missing || diagnostics[2].Path != second || diagnostics[2].Collision == nil {
		t.Fatalf("ordered diagnostics = %#v", diagnostics)
	}
}

func TestRegistryConcurrentReadsAndMutations(t *testing.T) {
	registry := Load(LoadOptions{AgentDir: t.TempDir(), CWD: t.TempDir(), Mode: TrueColor})
	themes := make([]*Theme, 64)
	for index := range themes {
		parsed, err := Parse(fmt.Sprintf("concurrent-%d", index), mustThemeJSON(t, fmt.Sprintf("concurrent-%d", index), "#112233"), TrueColor)
		if err != nil {
			t.Fatal(err)
		}
		themes[index] = parsed
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range themes {
		wait.Add(2)
		go func(index int) {
			defer wait.Done()
			<-start
			_ = registry.Register(themes[index])
		}(index)
		go func(index int) {
			defer wait.Done()
			<-start
			for iteration := 0; iteration < 32; iteration++ {
				_, _ = registry.Get(fmt.Sprintf("concurrent-%d", index))
				_ = registry.Available()
				_ = registry.Loaded()
				_ = registry.Diagnostics()
			}
		}(index)
	}
	close(start)
	wait.Wait()

	if got := len(registry.Available()); got != len(themes)+2 {
		t.Fatalf("available themes = %d, want %d", got, len(themes)+2)
	}
}

func TestRegistryReplaceLoadedAtomicallyValidatesAndRemovesStaleThemes(t *testing.T) {
	registry := Load(LoadOptions{AgentDir: t.TempDir(), CWD: t.TempDir(), Mode: TrueColor})
	first, err := Parse("first", mustThemeJSON(t, "first", "#112233"), TrueColor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Parse("second", mustThemeJSON(t, "second", "#445566"), TrueColor)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(first); err != nil {
		t.Fatal(err)
	}

	if err := registry.ReplaceLoaded([]*Theme{second, {Name: "bad/name"}}); err == nil {
		t.Fatal("invalid replacement theme name was accepted")
	}
	if retained, found := registry.Get("first"); !found || retained != first {
		t.Fatal("failed replacement mutated the registered theme set")
	}
	if _, found := registry.Get("second"); found {
		t.Fatal("failed replacement partially installed a valid prefix")
	}

	if err := registry.ReplaceLoaded([]*Theme{second}); err != nil {
		t.Fatal(err)
	}
	if _, found := registry.Get("first"); found {
		t.Fatal("successful replacement retained a stale nonbuiltin theme")
	}
	if replaced, found := registry.Get("second"); !found || replaced != second {
		t.Fatal("successful replacement did not preserve the supplied theme object")
	}
}

func TestRegistryNoThemesRelativePathsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	cwd, agentDir := filepath.Join(root, "project"), filepath.Join(root, "agent")
	autoPath := filepath.Join(agentDir, "themes", "auto.json")
	additionalPath := filepath.Join(cwd, "additional.json")
	resourcePath := filepath.Join(cwd, "resource.json")
	targetPath := filepath.Join(root, "target.json")
	symlinkPath := filepath.Join(cwd, "linked.json")
	writeTestTheme(t, autoPath, "auto", "#111111")
	writeTestTheme(t, additionalPath, "additional", "#222222")
	writeTestTheme(t, resourcePath, "resource", "#333333")
	writeTestTheme(t, targetPath, "linked", "#444444")
	if err := os.Symlink(targetPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	registry := Load(LoadOptions{
		CWD: cwd, AgentDir: agentDir, NoThemes: true, Mode: TrueColor,
		AdditionalPaths: []string{"additional.json", "linked.json"}, ResourceDiscoverPath: []string{"resource.json"},
	})
	for name, path := range map[string]string{"additional": additionalPath, "resource": resourcePath, "linked": symlinkPath} {
		assertSelectedTheme(t, registry, name, path)
	}
	if _, ok := registry.Get("auto"); ok {
		t.Fatal("NoThemes loaded an automatic user theme")
	}
}

func TestThemeValidationVariablesAndFallback(t *testing.T) {
	data, err := builtins.ReadFile("dark.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["name"] = "bad/name"
	encoded, _ := json.Marshal(document)
	if _, err := Parse("bad", encoded, TrueColor); err == nil {
		t.Fatal("slash in theme name was accepted")
	}
	document["name"] = "missing"
	delete(document["colors"].(map[string]any), "accent")
	encoded, _ = json.Marshal(document)
	if _, err := Parse("missing", encoded, TrueColor); err == nil || !strings.Contains(err.Error(), "accent") {
		t.Fatalf("missing token error = %v", err)
	}
	valid := mustThemeJSON(t, "trailing", "#112233")
	if _, err := Parse("trailing", append(valid, []byte("\n{}")...), TrueColor); err == nil {
		t.Fatal("trailing JSON document was accepted")
	}
	if _, err := Parse("whitespace", append(valid, []byte("\n\t ")...), TrueColor); err != nil {
		t.Fatalf("trailing JSON whitespace was rejected: %v", err)
	}
}

func TestControllerSettingsDetectionAndReload(t *testing.T) {
	if setting, ok := ParseAutoSetting(" light / dark "); !ok || setting.Light != "light" || setting.Dark != "dark" {
		t.Fatalf("auto setting = %#v, %v", setting, ok)
	}
	if _, ok := ParseAutoSetting("light/dark/extra"); ok {
		t.Fatal("multi-slash setting accepted")
	}
	if name, ok := ResolveSetting("light/dark", Light); !ok || name != "light" {
		t.Fatalf("resolved = %q, %v", name, ok)
	}
	if detection := DetectBackground(map[string]string{"COLORFGBG": "0;7;15"}); detection.Theme != Light || detection.Confidence != "high" {
		t.Fatalf("detection = %#v", detection)
	}
	if ThemeForRGB(8, 8, 8) != Dark || ThemeForRGB(250, 250, 250) != Light {
		t.Fatal("RGB luminance classification differs")
	}

	path := filepath.Join(t.TempDir(), "reload.json")
	writeTestTheme(t, path, "reload", "#111111")
	registry := Load(LoadOptions{AgentDir: t.TempDir(), CWD: t.TempDir(), Mode: TrueColor, AdditionalPaths: []string{path}})
	changes := 0
	controller := NewController(registry, "reload", Dark, func() { changes++ })
	writeTestTheme(t, path, "reload", "#abcdef")
	if err := controller.Reload(); err != nil {
		t.Fatal(err)
	}
	if ansi, _ := controller.Current().ForegroundANSI("accent"); ansi != "\x1b[38;2;171;205;239m" {
		t.Fatalf("reloaded accent = %q", ansi)
	}
	if changes != 2 {
		t.Fatalf("change callbacks = %d", changes)
	}
}

func TestHighlightAndLanguageFromPath(t *testing.T) {
	registry := Load(LoadOptions{AgentDir: t.TempDir(), CWD: t.TempDir(), Mode: TrueColor})
	dark, _ := registry.Get("dark")
	lines := Highlight("const x = 1", "typescript", dark)
	if len(lines) != 1 || !strings.Contains(lines[0], "\x1b[38;2;86;156;214mconst") {
		t.Fatalf("highlight = %#v", lines)
	}
	if LanguageFromPath("src/main.tsx") != "typescript" || LanguageFromPath("Dockerfile") != "dockerfile" || LanguageFromPath("README") != "" {
		t.Fatal("language path mapping differs")
	}
}

func TestHighlightECMAScriptPrimitiveAndUserTypeScopes(t *testing.T) {
	registry := Load(LoadOptions{AgentDir: t.TempDir(), CWD: t.TempDir(), Mode: TrueColor})
	dark, _ := registry.Get("dark")
	line := Highlight("const answer: number = factory.value", "typescript", dark)[0]
	if primitive := dark.Foreground("syntaxType", "number"); !strings.Contains(line, primitive) {
		t.Fatalf("primitive type is not styled: %q", line)
	}
	if userDefined := dark.Foreground("syntaxType", "factory.value"); strings.Contains(line, userDefined) {
		t.Fatalf("Chroma user-type false positive leaked through: %q", line)
	}
}

func TestHighlightOperatorAndPunctuationTokens(t *testing.T) {
	registry := Load(LoadOptions{AgentDir: t.TempDir(), CWD: t.TempDir(), Mode: TrueColor})
	dark, _ := registry.Get("dark")
	for _, test := range []struct {
		token chroma.TokenType
		color string
	}{{chroma.Operator, "syntaxOperator"}, {chroma.OperatorWord, "syntaxOperator"}, {chroma.NameOperator, "syntaxOperator"}, {chroma.Punctuation, "syntaxPunctuation"}} {
		prefix, err := dark.ForegroundANSI(test.color)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := highlightToken(test.token, "value", dark), prefix+"value\x1b[39m"; got != want {
			t.Fatalf("highlight token %v = %q, want %q", test.token, got, want)
		}
	}
}

func TestUnknownThemeTokenPanics(t *testing.T) {
	registry := Load(LoadOptions{AgentDir: t.TempDir(), CWD: t.TempDir(), Mode: TrueColor})
	dark, _ := registry.Get("dark")
	defer func() {
		if recover() == nil {
			t.Fatal("unknown foreground token did not panic")
		}
	}()
	_ = dark.Foreground("unknown-token", "value")
}

func writeTestTheme(t *testing.T, path, name, accent string) {
	t.Helper()
	encoded := mustThemeJSON(t, name, accent)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustThemeJSON(t *testing.T, name, accent string) []byte {
	t.Helper()
	data, err := builtins.ReadFile("dark.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["name"] = name
	document["colors"].(map[string]any)["accent"] = accent
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertSelectedTheme(t *testing.T, registry *Registry, name, sourcePath string) {
	t.Helper()
	selected, ok := registry.Get(name)
	if !ok || selected.SourcePath != sourcePath {
		t.Fatalf("selected %q = %#v, want source %q", name, selected, sourcePath)
	}
}

func ThemeForRGB(r, g, b int) TerminalTheme {
	if luminance(r, g, b) >= .5 {
		return Light
	}
	return Dark
}
