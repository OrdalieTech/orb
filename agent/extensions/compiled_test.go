package extensions

import (
	"reflect"
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
	if got := NewRunner(registry, RunnerOptions{}).ExtensionPaths(); !reflect.DeepEqual(got, []string{"<inline:second>", "<inline:third>"}) {
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
