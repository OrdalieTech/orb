package runner_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type wp360FileSpec struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type wp360SymlinkSpec struct {
	Link   string `json:"link"`
	Target string `json:"target"`
}

type wp360GitSource struct {
	Repo   string `json:"repo"`
	Host   string `json:"host"`
	Path   string `json:"path"`
	Ref    string `json:"ref,omitempty"`
	Pinned bool   `json:"pinned"`
}

type wp360Resource struct {
	Path     string `json:"path"`
	Enabled  bool   `json:"enabled"`
	Metadata struct {
		Source  string `json:"source"`
		Scope   string `json:"scope"`
		Origin  string `json:"origin"`
		BaseDir string `json:"baseDir,omitempty"`
	} `json:"metadata"`
}

type wp360SettingsOp struct {
	Op     string `json:"op"`
	Source string `json:"source"`
	Local  bool   `json:"local"`
}

type wp360TrustUpdate struct {
	Path     string `json:"path"`
	Decision *bool  `json:"decision"`
}

type wp360Fixture struct {
	SchemaVersion int `json:"schemaVersion"`
	GitURLCases   []struct {
		Input    string          `json:"input"`
		Expected *wp360GitSource `json:"expected"`
	} `json:"gitUrlCases"`
	ResolveCases []struct {
		Name            string             `json:"name"`
		Files           []wp360FileSpec    `json:"files"`
		Dirs            []string           `json:"dirs"`
		Symlinks        []wp360SymlinkSpec `json:"symlinks"`
		GlobalSettings  json.RawMessage    `json:"globalSettings"`
		ProjectSettings json.RawMessage    `json:"projectSettings"`
		ProjectTrusted  bool               `json:"projectTrusted"`
		Expected        map[string][]wp360Resource
	} `json:"resolveCases"`
	SettingsCases []struct {
		Name           string            `json:"name"`
		Dirs           []string          `json:"dirs"`
		InitialGlobal  json.RawMessage   `json:"initialGlobal"`
		InitialProject json.RawMessage   `json:"initialProject"`
		Ops            []wp360SettingsOp `json:"ops"`
		Expected       struct {
			Changed         []bool            `json:"changed"`
			GlobalPackages  []json.RawMessage `json:"globalPackages"`
			ProjectPackages []json.RawMessage `json:"projectPackages"`
		} `json:"expected"`
	} `json:"settingsCases"`
	TrustOptionCases []struct {
		Name               string   `json:"name"`
		CWD                string   `json:"cwd"`
		Dirs               []string `json:"dirs"`
		IncludeSessionOnly bool     `json:"includeSessionOnly"`
		Expected           []struct {
			Label     string             `json:"label"`
			Trusted   bool               `json:"trusted"`
			Updates   []wp360TrustUpdate `json:"updates"`
			SavedPath string             `json:"savedPath"`
		} `json:"expected"`
	} `json:"trustOptionCases"`
	TrustStoreCases []struct {
		Name     string               `json:"name"`
		Dirs     []string             `json:"dirs"`
		Ops      [][]wp360TrustUpdate `json:"ops"`
		Queries  []string             `json:"queries"`
		Expected struct {
			Decisions []*bool `json:"decisions"`
			File      string  `json:"file"`
		} `json:"expected"`
	} `json:"trustStoreCases"`
	HasTrustRequiringCases []struct {
		Name     string          `json:"name"`
		Files    []wp360FileSpec `json:"files"`
		Dirs     []string        `json:"dirs"`
		CWD      string          `json:"cwd"`
		Expected bool            `json:"expected"`
	} `json:"hasTrustRequiringCases"`
}

func wp360CaseRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	return root
}

func wp360WriteTree(t *testing.T, root string, files []wp360FileSpec, dirs []string, symlinks []wp360SymlinkSpec) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range files {
		target := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(file.Content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, link := range symlinks {
		linkPath := filepath.Join(root, filepath.FromSlash(link.Link))
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, filepath.FromSlash(link.Target)), linkPath); err != nil {
			t.Fatal(err)
		}
	}
}

func wp360Relativize(value, root string) string {
	return strings.ReplaceAll(value, root, "<fixture>")
}

func TestWP360GitURLParsing(t *testing.T) {
	var fixture wp360Fixture
	runner.LoadJSON(t, "WP360", "cases.json", &fixture)
	if len(fixture.GitURLCases) == 0 {
		t.Fatal("no git URL cases")
	}
	for _, testCase := range fixture.GitURLCases {
		parsed := codingagent.ParseGitURL(testCase.Input)
		if testCase.Expected == nil {
			if parsed != nil {
				t.Errorf("ParseGitURL(%q) = %+v, want nil", testCase.Input, parsed)
			}
			continue
		}
		if parsed == nil {
			t.Errorf("ParseGitURL(%q) = nil, want %+v", testCase.Input, testCase.Expected)
			continue
		}
		got := wp360GitSource{Repo: parsed.Repo, Host: parsed.Host, Path: parsed.Path, Ref: parsed.Ref, Pinned: parsed.Pinned}
		if got != *testCase.Expected {
			t.Errorf("ParseGitURL(%q) = %+v, want %+v", testCase.Input, got, *testCase.Expected)
		}
	}
}

func wp360SettingsFile(t *testing.T, path string, raw json.RawMessage) {
	t.Helper()
	contents := []byte("{}")
	if len(raw) > 0 {
		contents = raw
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWP360Resolve(t *testing.T) {
	var fixture wp360Fixture
	runner.LoadJSON(t, "WP360", "cases.json", &fixture)
	t.Setenv("PI_OFFLINE", "1")
	for _, testCase := range fixture.ResolveCases {
		t.Run(testCase.Name, func(t *testing.T) {
			root := wp360CaseRoot(t)
			t.Setenv("HOME", filepath.Join(root, "home"))
			if err := os.MkdirAll(filepath.Join(root, "home"), 0o755); err != nil {
				t.Fatal(err)
			}
			agentDir := filepath.Join(root, "agent")
			cwd := filepath.Join(root, "project")
			if strings.HasPrefix(testCase.Name, "auto-discovery") {
				cwd = filepath.Join(root, "parent", "project")
			}
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatal(err)
			}
			wp360WriteTree(t, root, testCase.Files, testCase.Dirs, testCase.Symlinks)
			wp360SettingsFile(t, filepath.Join(agentDir, "settings.json"), testCase.GlobalSettings)
			wp360SettingsFile(t, filepath.Join(cwd, config.ConfigDirName, "settings.json"), testCase.ProjectSettings)

			settings, err := config.NewSettingsManager(cwd,
				config.WithAgentDir(agentDir), config.WithProjectTrusted(testCase.ProjectTrusted))
			if err != nil {
				t.Fatal(err)
			}
			manager := codingagent.NewPackageManager(codingagent.PackageManagerOptions{
				CWD: cwd, AgentDir: agentDir, Settings: settings,
			})
			resolved, err := manager.Resolve(func(string) (codingagent.MissingSourceAction, error) {
				return codingagent.MissingSourceSkip, nil
			})
			if err != nil {
				t.Fatal(err)
			}

			actual := map[string][]wp360Resource{
				"extensions": wp360NormalizeResources(resolved.Extensions, root),
				"skills":     wp360NormalizeResources(resolved.Skills, root),
				"prompts":    wp360NormalizeResources(resolved.Prompts, root),
				"themes":     wp360NormalizeResources(resolved.Themes, root),
			}
			for _, resourceType := range []string{"extensions", "skills", "prompts", "themes"} {
				expected := testCase.Expected[resourceType]
				if expected == nil {
					expected = []wp360Resource{}
				}
				if !reflect.DeepEqual(actual[resourceType], expected) {
					t.Errorf("%s mismatch\n got: %s\nwant: %s", resourceType,
						wp360JSON(t, actual[resourceType]), wp360JSON(t, expected))
				}
			}
		})
	}
}

func wp360NormalizeResources(resources []codingagent.ResolvedResource, root string) []wp360Resource {
	normalized := make([]wp360Resource, 0, len(resources))
	for _, resource := range resources {
		entry := wp360Resource{Path: wp360Relativize(resource.Path, root), Enabled: resource.Enabled}
		entry.Metadata.Source = wp360Relativize(resource.Metadata.Source, root)
		entry.Metadata.Scope = resource.Metadata.Scope
		entry.Metadata.Origin = resource.Metadata.Origin
		entry.Metadata.BaseDir = wp360Relativize(resource.Metadata.BaseDir, root)
		normalized = append(normalized, entry)
	}
	sort.Slice(normalized, func(a, b int) bool { return normalized[a].Path < normalized[b].Path })
	return normalized
}

func wp360JSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestWP360SettingsMutations(t *testing.T) {
	var fixture wp360Fixture
	runner.LoadJSON(t, "WP360", "cases.json", &fixture)
	for _, testCase := range fixture.SettingsCases {
		t.Run(testCase.Name, func(t *testing.T) {
			root := wp360CaseRoot(t)
			t.Setenv("HOME", filepath.Join(root, "home"))
			agentDir := filepath.Join(root, "agent")
			cwd := filepath.Join(root, "project")
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatal(err)
			}
			wp360WriteTree(t, root, nil, testCase.Dirs, nil)
			wp360SettingsFile(t, filepath.Join(agentDir, "settings.json"), testCase.InitialGlobal)
			wp360SettingsFile(t, filepath.Join(cwd, config.ConfigDirName, "settings.json"), testCase.InitialProject)

			settings, err := config.NewSettingsManager(cwd,
				config.WithAgentDir(agentDir), config.WithProjectTrusted(true))
			if err != nil {
				t.Fatal(err)
			}
			manager := codingagent.NewPackageManager(codingagent.PackageManagerOptions{
				CWD: cwd, AgentDir: agentDir, Settings: settings,
			})

			changed := make([]bool, 0, len(testCase.Ops))
			for _, op := range testCase.Ops {
				var didChange bool
				var opErr error
				if op.Op == "add" {
					didChange, opErr = manager.AddSourceToSettings(op.Source, op.Local)
				} else {
					didChange, opErr = manager.RemoveSourceFromSettings(op.Source, op.Local)
				}
				if opErr != nil {
					t.Fatalf("op %+v: %v", op, opErr)
				}
				changed = append(changed, didChange)
			}
			if !reflect.DeepEqual(changed, testCase.Expected.Changed) {
				t.Errorf("changed = %v, want %v", changed, testCase.Expected.Changed)
			}

			compareRawPackages(t, "global", settings.GetGlobalSettings()["packages"], testCase.Expected.GlobalPackages)
			compareRawPackages(t, "project", settings.GetProjectSettings()["packages"], testCase.Expected.ProjectPackages)
		})
	}
}

func compareRawPackages(t *testing.T, scope string, actual any, expected []json.RawMessage) {
	t.Helper()
	actualJSON, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	if actual == nil {
		actualJSON = []byte("[]")
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	canonicalActual, err := runner.CanonicalJSON(actualJSON)
	if err != nil {
		t.Fatal(err)
	}
	canonicalExpected, err := runner.CanonicalJSON(expectedJSON)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonicalActual) != string(canonicalExpected) {
		t.Errorf("%s packages = %s, want %s", scope, canonicalActual, canonicalExpected)
	}
}

func TestWP360TrustOptions(t *testing.T) {
	var fixture wp360Fixture
	runner.LoadJSON(t, "WP360", "cases.json", &fixture)
	for _, testCase := range fixture.TrustOptionCases {
		t.Run(testCase.Name, func(t *testing.T) {
			root := wp360CaseRoot(t)
			wp360WriteTree(t, root, nil, testCase.Dirs, nil)
			options := config.GetProjectTrustOptions(filepath.Join(root, filepath.FromSlash(testCase.CWD)), testCase.IncludeSessionOnly)
			if len(options) != len(testCase.Expected) {
				t.Fatalf("options = %d, want %d", len(options), len(testCase.Expected))
			}
			for index, expected := range testCase.Expected {
				option := options[index]
				if wp360Relativize(option.Label, root) != expected.Label || option.Trusted != expected.Trusted ||
					wp360Relativize(option.SavedPath, root) != expected.SavedPath {
					t.Errorf("option[%d] = %+v, want %+v", index, option, expected)
				}
				if len(option.Updates) != len(expected.Updates) {
					t.Errorf("option[%d] updates = %+v, want %+v", index, option.Updates, expected.Updates)
					continue
				}
				for updateIndex, expectedUpdate := range expected.Updates {
					update := option.Updates[updateIndex]
					if wp360Relativize(update.Path, root) != expectedUpdate.Path ||
						!reflect.DeepEqual(update.Decision, expectedUpdate.Decision) {
						t.Errorf("option[%d].updates[%d] = %+v, want %+v", index, updateIndex, update, expectedUpdate)
					}
				}
			}
		})
	}
}

func TestWP360TrustStore(t *testing.T) {
	var fixture wp360Fixture
	runner.LoadJSON(t, "WP360", "cases.json", &fixture)
	for _, testCase := range fixture.TrustStoreCases {
		t.Run(testCase.Name, func(t *testing.T) {
			root := wp360CaseRoot(t)
			agentDir := filepath.Join(root, "agent")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatal(err)
			}
			wp360WriteTree(t, root, nil, testCase.Dirs, nil)
			store := config.NewProjectTrustStore(agentDir)
			for _, op := range testCase.Ops {
				updates := make([]config.ProjectTrustUpdate, 0, len(op))
				for _, update := range op {
					updates = append(updates, config.ProjectTrustUpdate{
						Path:     filepath.Join(root, filepath.FromSlash(update.Path)),
						Decision: update.Decision,
					})
				}
				if err := store.SetMany(updates); err != nil {
					t.Fatal(err)
				}
			}
			for index, query := range testCase.Queries {
				decision, err := store.Get(filepath.Join(root, filepath.FromSlash(query)))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(decision, testCase.Expected.Decisions[index]) {
					t.Errorf("query %q decision = %v, want %v", query, decision, testCase.Expected.Decisions[index])
				}
			}
			contents, err := os.ReadFile(filepath.Join(agentDir, "trust.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := wp360Relativize(string(contents), root); got != testCase.Expected.File {
				t.Errorf("trust.json mismatch:\n%s", runner.ByteDiff([]byte(testCase.Expected.File), []byte(got)))
			}
		})
	}
}

func TestWP360HasTrustRequiringProjectResources(t *testing.T) {
	var fixture wp360Fixture
	runner.LoadJSON(t, "WP360", "cases.json", &fixture)
	for _, testCase := range fixture.HasTrustRequiringCases {
		t.Run(testCase.Name, func(t *testing.T) {
			root := wp360CaseRoot(t)
			t.Setenv("HOME", filepath.Join(root, "home"))
			if err := os.MkdirAll(filepath.Join(root, "home"), 0o755); err != nil {
				t.Fatal(err)
			}
			wp360WriteTree(t, root, testCase.Files, testCase.Dirs, nil)
			cwd := filepath.Join(root, filepath.FromSlash(testCase.CWD))
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatal(err)
			}
			if got := config.HasTrustRequiringProjectResources(cwd); got != testCase.Expected {
				t.Errorf("HasTrustRequiringProjectResources = %v, want %v", got, testCase.Expected)
			}
		})
	}
}
