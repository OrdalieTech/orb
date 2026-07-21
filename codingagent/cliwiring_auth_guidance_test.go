package codingagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

// Finding 10: when the docs do not ship next to a standalone binary and no
// PI_PACKAGE_DIR is configured, the auth guidance must point at the hosted docs
// URL rather than a <dir-of-binary>/docs/providers.md path that does not exist.
func TestAuthGuidanceFallsBackToHostedDocsWhenAbsent(t *testing.T) {
	t.Setenv("PI_PACKAGE_DIR", "")
	message := formatNoAPIKeyFoundMessage(ai.ProviderID("anthropic"))
	if !strings.Contains(message, "https://github.com/OrdalieTech/pigo/blob/main/docs/providers.md") {
		t.Fatalf("expected hosted providers.md URL, got:\n%s", message)
	}
	if strings.Contains(message, "docs/providers.md\n") && !strings.Contains(message, "https://") {
		t.Fatalf("guidance still points at a nonexistent local docs path:\n%s", message)
	}
	models := formatNoModelsAvailableMessage()
	if !strings.Contains(models, "https://github.com/OrdalieTech/pigo/blob/main/docs/models.md") {
		t.Fatalf("expected hosted models.md URL, got:\n%s", models)
	}
}

// When a package layout is configured (PI_PACKAGE_DIR) with docs present, the
// guidance keeps pointing at the on-disk docs, matching upstream getDocsPath.
func TestAuthGuidanceUsesLocalDocsWhenPackageDirSet(t *testing.T) {
	packageDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(packageDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "docs", "providers.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_PACKAGE_DIR", packageDir)
	message := formatNoAPIKeyFoundMessage(ai.ProviderID("anthropic"))
	want := filepath.Join(packageDir, "docs", "providers.md")
	if !strings.Contains(message, want) {
		t.Fatalf("expected local docs path %q, got:\n%s", want, message)
	}
	if strings.Contains(message, "https://") {
		t.Fatalf("guidance used the hosted URL despite present local docs:\n%s", message)
	}
}
