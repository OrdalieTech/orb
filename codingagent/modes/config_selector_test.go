package modes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/tui"
)

func selectorSettings(t *testing.T, global, project string, trusted bool) (*config.SettingsManager, string, string) {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	cwd := filepath.Join(root, "project")
	for _, directory := range []string{agentDir, filepath.Join(cwd, config.ConfigDirName)} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if global != "" {
		if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(global), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if project != "" {
		if err := os.WriteFile(filepath.Join(cwd, config.ConfigDirName, "settings.json"), []byte(project), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir), config.WithProjectTrusted(trusted))
	if err != nil {
		t.Fatal(err)
	}
	return manager, cwd, agentDir
}

func selectorResource(path string, enabled bool, source, scope, origin, baseDir string) codingagent.ResolvedResource {
	return codingagent.ResolvedResource{
		Path: path, Enabled: enabled,
		Metadata: codingagent.PathMetadata{Source: source, Scope: scope, Origin: origin, BaseDir: baseDir},
	}
}

func selectorEvent(raw string) tui.KeyEvent {
	return tui.KeyEvent{Raw: raw, Key: tui.ParseKey(raw), Type: tui.KeyEventTypeOf(raw)}
}

func renderSelectorText(selector *ConfigSelector, width int) string {
	return strings.Join(selector.Render(width), "\n")
}

func TestConfigSelectorGroupingSortingFilteringAndWidths(t *testing.T) {
	settings, cwd, agentDir := selectorSettings(t, `{}`, `{}`, true)
	packageA := filepath.Join(agentDir, "npm", "a")
	packageZ := filepath.Join(agentDir, "npm", "z")
	projectPackage := filepath.Join(cwd, config.ConfigDirName, "npm", "b")
	resolved := &codingagent.ResolvedPaths{
		Extensions: []codingagent.ResolvedResource{
			selectorResource(filepath.Join(packageZ, "extensions", "zeta.ts"), true, "npm:z-tools", "user", "package", packageZ),
			selectorResource(filepath.Join(packageA, "extensions", "zeta.ts"), true, "npm:a-tools", "user", "package", packageA),
			selectorResource(filepath.Join(packageA, "extensions", "alpha.ts"), true, "npm:a-tools", "user", "package", packageA),
			selectorResource(filepath.Join(agentDir, "extensions", "user.ts"), true, "auto", "user", "top-level", agentDir),
			selectorResource(filepath.Join(cwd, config.ConfigDirName, "extensions", "project.ts"), true, "auto", "project", "top-level", filepath.Join(cwd, config.ConfigDirName)),
		},
		Skills: []codingagent.ResolvedResource{
			selectorResource(filepath.Join(packageA, "skills", "review", "SKILL.md"), true, "npm:a-tools", "user", "package", packageA),
		},
		Prompts: []codingagent.ResolvedResource{
			selectorResource(filepath.Join(projectPackage, "prompts", "review.md"), true, "npm:b-tools", "project", "package", projectPackage),
		},
		Themes: []codingagent.ResolvedResource{
			selectorResource(filepath.Join(packageA, "themes", "dark.json"), true, "npm:a-tools", "user", "package", packageA),
		},
	}
	selector := NewConfigSelector(ConfigSelectorOptions{
		ResolvedPaths: ScopedResolvedPaths{Global: resolved, Project: resolved}, SettingsManager: settings,
		CWD: cwd, AgentDir: agentDir, WriteScope: ConfigWriteProject, ProjectModeAvailable: true, TerminalHeight: 80,
	}, nil, nil, nil)

	rendered := renderSelectorText(selector, 100)
	ordered := []string{"npm:a-tools (user)", "npm:z-tools (user)", "npm:b-tools (project)", "User (", "Project ("}
	previous := -1
	for _, label := range ordered {
		index := strings.Index(rendered, label)
		if index < 0 || index <= previous {
			t.Fatalf("group %q is not in upstream order:\n%s", label, rendered)
		}
		previous = index
	}
	for _, label := range []string{"Extensions", "Skills", "Themes"} {
		index := strings.Index(rendered, label)
		if index < 0 || index <= strings.Index(rendered, "npm:a-tools (user)") {
			t.Fatalf("missing subgroup %q:\n%s", label, rendered)
		}
	}
	if strings.Index(rendered, "alpha.ts") > strings.Index(rendered, "zeta.ts") {
		t.Fatalf("resource names are not sorted:\n%s", rendered)
	}
	for _, width := range []int{18, 31, 64} {
		for lineNumber, line := range selector.Render(width) {
			if got := tui.VisibleWidth(line); got > width {
				t.Fatalf("width %d line %d rendered %d cells: %q", width, lineNumber, got, line)
			}
		}
	}

	selector.HandleInput(selectorEvent("review.md"))
	filtered := renderSelectorText(selector, 80)
	if !strings.Contains(filtered, "review.md") || strings.Contains(filtered, "alpha.ts") || strings.Contains(filtered, "user.ts") {
		t.Fatalf("filter did not retain only matching resources:\n%s", filtered)
	}
	selector.HandleInput(selectorEvent("\x15"))
	if cleared := renderSelectorText(selector, 80); !strings.Contains(cleared, "alpha.ts") {
		t.Fatalf("Ctrl+U did not clear search:\n%s", cleared)
	}
}

func TestConfigSelectorGlobalPackageAndTopLevelToggles(t *testing.T) {
	settings, cwd, agentDir := selectorSettings(t, `{
  "unrelated": {"keep": true},
  "packages": [{"source":"npm:tools","skills":["skills/**"]}],
  "extensions": ["extensions/top.ts"]
}`, "", false)
	packageRoot := filepath.Join(agentDir, "npm", "tools")
	resolved := &codingagent.ResolvedPaths{
		Extensions: []codingagent.ResolvedResource{
			selectorResource(filepath.Join(packageRoot, "extensions", "pkg.ts"), true, "npm:tools", "user", "package", packageRoot),
			selectorResource(filepath.Join(agentDir, "extensions", "top.ts"), true, "auto", "user", "top-level", agentDir),
		},
	}
	selector := NewConfigSelector(ConfigSelectorOptions{
		ResolvedPaths: ScopedResolvedPaths{Global: resolved, Project: resolved}, SettingsManager: settings,
		CWD: cwd, AgentDir: agentDir, WriteScope: ConfigWriteGlobal, TerminalHeight: 30,
	}, nil, nil, nil)

	selector.HandleInput(selectorEvent(" "))
	packages := settings.GetGlobalPackages()
	if len(packages) != 1 || !reflect.DeepEqual(packages[0].Extensions, []string{"-extensions/pkg.ts"}) ||
		!reflect.DeepEqual(packages[0].Skills, []string{"skills/**"}) {
		t.Fatalf("package toggle = %#v", packages)
	}
	selector.HandleInput(selectorEvent(" "))
	if packages = settings.GetGlobalPackages(); !reflect.DeepEqual(packages[0].Extensions, []string{"+extensions/pkg.ts"}) {
		t.Fatalf("second package toggle = %#v", packages)
	}

	selector.HandleInput(selectorEvent("\x1b[B"))
	selector.HandleInput(selectorEvent(" "))
	if got := settings.GetGlobalExtensionPaths(); !reflect.DeepEqual(got, []string{"-extensions/top.ts"}) {
		t.Fatalf("top-level toggle = %#v", got)
	}
	selector.HandleInput(selectorEvent(" "))
	if got := settings.GetGlobalExtensionPaths(); !reflect.DeepEqual(got, []string{"+extensions/top.ts"}) {
		t.Fatalf("second top-level toggle = %#v", got)
	}
	global := settings.GetGlobalSettings()
	unrelated, ok := global["unrelated"].(config.Settings)
	if !ok || unrelated["keep"] != true {
		t.Fatalf("unrelated global field changed: %#v", global)
	}
}

func TestConfigSelectorProjectPackageOverrideCycles(t *testing.T) {
	tests := []struct {
		name      string
		inherited bool
		first     string
		second    string
	}{
		{name: "inherited enabled", inherited: true, first: "-extensions/bar.ts", second: "+extensions/bar.ts"},
		{name: "inherited disabled", inherited: false, first: "+extensions/bar.ts", second: "-extensions/bar.ts"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings, cwd, agentDir := selectorSettings(t, `{"packages":["npm:pi-tools"],"keep":"global"}`, `{"keep":"project"}`, true)
			packageRoot := filepath.Join(agentDir, "npm", "pi-tools")
			resolved := &codingagent.ResolvedPaths{Extensions: []codingagent.ResolvedResource{
				selectorResource(filepath.Join(packageRoot, "extensions", "bar.ts"), test.inherited, "npm:pi-tools", "user", "package", packageRoot),
			}}
			selector := NewConfigSelector(ConfigSelectorOptions{
				ResolvedPaths: ScopedResolvedPaths{Global: resolved, Project: resolved}, SettingsManager: settings,
				CWD: cwd, AgentDir: agentDir, WriteScope: ConfigWriteProject, ProjectModeAvailable: true,
			}, nil, nil, nil)

			for index, want := range []string{test.first, test.second} {
				selector.HandleInput(selectorEvent(" "))
				packages := settings.GetProjectPackages()
				if len(packages) != 1 || packages[0].Autoload == nil || *packages[0].Autoload ||
					!reflect.DeepEqual(packages[0].Extensions, []string{want}) {
					t.Fatalf("cycle %d = %#v, want %q", index, packages, want)
				}
			}
			selector.HandleInput(selectorEvent(" "))
			if packages := settings.GetProjectPackages(); len(packages) != 0 {
				t.Fatalf("third cycle did not restore inherit: %#v", packages)
			}
			if got := settings.GetProjectSettings()["keep"]; got != "project" {
				t.Fatalf("unrelated project field changed: %#v", settings.GetProjectSettings())
			}
		})
	}
}

func TestConfigSelectorProjectLocalPackageOverrideUsesProjectRelativeSource(t *testing.T) {
	settings, cwd, agentDir := selectorSettings(t, `{}`, `{}`, true)
	packageRoot := filepath.Join(filepath.Dir(cwd), "local-package")
	globalSource := relativeConfigPath(agentDir, packageRoot)
	if err := settings.SetPackages([]config.PackageSource{{Source: globalSource}}); err != nil {
		t.Fatal(err)
	}
	resolved := &codingagent.ResolvedPaths{Extensions: []codingagent.ResolvedResource{
		selectorResource(filepath.Join(packageRoot, "extensions", "local.ts"), true, globalSource, "user", "package", packageRoot),
	}}
	selector := NewConfigSelector(ConfigSelectorOptions{
		ResolvedPaths: ScopedResolvedPaths{Global: resolved, Project: resolved}, SettingsManager: settings,
		CWD: cwd, AgentDir: agentDir, WriteScope: ConfigWriteProject, ProjectModeAvailable: true,
	}, nil, nil, nil)
	selector.HandleInput(selectorEvent(" "))
	packages := settings.GetProjectPackages()
	wantSource := relativeConfigPath(filepath.Join(cwd, config.ConfigDirName), packageRoot)
	if len(packages) != 1 || packages[0].Source != wantSource || packages[0].Autoload == nil || *packages[0].Autoload ||
		!reflect.DeepEqual(packages[0].Extensions, []string{"-extensions/local.ts"}) {
		t.Fatalf("local project override = %#v, want source %q", packages, wantSource)
	}
}

func TestConfigSelectorProjectTopLevelOverrideCycleAndDimming(t *testing.T) {
	settings, cwd, agentDir := selectorSettings(t, `{"extensions":["extensions/global.ts"]}`, `{}`, true)
	path := filepath.Join(agentDir, "extensions", "global.ts")
	resolved := &codingagent.ResolvedPaths{Extensions: []codingagent.ResolvedResource{
		selectorResource(path, true, "auto", "user", "top-level", agentDir),
	}}
	selector := NewConfigSelector(ConfigSelectorOptions{
		ResolvedPaths: ScopedResolvedPaths{Global: resolved, Project: resolved}, SettingsManager: settings,
		CWD: cwd, AgentDir: agentDir, WriteScope: ConfigWriteProject, ProjectModeAvailable: true,
	}, nil, nil, nil)
	if rendered := renderSelectorText(selector, 100); !strings.Contains(rendered, "inherited global") || !strings.Contains(rendered, "[x]") {
		t.Fatalf("inherited item is not rendered dimmed with its effective state:\n%s", rendered)
	}

	selector.HandleInput(selectorEvent(" "))
	if got := settings.GetProjectExtensionPaths(); !reflect.DeepEqual(got, []string{path, "-" + path}) {
		t.Fatalf("unload override = %#v", got)
	}
	selector.HandleInput(selectorEvent(" "))
	if got := settings.GetProjectExtensionPaths(); !reflect.DeepEqual(got, []string{path, "+" + path}) {
		t.Fatalf("load override = %#v", got)
	}
	selector.HandleInput(selectorEvent(" "))
	if got := settings.GetProjectExtensionPaths(); len(got) != 0 {
		t.Fatalf("inherit override = %#v", got)
	}
}

func TestConfigSelectorScopeSwitchCloseAndExit(t *testing.T) {
	settings, cwd, agentDir := selectorSettings(t, `{}`, `{}`, true)
	closed, exited := 0, 0
	selector := NewConfigSelector(ConfigSelectorOptions{
		ResolvedPaths:   ScopedResolvedPaths{Global: &codingagent.ResolvedPaths{}, Project: &codingagent.ResolvedPaths{}},
		SettingsManager: settings, CWD: cwd, AgentDir: agentDir, ProjectModeAvailable: true,
	}, func() { closed++ }, func() { exited++ }, nil)
	selector.HandleInput(selectorEvent("\t"))
	if selector.WriteScope() != ConfigWriteProject || !strings.Contains(renderSelectorText(selector, 80), "Project Local Resources") {
		t.Fatalf("Tab did not switch to project mode")
	}
	selector.HandleInput(selectorEvent("\t"))
	if selector.WriteScope() != ConfigWriteGlobal {
		t.Fatalf("second Tab did not switch to global mode")
	}
	selector.HandleInput(selectorEvent("\x1b"))
	selector.HandleInput(selectorEvent("\x03"))
	if closed != 1 || exited != 1 {
		t.Fatalf("callbacks close=%d exit=%d", closed, exited)
	}

	globalOnly := NewConfigSelector(ConfigSelectorOptions{
		ResolvedPaths:   ScopedResolvedPaths{Global: &codingagent.ResolvedPaths{}, Project: &codingagent.ResolvedPaths{}},
		SettingsManager: settings, CWD: cwd, AgentDir: agentDir, ProjectModeAvailable: false,
	}, nil, nil, nil)
	globalOnly.HandleInput(selectorEvent("\t"))
	if globalOnly.WriteScope() != ConfigWriteGlobal || strings.Contains(renderSelectorText(globalOnly, 80), "switch mode") {
		t.Fatalf("Tab was available without project trust")
	}
}

func TestConfigSelectorConcurrentRenderAndScopeInput(t *testing.T) {
	settings, cwd, agentDir := selectorSettings(t, `{}`, `{}`, true)
	resolved := &codingagent.ResolvedPaths{Extensions: []codingagent.ResolvedResource{
		selectorResource(filepath.Join(agentDir, "extensions", "a.ts"), true, "auto", "user", "top-level", agentDir),
	}}
	selector := NewConfigSelector(ConfigSelectorOptions{
		ResolvedPaths: ScopedResolvedPaths{Global: resolved, Project: resolved}, SettingsManager: settings,
		CWD: cwd, AgentDir: agentDir, ProjectModeAvailable: true,
	}, nil, nil, nil)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		for range 200 {
			_ = selector.Render(40)
		}
	}()
	for range 200 {
		selector.HandleInput(selectorEvent("\t"))
		selector.HandleInput(selectorEvent("\x1b[B"))
	}
	wait.Wait()
}

type configLifecycleTerminal struct {
	mu       sync.Mutex
	input    string
	startErr error
	stopErr  error
	starts   int
	stops    int
	writes   []string
	columns  int
	rows     int
}

func (terminal *configLifecycleTerminal) Start(onInput func(string), _ func()) error {
	terminal.mu.Lock()
	terminal.starts++
	input, err := terminal.input, terminal.startErr
	terminal.mu.Unlock()
	if err == nil && input != "" {
		onInput(input)
	}
	return err
}

func (terminal *configLifecycleTerminal) Stop() error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.stops++
	return terminal.stopErr
}
func (terminal *configLifecycleTerminal) DrainInput(_, _ time.Duration) {}
func (terminal *configLifecycleTerminal) Write(value string) {
	terminal.mu.Lock()
	terminal.writes = append(terminal.writes, value)
	terminal.mu.Unlock()
}
func (terminal *configLifecycleTerminal) Columns() int              { return terminal.columns }
func (terminal *configLifecycleTerminal) Rows() int                 { return terminal.rows }
func (terminal *configLifecycleTerminal) KittyProtocolActive() bool { return false }
func (terminal *configLifecycleTerminal) MoveBy(int)                {}
func (terminal *configLifecycleTerminal) HideCursor()               {}
func (terminal *configLifecycleTerminal) ShowCursor()               {}
func (terminal *configLifecycleTerminal) ClearLine()                {}
func (terminal *configLifecycleTerminal) ClearFromCursor()          {}
func (terminal *configLifecycleTerminal) ClearScreen()              {}
func (terminal *configLifecycleTerminal) SetTitle(string)           {}
func (terminal *configLifecycleTerminal) SetProgress(bool)          {}

func TestRunConfigSelectorTerminalLifecycle(t *testing.T) {
	settings, cwd, agentDir := selectorSettings(t, `{}`, `{}`, true)
	options := ConfigSelectorOptions{
		ResolvedPaths:   ScopedResolvedPaths{Global: &codingagent.ResolvedPaths{}, Project: &codingagent.ResolvedPaths{}},
		SettingsManager: settings, CWD: cwd, AgentDir: agentDir, ProjectModeAvailable: true,
	}
	terminal := &configLifecycleTerminal{input: "\x1b", columns: 80, rows: 24}
	if err := RunConfigSelectorWithTerminal(context.Background(), options, terminal); err != nil {
		t.Fatal(err)
	}
	terminal.mu.Lock()
	starts, stops := terminal.starts, terminal.stops
	terminal.mu.Unlock()
	if starts != 1 || stops != 1 {
		t.Fatalf("terminal starts=%d stops=%d", starts, stops)
	}

	stopFailure := errors.New("restore failed")
	terminal = &configLifecycleTerminal{input: "\x03", stopErr: stopFailure, columns: 80, rows: 24}
	if err := RunConfigSelectorWithTerminal(context.Background(), options, terminal); !errors.Is(err, stopFailure) {
		t.Fatalf("stop error = %v", err)
	}

	startFailure := errors.New("raw mode failed")
	terminal = &configLifecycleTerminal{startErr: startFailure, columns: 80, rows: 24}
	if err := RunConfigSelectorWithTerminal(context.Background(), options, terminal); !errors.Is(err, startFailure) {
		t.Fatalf("start error = %v", err)
	}
	terminal.mu.Lock()
	starts, stops = terminal.starts, terminal.stops
	terminal.mu.Unlock()
	if starts != 1 || stops != 0 {
		t.Fatalf("failed start lifecycle starts=%d stops=%d", starts, stops)
	}
}
