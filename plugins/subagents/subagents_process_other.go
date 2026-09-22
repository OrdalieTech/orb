//go:build !linux && !darwin

package subagents

import (
	"fmt"
	"os/exec"
)

func isolateExternalProcess(*exec.Cmd) (func() error, error) {
	return nil, fmt.Errorf("process-group isolation is supported only on linux and darwin")
}
