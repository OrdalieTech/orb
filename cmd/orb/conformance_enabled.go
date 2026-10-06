//go:build conformance

package main

import (
	"context"
	"os"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/conformance/f7"
)

func platformCLIDependencies() cliDependencies {
	return cliDependencies{createRuntime: createRuntimeInputs, runRPCFixture: runF7RPCFixture}
}

// runF7RPCFixture supplies deterministic model and session boundaries to the
// generated F7 transcript while retaining the production CLI and RPC paths.
func runF7RPCFixture(ctx context.Context, _ CLIArgs, streams cliStreams, _ string) (bool, int) {
	path := os.Getenv(f7.ScenarioEnv)
	if path == "" {
		return false, 0
	}
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	runtime, err := f7.Load(path, agentDir)
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	return true, serveRPC(ctx, &f7.Host{Runtime: runtime}, streams, nil)
}
