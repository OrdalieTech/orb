package tools

import (
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestToolBundlesMatchUpstreamOrder(t *testing.T) {
	coding := NewCodingTools(t.TempDir(), nil)
	codingNames := make([]string, 0, len(coding))
	for _, tool := range coding {
		codingNames = append(codingNames, tool.Spec().Name)
	}
	wantCoding := []string{"read", "bash", "edit", "write"}
	for index, name := range wantCoding {
		if codingNames[index] != name {
			t.Fatalf("coding tools = %v, want %v", codingNames, wantCoding)
		}
	}
	readOnly := NewReadOnlyTools(t.TempDir(), &ToolsOptions{})
	readOnlyNames := make([]string, 0, len(readOnly))
	for _, tool := range readOnly {
		readOnlyNames = append(readOnlyNames, tool.Spec().Name)
	}
	wantReadOnly := []string{"read", "grep", "find", "ls"}
	for index, name := range wantReadOnly {
		if readOnlyNames[index] != name {
			t.Fatalf("read-only tools = %v, want %v", readOnlyNames, wantReadOnly)
		}
	}
}

func TestExperimentalToolSamplingGate(t *testing.T) {
	if experimentalToolSampling() != nil {
		t.Fatal("constrained sampling must be off without PI_EXPERIMENTAL")
	}
	t.Setenv("PI_EXPERIMENTAL", "1")
	sampling := experimentalToolSampling()
	if sampling == nil || sampling.Type != ai.ConstrainedSamplingJSONSchema || sampling.Strict != ai.ConstrainedSamplingPrefer {
		t.Fatalf("constrained sampling = %#v", sampling)
	}
	for _, tool := range NewCodingTools(t.TempDir(), nil) {
		if tool.Spec().ConstrainedSampling == nil {
			t.Fatalf("%s does not carry constrained sampling", tool.Spec().Name)
		}
	}
}
