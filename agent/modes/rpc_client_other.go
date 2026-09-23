//go:build !windows

package modes

import (
	"os"
	"syscall"
)

func terminateRPCClientProcess(process *os.Process) {
	_ = process.Signal(syscall.SIGTERM)
}
