package host

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestSDKJavaScriptUnitTests drives the node-side unit tests for the embedded
// orb-extension-sdk's pure symbols (testdata/sdk_pure_test.mjs) through the
// same discovered runtime the extension host uses.
func TestSDKJavaScriptUnitTests(t *testing.T) {
	runtime := requireRuntime(t)
	if runtime.Name != "node" {
		t.Skipf("sdk unit tests need node's test runner; discovered %s", runtime.Name)
	}
	testPath, err := filepath.Abs(filepath.Join("testdata", "sdk_pure_test.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, runtime.Path, "--test", testPath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("sdk js tests failed: %v\n%s", err, output)
	}
}
