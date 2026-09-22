package usage

import (
	"context"
	"errors"
	"sync"
	"time"
)

type cached struct {
	snapshot Snapshot
	err      error
	at       time.Time
	done     chan struct{}
}

// Cache bounds quota history to 64 account keys and coalesces concurrent reads.
// Keys are caller-owned account identities or credential digests, never raw credentials.
type Cache struct {
	mu      sync.Mutex
	entries map[string]*cached
}

func (c *Cache) Peek(key string) (Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil || entry.at.IsZero() || entry.err != nil {
		return Snapshot{}, false
	}
	snapshot := entry.snapshot
	snapshot.Windows = append([]Window(nil), snapshot.Windows...)
	return snapshot, true
}
func (c *Cache) Clear() { c.mu.Lock(); clear(c.entries); c.mu.Unlock() }

func (c *Cache) Fetch(ctx context.Context, key string, fetch func(context.Context) (Snapshot, error)) (Snapshot, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*cached{}
	}
	if entry := c.entries[key]; entry != nil {
		if entry.at.IsZero() {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			case <-entry.done:
				return cloneSnapshot(entry.snapshot), entry.err
			}
		}
		ttl := time.Minute
		if entry.err != nil {
			ttl = 10 * time.Second
		}
		if time.Since(entry.at) < ttl {
			c.mu.Unlock()
			return cloneSnapshot(entry.snapshot), entry.err
		}
	}
	if len(c.entries) >= 64 && c.entries[key] == nil {
		oldest := ""
		for id, entry := range c.entries {
			if !entry.at.IsZero() && (oldest == "" || entry.at.Before(c.entries[oldest].at)) {
				oldest = id
			}
		}
		if oldest == "" {
			c.mu.Unlock()
			return Snapshot{}, errors.New("usage refresh is busy")
		}
		delete(c.entries, oldest)
	}
	entry := &cached{done: make(chan struct{})}
	c.entries[key] = entry
	c.mu.Unlock()
	snapshot, err := fetch(ctx)
	c.mu.Lock()
	entry.snapshot, entry.err, entry.at = cloneSnapshot(snapshot), err, time.Now()
	if ctx.Err() != nil && c.entries[key] == entry {
		delete(c.entries, key)
	}
	close(entry.done)
	c.mu.Unlock()
	return snapshot, err
}
func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Windows = append([]Window(nil), snapshot.Windows...)
	return snapshot
}
