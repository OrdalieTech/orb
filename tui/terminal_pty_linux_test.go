//go:build linux

package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open ptmx: %v", err)
	}
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		_ = unix.Close(masterFD)
		t.Skipf("unlock ptmx: %v", err)
	}
	number, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		_ = unix.Close(masterFD)
		t.Skipf("get pty number: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
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

func TestProcessTerminalPTYInputKittyAndResize(t *testing.T) {
	master, slave := openPTY(t)
	terminal := NewProcessTerminalFiles(slave, slave)
	input := make(chan string, 4)
	resize := make(chan struct{}, 1)
	if err := terminal.Start(func(value string) { input <- value }, func() {
		select {
		case resize <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = terminal.Stop() }()
	if _, err := master.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-input:
		if got != "x" {
			t.Fatalf("input = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("PTY input not delivered")
	}
	if _, err := master.WriteString("\x1b[?"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := master.WriteString("7u"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !terminal.KittyProtocolActive() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !terminal.KittyProtocolActive() {
		t.Fatal("Kitty negotiation did not activate")
	}
	if err := unix.Kill(unix.Getpid(), unix.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resize:
	case <-time.After(time.Second):
		t.Fatal("resize callback not delivered")
	}
}

func TestProcessTerminalPTYRestoresRawModeAfterPanic(t *testing.T) {
	_, slave := openPTY(t)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	terminal := NewProcessTerminalFiles(slave, slave)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		_ = terminal.Run(nil, nil, func() {
			current, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			if current.Lflag&unix.ICANON != 0 || current.Lflag&unix.ECHO != 0 {
				t.Fatalf("terminal not raw: %#v", current)
			}
			panic("boom")
		})
	}()
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	mask := uint32(unix.ICANON | unix.ECHO)
	if before.Lflag&mask != after.Lflag&mask {
		t.Fatalf("terminal flags not restored: before=%#x after=%#x", before.Lflag, after.Lflag)
	}
}

func TestProcessTerminalPTYTerminalProfiles(t *testing.T) {
	profiles := []struct {
		name, term, program, colorTerm string
	}{
		{name: "iterm2", term: "xterm-256color", program: "iTerm.app", colorTerm: "truecolor"},
		{name: "kitty", term: "xterm-kitty", program: "kitty", colorTerm: "truecolor"},
		{name: "gnome-terminal", term: "xterm-256color", program: "gnome-terminal", colorTerm: "truecolor"},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			t.Setenv("TERM", profile.term)
			t.Setenv("TERM_PROGRAM", profile.program)
			t.Setenv("COLORTERM", profile.colorTerm)
			t.Setenv("COLUMNS", "91")
			t.Setenv("LINES", "37")
			master, slave := openPTY(t)
			output, err := os.CreateTemp(t.TempDir(), "terminal-output")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = output.Close() })
			terminal := NewProcessTerminalFiles(slave, output)
			t.Cleanup(func() { _ = terminal.Stop() })
			input := make(chan string, 1)
			if err := terminal.Start(func(value string) { input <- value }, func() {}); err != nil {
				t.Fatal(err)
			}
			if terminal.Columns() != 91 || terminal.Rows() != 37 {
				t.Fatalf("fallback size = %dx%d", terminal.Columns(), terminal.Rows())
			}
			if _, err := master.WriteString("q"); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-input:
				if got != "q" {
					t.Fatalf("input = %q", got)
				}
			case <-time.After(time.Second):
				t.Fatal("profile input not delivered")
			}
			terminal.DrainInput(100*time.Millisecond, 10*time.Millisecond)
			if err := terminal.Stop(); err != nil {
				t.Fatal(err)
			}
			outputBytes, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			written := string(outputBytes)
			for _, sequence := range []string{"\x1b[?2004h", kittyKeyboardQuery, "\x1b[<u", "\x1b[?2004l"} {
				if !strings.Contains(written, sequence) {
					t.Fatalf("terminal protocol output missing %q: %q", sequence, written)
				}
			}
		})
	}
}
