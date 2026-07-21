//go:build !linux && !darwin

package api

import "runtime"

func openAICodexUserAgent() string {
	return "pi (" + codexNodePlatform(runtime.GOOS) + " unknown; " + codexArchitecture() + ")"
}
