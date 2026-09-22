package tools

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

func defaultShellConfig() (ShellConfig, error) {
	var paths []string
	if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
		paths = append(paths, programFiles+`\Git\bin\bash.exe`)
	}
	if programFilesX86 := os.Getenv("ProgramFiles(x86)"); programFilesX86 != "" {
		paths = append(paths, programFilesX86+`\Git\bin\bash.exe`)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return bashShellConfig(path), nil
		}
	}
	if shell := findExecutableOnPath("bash.exe"); shell != "" {
		return bashShellConfig(shell), nil
	}
	searched := make([]string, len(paths))
	for index, path := range paths {
		searched[index] = "  " + path
	}
	return ShellConfig{}, upstreamToolError("No bash shell found. Options:\n" +
		"  1. Install Git for Windows: https://git-scm.com/download/win\n" +
		"  2. Add your bash to PATH (Cygwin, MSYS2, etc.)\n" +
		"  3. Set shellPath in settings.json\n\n" +
		"Searched Git Bash in:\n" + strings.Join(searched, "\n"))
}

// where.exe can list paths that no longer exist, so the first match is verified.
func findExecutableOnPath(executable string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "where", executable)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	output, err := command.Output()
	if err != nil || len(output) == 0 {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	first = strings.TrimSuffix(first, "\r")
	if first == "" {
		return ""
	}
	if _, err := os.Stat(first); err != nil {
		return ""
	}
	return first
}

// Node spawns with windowsHide and no inherited stdio: SW_HIDE plus CREATE_NO_WINDOW, and
// no detached process group on win32.
func configureShellProcess(child *exec.Cmd) {
	child.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func spawnErrorCode(err error) string {
	if code := libuvErrorCode(err); code != "" {
		return code
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
		return "ENOENT"
	}
	return ""
}

// taskkill comes from System32 so cleanup does not depend on PATH; it runs detached and is
// not awaited, matching upstream's fire-and-forget spawn.
func KillProcessTree(pid int) {
	systemRoot, ok := os.LookupEnv("SystemRoot")
	if !ok {
		systemRoot = `C:\Windows`
	}
	command := exec.Command(filepath.Join(systemRoot, "System32", "taskkill.exe"), "/F", "/T", "/PID", strconv.Itoa(pid))
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if command.Start() == nil {
		go func() { _ = command.Wait() }()
	}
}
