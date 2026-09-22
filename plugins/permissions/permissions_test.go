package permissions

import (
	"github.com/OrdalieTech/orb/agent/extensions"
	"strings"
	"testing"
)

func TestExtensionRequiresPolicy(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("permissions", Extension(nil, nil, nil)); err == nil || !strings.Contains(err.Error(), "policy is required") {
		t.Fatalf("nil policy registration: %v", err)
	}
}
