// Package storage defines transactional document persistence for native stores.
package storage

import "context"

// Document updates must commit before returning; nil deletes the document.
type Document interface {
	Read(context.Context) ([]byte, error)
	Update(context.Context, func([]byte) ([]byte, error)) error
}
