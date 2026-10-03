package host

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
)

func TestChildEnvReachesOnlyTheHostChild(t *testing.T) {
	t.Setenv("ORB_CHILD_PROBE", "parent")
	path := filepath.Join(t.TempDir(), "probe.mjs")
	source := `export default function (pi) {
 pi.registerTool({name: "probe", label: "probe", description: "probe", parameters: {type: "object", properties: {}},
 execute: async () => ({content: [{type: "text", text: process.env.ORB_CHILD_PROBE}], details: {}})});
}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := requireRuntime(t)
	var seen []string
	wrapped := 0
	manager := NewManager(Options{
		AgentDir: t.TempDir(), CWD: t.TempDir(), Runtime: &runtime,
		ChildEnv: func(paths []string) []string { seen = paths; return []string{"ORB_CHILD_PROBE=child"} },
		WrapFactory: func(_ string, factory extensions.Factory) extensions.Factory {
			wrapped++
			return factory
		},
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	registry := extensions.NewRegistry(t.TempDir())
	if result := manager.RegisterInto(context.Background(), registry, []string{path}); len(result.Errors) != 0 {
		t.Fatalf("load result: %#v", result)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{})
	tool := runner.ToolDefinition("probe")
	if tool == nil {
		t.Fatal("probe tool missing")
	}
	outcome, err := tool.Execute(context.Background(), "test", map[string]any{}, nil, runner.CreateContext())
	if err != nil || toolText(outcome) != "child" {
		t.Fatalf("host child environment: %q, %v", toolText(outcome), err)
	}
	if !slices.Contains(seen, path) || wrapped != 1 {
		t.Fatalf("hooks saw paths %v, wrapped %d", seen, wrapped)
	}
	if os.Getenv("ORB_CHILD_PROBE") != "parent" {
		t.Fatal("changed parent environment")
	}
}
