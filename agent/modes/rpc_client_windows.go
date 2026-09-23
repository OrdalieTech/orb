package modes

import "os"

// Windows has no SIGTERM delivery; libuv implements Node's kill("SIGTERM")
// there as TerminateProcess, so upstream's stop ends the child at once.
func terminateRPCClientProcess(process *os.Process) {
	_ = process.Kill()
}
