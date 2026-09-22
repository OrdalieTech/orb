package native

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SIO_AF_UNIX_GETPEERPID = _WSAIOR(IOC_VENDOR, 256); AF_UNIX on Windows has no peer
// credentials, so the peer process token's user SID is compared with this process's.
const sioAFUnixGetPeerPID = 0x58000100

func sameUser(c *net.UnixConn) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	var pid uint32
	ok := false
	err = raw.Control(func(fd uintptr) {
		var returned uint32
		e := windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0, (*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &returned, nil, 0)
		ok = e == nil && returned == uint32(unsafe.Sizeof(pid)) && pid != 0
	})
	if err != nil || !ok {
		return false
	}
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var token windows.Token
	if windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token) != nil {
		return false
	}
	defer func() { _ = token.Close() }()
	peer, err := token.GetTokenUser()
	return err == nil && windows.EqualSid(peer.User.Sid, self.User.Sid)
}
