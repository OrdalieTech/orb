//go:build darwin

package api

import "syscall"

func piUserAgent() string {
	release, err := syscall.Sysctl("kern.osrelease")
	if err != nil {
		release = "unknown"
	}
	return "pi (darwin " + release + "; " + piArchitecture() + ")"
}
