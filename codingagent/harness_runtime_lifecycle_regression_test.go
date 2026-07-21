package codingagent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

func TestHarnessBackedReplacementHooksCanCancelBeforeReplacementErrors(t *testing.T) {
	tests := []struct {
		name      string
		wantEvent string
		run       func(*AgentSessionRuntime, string) (bool, error)
	}{
		{
			name: "new", wantEvent: "switch:new",
			run: func(runtime *AgentSessionRuntime, _ string) (bool, error) {
				result, err := runtime.NewSession(context.Background(), nil)
				return result.Cancelled, err
			},
		},
		{
			name: "resume", wantEvent: "switch:resume",
			run: func(runtime *AgentSessionRuntime, _ string) (bool, error) {
				result, err := runtime.SwitchSession(context.Background(), filepath.Join(t.TempDir(), "missing.jsonl"), nil)
				return result.Cancelled, err
			},
		},
		{
			name: "fork", wantEvent: "fork:missing-entry",
			run: func(runtime *AgentSessionRuntime, _ string) (bool, error) {
				result, err := runtime.Fork(context.Background(), "missing-entry", nil)
				return result.Cancelled, err
			},
		},
		{
			name: "import", wantEvent: "switch:resume",
			run: func(runtime *AgentSessionRuntime, importPath string) (bool, error) {
				result, err := runtime.ImportFromJSONL(context.Background(), importPath, "")
				return result.Cancelled, err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := make([]string, 0, 1)
			runtime, importPath := newCancellableHarnessRuntime(t, &events)
			cancelled, err := test.run(runtime, importPath)
			if err != nil || !cancelled {
				t.Fatalf("cancelled = %v, error = %v", cancelled, err)
			}
			if got := events; !reflect.DeepEqual(got, []string{test.wantEvent}) {
				t.Fatalf("lifecycle events = %v, want [%s]", got, test.wantEvent)
			}
		})
	}
}

func newCancellableHarnessRuntime(t *testing.T, events *[]string) (*AgentSessionRuntime, string) {
	t.Helper()
	cwd := t.TempDir()
	env := harness.NodeExecutionEnv{CWD: cwd}
	t.Cleanup(func() { _ = env.Cleanup() })
	repo := harness.NewJSONLSessionRepo(env, filepath.Join(cwd, "sessions"))
	stored, err := repo.Create(context.Background(), harness.SessionCreateOptions{ID: "runtime", CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(stored.Storage())
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<harness-cancel>", func(api extensions.API) error {
		api.On(extensions.EventSessionBeforeSwitch, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionBeforeSwitchEvent)
			*events = append(*events, "switch:"+string(event.Reason))
			return extensions.SessionBeforeSwitchResult{Cancel: true}, nil
		})
		api.On(extensions.EventSessionBeforeFork, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionBeforeForkEvent)
			*events = append(*events, "fork:"+event.EntryID)
			return extensions.SessionBeforeForkResult{Cancel: true}, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	provider := harnessRegressionFaux()
	runtime, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, ExtensionRegistry: registry,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Dispose(context.Background()) })
	if err := runtime.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}

	importPath := filepath.Join(t.TempDir(), "import.jsonl")
	if err := os.WriteFile(importPath, []byte(
		`{"type":"session","version":3,"id":"import","timestamp":"2026-07-18T00:00:00.000Z","cwd":"`+cwd+`"}`+"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	return runtime, importPath
}
