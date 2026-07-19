package codingagent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
)

type fakeReadOperations struct {
	reads   atomic.Int64
	content string
}

func (operations *fakeReadOperations) ReadFile(context.Context, string) ([]byte, error) {
	operations.reads.Add(1)
	return []byte(operations.content), nil
}

func (operations *fakeReadOperations) Access(context.Context, string) error {
	return nil
}

func TestNewAgentSessionToolOptionsInjectsReadOperations(t *testing.T) {
	isolateSDKAgentDir(t)
	cwd := t.TempDir()
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	operations := &fakeReadOperations{content: "injected read operations content"}
	result, err := NewAgentSession(AgentSessionOptions{
		CWD:            cwd,
		SessionManager: manager,
		Model:          provider.GetModel(),
		StreamFn:       provider.StreamSimple,
		ToolOptions: &tools.ToolsOptions{
			Read: &tools.ReadToolOptions{Operations: operations},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	runRead := func() string {
		t.Helper()
		var read agent.AgentTool
		for _, candidate := range result.Session.State().Tools {
			if candidate.Spec().Name == "read" {
				read = candidate
				break
			}
		}
		if read == nil {
			t.Fatal("active read tool is missing")
		}
		output, executeErr := read.Execute(context.Background(), "read-ops", map[string]any{
			"path": "does-not-exist-on-disk.txt",
		}, nil)
		if executeErr != nil {
			t.Fatal(executeErr)
		}
		if len(output.Content) != 1 {
			t.Fatalf("read output = %#v", output.Content)
		}
		text, ok := output.Content[0].(*ai.TextContent)
		if !ok {
			t.Fatalf("read output block = %T", output.Content[0])
		}
		return text.Text
	}

	if got := runRead(); !strings.Contains(got, operations.content) {
		t.Fatalf("read output %q does not contain injected content", got)
	}
	if got := operations.reads.Load(); got != 1 {
		t.Fatalf("injected ReadFile calls = %d, want 1", got)
	}

	// Reload rebuilds base tools via the RebuildBaseTools closure; the
	// injected operations must survive the rebuild.
	if err := result.Session.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := runRead(); !strings.Contains(got, operations.content) {
		t.Fatalf("post-reload read output %q does not contain injected content", got)
	}
	if got := operations.reads.Load(); got != 2 {
		t.Fatalf("injected ReadFile calls after reload = %d, want 2", got)
	}
}

func TestBuildBuiltInToolsFillsSettingsDerivedFieldsOnlyWhenUnset(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	autoResize := false
	overrides := &tools.ToolsOptions{
		Read: &tools.ReadToolOptions{AutoResizeImages: &autoResize},
	}
	if _, err := buildBuiltInTools(cwd, settings, overrides); err != nil {
		t.Fatal(err)
	}
	if overrides.Read.AutoResizeImages != &autoResize {
		t.Fatal("caller override struct was mutated")
	}
	if *overrides.Read.AutoResizeImages != false {
		t.Fatal("explicit AutoResizeImages override was replaced")
	}
}
