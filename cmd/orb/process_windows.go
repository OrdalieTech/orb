package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func detachedDaemonProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}

// Windows has no exec(2); sandbox.SelfRestrict already refuses before this runs.
func execReplacingProcess(string, []string, []string) error { return errors.ErrUnsupported }

// Windows cannot read another process's environment, so any other Orb process owned by this
// user blocks migration regardless of which agent directory it serves.
func requireOfflineMigration(context.Context, string) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return errors.New("cannot verify that legacy Orb writers are stopped")
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return errors.New("cannot verify that legacy Orb writers are stopped")
	}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		pid := entry.ProcessID
		name := strings.TrimSuffix(strings.ToLower(windows.UTF16ToString(entry.ExeFile[:])), ".exe")
		if pid == uint32(os.Getpid()) || (name != "orb" && !strings.HasPrefix(name, "orb-")) {
			continue
		}
		owned, alive, inspectErr := processOwnedBy(pid, self.User.Sid)
		if inspectErr != nil {
			return errors.New("cannot inspect a running Orb process before migration")
		}
		if alive && owned {
			return fmt.Errorf("close other Orb processes before migration (process %d is still running)", pid)
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return errors.New("cannot verify that legacy Orb writers are stopped")
	}
	return nil
}

func processOwnedBy(pid uint32, user *windows.SID) (owned, alive bool, err error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, false, nil
	}
	if err != nil {
		return false, true, err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var token windows.Token
	if err = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return false, true, err
	}
	defer func() { _ = token.Close() }()
	owner, err := token.GetTokenUser()
	if err != nil {
		return false, true, err
	}
	return windows.EqualSid(owner.User.Sid, user), true, nil
}
