package api

import (
	"runtime"
	"sync"
)

// piUserAgent reads the OS release once: it cannot change while Orb runs, and
// the syscall cost every provider request a few hundred microseconds.
var piUserAgent = sync.OnceValue(readPIUserAgent)

func piArchitecture() string {
	return piNodeArchitecture(runtime.GOARCH)
}

// piNodeArchitecture maps Go architecture names onto Node os.arch() values.
func piNodeArchitecture(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	case "mipsle":
		return "mipsel"
	case "ppc64le":
		return "ppc64"
	}
	return goarch
}
