//go:build darwin

package tui

import (
	"os"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

const ptyIoctlGetTermios = unix.TIOCGETA

func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open ptmx: %v", err)
	}
	if err := unix.IoctlSetInt(masterFD, unix.TIOCPTYGRANT, 0); err != nil {
		_ = unix.Close(masterFD)
		t.Skipf("grant pty: %v", err)
	}
	if err := unix.IoctlSetInt(masterFD, unix.TIOCPTYUNLK, 0); err != nil {
		_ = unix.Close(masterFD)
		t.Skipf("unlock pty: %v", err)
	}
	var name [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(masterFD), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		_ = unix.Close(masterFD)
		t.Skipf("pty slave name: %v", errno)
	}
	length := 0
	for length < len(name) && name[length] != 0 {
		length++
	}
	slave, err := os.OpenFile(string(name[:length]), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = unix.Close(masterFD)
		t.Skipf("open pty slave: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "ptmx")
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})
	return master, slave
}
