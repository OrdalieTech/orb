package tui

import (
	"fmt"
	"os"
	"runtime/debug"
	"sync"
)

// A panic on any goroutine kills the process without unwinding main, so the
// raw-mode/alt-screen state set by Start would never be undone. Every
// goroutine this package spawns is wrapped in guarded, which restores the
// terminal before the crash output, mirroring upstream's uncaughtException ->
// ui.stop() handler (interactive-mode.ts uncaughtCrash).
var (
	crashMu       sync.Mutex
	crashRestores = map[uint64]func(){}
	crashNextID   uint64
	// crashExit is swapped by tests; production always prints then exits 1.
	crashExit = func(message string) {
		fmt.Fprint(os.Stderr, message)
		os.Exit(1)
	}
)

// registerCrashRestore records a terminal-restore callback to run if a
// guarded goroutine panics, and returns its unregister function.
func registerCrashRestore(restore func()) func() {
	crashMu.Lock()
	crashNextID++
	id := crashNextID
	crashRestores[id] = restore
	crashMu.Unlock()
	return func() {
		crashMu.Lock()
		delete(crashRestores, id)
		crashMu.Unlock()
	}
}

func guarded(run func()) func() {
	return func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			stack := debug.Stack()
			crashMu.Lock()
			restores := make([]func(), 0, len(crashRestores))
			for _, restore := range crashRestores {
				restores = append(restores, restore)
			}
			crashRestores = map[uint64]func(){}
			crashMu.Unlock()
			for _, restore := range restores {
				func() {
					defer func() { _ = recover() }()
					restore()
				}()
			}
			crashExit(fmt.Sprintf("pi exiting due to uncaughtException:\npanic: %v\n\n%s", recovered, stack))
		}()
		run()
	}
}
