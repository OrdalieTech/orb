//go:build linux || darwin

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestPanicGuardChildHelper is the subprocess half of
// TestGoroutinePanicRestoresTerminal: with a pty slave as stdio it enters raw
// mode and panics inside the terminal input goroutine on the first input byte.
func TestPanicGuardChildHelper(t *testing.T) {
	if os.Getenv("ORB_TUI_PANIC_CHILD") == "" {
		t.Skip("subprocess helper for TestGoroutinePanicRestoresTerminal")
	}
	terminal := NewProcessTerminal()
	if err := terminal.Start(func(string) { panic("boom in input goroutine") }, func() {}); err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		os.Exit(3)
	}
	time.Sleep(10 * time.Second)
	os.Exit(4)
}

// Ported from the review's executed pty repro (scratchpad/verify-panicterm):
// before the guard, a panic on a tui-spawned goroutine killed the process with
// the terminal still raw (ICANON/ECHO off, bracketed paste on).
func TestGoroutinePanicRestoresTerminal(t *testing.T) {
	master, slave := openPTY(t)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), ptyIoctlGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	if before.Lflag&unix.ICANON == 0 {
		t.Fatal("pty unexpectedly starts raw")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=TestPanicGuardChildHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "ORB_TUI_PANIC_CHILD=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	drained := make(chan string, 1)
	go func() {
		var builder strings.Builder
		buffer := make([]byte, 4096)
		for {
			count, readErr := master.Read(buffer)
			if count > 0 {
				builder.Write(buffer[:count])
			}
			if readErr != nil {
				drained <- builder.String()
				return
			}
		}
	}()

	rawDeadline := time.Now().Add(5 * time.Second)
	raw := false
	for time.Now().Before(rawDeadline) {
		state, stateErr := unix.IoctlGetTermios(int(slave.Fd()), ptyIoctlGetTermios)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		if state.Lflag&unix.ICANON == 0 {
			raw = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !raw {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatal("child never entered raw mode")
	}
	if _, err := master.WriteString("x"); err != nil {
		t.Fatal(err)
	}

	waitErr := cmd.Wait()
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("child exit = %v, want exit status 1", waitErr)
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), ptyIoctlGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	if before.Lflag&(unix.ICANON|unix.ECHO) != after.Lflag&(unix.ICANON|unix.ECHO) {
		t.Fatalf("terminal not restored after goroutine panic: before=%#x after=%#x", before.Lflag, after.Lflag)
	}

	_ = slave.Close()
	var output string
	select {
	case output = <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("pty output not drained")
	}
	crashIndex := strings.Index(output, "pi exiting due to uncaughtException")
	if crashIndex < 0 || !strings.Contains(output, "boom in input goroutine") {
		t.Fatalf("child output missing crash report: %q", output)
	}
	restoreIndex := strings.Index(output, "\x1b[?2004l")
	if restoreIndex < 0 || restoreIndex > crashIndex {
		t.Fatalf("bracketed-paste restore did not precede crash output: %q", output)
	}
}

// TestPanicGuardMutexChildHelper is the subprocess half of
// TestPanicWhileHoldingMutexRestoresTerminal: it starts a full TUI (so both
// the terminal-level and UI-level crash restores are registered), then on the
// first input byte a guarded goroutine locks the named mutex and panics,
// simulating a panic escaping one of the non-deferred critical sections
// (handleSequence holds terminal.mu, StdinBuffer.Process holds buffer.mu).
func TestPanicGuardMutexChildHelper(t *testing.T) {
	target := os.Getenv("ORB_TUI_PANIC_MUTEX_CHILD")
	if target == "" {
		t.Skip("subprocess helper for TestPanicWhileHoldingMutexRestoresTerminal")
	}
	terminal := NewProcessTerminal()
	ui := NewTUI(terminal)
	trigger := make(chan struct{}, 1)
	ui.AddInputListener(func(string) InputListenerResult {
		select {
		case trigger <- struct{}{}:
		default:
		}
		return InputListenerResult{Consume: true}
	})
	if err := ui.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		os.Exit(3)
	}
	go guarded(func() {
		<-trigger
		if target == "buffer" {
			terminal.mu.Lock()
			buffer := terminal.buffer
			terminal.mu.Unlock()
			buffer.mu.Lock()
		} else {
			terminal.mu.Lock()
		}
		panic("boom while holding " + target + " mutex")
	})()
	time.Sleep(10 * time.Second)
	os.Exit(4)
}

// A panic that escapes a critical section entered without defer leaves the
// mutex locked during unwinding; the crash restore must not block on it, or
// the process hangs with the terminal raw instead of exiting 1.
func TestPanicWhileHoldingMutexRestoresTerminal(t *testing.T) {
	for _, target := range []string{"terminal", "buffer"} {
		t.Run(target, func(t *testing.T) {
			master, slave := openPTY(t)
			before, err := unix.IoctlGetTermios(int(slave.Fd()), ptyIoctlGetTermios)
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=TestPanicGuardMutexChildHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "ORB_TUI_PANIC_MUTEX_CHILD="+target)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			drained := make(chan string, 1)
			go func() {
				var builder strings.Builder
				buffer := make([]byte, 4096)
				for {
					count, readErr := master.Read(buffer)
					if count > 0 {
						builder.Write(buffer[:count])
					}
					if readErr != nil {
						drained <- builder.String()
						return
					}
				}
			}()

			rawDeadline := time.Now().Add(5 * time.Second)
			raw := false
			for time.Now().Before(rawDeadline) {
				state, stateErr := unix.IoctlGetTermios(int(slave.Fd()), ptyIoctlGetTermios)
				if stateErr != nil {
					t.Fatal(stateErr)
				}
				if state.Lflag&unix.ICANON == 0 {
					raw = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !raw {
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
				t.Fatal("child never entered raw mode")
			}
			if _, err := master.WriteString("x"); err != nil {
				t.Fatal(err)
			}

			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			var waitErr error
			select {
			case waitErr = <-waited:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-waited
				t.Fatal("crash restore deadlocked: child never exited after panic")
			}
			exitErr, ok := waitErr.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 1 {
				t.Fatalf("child exit = %v, want exit status 1", waitErr)
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), ptyIoctlGetTermios)
			if err != nil {
				t.Fatal(err)
			}
			if before.Lflag&(unix.ICANON|unix.ECHO) != after.Lflag&(unix.ICANON|unix.ECHO) {
				t.Fatalf("terminal not restored after mutex-holding panic: before=%#x after=%#x", before.Lflag, after.Lflag)
			}

			_ = slave.Close()
			var output string
			select {
			case output = <-drained:
			case <-time.After(2 * time.Second):
				t.Fatal("pty output not drained")
			}
			if !strings.Contains(output, "boom while holding "+target+" mutex") {
				t.Fatalf("child output missing crash report: %q", output)
			}
		})
	}
}

// TestStderrChildHelper is the subprocess half of TestStrayStderrStaysOffTheScreen: with a pty
// as stdio it starts the terminal, and on the first input byte writes what macOS's allocator
// writes under memory pressure to stderr, then panics outside any guard.
func TestStderrChildHelper(t *testing.T) {
	if os.Getenv("ORB_TUI_STDERR_CHILD") == "" {
		t.Skip("subprocess helper for TestStrayStderrStaysOffTheScreen")
	}
	terminal := NewProcessTerminal()
	if err := terminal.Start(func(string) {
		_, _ = unix.Write(2, []byte("orb(1) MallocStackLogging: can't turn off malloc stack logging because it was not enabled.\n"))
		go func() { panic("boom outside the guard") }()
		time.Sleep(10 * time.Second)
	}, func() {}); err != nil {
		os.Exit(3)
	}
	time.Sleep(10 * time.Second)
	os.Exit(4)
}

// What other writers put on stderr while the TUI draws on the same terminal stays off the
// screen, so it cannot leave stray text in the prompt; a crash still reaches the terminal.
func TestStrayStderrStaysOffTheScreen(t *testing.T) {
	master, slave := openPTY(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=TestStderrChildHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "ORB_TUI_STDERR_CHILD=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	drained := make(chan string, 1)
	go func() {
		var builder strings.Builder
		buffer := make([]byte, 4096)
		for {
			count, readErr := master.Read(buffer)
			builder.Write(buffer[:max(count, 0)])
			if readErr != nil {
				drained <- builder.String()
				return
			}
		}
	}()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if state, err := unix.IoctlGetTermios(int(slave.Fd()), ptyIoctlGetTermios); err == nil && state.Lflag&unix.ICANON == 0 {
			break
		}
	}
	if _, err := master.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	_ = slave.Close()
	output := <-drained
	if strings.Contains(output, "MallocStackLogging") || !strings.Contains(output, "boom outside the guard") {
		t.Fatalf("screen = %q; want the crash and not the allocator's report", output)
	}
}
