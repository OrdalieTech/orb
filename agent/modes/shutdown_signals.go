package modes

import "os"

// ShutdownSignals are the signals the headless modes (print, RPC) treat as a
// shutdown request, matching upstream's per-platform registration.
func ShutdownSignals() []os.Signal { return printModeSignals() }

// ShutdownExitCode is the exit code upstream reports after received.
func ShutdownExitCode(received os.Signal) int { return printModeSignalExitCode(received) }
