//go:build !linux && !darwin

package api

import "runtime"

func piUserAgent() string {
	return "pi (" + piNodePlatform(runtime.GOOS) + " unknown; " + piArchitecture() + ")"
}
