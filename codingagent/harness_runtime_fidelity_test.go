package codingagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

func TestRepoBoundHarnessRuntimeSwitchesToRelativeSessionPath(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, repo := newFidelityHarnessRepoRuntime(t, cwd, nil)
	target, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "relative-target", CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	processCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativePath, err := filepath.Rel(processCWD, target.Metadata().Path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(relativePath) {
		t.Fatalf("test target unexpectedly remained absolute: %q", relativePath)
	}

	result, err := runtime.SwitchSession(ctx, relativePath, nil)
	if err != nil || result.Cancelled {
		t.Fatalf("relative repo-backed switch = %#v, %v", result, err)
	}
	metadata, ok := runtime.Session().Manager().HarnessMetadata()
	if !ok {
		t.Fatal("active session is no longer harness-backed")
	}
	if got := metadata.ID; got != "relative-target" {
		t.Fatalf("active session id = %q, want relative-target", got)
	}
}

func TestRepoBoundHarnessRuntimeImportMatchesUpstreamValidationAndHookTarget(t *testing.T) {
	t.Run("missing source fails before session_before_switch", func(t *testing.T) {
		ctx := context.Background()
		cwd := t.TempDir()
		eventCount := 0
		registry := extensions.NewRegistry(cwd)
		if err := registry.Register("<import-order-fidelity>", func(api extensions.API) error {
			api.On(extensions.EventSessionBeforeSwitch, func(context.Context, extensions.Event, extensions.Context) (any, error) {
				eventCount++
				return extensions.SessionBeforeSwitchResult{Cancel: true}, nil
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, registry)

		result, err := runtime.ImportFromJSONL(ctx, filepath.Join(t.TempDir(), "missing.jsonl"), "")
		var notFound *SessionImportFileNotFoundError
		if !errors.As(err, &notFound) {
			t.Errorf("missing import error = %T %v, want SessionImportFileNotFoundError", err, err)
		}
		if result.Cancelled {
			t.Error("missing import was cancelled before source validation")
		}
		if eventCount != 0 {
			t.Errorf("session_before_switch count = %d, want 0 before source validation", eventCount)
		}
	})

	t.Run("hook target is imported destination", func(t *testing.T) {
		ctx := context.Background()
		cwd := t.TempDir()
		var hookTarget string
		registry := extensions.NewRegistry(cwd)
		if err := registry.Register("<import-target-fidelity>", func(api extensions.API) error {
			api.On(extensions.EventSessionBeforeSwitch, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
				event := raw.(extensions.SessionBeforeSwitchEvent)
				if event.TargetSessionFile != nil {
					hookTarget = *event.TargetSessionFile
				}
				return nil, nil
			})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, registry)

		sourcePath := filepath.Join(t.TempDir(), "external.jsonl")
		contents := []byte(`{"type":"session","version":3,"id":"import-target","timestamp":"2026-07-18T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n")
		if err := os.WriteFile(sourcePath, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := runtime.ImportFromJSONL(ctx, sourcePath, "")
		if err != nil || result.Cancelled {
			t.Fatalf("repo-backed import = %#v, %v", result, err)
		}
		destination := runtime.Session().Manager().GetSessionFile()
		if hookTarget != destination {
			t.Fatalf("session_before_switch target = %q, want imported destination %q", hookTarget, destination)
		}
	})
}

func newFidelityHarnessRepoRuntime(
	t *testing.T,
	cwd string,
	registry *extensions.Registry,
) (*AgentSessionRuntime, *harness.JSONLSessionRepo) {
	t.Helper()
	ctx := context.Background()
	env := harness.NodeExecutionEnv{CWD: cwd}
	t.Cleanup(func() { _ = env.Cleanup() })
	repo := harness.NewJSONLSessionRepo(env, filepath.Join(cwd, "sessions"))
	active, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "active", CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(active.Storage(), sessionstore.WithHarnessRepo(repo))
	if err != nil {
		t.Fatal(err)
	}
	provider := harnessRegressionFaux()
	runtime, err := NewAgentSessionRuntime(ctx, AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, ExtensionRegistry: registry,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Dispose(context.Background()) })
	if registry != nil {
		if err := runtime.Session().BindExtensions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return runtime, repo
}
