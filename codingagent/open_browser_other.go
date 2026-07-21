//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package codingagent

import "os/exec"

func configureDetachedProcess(*exec.Cmd) {}
