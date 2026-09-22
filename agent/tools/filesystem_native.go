//go:build !wasm && !windows

package tools

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func accessFile(path string, mode uint32) error { return syscall.Access(path, mode) }

func nativeFilesystemErrorCode(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "ENOENT"
	case errors.Is(err, syscall.EPERM):
		return "EPERM"
	case errors.Is(err, syscall.EACCES):
		return "EACCES"
	case errors.Is(err, fs.ErrPermission):
		return "EACCES"
	case errors.Is(err, syscall.ENOTDIR):
		return "ENOTDIR"
	case errors.Is(err, syscall.ELOOP):
		return "ELOOP"
	case errors.Is(err, syscall.EROFS):
		return "EROFS"
	case errors.Is(err, syscall.ENAMETOOLONG):
		return "ENAMETOOLONG"
	case errors.Is(err, syscall.EIO):
		return "EIO"
	case errors.Is(err, syscall.ENOMEM):
		return "ENOMEM"
	case errors.Is(err, syscall.ETXTBSY):
		return "ETXTBSY"
	case errors.Is(err, syscall.EINVAL):
		return "EINVAL"
	case errors.Is(err, syscall.ENOSPC):
		return "ENOSPC"
	case errors.Is(err, syscall.EISDIR):
		return "EISDIR"
	case errors.Is(err, syscall.EEXIST):
		return "EEXIST"
	}
	return ""
}

func canonicalMutationPath(resolved string) (string, error) {
	realPath, err := filepath.EvalSymlinks(resolved)
	if err == nil {
		return realPath, nil
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return resolved, nil
	}
	if errors.Is(err, syscall.ELOOP) || strings.Contains(err.Error(), "too many links") {
		return "", nodeFilesystemError{code: "ELOOP", operation: "realpath", path: resolved}
	}
	return "", asNodeFilesystemErrorAt("realpath", resolved, err)
}
