//go:build !windows

package jobs

import (
	"strconv"
	"syscall"
)

// alive reports whether the job's process group still has a member.
func alive(pid string) bool {
	n, _ := strconv.Atoi(pid)
	return syscall.Kill(-n, 0) == nil || syscall.Kill(n, 0) == nil
}
