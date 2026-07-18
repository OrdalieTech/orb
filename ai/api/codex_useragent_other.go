//go:build !linux && !darwin

package api

import "runtime"

func openAICodexUserAgent() string {
	return "pi (" + runtime.GOOS + " unknown; " + codexArchitecture() + ")"
}
