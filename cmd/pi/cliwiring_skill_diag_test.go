package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/session"
)

// Finding 9: skill/prompt resource diagnostics must not be printed in print/RPC
// modes. createRuntimeInputs now returns them separately (ResourceDiagnostics),
// keeping them out of the always-printed Diagnostics; the print-mode session
// host prints Diagnostics only, so the skill warning never reaches stderr.
func TestSkillDiagnosticsAreSeparatedFromPrintedDiagnostics(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv(config.EnvAgentDir, filepath.Join(t.TempDir(), "agent"))
	t.Setenv("HOME", t.TempDir())

	skillPath := filepath.Join(cwd, ".pi", "skills", "bad", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Uppercase/space name fails validation, producing a resource diagnostic.
	if err := os.WriteFile(skillPath, []byte("---\nname: Bad Name Upper\ndescription: invalid.\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	trusted := true
	inputs, err := createRuntimeInputs(cwd, CLIArgs{allowNoModel: true, ProjectTrusted: &trusted}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "invalid characters"
	if !anyContains(inputs.ResourceDiagnostics, marker) {
		t.Fatalf("skill diagnostic missing from ResourceDiagnostics: %#v", inputs.ResourceDiagnostics)
	}
	if anyContains(inputs.Diagnostics, marker) {
		t.Fatalf("skill diagnostic leaked into printed Diagnostics: %#v", inputs.Diagnostics)
	}

	// The print-mode session host prints only Diagnostics; stderr must stay clean
	// of the skill warning.
	manager, err := session.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	host, err := newCLISessionRuntimeHost(context.Background(), cliSessionRuntimeHostOptions{
		BaseArgs: CLIArgs{allowNoModel: true, ProjectTrusted: &trusted}, Manager: manager,
		Dependencies: cliDependencies{createRuntime: createRuntimeInputs},
		Streams:      cliStreams{Stderr: &stderr}, ExtensionMode: extensions.ModePrint,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose(context.Background())
	if strings.Contains(stderr.String(), marker) {
		t.Fatalf("print mode printed the skill diagnostic: %q", stderr.String())
	}
}

func anyContains(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
