package native

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SIO_AF_UNIX_GETPEERPID = _WSAIOR(IOC_VENDOR, 256). AF_UNIX on Windows has no peer
// credentials; where it reports the peer PID, that process token's user SID is compared
// with this process's.
const sioAFUnixGetPeerPID = 0x58000100

// sameUser checks the peer's process when Windows reports its PID. It does not for Go's
// sockets on either side (connect or AcceptEx), so otherwise the socket file at path
// must be owned by this user: restrictSocket gave it this owner and a DACL that admits
// only this user, and connecting requires access to the file.
func sameUser(c *net.UnixConn, path string) bool {
	if pid, ok := peerPID(c); ok {
		return processOfCurrentUser(pid)
	}
	return ownedByCurrentUser(path)
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

func currentUser() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

func processOfCurrentUser(pid uint32) bool {
	self, err := currentUser()
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
	return err == nil && windows.EqualSid(peer.User.Sid, self)
}

// openSocketFile opens the socket file itself: it is an AF_UNIX reparse point that
// must not be followed.
func openSocketFile(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

// restrictSocket makes this user the owner of the socket file and replaces its
// inherited ACL (which grants Administrators and SYSTEM, and owns the file by the
// Administrators group for an elevated token) with one that admits only this user.
// Until it runs, the file carries its directory's ACL; the IPC directories live in
// the user's profile.
func restrictSocket(path string) error {
	user, err := currentUser()
	if err != nil {
		return err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	handle, err := openSocketFile(path, windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, user, nil, dacl, nil)
}

// ownedByCurrentUser requires this user's own SID as the socket file's owner, never a
// group such as Administrators that other users' tokens also carry.
func ownedByCurrentUser(path string) bool {
	if path == "" {
		return false
	}
	user, err := currentUser()
	if err != nil {
		return false
	}
	handle, err := openSocketFile(path, windows.READ_CONTROL)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := descriptor.Owner()
	return err == nil && owner != nil && windows.EqualSid(owner, user)
}
