package jobs

// ponytail: Git Bash job pids are not Windows pids; a job killed outside Orb goes unreported there.
func alive(string) bool { return true }
