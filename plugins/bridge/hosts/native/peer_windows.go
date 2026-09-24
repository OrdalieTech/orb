package native

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SIO_AF_UNIX_GETPEERPID = _WSAIOR(IOC_VENDOR, 256); AF_UNIX on Windows has no peer
// credentials, so the peer process token's user SID is compared with this process's.
const sioAFUnixGetPeerPID = 0x58000100

// sameUser checks the peer's process when Windows reports its PID, which it does for
// accepted sockets. A dialing client that gets no PID instead requires that the socket
// file at path belongs to this user; the listener's bind created it.
func sameUser(c *net.UnixConn, path string) bool {
	if pid, ok := peerPID(c); ok {
		return processOfCurrentUser(pid)
	}
	return path != "" && ownedByCurrentUser(path)
}

func peerPID(c *net.UnixConn) (uint32, bool) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, false
	}
	var pid uint32
	ok := false
	err = raw.Control(func(fd uintptr) {
		var returned uint32
		e := windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0, (*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &returned, nil, 0)
		ok = e == nil && returned == uint32(unsafe.Sizeof(pid)) && pid != 0
	})
	return pid, err == nil && ok
}

func processOfCurrentUser(pid uint32) bool {
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

// ownedByCurrentUser reads the owner of the socket file itself (an AF_UNIX reparse
// point that must not be followed). Files take the creating token's default owner,
// which is the Administrators group for an elevated administrator.
func ownedByCurrentUser(path string) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return false
	}
	token := windows.GetCurrentProcessToken()
	if user, err := token.GetTokenUser(); err == nil && windows.EqualSid(owner, user.User.Sid) {
		return true
	}
	var size uint32
	_ = windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &size)
	if size < uint32(unsafe.Sizeof(uintptr(0))) {
		return false
	}
	buffer := make([]byte, size)
	if windows.GetTokenInformation(token, windows.TokenOwner, &buffer[0], size, &size) != nil {
		return false
	}
	defaultOwner := (*struct{ Owner *windows.SID })(unsafe.Pointer(&buffer[0])).Owner
	return defaultOwner != nil && windows.EqualSid(owner, defaultOwner)
}
