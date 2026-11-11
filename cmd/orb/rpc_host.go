package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/OrdalieTech/orb/agent/modes"
	"github.com/OrdalieTech/orb/agent/rpc"
	"github.com/OrdalieTech/orb/agent/tools"
)

// serveRPC runs RPC mode on the process streams. A shutdown signal kills
// tracked detached children before the server disposes, and the process exits
// with the signal's code, as upstream's RPC mode does.
func serveRPC(ctx context.Context, host rpc.SessionHost, streams cliStreams, commands func() []rpc.SlashCommand) int {
	terminate := make(chan int, 1)
	defer catchShutdownSignals(terminate)()
	return rpc.Serve(ctx, host, rpc.Options{
		Input: streams.Stdin, Output: streams.Stdout, Diagnostics: streams.Stderr,
		Commands: commands, Terminate: terminate,
	})
}

// catchShutdownSignals keeps shutdown signals from killing the process until
// the returned release runs, so cleanup such as a pending token refresh
// finishes; see forwardShutdownSignals.
func catchShutdownSignals(terminate chan<- int) (release func()) {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, modes.ShutdownSignals()...)
	done := make(chan struct{})
	go forwardShutdownSignals(signals, terminate, done, os.Exit)
	return func() {
		signal.Stop(signals)
		close(done)
	}
}

// forwardShutdownSignals kills tracked detached children on each signal and
// offers the first one's exit code to terminate. A repeated signal exits at
// once: process managers close stdin and send SIGTERM together, so one signal
// during a shutdown is not a request to skip its cleanup (upstream #11599).
func forwardShutdownSignals(signals <-chan os.Signal, terminate chan<- int, done <-chan struct{}, exit func(int)) {
	for repeated := false; ; repeated = true {
		select {
		case received := <-signals:
			tools.KillTrackedDetachedChildren()
			code := modes.ShutdownExitCode(received)
			if repeated {
				exit(code)
				return
			}
			select {
			case terminate <- code:
			default:
			}
		case <-done:
			return
		}
	}
}
