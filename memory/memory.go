// Package memory defines orb's root-level durable memory SDK seam. It is a
// orb-original addition with no upstream mirror and is importable standalone.
package memory

import (
	"context"
	"time"
)

type Item struct {
	ID      string            `json:"id"`
	Time    time.Time         `json:"time"`
	Content string            `json:"content"`
	Tags    []string          `json:"tags,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}

type Filter struct {
	Tags     []string
	Since    time.Time
	Until    time.Time
	Contains string
	Limit    int
}

// Store is a tenant-scoped durable memory backend. Implementations must accept
// concurrent calls. Query returns newest items first, treats Tags as an AND
// filter, matches Contains case-insensitively, and honors positive Limit values.
type Store interface {
	Append(context.Context, Item) (string, error)
	Get(context.Context, string) (Item, error)
	Query(context.Context, Filter) ([]Item, error)
	Delete(context.Context, string) error
}

// TransactionalStore makes compound profile mutations atomic across runtime
// instances and processes. The callback must use only the supplied Store.
type TransactionalStore interface {
	Store
	Transact(context.Context, func(Store) error) error
}

// SemanticSearcher returns highest-scoring items first and honors positive
// limit values.
type SemanticSearcher interface {
	Search(context.Context, string, int) ([]Scored, error)
}

type Scored struct {
	Item
	Score float64
}
