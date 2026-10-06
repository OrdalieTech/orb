package proctree

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Isolate hides the console window, as Node's windowsHide spawn does; win32
// has no detached process group, and Kill walks the tree instead.
func Isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// Kill runs System32's taskkill over pid's tree, detached and not awaited,
// matching upstream's fire-and-forget spawn; its error is only the spawn's.
func Kill(pid int) error {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	command := exec.Command(filepath.Join(system, "taskkill.exe"), "/F", "/T", "/PID", strconv.Itoa(pid))
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

func defaultShell(getenv func(string) string) (Shell, error) {
	var searched []string
	for _, variable := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if root := getenv(variable); root != "" {
			path := root + `\Git\bin\bash.exe`
			if _, err := os.Stat(path); err == nil {
				return bashShell(path), nil
			}
			searched = append(searched, "  "+path)
		}
	}
	if shell := where("bash.exe"); shell != "" {
		return bashShell(shell), nil
	}
	return Shell{}, errors.New("No bash shell found. Options:\n" + //nolint:staticcheck // Upstream error text is observable.
		"  1. Install Git for Windows: https://git-scm.com/download/win\n" +
		"  2. Add your bash to PATH (Cygwin, MSYS2, etc.)\n" +
		"  3. Set shellPath in settings.json\n\n" +
		"Searched Git Bash in:\n" + strings.Join(searched, "\n"))
}

// where.exe can list paths that no longer exist, so the first match is verified.
func where(executable string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "where", executable)
	Isolate(command)
	output, err := command.Output()
	first, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	first = strings.TrimSuffix(first, "\r")
	if err != nil || first == "" {
		return ""
	}
	if _, err := os.Stat(first); err != nil {
		return ""
	}
	return first
}

// SpawnErrorCode is the code Node reports for a failed spawn ("ENOENT"), or "".
func SpawnErrorCode(err error) string {
	if code := libuvErrorCode(err); code != "" {
		return code
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
		return "ENOENT"
	}
	return ""
}

// libuvErrorCode follows libuv's uv_translate_sys_error for the Win32 errors
// Node surfaces from filesystem calls and process spawn.
func libuvErrorCode(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return ""
	}
	return libuvErrorCodes[errno]
}

var libuvErrorCodes = map[syscall.Errno]string{
	windows.ERROR_FILE_NOT_FOUND:          "ENOENT",
	windows.ERROR_PATH_NOT_FOUND:          "ENOENT",
	windows.ERROR_INVALID_NAME:            "ENOENT",
	windows.ERROR_INVALID_DRIVE:           "ENOENT",
	windows.ERROR_BAD_PATHNAME:            "ENOENT",
	windows.ERROR_DIRECTORY:               "ENOENT",
	windows.ERROR_INVALID_REPARSE_DATA:    "ENOENT",
	windows.ERROR_MOD_NOT_FOUND:           "ENOENT",
	windows.ERROR_ENVVAR_NOT_FOUND:        "ENOENT",
	windows.ERROR_ACCESS_DENIED:           "EPERM",
	windows.ERROR_PRIVILEGE_NOT_HELD:      "EPERM",
	windows.ERROR_NOACCESS:                "EACCES",
	windows.ERROR_CANT_ACCESS_FILE:        "EACCES",
	windows.ERROR_ELEVATION_REQUIRED:      "EACCES",
	windows.ERROR_CANT_RESOLVE_FILENAME:   "ELOOP",
	windows.ERROR_WRITE_PROTECT:           "EROFS",
	windows.ERROR_FILENAME_EXCED_RANGE:    "ENAMETOOLONG",
	windows.ERROR_IO_DEVICE:               "EIO",
	windows.ERROR_GEN_FAILURE:             "EIO",
	windows.ERROR_CRC:                     "EIO",
	windows.ERROR_OPEN_FAILED:             "EIO",
	windows.ERROR_NOT_ENOUGH_MEMORY:       "ENOMEM",
	windows.ERROR_OUTOFMEMORY:             "ENOMEM",
	windows.ERROR_INVALID_PARAMETER:       "EINVAL",
	windows.ERROR_INVALID_DATA:            "EINVAL",
	windows.ERROR_INSUFFICIENT_BUFFER:     "EINVAL",
	windows.ERROR_SYMLINK_NOT_SUPPORTED:   "EINVAL",
	windows.ERROR_DISK_FULL:               "ENOSPC",
	windows.ERROR_HANDLE_DISK_FULL:        "ENOSPC",
	windows.ERROR_CANNOT_MAKE:             "ENOSPC",
	windows.ERROR_EA_TABLE_FULL:           "ENOSPC",
	windows.ERROR_END_OF_MEDIA:            "ENOSPC",
	windows.ERROR_INVALID_FUNCTION:        "EISDIR",
	windows.ERROR_FILE_EXISTS:             "EEXIST",
	windows.ERROR_ALREADY_EXISTS:          "EEXIST",
	windows.ERROR_DIR_NOT_EMPTY:           "ENOTEMPTY",
	windows.ERROR_NOT_SAME_DEVICE:         "EXDEV",
	windows.ERROR_SHARING_VIOLATION:       "EBUSY",
	windows.ERROR_LOCK_VIOLATION:          "EBUSY",
	windows.ERROR_PIPE_BUSY:               "EBUSY",
	windows.ERROR_BAD_EXE_FORMAT:          "EFTYPE",
	windows.ERROR_META_EXPANSION_TOO_LONG: "E2BIG",
}
