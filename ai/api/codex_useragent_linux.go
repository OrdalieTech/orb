//go:build linux

package api

import (
	"syscall"
)

func openAICodexUserAgent() string {
	release := "unknown"
	var name syscall.Utsname
	if syscall.Uname(&name) == nil {
		bytes := make([]byte, 0, len(name.Release))
		for _, value := range name.Release {
			if value == 0 {
				break
			}
			bytes = append(bytes, byte(value))
		}
		release = string(bytes)
	}
	return "pi (linux " + release + "; " + codexArchitecture() + ")"
}
