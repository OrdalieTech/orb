package extensions

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoadCompiledPreservesCatalogOrderAndSkipsDisabled(t *testing.T) {
	var loaded []string
	catalog := []CompiledExtension{
		{Name: "first", Factory: func(API) error { loaded = append(loaded, "first"); return nil }},
		{Name: "second", DefaultEnabled: true, Factory: func(API) error { loaded = append(loaded, "second"); return nil }},
		{Name: "third", DefaultEnabled: true, Factory: func(API) error { loaded = append(loaded, "third"); return nil }},
	}
	registry, diagnostics := LoadCompiled(t.TempDir(), catalog)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if !reflect.DeepEqual(loaded, []string{"second", "third"}) {
		t.Fatalf("load order = %v", loaded)
	}
	if got := NewRunner(registry, RunnerOptions{}).ExtensionPaths(); !reflect.DeepEqual(got, []string{"builtin:second", "builtin:third"}) {
		t.Fatalf("paths = %v", got)
	}
}

func TestLoadCompiledAllDisabledAvoidsFactoriesAndRegistry(t *testing.T) {
	called := false
	registry, diagnostics := LoadCompiled(t.TempDir(), []CompiledExtension{{
		Name: "dormant", Factory: func(API) error { called = true; return nil },
	}})
	if registry != nil || len(diagnostics) != 0 || called {
		t.Fatalf("registry=%v diagnostics=%v called=%t", registry, diagnostics, called)
	}
}

func TestReplaceableBuiltinStepsAsideForAnExtensionWithTheSameCommand(t *testing.T) {
	registry, _ := LoadCompiled(t.TempDir(), []CompiledExtension{{
		Name: "mcp", DefaultEnabled: true, Replaceable: true,
		Factory: func(api API) error { api.RegisterCommand("mcp", Command{}); return nil },
	}, {
		Name: "tasks", DefaultEnabled: true,
		Factory: func(api API) error { api.RegisterCommand("tasks", Command{}); return nil },
	}})
	if err := registry.Register("/ext/mcp.ts", func(api API) error { api.RegisterCommand("mcp", Command{}); return nil }); err != nil {
		t.Fatal(err)
	}
	warnings := registry.OmitReplaced()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "registers command `/mcp`, so built-in extension `mcp` was not loaded") {
		t.Fatalf("warnings = %q", warnings)
	}
	if got := NewRunner(registry, RunnerOptions{}).ExtensionPaths(); !reflect.DeepEqual(got, []string{"builtin:tasks", "/ext/mcp.ts"}) {
		t.Fatalf("paths = %v", got)
	}
}
