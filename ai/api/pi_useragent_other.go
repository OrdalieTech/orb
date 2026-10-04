//go:build !linux && !darwin

package api

import "runtime"

func readPIUserAgent() string {
	return "pi (" + piNodePlatform(runtime.GOOS) + " unknown; " + piArchitecture() + ")"
}

// piNodePlatform maps Go platform names onto Node os.platform() values.
func piNodePlatform(goos string) string {
	switch goos {
	case "windows":
		return "win32"
	case "solaris", "illumos":
		return "sunos"
	}
	return goos
}
