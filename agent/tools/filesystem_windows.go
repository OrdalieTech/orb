package tools

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/OrdalieTech/orb/internal/proctree"
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
	if code := proctree.SpawnErrorCode(err); code != "" {
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
