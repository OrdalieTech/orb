//go:build !wasm

package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

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
