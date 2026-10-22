//go:build !unix

package clipboard

import "os/exec"

func detach(*exec.Cmd) {}
