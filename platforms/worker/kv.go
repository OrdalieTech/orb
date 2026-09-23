// Package worker is the Worker host (DECISIONS.md P2, P10): the ports an Orb
// runtime needs inside a Cloudflare Durable Object or a self-hosted Celld
// cell, backed by the object's key-value storage, plus the assembly that
// resumes one session per object and serves it over kernel RPC frames.
//
// The package is portable; only durable_js.go binds a live Durable Object
// through syscall/js. Everything else runs, and is tested, on every target.
package worker

import (
	"context"
	"strconv"
)

// KV is the storage port: the subset of the Durable Object key-value API
// (ctx.storage get/put/delete/list) that the Worker host persists through.
// Values are opaque bytes; keys are at most 2 KiB.
type KV interface {
	// Get returns the present keys among keys; absent keys are omitted.
	Get(ctx context.Context, keys []string) (map[string][]byte, error)
	// List returns every entry whose key starts with prefix.
	List(ctx context.Context, prefix string) (map[string][]byte, error)
	// Write stores put and removes del as one atomic write where the backend
	// allows it (a Durable Object coalesces writes issued without an await).
	Write(ctx context.Context, put map[string][]byte, del []string) error
}

// chunkSize bounds one stored value well under the Durable Object value
// limits (128 KiB key-value backend, 2 MB SQLite backend), and keeps the
// rewrite of a file's last chunk cheap on every journal append.
const chunkSize = 64 << 10

// Keys within a namespace: "m"+path holds an entry's metadata and
// "c"+path+"#"+index its content chunks. Paths are absolute and never contain
// NUL; the chunk index is decimal, so a key parses back from its last '#'.
func metaKey(namespace, name string) string { return namespace + "m" + name }

func chunkKey(namespace, name string, index int) string {
	return namespace + "c" + name + "#" + strconv.Itoa(index)
}

func chunkCount(size int64) int { return int((size + chunkSize - 1) / chunkSize) }
