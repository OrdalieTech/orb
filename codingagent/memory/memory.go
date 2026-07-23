// Package memory defines pigo's durable memory SDK seam. It is a pigo-original
// addition with no upstream mirror.
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

type Store interface {
	Append(context.Context, Item) (string, error)
	Get(context.Context, string) (Item, error)
	Query(context.Context, Filter) ([]Item, error)
	Delete(context.Context, string) error
}

type SemanticSearcher interface {
	Search(context.Context, string, int) ([]Scored, error)
}

type Scored struct {
	Item
	Score float64
}
