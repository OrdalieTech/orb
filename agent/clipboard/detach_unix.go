//go:build unix

package clipboard

import (
	"os/exec"
	"syscall"
)

// detach starts a clipboard helper in its own process group: wl-copy, xclip and xsel keep serving the
// clipboard in the background, and Ctrl+Z suspending Orb's group would stop them too (pi 1.4.0).
func detach(command *exec.Cmd) { command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
