package runner_test

import (
	"bytes"
	"testing"

	"github.com/OrdalieTech/pi-go/conformance/runner"
)

func TestF5Manifest(t *testing.T) {
	manifest := runner.LoadManifest(t, "F5")
	if manifest.Family != "F5" {
		t.Fatalf("family = %q, want F5", manifest.Family)
	}
	if manifest.UpstreamCommit == "" || len(manifest.Files) == 0 {
		t.Fatalf("incomplete manifest: %+v", manifest)
	}
}

func TestCanonicalJSON(t *testing.T) {
	left, err := runner.CanonicalJSON([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := runner.CanonicalJSON([]byte("{\n\t\"a\": 1, \"b\": 2\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := runner.ByteDiff(left, right); diff != "" {
		t.Fatal(diff)
	}
}

func TestByteDiff(t *testing.T) {
	if diff := runner.ByteDiff([]byte("same"), []byte("same")); diff != "" {
		t.Fatalf("equal input diff = %q", diff)
	}
	diff := runner.ByteDiff([]byte("prefix-want"), []byte("prefix-got"))
	if !bytes.Contains([]byte(diff), []byte("byte 7")) {
		t.Fatalf("diff lacks offset: %q", diff)
	}
}

func TestReadFixtureRejectsTraversal(t *testing.T) {
	if _, err := runner.ReadFixture("../F5", "manifest.json"); err == nil {
		t.Fatal("ReadFixture accepted traversal")
	}
}
