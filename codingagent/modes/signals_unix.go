//go:build !windows

package modes

import (
	"os"
	"syscall"
)

func printModeSignals() []os.Signal {
	return []os.Signal{syscall.SIGTERM, syscall.SIGHUP}
}

func printModeSignalExitCode(received os.Signal) int {
	if received == syscall.SIGHUP {
		return 129
	}
	return 143
}
