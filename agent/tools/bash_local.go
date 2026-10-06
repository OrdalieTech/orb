//go:build !wasm

package tools

import (
	"context"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/internal/proctree"
	"github.com/OrdalieTech/orb/internal/toolenv"
)

// GetShellConfig resolves upstream's bash: customShellPath when it is set,
// else Git Bash on win32 and bash or sh elsewhere.
func GetShellConfig(customShellPath string) (proctree.Shell, error) {
	return proctree.FindShell(customShellPath, os.Getenv)
}

// GetShellEnv is the tool environment with the managed binary directory first on PATH.
func GetShellEnv() (map[string]string, error) {
	binDir, err := managedBinDir()
	if err != nil {
		return nil, err
	}
	environment := toolenv.Environ()
	environment = toolenv.Set(environment, "PATH", toolenv.PrependPath(binDir, toolenv.Get(environment, "PATH")))
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		// Windows keeps hidden per-drive cwd variables ("=C:") that Node's process.env omits.
		if key, value, ok := strings.Cut(entry, "="); ok && key != "" {
			values[key] = value
		}
	}
	return values, nil
}

type localBashOperations struct {
	shell func() (proctree.Shell, error)
}

func NewLocalBashOperations(options ...LocalBashOperationsOptions) BashOperations {
	shellPath := ""
	if len(options) > 0 {
		shellPath = options[0].ShellPath
	}
	return &localBashOperations{shell: func() (proctree.Shell, error) { return GetShellConfig(shellPath) }}
}

func (operations *localBashOperations) Exec(ctx context.Context, command, cwd string, options BashExecOptions) (BashExecResult, error) {
	environment := options.Env
	if environment == nil {
		var err error
		if environment, err = GetShellEnv(); err != nil {
			return BashExecResult{}, err
		}
	}
	pid := 0
	code, err := proctree.Run(ctx, proctree.Command{
		Script: command, Dir: cwd, Env: toolenv.Merge(nil, environment), Timeout: options.Timeout, Shell: operations.shell,
		Started: func(started int) {
			pid = started
			trackedDetachedChildren.Lock()
			trackedDetachedChildren.pids[pid] = struct{}{}
			trackedDetachedChildren.Unlock()
		},
		OnData: func(_ bool, chunk []byte) error {
			if options.OnData != nil {
				options.OnData(chunk)
			}
			return nil
		},
	})
	trackedDetachedChildren.Lock()
	delete(trackedDetachedChildren.pids, pid)
	trackedDetachedChildren.Unlock()
	if err != nil {
		return BashExecResult{}, err
	}
	return BashExecResult{ExitCode: &code}, nil
}

var trackedDetachedChildren = struct {
	sync.Mutex
	pids map[int]struct{}
}{pids: make(map[int]struct{})}

// KillTrackedDetachedChildren kills every running bash process tree, for exit paths.
func KillTrackedDetachedChildren() {
	trackedDetachedChildren.Lock()
	pids := slices.Collect(maps.Keys(trackedDetachedChildren.pids))
	clear(trackedDetachedChildren.pids)
	trackedDetachedChildren.Unlock()
	for _, pid := range pids {
		_ = proctree.Kill(pid)
	}
}
