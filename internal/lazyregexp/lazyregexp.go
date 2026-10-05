// Package lazyregexp compiles package-level regular expressions on first use,
// so a program that links a pattern but never matches it, as every Worker
// isolate does at startup, does not compile it.
package lazyregexp

import (
	"regexp"
	"sync"
)

// New returns expr's compiled form, compiled on the first call; it panics on
// an invalid expr as regexp.MustCompile does.
func New(expr string) func() *regexp.Regexp {
	return sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(expr) })
}
