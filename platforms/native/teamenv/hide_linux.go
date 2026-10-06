// Package teamenv is the receiving side of a team agent's entrypoint contract
// (platforms/agent/harness): credentials on ORB_SECRETS_FD and ORB_AUTH_FD,
// never in the environment or files the agent's tools could open, and a
// process its same-user tools cannot inspect.
package teamenv

import "syscall"

// Hide makes this process non-dumpable: its /proc entries, environ included,
// become root's, so tools running as the same user cannot read the
// credentials it was started with. Its children are dumpable again after exec.
func Hide() {
	_, _, _ = syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0)
}
