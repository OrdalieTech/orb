//go:build !windows

package filelock

func deletePending(error) bool { return false }
