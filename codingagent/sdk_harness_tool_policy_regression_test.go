package codingagent

import (
	"reflect"
	"testing"

	"github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

func TestCodingSessionResumeKeepsUpstreamDefaultTools(t *testing.T) {
	cwd := t.TempDir()
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendActiveToolsChange([]string{"read"}); err != nil {
		t.Fatal(err)
	}
	provider := harnessRegressionFaux()
	result, err := NewAgentSession(AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	want := []string{"read", "bash", "edit", "write"}
	if got := activeToolNames(result.Session); !reflect.DeepEqual(got, want) {
		t.Fatalf("coding-session resumed tools = %v, want upstream defaults %v", got, want)
	}
}

func TestHarnessBackedToolRestoreHonorsExplicitEmptyTools(t *testing.T) {
	t.Run("implicit tools restore harness state", func(t *testing.T) {
		result := newHarnessBackedToolSession(t, []string{"read", "bash"}, nil)
		defer result.Session.Dispose()
		if got, want := activeToolNames(result.Session), []string{"read", "bash"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("implicitly restored harness tools = %v, want %v", got, want)
		}
	})

	t.Run("stored empty tools remain explicitly empty", func(t *testing.T) {
		result := newHarnessBackedToolSession(t, []string{}, nil)
		defer result.Session.Dispose()
		if got := activeToolNames(result.Session); len(got) != 0 {
			t.Fatalf("stored empty harness tools became defaults: %v", got)
		}
	})

	t.Run("SDK empty tools override harness state", func(t *testing.T) {
		result := newHarnessBackedToolSession(t, []string{"read", "bash"}, []string{})
		defer result.Session.Dispose()
		if got := activeToolNames(result.Session); len(got) != 0 {
			t.Fatalf("explicit empty tools restored harness state: %v", got)
		}
	})
}

func newHarnessBackedToolSession(t *testing.T, storedTools, configuredTools []string) *AgentSessionResult {
	t.Helper()
	cwd := t.TempDir()
	storage, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{{
		Type: "active_tools_change", ID: "tools", Timestamp: "2026-07-18T00:00:01.000Z",
		ActiveToolNames: storedTools,
	}}, harness.SessionMetadata{ID: "harness-tools", CreatedAt: "2026-07-18T00:00:00.000Z", CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(storage)
	if err != nil {
		t.Fatal(err)
	}
	provider := harnessRegressionFaux()
	result, err := NewAgentSession(AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Tools: configuredTools,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func harnessRegressionFaux() *faux.Provider {
	contextWindow, maxTokens := float64(100000), float64(100)
	return faux.New(faux.Options{
		API: "faux", Provider: "faux",
		Models:    []faux.ModelDefinition{{ID: "faux-1", ContextWindow: &contextWindow, MaxTokens: &maxTokens}},
		TokenSize: faux.FixedTokenSize(1000),
	})
}

func activeToolNames(session *AgentSession) []string {
	state := session.State()
	names := make([]string, len(state.Tools))
	for index := range state.Tools {
		names[index] = state.Tools[index].Spec().Name
	}
	return names
}
