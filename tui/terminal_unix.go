//go:build unix

package tui

import (
	"errors"
	"os"
	"os/signal"
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
