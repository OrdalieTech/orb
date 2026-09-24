package bridge

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func sameUser(c *net.UnixConn, _ string) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	ok := false
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		ok = e == nil && cred.Uid == uint32(os.Geteuid())
	})
	return err == nil && ok
}

func restrictSocket(path string) error { return os.Chmod(path, 0600) }
