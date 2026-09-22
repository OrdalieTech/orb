package harness_test

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/engine/harness/envtest"
)

type symlinkingEnv struct{ *harness.NodeExecutionEnv }

func (env symlinkingEnv) Symlink(ctx context.Context, target, link string) error {
	resolved, err := env.AbsolutePath(ctx, link)
	if err != nil {
		return err
	}
	return os.Symlink(target, resolved)
}

func TestNodeExecutionEnvFileSystemConformance(t *testing.T) {
	// Temp directories land under os.TempDir; keep them inside the test's own directory.
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, t.TempDir())
	}
	envtest.TestFileSystem(t, func(t *testing.T) harness.FileSystem {
		env := &harness.NodeExecutionEnv{CWD: t.TempDir()}
		if runtime.GOOS == "windows" {
			return env
		}
		return symlinkingEnv{env}
	})
}
