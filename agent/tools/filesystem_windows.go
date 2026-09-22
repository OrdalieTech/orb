package tools

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// Mirrors libuv fs__access: only existence and the read-only attribute are checked, and
// directories never report read-only.
func accessFile(path string, mode uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			return pathError.Err
		}
		return err
	}
	attributes := info.Sys().(*syscall.Win32FileAttributeData).FileAttributes
	if mode&accessWrite == 0 || attributes&windows.FILE_ATTRIBUTE_READONLY == 0 || attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return nil
	}
	return windows.ERROR_ACCESS_DENIED
}

func nativeFilesystemErrorCode(err error) string {
	if code := libuvErrorCode(err); code != "" {
		return code
	}
	for _, candidate := range []struct {
		err  error
		code string
	}{
		{syscall.EPERM, "EPERM"},
		{syscall.EACCES, "EACCES"},
		{syscall.ENOTDIR, "ENOTDIR"},
		{syscall.ELOOP, "ELOOP"},
		{syscall.EROFS, "EROFS"},
		{syscall.ENAMETOOLONG, "ENAMETOOLONG"},
		{syscall.EIO, "EIO"},
		{syscall.ENOMEM, "ENOMEM"},
		{syscall.ETXTBSY, "ETXTBSY"},
		{syscall.EINVAL, "EINVAL"},
		{syscall.ENOSPC, "ENOSPC"},
		{syscall.EISDIR, "EISDIR"},
		{syscall.EEXIST, "EEXIST"},
	} {
		if errors.Is(err, candidate.err) {
			return candidate.code
		}
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "ENOENT"
	case errors.Is(err, fs.ErrPermission):
		return "EACCES"
	}
	return ""
}

// Win32 error translation follows libuv uv_translate_sys_error for the codes Node surfaces
// from filesystem calls and process spawn.
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

func libuvErrorCode(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return ""
	}
	return libuvErrorCodes[errno]
}
