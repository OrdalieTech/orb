//go:build unix

package tui

import (
	"errors"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const nativeShiftEnterDetection = false

func nativeShiftPressed() bool { return false }

// terminalReader reads a duplicate of the input descriptor so Stop can close
// it without closing the caller's file.
type terminalReader struct {
	file   *os.File
	poll   []unix.PollFd
	buffer []byte
}

func openTerminalReader(input, _ *os.File) (*terminalReader, error) {
	duplicate, err := unix.Dup(int(input.Fd()))
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicate), input.Name()+"-pi-read")
	return &terminalReader{file: file, poll: []unix.PollFd{{Fd: int32(file.Fd()), Events: unix.POLLIN}}, buffer: make([]byte, 4096)}, nil
}

func (reader *terminalReader) wait(timeout time.Duration) (bool, error) {
	ready, err := unix.Poll(reader.poll, int(timeout/time.Millisecond))
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return ready > 0, err
}

func (reader *terminalReader) read() (string, error) {
	count, err := reader.file.Read(reader.buffer)
	return string(reader.buffer[:max(count, 0)]), err
}

func (reader *terminalReader) close() error { return reader.file.Close() }

// quietStderr points the process's stderr at /dev/null while the TUI draws on output, when
// stderr is that same terminal: what other writers put there lands in the drawn screen and stays
// until a full redraw. macOS's allocator reports memory pressure there ("MallocStackLogging:
// can't turn off malloc stack logging…"), and children inherit it. Fatal errors still reach the
// terminal. It returns the restore.
func quietStderr(output *os.File) func() {
	var screen, stderr unix.Stat_t
	if unix.Fstat(int(output.Fd()), &screen) != nil || unix.Fstat(2, &stderr) != nil ||
		screen.Mode&unix.S_IFMT != unix.S_IFCHR || stderr.Mode&unix.S_IFMT != unix.S_IFCHR || screen.Rdev != stderr.Rdev {
		return func() {}
	}
	saved, err := unix.Dup(2)
	if err != nil {
		return func() {}
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err == nil {
		err = unix.Dup2(int(null.Fd()), 2)
		_ = null.Close()
	}
	if err != nil {
		_ = unix.Close(saved)
		return func() {}
	}
	terminal := os.NewFile(uintptr(saved), "stderr")
	_ = debug.SetCrashOutput(terminal, debug.CrashOptions{})
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unix.Dup2(saved, 2)
			_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
			_ = terminal.Close()
		})
	}
}

func watchTerminalResize(_ *ProcessTerminal, notify func()) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	go guarded(func() {
		for range signals {
			notify()
		}
	})()
	return func() {
		signal.Stop(signals)
		close(signals)
	}
}
