//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestV086BuiltInToolConformance(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/F11BuiltInTools/tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int `json:"schemaVersion"`
		Tools         []struct {
			Name                string                        `json:"name"`
			ConstrainedSampling *ai.ConstrainedSamplingConfig `json:"constrainedSampling"`
		} `json:"tools"`
		Bash struct {
			NullExitError   string `json:"nullExitError"`
			SignalExitError string `json:"signalExitError"`
		} `json:"bash"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d, want 1", fixture.SchemaVersion)
	}

	t.Setenv("PI_EXPERIMENTAL", "0")
	tools := map[string]engine.AgentTool{
		"bash":  NewBashTool(t.TempDir(), nil),
		"read":  NewReadTool(t.TempDir(), nil),
		"edit":  NewEditTool(t.TempDir(), nil),
		"write": NewWriteTool(t.TempDir(), nil),
	}
	for _, expected := range fixture.Tools {
		tool := tools[expected.Name]
		if tool == nil {
			t.Fatalf("unknown fixture tool %q", expected.Name)
		}
		got := tool.Spec().ConstrainedSampling
		if got == nil || expected.ConstrainedSampling == nil || *got != *expected.ConstrainedSampling {
			t.Errorf("%s constrainedSampling = %#v, want %#v", expected.Name, got, expected.ConstrainedSampling)
		}
	}

	nullExit := bashOperationsFunc(func(
		_ context.Context,
		_ string,
		_ string,
		options BashExecOptions,
	) (BashExecResult, error) {
		options.OnData([]byte("before"))
		return BashExecResult{}, nil
	})
	_, err = NewBashTool(t.TempDir(), &BashToolOptions{Operations: nullExit}).Execute(
		context.Background(), "fixture", BashToolInput{Command: "fixture"}, nil,
	)
	if err == nil || err.Error() != fixture.Bash.NullExitError {
		t.Errorf("null exit error = %v, want %q", err, fixture.Bash.NullExitError)
	}

	_, err = NewBashTool(t.TempDir(), &BashToolOptions{ShellPath: "/bin/sh"}).Execute(
		context.Background(), "fixture", BashToolInput{Command: "kill -TERM $$"}, nil,
	)
	if err == nil || err.Error() != fixture.Bash.SignalExitError {
		t.Errorf("signal exit error = %v, want %q", err, fixture.Bash.SignalExitError)
	}
}
