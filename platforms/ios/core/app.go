//go:build ios && appshell

// The iOS app's orb: cmd/orb built as a c-archive with this file laid into it (platforms/ios/build.sh,
// go build -overlay -tags appshell), since iOS runs no binary of its own; on its own it is no
// package, and its cgo stays in the app shell (P2). It is the CLI the other apps run, in process:
// `orb bridge run` for this device's Bridge, then `orb app` over the app's two pipes.
package main

import "C"

import (
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The Bridge profile's sockets must fit in sun_path (103 bytes): a short profile name under the
// app's Library fits on a device; the simulator's container is too deep, and unsandboxed, so /tmp.
const profile = "p"

var bridged bool

//export OrbApp
func OrbApp(home *C.char, in, out C.int) C.int {
	stdin, stdout := os.NewFile(uintptr(in), "app-in"), os.NewFile(uintptr(out), "app-out")
	defer func() { _ = stdin.Close(); _ = stdout.Close() }()
	root := C.GoString(home)
	bridge := filepath.Join(root, "Library", "b")
	if len(bridge)+len("/"+profile+"/attach.sock") > 103 {
		bridge = fmt.Sprintf("/tmp/orb-%08x", crc32.ChecksumIEEE([]byte(root)))
	}
	if !bridged {
		bridged = true
		_ = os.Setenv("HOME", filepath.Join(root, "Library"))
		_ = os.Setenv("ORB_BRIDGE_HOME", bridge)
		setEntryIdentity()
		go func() {
			quiet := cliStreams{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: os.Stderr}
			for {
				runNativeCLI(context.Background(), []string{"bridge", "run", "--profile", profile}, quiet)
				time.Sleep(time.Second)
			}
		}()
	}
	// `orb app` reaches the Bridge it finds running; it cannot start one, which execs.
	for range 300 {
		if c, err := net.Dial("unix", filepath.Join(bridge, profile, "admin.sock")); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return C.int(runNativeCLI(context.Background(), []string{"app", "--profile", profile, "--name", "this iPhone"}, cliStreams{Stdin: stdin, Stdout: stdout, Stderr: os.Stderr}))
}
