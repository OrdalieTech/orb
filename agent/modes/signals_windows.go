package modes

import (
	"os"
	"syscall"
)

// Upstream registers only SIGTERM on win32; Go raises it for console close,
// logoff, and shutdown events.
func printModeSignals() []os.Signal {
	return []os.Signal{syscall.SIGTERM}
}

func printModeSignalExitCode(os.Signal) int { return 143 }

func (mode *InteractiveMode) suspend() {
	mode.showStatusMessage("Suspend to background is not supported on Windows")
}
