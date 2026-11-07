// Package schedule lets a model schedule its own turns: a prompt that runs once
// or on a recurrence, in the session that created it or in a new one. Tasks and
// their latest runs live in one document that every holder process (orb chat,
// the Bridge daemon) shares; claiming an occurrence there is what keeps two
// holders from firing it twice.
package schedule

import (
	"context"
	"errors"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/internal/document"
)

// Options configure a Scheduler. Only Store is required.
type Options struct {
	Store document.Document
	// Open opens a stored session (session != "") or a new one in cwd; close
	// disposes it. Nil means this process fires nothing.
	Open func(ctx context.Context, session, cwd string) (*agent.AgentSession, func(), error)
	Zone *time.Location // the owner's default zone; nil is UTC
	// ActiveHours is "08:00-22:00" in Zone; "" is always.
	ActiveHours string
	Timeout     time.Duration // a run's turn; 0 is 30 min
	Now         func() time.Time
	After       func(time.Duration) <-chan time.Time
	Name        string // how this holder shows in runs, e.g. "orb chat"
}

// Scheduler is one holder's view of the schedule document.
type Scheduler struct {
	o Options
}

func New(o Options) *Scheduler { return &Scheduler{o: o} }

// Extension adds the schedule tool, and delivers runs in place to the sessions
// this process has open.
func (s *Scheduler) Extension() extensions.Factory {
	return func(extensions.API) error { return errors.New("schedule: not implemented") }
}

// Run fires due occurrences until ctx ends.
func (s *Scheduler) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
