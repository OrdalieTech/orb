package theme

import (
	"embed"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/internal/themefile"
)

//go:embed dark.json light.json
var builtins embed.FS

type (
	Diagnostic = themefile.Diagnostic
	Collision  = themefile.Collision
)

type LoadOptions struct {
	CWD                  string
	AgentDir             string
	ProjectTrusted       bool
	NoThemes             bool
	Mode                 ColorMode
	GlobalPaths          []string
	ProjectPaths         []string
	PackagePaths         []string
	AdditionalPaths      []string
	ResourceDiscoverPath []string
}

type Registry struct {
	mu                   sync.RWMutex
	mode                 ColorMode
	cwd                  string
	builtins             map[string]*Theme
	themes               map[string]*Theme
	themeOrder           []string
	diagnostics          []Diagnostic
	collisionDiagnostics []Diagnostic
	loader               themefile.Loader
}

func Load(options LoadOptions) *Registry {
	mode := options.Mode
	if mode == "" {
		mode = DetectColorMode(nil)
	}
	cwd := themefile.CleanPath(options.CWD)
	registry := &Registry{mode: mode, cwd: cwd, builtins: map[string]*Theme{}, themes: map[string]*Theme{}}
	for _, name := range []string{"dark", "light"} {
		data, err := builtins.ReadFile(name + ".json")
		if err != nil {
			registry.diagnostics = append(registry.diagnostics, Diagnostic{Type: "error", Path: name, Message: err.Error()})
			continue
		}
		theme, err := Parse(name, data, mode)
		if err != nil {
			registry.diagnostics = append(registry.diagnostics, Diagnostic{Type: "error", Path: name, Message: err.Error()})
			continue
		}
		registry.builtins[name] = theme
	}
	registry.builtins["terminal"] = terminalTheme(mode)
	if options.NoThemes {
		registry.loadPaths(themefile.ResolvePaths(options.AdditionalPaths, cwd))
		registry.loadPaths(themefile.ResolvePaths(options.ResourceDiscoverPath, cwd))
		return registry
	}
	agentDir := themefile.CleanPath(options.AgentDir)
	if options.ProjectTrusted && cwd != "" {
		projectDir := filepath.Join(cwd, ".pi")
		registry.loadPaths(themefile.ResolvePaths(options.ProjectPaths, projectDir))
		registry.loadDefaultDirectory(filepath.Join(projectDir, "themes"))
	}
	registry.loadPaths(themefile.ResolvePaths(options.GlobalPaths, agentDir))
	if agentDir != "" {
		registry.loadDefaultDirectory(filepath.Join(agentDir, "themes"))
	}
	registry.loadPaths(themefile.ResolvePaths(options.PackagePaths, cwd))
	registry.loadPaths(themefile.ResolvePaths(options.AdditionalPaths, cwd))
	registry.loadPaths(themefile.ResolvePaths(options.ResourceDiscoverPath, cwd))
	return registry
}

// Mode is the color mode the registry renders loaded themes in.
func (registry *Registry) Mode() ColorMode { return registry.mode }

func (registry *Registry) Extend(paths []string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.loadPaths(themefile.ResolvePaths(paths, registry.cwd))
}

func (registry *Registry) Register(theme *Theme) error {
	if err := validateRegisteredTheme(theme); err != nil {
		return err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.register(theme)
	return nil
}

// ReplaceLoaded atomically replaces every nonbuiltin theme with the supplied
// loader-owned objects. Validation happens before mutation so a bad override
// leaves the prior registered set intact.
func (registry *Registry) ReplaceLoaded(themes []*Theme) error {
	replacement := make(map[string]*Theme, len(themes))
	order := make([]string, 0, len(themes))
	for _, loaded := range themes {
		if loaded == nil || loaded.Name == "" {
			continue
		}
		if strings.Contains(loaded.Name, "/") {
			return fmt.Errorf("invalid theme name %q", loaded.Name)
		}
		if _, exists := replacement[loaded.Name]; !exists {
			order = append(order, loaded.Name)
		}
		replacement[loaded.Name] = loaded
	}
	registry.mu.Lock()
	registry.themes = replacement
	registry.themeOrder = order
	registry.mu.Unlock()
	return nil
}

func validateRegisteredTheme(theme *Theme) error {
	if theme == nil || theme.Name == "" {
		return errors.New("theme requires a name")
	}
	if strings.Contains(theme.Name, "/") {
		return fmt.Errorf("invalid theme name %q", theme.Name)
	}
	return nil
}

func (registry *Registry) register(theme *Theme) {
	if _, exists := registry.themes[theme.Name]; !exists {
		registry.themeOrder = append(registry.themeOrder, theme.Name)
	}
	registry.themes[theme.Name] = theme
}

func (registry *Registry) Get(name string) (*Theme, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if selected, ok := registry.themes[name]; ok {
		return selected, true
	}
	selected, ok := registry.builtins[name]
	return selected, ok
}

func (registry *Registry) Available() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	names := make([]string, 0, len(registry.themes)+len(registry.builtins))
	seen := make(map[string]bool, len(registry.themes)+len(registry.builtins))
	for name := range registry.themes {
		names = append(names, name)
		seen[name] = true
	}
	for name := range registry.builtins {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Loaded returns non-builtin themes in first-winner path order. ResourceLoader
// exposes these exact objects to interactive mode, matching upstream identity.
func (registry *Registry) Loaded() []*Theme {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]*Theme, 0, len(registry.themeOrder))
	for _, name := range registry.themeOrder {
		if loaded := registry.themes[name]; loaded != nil {
			result = append(result, loaded)
		}
	}
	return result
}

func (registry *Registry) Diagnostics() []Diagnostic {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := append([]Diagnostic(nil), registry.diagnostics...)
	result = append(result, registry.loader.Warnings()...)
	return append(result, registry.collisionDiagnostics...)
}

func (registry *Registry) loadPaths(paths []string) {
	registry.loader.LoadPaths(paths, registry.addFile)
}

func (registry *Registry) loadDefaultDirectory(path string) {
	registry.loader.LoadDefaultDirectory(path, registry.addFile)
}

func (registry *Registry) addFile(file *themefile.Theme) {
	theme := FromFile(file, registry.mode)
	if winner, exists := registry.themes[theme.Name]; exists {
		registry.collisionDiagnostics = append(registry.collisionDiagnostics, themefile.CollisionDiagnostic(theme.Name, winner.SourcePath, theme.SourcePath))
		return
	}
	registry.themes[theme.Name] = theme
	registry.themeOrder = append(registry.themeOrder, theme.Name)
}

func DetectColorMode(environment map[string]string) ColorMode {
	get := func(name string) string {
		if environment != nil {
			return environment[name]
		}
		return os.Getenv(name)
	}
	colorTerm := strings.ToLower(get("COLORTERM"))
	if colorTerm == "truecolor" || colorTerm == "24bit" {
		return TrueColor
	}
	return Color256
}

type TerminalTheme string

const (
	Dark  TerminalTheme = "dark"
	Light TerminalTheme = "light"
)

type Detection struct {
	Theme      TerminalTheme
	Source     string
	Detail     string
	Confidence string
}

func DetectBackground(environment map[string]string) Detection {
	value := ""
	if environment == nil {
		value = os.Getenv("COLORFGBG")
	} else {
		value = environment["COLORFGBG"]
	}
	parts := strings.Split(value, ";")
	for index := len(parts) - 1; index >= 0; index-- {
		color, err := strconv.Atoi(strings.TrimSpace(parts[index]))
		if err == nil && color >= 0 && color <= 255 {
			theme := Dark
			if luminanceHex(themefile.ANSI256ToHex(color)) >= .5 {
				theme = Light
			}
			return Detection{Theme: theme, Source: "COLORFGBG", Detail: fmt.Sprintf("background color index %d", color), Confidence: "high"}
		}
	}
	return Detection{Theme: Dark, Source: "fallback", Detail: "no terminal background hint found", Confidence: "low"}
}

func luminanceHex(value string) float64 {
	r, g, b, err := themefile.ParseHex(value)
	if err != nil {
		return 0
	}
	return luminance(r, g, b)
}

func luminance(r, g, b int) float64 {
	linear := func(channel int) float64 {
		value := float64(channel) / 255
		if value <= .03928 {
			return value / 12.92
		}
		return math.Pow((value+.055)/1.055, 2.4)
	}
	return .2126*linear(r) + .7152*linear(g) + .0722*linear(b)
}
