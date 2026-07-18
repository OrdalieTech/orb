package extensions

import (
	"reflect"
	"testing"
)

func TestLoadCompiledPreservesCatalogOrderAndAppliesSettings(t *testing.T) {
	var loaded []string
	catalog := []CompiledExtension{
		{Name: "first", DefaultEnabled: true, Factory: func(API) error { loaded = append(loaded, "first"); return nil }},
		{Name: "second", Factory: func(API) error { loaded = append(loaded, "second"); return nil }},
		{Name: "third", DefaultEnabled: true, Factory: func(API) error { loaded = append(loaded, "third"); return nil }},
	}
	registry, diagnostics := LoadCompiled(t.TempDir(), catalog, map[string]bool{"first": false, "second": true}, false)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	if !reflect.DeepEqual(loaded, []string{"second", "third"}) {
		t.Fatalf("load order = %v", loaded)
	}
	if got := NewRunner(registry, RunnerOptions{}).ExtensionPaths(); !reflect.DeepEqual(got, []string{"<inline:second>", "<inline:third>"}) {
		t.Fatalf("paths = %v", got)
	}
}

func TestLoadCompiledDisableAllAvoidsFactoriesAndRegistry(t *testing.T) {
	called := false
	registry, diagnostics := LoadCompiled(t.TempDir(), []CompiledExtension{{
		Name: "enabled", DefaultEnabled: true, Factory: func(API) error { called = true; return nil },
	}}, nil, true)
	if registry != nil || len(diagnostics) != 0 || called {
		t.Fatalf("registry=%v diagnostics=%v called=%t", registry, diagnostics, called)
	}
}
