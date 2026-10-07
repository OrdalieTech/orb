package proctree

import (
	"errors"
	"os"
	"os/exec"
)

// Wasm cannot spawn a process; os/exec rejects execution before these run.
func Isolate(*exec.Cmd) {}
func Kill(int) error    { return nil }
func expendable(int)    {}

// defaultShell still resolves through the host filesystem a Wasm runtime may
// expose, so run failures read as they do natively.
func defaultShell(func(string) string) (Shell, error) {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return bashShell("/bin/bash"), nil
	}
	return Shell{}, errors.New("No bash shell found") //nolint:staticcheck // Upstream error text is observable.
}

// SpawnErrorCode is the code Node reports for a failed spawn: none on Wasm.
func SpawnErrorCode(error) string { return "" }
