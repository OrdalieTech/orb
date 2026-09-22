//go:build !linux && !darwin

package claudesessions

import "os/exec"

func isolate(cmd *exec.Cmd) func() error {
	return func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
