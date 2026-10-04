// Package document is the durable-document port every layer shares, ai and
// bridge included; host re-exports it as host.Document for embedders.
package document

import (
	"context"
	"sync"
)

// Document updates must commit before returning; nil deletes the document.
type Document interface {
	Read(context.Context) ([]byte, error)
	Update(context.Context, func([]byte) ([]byte, error)) error
}

// Replace commits data as the whole document; nil deletes it.
func Replace(ctx context.Context, d Document, data []byte) error {
	return d.Update(ctx, func([]byte) ([]byte, error) { return data, nil })
}

// Memory is a Document that lives only as long as the value.
type Memory struct {
	mu   sync.Mutex
	data []byte
}

func (m *Memory) Read(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data, nil
}

func (m *Memory) Update(_ context.Context, change func([]byte) ([]byte, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := change(m.data)
	if err == nil {
		m.data = next
	}
	return err
}
