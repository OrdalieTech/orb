//go:build !js && !wasip1

package browser

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestLightRegistryLinksNoUnselectedProvider proves the browser assembly links
// only the provider families its registry selects: no Bedrock backend or AWS
// SDK, and not the default all-families registry.
func TestLightRegistryLinksNoUnselectedProvider(t *testing.T) {
	forbidden := []string{
		"github.com/OrdalieTech/orb/ai/api/all", "github.com/OrdalieTech/orb/ai/api/bedrock",
		"github.com/aws/aws-sdk-go-v2", "github.com/aws/smithy-go",
	}
	command := exec.CommandContext(t.Context(), "go", "list", "-deps", "github.com/OrdalieTech/orb/platforms/browser", "github.com/OrdalieTech/orb/cmd/orb-wasm")
	command.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, output)
	}
	for dep := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		for _, prefix := range forbidden {
			if dep == prefix || strings.HasPrefix(dep, prefix+"/") {
				t.Errorf("js/wasm browser assembly links %s", dep)
			}
		}
	}
}
