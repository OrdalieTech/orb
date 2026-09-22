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

func (mode *InteractiveMode) suspend() {
	_ = mode.ui.Stop()
	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(syscall.SIGTSTP)
	_ = mode.ui.Start()
}
