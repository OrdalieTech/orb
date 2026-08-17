package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeSDKWritesEmbeddedTree(t *testing.T) {
	agentDir := t.TempDir()
	root, err := materializeSDK(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(root), "sdk-") {
		t.Fatalf("expected content-addressed sdk directory, got %s", root)
	}
	entries, _, err := collectSDKEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded sdk tree is empty")
	}
	required := map[string]bool{
		"sdk.json": false, "coding-agent.mjs": false, "ai.mjs": false, "tui.mjs": false,
		"coding-agent.d.ts": false, "ai.d.ts": false, "tui.d.ts": false,
		"internal/services.mjs": false, "internal/unsupported.mjs": false,
	}
	for _, entry := range entries {
		if _, tracked := required[entry.name]; tracked {
			required[entry.name] = true
		}
		materialized, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.name)))
		if err != nil {
			t.Fatalf("materialized file missing: %v", err)
		}
		if string(materialized) != string(entry.data) {
			t.Fatalf("materialized %s differs from embedded copy", entry.name)
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("embedded sdk tree is missing %s", name)
		}
	}
}

func TestMaterializeSDKIsIdempotent(t *testing.T) {
	agentDir := t.TempDir()
	first, err := materializeSDK(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate a materialized file; a second call must trust the
	// content-addressed name and leave the tree alone (same contract as the
	// hash-named host.mjs materialization).
	marker := filepath.Join(first, "sdk.json")
	original, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	second, err := materializeSDK(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("materialization is not stable: %s then %s", first, second)
	}
	current, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(original) {
		t.Fatal("second materialization rewrote an existing tree")
	}
}

func TestMaterializeSDKRequiresAgentDir(t *testing.T) {
	if _, err := materializeSDK(""); err == nil {
		t.Fatal("expected error for empty agent directory")
	}
}
