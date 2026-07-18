package ai

import (
	"errors"
	"sync"
)

// SessionResourceCleanup releases provider resources associated with one
// session. An empty session ID means release every cached session resource.
type SessionResourceCleanup func(sessionID string) error

var sessionResourceRegistry struct {
	sync.Mutex
	cleanups []SessionResourceCleanup
}

// RegisterSessionResourceCleanup installs a provider cleanup and returns an
// idempotent function that unregisters it.
func RegisterSessionResourceCleanup(cleanup SessionResourceCleanup) func() {
	if cleanup == nil {
		return func() {}
	}
	sessionResourceRegistry.Lock()
	index := len(sessionResourceRegistry.cleanups)
	sessionResourceRegistry.cleanups = append(sessionResourceRegistry.cleanups, cleanup)
	sessionResourceRegistry.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			sessionResourceRegistry.Lock()
			sessionResourceRegistry.cleanups[index] = nil
			sessionResourceRegistry.Unlock()
		})
	}
}

// CleanupSessionResources runs registered provider cleanups in registration
// order. Omitting sessionID releases resources for every session.
func CleanupSessionResources(sessionID ...string) error {
	id := ""
	if len(sessionID) > 0 {
		id = sessionID[0]
	}
	sessionResourceRegistry.Lock()
	cleanups := append([]SessionResourceCleanup(nil), sessionResourceRegistry.cleanups...)
	sessionResourceRegistry.Unlock()

	var failures []error
	for _, cleanup := range cleanups {
		if cleanup == nil {
			continue
		}
		if err := cleanup(id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
