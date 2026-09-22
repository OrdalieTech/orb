//go:build wasm

package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"runtime"

	"github.com/OrdalieTech/orb/engine"
)

func nativeUnavailable() error {
	return fmt.Errorf("native tools are unavailable on %s/%s; supply tool operations: %w", runtime.GOOS, runtime.GOARCH, errors.ErrUnsupported)
}
func accessFile(string, uint32) error { return nativeUnavailable() }
func nativeFilesystemErrorCode(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "ENOENT"
	}
	if errors.Is(err, fs.ErrPermission) {
		return "EACCES"
	}
	return ""
}
func GetShellConfig(string) (ShellConfig, error)                          { return ShellConfig{}, nativeUnavailable() }
func GetShellEnv() (map[string]string, error)                             { return map[string]string{}, nil }
func NewLocalBashOperations(...LocalBashOperationsOptions) BashOperations { return unavailableBash{} }

type unavailableBash struct{}

func (unavailableBash) Exec(context.Context, string, string, BashExecOptions) (BashExecResult, error) {
	return BashExecResult{}, nativeUnavailable()
}
func TrackDetachedChildPID(int)    {}
func UntrackDetachedChildPID(int)  {}
func KillTrackedDetachedChildren() {}
func KillProcessTree(int)          {}

func (*findTool) executeFD(context.Context, string, string, float64) (engine.AgentToolResult, error) {
	return engine.AgentToolResult{}, nativeUnavailable()
}
func (*grepTool) Execute(context.Context, string, any, engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
	return engine.AgentToolResult{}, nativeUnavailable()
}

// ManagedFDPath is advisory; Wasm hosts cannot run downloaded executables.
func ManagedFDPath() string { return "" }

// Delegated Wasm filesystems own path identity; there is no host realpath to consult.
func canonicalMutationPath(resolved string) (string, error) { return resolved, nil }
