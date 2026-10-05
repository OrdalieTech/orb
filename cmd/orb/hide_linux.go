package main

import "syscall"

// hideProcess makes this process non-dumpable: its /proc entries, environ
// included, become root's, so tools running as the same user cannot read the
// credentials it was started with. Its children are dumpable again after exec.
func hideProcess() {
	_, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0)
}
