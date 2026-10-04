//go:build js || wasip1

package codexsessions

import "context"

// Wasm hosts have no Codex CLI state to open.
func lookup(context.Context, string, string) (thread, error) { return thread{}, ErrNoCodexSession }
