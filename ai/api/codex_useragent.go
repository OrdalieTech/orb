package api

import "runtime"

func codexArchitecture() string {
	return codexNodeArchitecture(runtime.GOARCH)
}

// codexNodeArchitecture maps Go architecture names onto Node os.arch() values.
func codexNodeArchitecture(goarch string) string {
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

// codexNodePlatform maps Go platform names onto Node os.platform() values.
func codexNodePlatform(goos string) string {
	switch goos {
	case "windows":
		return "win32"
	case "solaris", "illumos":
		return "sunos"
	}
	return goos
}
