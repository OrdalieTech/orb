// Package herdr reports Orb's interactive lifecycle to a containing Herdr pane.
package herdr

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
)

const (
	reportSource = "custom:orb"
	agentLabel   = "orb"
)

type reporter struct {
	binaryPath, paneID string
	resumeArgs         []string
	command            sync.Mutex
	active             atomic.Bool
	claimed            atomic.Bool
	seq                atomic.Uint64
	pendingMu          sync.Mutex
	pending            []string
	draining           bool
	stateOnly          bool // Older Herdr CLIs reject the trailing resume command.
	hasResume          bool // Guarded by command; a timed-out report may still have applied.
}

// Extension returns an inert-until-TUI native Orb attachment. resumeArgs are
// non-secret CLI options explicitly selected by the assembly, never raw argv.
func Extension(binaryPath, paneID string, resumeArgs ...string) extensions.Factory {
	resumeArgs = slices.Clone(resumeArgs)
	return func(api extensions.API) error {
		if binaryPath == "" || paneID == "" {
			return errors.New("herdr integration requires a binary path and pane id")
		}
		// A previous process or registry may have left a resume command behind.
		reporter := &reporter{binaryPath: binaryPath, paneID: paneID, resumeArgs: resumeArgs, hasResume: true}
		reporter.seq.Store(uint64(time.Now().UnixNano()))
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, session extensions.Context) (any, error) {
			if interactive(session) {
				reporter.active.Store(true)
				reporter.claimed.Store(true)
				reporter.report(session, currentState(session))
			}
			return nil, nil
		})
		api.On(extensions.EventAgentStart, func(_ context.Context, _ extensions.Event, session extensions.Context) (any, error) {
			if interactive(session) {
				reporter.report(session, "working")
			}
			return nil, nil
		})
		api.On(extensions.EventAgentSettled, func(_ context.Context, _ extensions.Event, session extensions.Context) (any, error) {
			if interactive(session) && session.IsIdle() {
				reporter.report(session, "idle")
			}
			return nil, nil
		})
		api.On(extensions.EventUIPromptStart, func(_ context.Context, event extensions.Event, session extensions.Context) (any, error) {
			if interactive(session) {
				var message []string
				if title := event.(extensions.UIPromptStartEvent).Title; title != nil {
					message = []string{"--message", *title}
				}
				reporter.report(session, "blocked", message...)
			}
			return nil, nil
		})
		api.On(extensions.EventUIPromptEnd, func(_ context.Context, _ extensions.Event, session extensions.Context) (any, error) {
			if interactive(session) {
				reporter.report(session, currentState(session))
			}
			return nil, nil
		})
		api.On(extensions.EventSessionShutdown, func(_ context.Context, event extensions.Event, _ extensions.Context) (any, error) {
			if event.(extensions.SessionShutdownEvent).Reason == extensions.SessionShutdownQuit {
				reporter.release()
			} else {
				reporter.active.Store(false)
			}
			return nil, nil
		})
		return nil
	}
}

func interactive(session extensions.Context) bool {
	return session.Mode() == extensions.ModeTUI && session.HasUI()
}
func currentState(session extensions.Context) string {
	if session.IsIdle() {
		return "idle"
	}
	return "working"
}

func (reporter *reporter) report(session extensions.Context, state string, extra ...string) {
	reporter.pendingMu.Lock()
	defer reporter.pendingMu.Unlock()
	args := reporter.args("report-agent", append([]string{"--state", state}, extra...)...)
	if manager := session.SessionManager(); manager != nil && manager.IsPersisted() {
		var resume []string
		if path := manager.GetSessionFile(); filepath.IsAbs(path) {
			args = append(args, "--agent-session-path", path)
			resume = append([]string{"orb", "--pi-files"}, reporter.resumeArgs...)
			resume = append(resume, "--session", path)
		} else if id := manager.GetSessionID(); id != "" && manager.GetSessionFile() == "" {
			args = append(args, "--agent-session-id", id)
			resume = append([]string{"orb"}, reporter.resumeArgs...)
			resume = append(resume, "--session", id)
		}
		// Herdr rejects the entire report if argv contains an apostrophe or control
		// character. Such sessions still report lifecycle, without unsafe restore.
		if len(resume) != 0 && validResume(resume) {
			args = append(append(args, "--"), resume...)
		}
	}
	reporter.enqueueLocked(args)
}

func validResume(args []string) bool {
	size := 0
	for _, arg := range args {
		size += len(arg)
		if strings.ContainsAny(arg, "'\r\n\t") || strings.ContainsFunc(arg, func(r rune) bool { return r < 32 || r >= 127 && r <= 159 }) {
			return false
		}
	}
	return len(args) <= 64 && size <= 8192
}

// At most one subprocess and one pending state exist; bursts replace stale
// pending reports rather than growing a goroutine queue.
func (reporter *reporter) enqueueLocked(args []string) {
	if !reporter.active.Load() {
		return
	}
	reporter.pending = args
	if reporter.draining {
		return
	}
	reporter.draining = true
	go reporter.drain()
}

func (reporter *reporter) drain() {
	for {
		reporter.command.Lock()
		reporter.pendingMu.Lock()
		args := reporter.pending
		reporter.pending = nil
		if !reporter.active.Load() || len(args) == 0 {
			reporter.draining = false
			reporter.pendingMu.Unlock()
			reporter.command.Unlock()
			return
		}
		reporter.pendingMu.Unlock()
		reporter.run(args)
		reporter.command.Unlock()
	}
}

func (reporter *reporter) release() {
	reporter.active.Store(false)
	if !reporter.claimed.Swap(false) {
		return
	}
	reporter.command.Lock()
	defer reporter.command.Unlock()
	reporter.run(reporter.args("release-agent"))
}

func (reporter *reporter) args(command string, extra ...string) []string {
	args := []string{"pane", command, reporter.paneID, "--source", reportSource, "--agent", agentLabel}
	args = append(args, extra...)
	return append(args, "--seq", "0")
}

func (reporter *reporter) run(args []string) {
	separator := slices.Index(args, "--")
	if reporter.stateOnly && separator >= 0 {
		args = args[:separator]
		separator = -1
	}
	if args[1] == "report-agent" && separator < 0 && reporter.hasResume {
		// Omitting resume_argv does not clear it in Herdr. Release first when
		// switching to an ephemeral or unrepresentable session, then reclaim.
		if _, err := reporter.invokeNext(reporter.args("release-agent")); err != nil {
			return
		}
		reporter.hasResume = false
	}
	if separator >= 0 {
		reporter.hasResume = true
	}
	output, err := reporter.invokeNext(args)
	if err == nil && args[1] == "release-agent" {
		reporter.hasResume = false
	}
	if err != nil && !reporter.stateOnly && separator >= 0 && (strings.Contains(string(output), "unexpected argument") || strings.Contains(string(output), "unknown option")) {
		reporter.stateOnly = true
		reporter.hasResume = false
		_, _ = reporter.invokeNext(args[:separator])
	}
}

// Assign sequences at dispatch, not enqueue: a reset and coalesced states must
// be strictly ordered even when newer events arrive during a subprocess.
func (reporter *reporter) invokeNext(args []string) ([]byte, error) {
	args = slices.Clone(args)
	if index := slices.Index(args, "--seq"); index >= 0 {
		args[index+1] = strconv.FormatUint(reporter.seq.Add(1), 10)
	}
	return reporter.invoke(args)
}

func (reporter *reporter) invoke(args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	return exec.CommandContext(ctx, reporter.binaryPath, args...).CombinedOutput()
}
