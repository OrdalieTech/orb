// Package search scans session storages for entries matching a text query.
//
// Upstream made session search a standalone service in v0.84.2
// (packages/agent/src/search/). This is the scanning implementation: it pages
// each session's entries oldest first and matches a projected text. Indexed
// backends implement the same shape by yielding Hits from their own index.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"github.com/OrdalieTech/orb/agent/harness"
)

// Readable is the storage subset a scan reads. Both v4 session storages satisfy it.
type Readable interface {
	FindEntries(query harness.SessionV4EntryQuery) ([]harness.SessionV4Entry, error)
	Label(id string) (string, bool)
}

// Session pairs a session's logical id with the storage to scan.
type Session struct {
	ID       string
	Readable Readable
}

// Hit is one matching entry.
type Hit struct {
	SessionID string
	EntryID   string
	Timestamp int64
	Snippet   string
}

// Options bound a search.
type Options struct {
	// EntryTypes restricts results to these canonical entry types; empty means every type.
	EntryTypes []string
	// Limit stops the search after this many hits; zero or less means unlimited.
	Limit int
	// PageSize is how many entries are read per storage query; zero defaults to 100.
	PageSize int
	// ProjectText builds the text matched and reported as the snippet. The
	// default is the entry's JSON, with the entry's label appended when it has one.
	ProjectText func(sessionID string, entry harness.SessionV4Entry, label string, labeled bool) string
}

func defaultProjection(_ string, entry harness.SessionV4Entry, label string, labeled bool) string {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return ""
	}
	if !labeled {
		return string(encoded)
	}
	return string(encoded) + " " + label
}

// Search yields every entry whose projected text contains text, matched
// case-insensitively with the query trimmed. Sessions are scanned in the order
// the source yields them, entries oldest first. An empty query yields nothing;
// a session id repeated by the source, a storage failure, or a cancelled ctx
// ends the iteration with an error.
func Search(ctx context.Context, sessions iter.Seq2[Session, error], text string, options Options) iter.Seq2[Hit, error] {
	return func(yield func(Hit, error) bool) {
		query := strings.ToLower(strings.TrimSpace(text))
		if query == "" {
			return
		}
		project := options.ProjectText
		if project == nil {
			project = defaultProjection
		}
		pageSize := options.PageSize
		if pageSize <= 0 {
			pageSize = 100
		}
		wanted := make(map[string]bool, len(options.EntryTypes))
		for _, entryType := range options.EntryTypes {
			wanted[entryType] = true
		}
		page := harness.SessionV4EntryQuery{Order: "oldestFirst", Limit: &pageSize}
		if len(options.EntryTypes) == 1 {
			page.Type = options.EntryTypes[0]
		}

		hits := 0
		seen := map[string]bool{}
		for session, err := range sessions {
			if err == nil && seen[session.ID] {
				err = fmt.Errorf("search: duplicate session id %q", session.ID)
			}
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				yield(Hit{}, err)
				return
			}
			seen[session.ID] = true

			for afterSeq := 0; ; {
				page.AfterSeq = &afterSeq
				entries, err := session.Readable.FindEntries(page)
				if err != nil {
					yield(Hit{}, err)
					return
				}
				if len(entries) == 0 {
					break
				}
				for _, entry := range entries {
					if err := ctx.Err(); err != nil {
						yield(Hit{}, err)
						return
					}
					if len(wanted) > 0 && !wanted[entry.Type] {
						continue
					}
					label, labeled := session.Readable.Label(entry.ID)
					snippet := project(session.ID, entry, label, labeled)
					if !strings.Contains(strings.ToLower(snippet), query) {
						continue
					}
					hit := Hit{SessionID: session.ID, EntryID: entry.ID, Timestamp: entry.Timestamp, Snippet: snippet}
					if !yield(hit, nil) {
						return
					}
					if hits++; options.Limit > 0 && hits >= options.Limit {
						return
					}
				}
				if len(entries) < pageSize {
					break
				}
				afterSeq = entries[len(entries)-1].Seq
			}
		}
	}
}
