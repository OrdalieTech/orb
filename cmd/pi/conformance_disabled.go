//go:build !conformance

package main

func platformCLIDependencies() cliDependencies {
	return cliDependencies{createRuntime: createRuntimeInputs}
}
