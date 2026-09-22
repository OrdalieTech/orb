package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"

	"github.com/OrdalieTech/orb/agent/modes"
	"github.com/OrdalieTech/orb/agent/rpc"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// serveRPC runs RPC mode on the process streams. A shutdown signal kills
// tracked detached children before the server disposes, and the process exits
// with the signal's code, as upstream's RPC mode does.
func serveRPC(ctx context.Context, host rpc.SessionHost, streams cliStreams, commands func() []rpc.SlashCommand) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, modes.ShutdownSignals()...)
	defer signal.Stop(signals)
	terminate := make(chan int)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case received := <-signals:
			tools.KillTrackedDetachedChildren()
			select {
			case terminate <- modes.ShutdownExitCode(received):
			case <-done:
			}
		case <-done:
		}
	}()
	return rpc.Serve(ctx, host, rpc.Options{
		Input: streams.Stdin, Output: streams.Stdout, Diagnostics: streams.Stderr,
		Commands: commands, Terminate: terminate,
	})
}

func rpcMessageRoleAndText(raw json.RawMessage) (string, string) {
	return jsonwire.MessageRoleAndText(raw)
}
