// Package herdr reports Orb's interactive lifecycle to a containing Herdr pane.
package herdr

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
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
	binaryPath string
	paneID     string
	command    sync.Mutex
	active     atomic.Bool
	claimed    atomic.Bool
	seq        atomic.Uint64
}

// Extension returns the inert-until-TUI compatibility attachment.
func Extension(binaryPath, paneID string) extensions.Factory {
	return func(api extensions.API) error {
		if binaryPath == "" || paneID == "" {
			return errors.New("herdr compatibility requires a binary path and pane id")
		}
		reporter := &reporter{binaryPath: binaryPath, paneID: paneID}
		reporter.seq.Store(uint64(time.Now().UnixNano()))
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, session extensions.Context) (any, error) {
			if interactive(session) {
				reporter.active.Store(true)
				reporter.claimed.Store(true)
				reporter.report(currentState(session))
			}
			return nil, nil
		})
		api.On(extensions.EventAgentStart, func(_ context.Context, _ extensions.Event, session extensions.Context) (any, error) {
			if interactive(session) {
				reporter.report("working")
			}
			return nil, nil
		})
		api.On(extensions.EventAgentSettled, func(_ context.Context, _ extensions.Event, session extensions.Context) (any, error) {
			if interactive(session) && session.IsIdle() {
				reporter.report("idle")
			}
			return nil, nil
		})
		api.On(extensions.EventUIPromptStart, func(_ context.Context, event extensions.Event, session extensions.Context) (any, error) {
			if interactive(session) {
				var message []string
				if title := event.(extensions.UIPromptStartEvent).Title; title != nil {
					message = []string{"--message", *title}
				}
				reporter.report("blocked", message...)
			}
			return nil, nil
		})
		api.On(extensions.EventUIPromptEnd, func(_ context.Context, _ extensions.Event, session extensions.Context) (any, error) {
			if interactive(session) {
				reporter.report(currentState(session))
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

func (reporter *reporter) report(state string, extra ...string) {
	if !reporter.active.Load() {
		return
	}
	args := reporter.args("report-agent", append([]string{"--state", state}, extra...)...)
	go func() {
		reporter.command.Lock()
		defer reporter.command.Unlock()
		if reporter.active.Load() {
			reporter.run(args)
		}
	}()
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
	return append(args, "--seq", strconv.FormatUint(reporter.seq.Add(1), 10))
}

func (reporter *reporter) run(args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = exec.CommandContext(ctx, reporter.binaryPath, args...).Run()
}
