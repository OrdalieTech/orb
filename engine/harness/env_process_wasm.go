package harness

import (
	"os"
	"os/exec"
)

// Wasm cannot spawn a process; os/exec rejects execution before these hooks run.
func configureProcessTree(*exec.Cmd) {}
func killProcessTree(*os.Process)    {}
