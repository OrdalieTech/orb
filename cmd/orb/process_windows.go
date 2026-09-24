package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/OrdalieTech/orb/agent/config"
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

// requireOfflineMigration matches the POSIX check: another Orb process owned by this user
// blocks migration when its environment selects the same agent directory.
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
		if !alive || !owned {
			continue
		}
		value, set, inspectErr := processEnvironmentValue(pid, config.EnvAgentDir)
		if inspectErr != nil {
			if running, _ := processRunning(pid); !running {
				continue
			}
			return errors.New("cannot inspect a running Orb process before migration")
		}
		if configured := os.Getenv(config.EnvAgentDir); (configured != "" && value != configured) || (configured == "" && set) {
			continue
		}
		return fmt.Errorf("close other Orb processes before migration (process %d is still running)", pid)
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

func processRunning(pid uint32) (bool, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var code uint32
	if err = windows.GetExitCodeProcess(process, &code); err != nil {
		return true, err
	}
	return code == uint32(windows.STATUS_PENDING), nil
}

// processEnvironmentValue reads name from another process's environment block, as procfs
// and ps do on POSIX: PEB.ProcessParameters, then its Environment and EnvironmentSize.
// Remote addresses stay uintptr so the collector never sees them as Go pointers.
func processEnvironmentValue(pid uint32, name string) (string, bool, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	// PROCESS_BASIC_INFORMATION: every field occupies one pointer-sized slot.
	var basic [6]uintptr
	if err = windows.NtQueryInformationProcess(process, windows.ProcessBasicInformation, unsafe.Pointer(&basic), uint32(unsafe.Sizeof(basic)), nil); err != nil {
		return "", false, err
	}
	readPointer := func(address uintptr) (uintptr, error) {
		var value uintptr
		err := windows.ReadProcessMemory(process, address, (*byte)(unsafe.Pointer(&value)), unsafe.Sizeof(value), nil)
		return value, err
	}
	// PEB and RTL_USER_PROCESS_PARAMETERS offsets for 32- and 64-bit layouts.
	parametersOffset, environmentOffset, sizeOffset := uintptr(0x10), uintptr(0x48), uintptr(0x290)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		parametersOffset, environmentOffset, sizeOffset = 0x20, 0x80, 0x3f0
	}
	parameters, err := readPointer(basic[1] + parametersOffset)
	if err != nil {
		return "", false, err
	}
	environment, err := readPointer(parameters + environmentOffset)
	if err != nil {
		return "", false, err
	}
	size, err := readPointer(parameters + sizeOffset)
	if err != nil {
		return "", false, err
	}
	if environment == 0 || size < 2 || size > 1<<24 {
		return "", false, errors.New("unreadable process environment")
	}
	block := make([]uint16, size/2)
	if err = windows.ReadProcessMemory(process, environment, (*byte)(unsafe.Pointer(&block[0])), uintptr(len(block))*2, nil); err != nil {
		return "", false, err
	}
	for _, entry := range strings.Split(string(utf16.Decode(block)), "\x00") {
		// Hidden per-drive entries ("=C:=C:\dir") have an empty name.
		if key, value, ok := strings.Cut(entry, "="); ok && strings.EqualFold(key, name) {
			return value, true, nil
		}
	}
	return "", false, nil
}
