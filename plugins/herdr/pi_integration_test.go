package herdr

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecognizesOnlyManagedPiIntegration(t *testing.T) {
	for _, test := range []struct {
		name, source string
		want         bool
	}{
		{"managed", "// installed by herdr\n// HERDR_INTEGRATION_ID=pi\n", true},
		{"windows", "// installed by herdr\r\n// HERDR_INTEGRATION_ID=pi\r\n", true},
		{"unmanaged", "export default function () {}\n", false},
		{"other integration", "// installed by herdr\n// HERDR_INTEGRATION_ID=other\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "herdr-agent-state.ts")
			if err := os.WriteFile(path, []byte(test.source), 0600); err != nil {
				t.Fatal(err)
			}
			if got := IsPiIntegration(path); got != test.want {
				t.Fatalf("IsPiIntegration = %v", got)
			}
		})
	}
	if IsPiIntegration(filepath.Join(t.TempDir(), "missing")) {
		t.Fatal("missing integration recognized")
	}
}
