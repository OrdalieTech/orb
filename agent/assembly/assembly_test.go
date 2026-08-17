package assembly_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/agent/assembly"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
)

func newTestSettings(t *testing.T, settingsJSON string) (*config.SettingsManager, string, string) {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if settingsJSON != "" {
		if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	return settings, root, agentDir
}

func noopFactory(extensions.API) error { return nil }

func compiled(names ...string) []extensions.CompiledExtension {
	entries := make([]extensions.CompiledExtension, 0, len(names))
	for _, name := range names {
		entries = append(entries, extensions.CompiledExtension{Name: name, Factory: noopFactory, DefaultEnabled: true})
	}
	return entries
}

func rowIDs(rows []assembly.Row) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func TestRowsEnumerateInBootOrder(t *testing.T) {
	settings, root, agentDir := newTestSettings(t, "")
	rows, warnings := assembly.Rows(assembly.Options{
		CWD: root, AgentDir: agentDir, Settings: settings,
		Compiled: compiled("alpha", "beta"), MCP: true,
	})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	want := []string{"alpha", "beta", "plugin-control", "tasks", "websearch", "subagents", "permissions", "memory"}
	got := rowIDs(rows)
	if len(got) != len(want) {
		t.Fatalf("row ids = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("row ids = %v, want %v", got, want)
		}
	}
}

func TestRowsIncludeConfiguredMCPServers(t *testing.T) {
	settings, root, agentDir := newTestSettings(t, `{"mcpServers":{"probe":{"command":"true"}}}`)
	rows, warnings := assembly.Rows(assembly.Options{
		CWD: root, AgentDir: agentDir, Settings: settings, MCP: true,
	})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	last := rows[len(rows)-1]
	if last.ID != "mcp" || last.Source != assembly.SourceMCP || !last.Hidden || !last.DefaultEnabled {
		t.Fatalf("mcp row = %+v", last)
	}
	// Metadata-only assemblies must not touch MCP configuration.
	rows, _ = assembly.Rows(assembly.Options{CWD: root, AgentDir: agentDir, Settings: settings, MCP: false})
	if got := rowIDs(rows); got[len(got)-1] == "mcp" {
		t.Fatalf("MCP row present with MCP disabled: %v", got)
	}
}

func TestResolvePrecedence(t *testing.T) {
	settings, _, _ := newTestSettings(t, `{
		"goExtensions": {"alpha": false, "plugin-control": false},
		"plugins": {"tasks": true}
	}`)
	rows := []assembly.Row{
		{ID: "alpha", Source: assembly.SourceCompiled, DefaultEnabled: true, Factory: noopFactory},
		{ID: "plugin-control", Source: assembly.SourcePlugin, Hidden: true, DefaultEnabled: true, Factory: noopFactory},
		{ID: "tasks", Source: assembly.SourcePlugin, Factory: noopFactory},
		{ID: "websearch", Source: assembly.SourcePlugin, Factory: noopFactory},
		{ID: "mcp", Source: assembly.SourceMCP, Hidden: true, DefaultEnabled: true, Factory: noopFactory},
	}
	resolved := assembly.Resolve(rows, settings, false)
	expect := map[string]struct {
		enabled   bool
		decidedBy string
	}{
		"alpha":          {false, "goExtensions"},
		"plugin-control": {true, "always"},
		"tasks":          {true, "plugins"},
		"websearch":      {false, "plugins"},
		"mcp":            {true, "default"},
	}
	for _, entry := range resolved {
		want := expect[entry.ID]
		if entry.Enabled != want.enabled || entry.DecidedBy != want.decidedBy {
			t.Errorf("%s = enabled:%v decidedBy:%s, want enabled:%v decidedBy:%s",
				entry.ID, entry.Enabled, entry.DecidedBy, want.enabled, want.decidedBy)
		}
	}
	for _, entry := range assembly.Resolve(rows, settings, true) {
		if entry.Enabled || entry.DecidedBy != "--no-extensions" {
			t.Errorf("disableAll left %s enabled:%v decidedBy:%s", entry.ID, entry.Enabled, entry.DecidedBy)
		}
	}
}

func TestLoadPreservesLoadCompiledSemantics(t *testing.T) {
	settings, root, agentDir := newTestSettings(t, `{"plugins":{"tasks":true}}`)
	rows, _ := assembly.Rows(assembly.Options{
		CWD: root, AgentDir: agentDir, Settings: settings, Compiled: compiled("alpha"),
	})
	registry, loadErrors := assembly.Load(root, assembly.Resolve(rows, settings, false))
	if len(loadErrors) != 0 {
		t.Fatalf("load errors = %v", loadErrors)
	}
	if registry == nil || !registry.HasPath("<inline:alpha>") || !registry.HasPath("<inline:tasks>") ||
		!registry.HasPath("<inline:plugin-control>") || registry.HasPath("<inline:websearch>") {
		t.Fatalf("registry state unexpected (nil=%v)", registry == nil)
	}
	if registry, _ := assembly.Load(root, assembly.Resolve(rows, settings, true)); registry != nil {
		t.Fatalf("--no-extensions produced a non-nil registry")
	}
}

func TestConcurrentAssembliesAreIndependent(t *testing.T) {
	settingsOn, rootOn, agentDirOn := newTestSettings(t, `{"plugins":{"tasks":true}}`)
	settingsOff, rootOff, agentDirOff := newTestSettings(t, `{}`)
	var wg sync.WaitGroup
	run := func(settings *config.SettingsManager, root, agentDir string, wantTasks bool) {
		defer wg.Done()
		for range 25 {
			rows, _ := assembly.Rows(assembly.Options{
				CWD: root, AgentDir: agentDir, Settings: settings, Compiled: compiled("alpha"),
			})
			registry, loadErrors := assembly.Load(root, assembly.Resolve(rows, settings, false))
			if len(loadErrors) != 0 || registry == nil {
				t.Errorf("load = registry nil:%v errors:%v", registry == nil, loadErrors)
				return
			}
			if registry.HasPath("<inline:tasks>") != wantTasks {
				t.Errorf("tasks enabled = %v, want %v", !wantTasks, wantTasks)
				return
			}
		}
	}
	wg.Add(2)
	go run(settingsOn, rootOn, agentDirOn, true)
	go run(settingsOff, rootOff, agentDirOff, false)
	wg.Wait()
}
