package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

type mutationQueueEntry struct {
	done chan struct{}
}

type mutationReservation struct {
	key      string
	previous *mutationQueueEntry
	current  *mutationQueueEntry
	release  sync.Once
}

type mutationReservationContextKey struct{}

var mutationQueues = struct {
	sync.Mutex
	byPath map[string]*mutationQueueEntry
}{byPath: make(map[string]*mutationQueueEntry)}

// WithFileMutationQueue serializes mutations of the same real path while
// allowing operations on unrelated files to proceed concurrently.
func WithFileMutationQueue[T any](filePath string, fn func() (T, error)) (T, error) {
	reservation, err := reserveFileMutation(filePath)
	if err != nil {
		var zero T
		return zero, err
	}
	return runMutationReservation(reservation, fn)
}

func withFileMutationQueueContext[T any](ctx context.Context, filePath string, fn func() (T, error)) (T, error) {
	if ctx != nil {
		if reservation, ok := ctx.Value(mutationReservationContextKey{}).(*mutationReservation); ok {
			return runMutationReservation(reservation, fn)
		}
	}
	return WithFileMutationQueue(filePath, fn)
}

func reserveFileMutation(filePath string) (*mutationReservation, error) {
	// Key resolution is part of registration ordering upstream. Keeping it under
	// the registration lock also prevents symlink aliases from racing to install
	// independent queue tails.
	mutationQueues.Lock()
	key, err := mutationQueueKey(filePath)
	if err != nil {
		mutationQueues.Unlock()
		return nil, err
	}
	previous := mutationQueues.byPath[key]
	current := &mutationQueueEntry{done: make(chan struct{})}
	mutationQueues.byPath[key] = current
	mutationQueues.Unlock()
	return &mutationReservation{key: key, previous: previous, current: current}, nil
}

func runMutationReservation[T any](reservation *mutationReservation, fn func() (T, error)) (T, error) {
	if reservation.previous != nil {
		<-reservation.previous.done
	}
	defer reservation.finish()
	return fn()
}

func (reservation *mutationReservation) cancel() {
	if reservation.previous != nil {
		<-reservation.previous.done
	}
	reservation.finish()
}

func (reservation *mutationReservation) finish() {
	reservation.release.Do(func() {
		close(reservation.current.done)
		mutationQueues.Lock()
		if mutationQueues.byPath[reservation.key] == reservation.current {
			delete(mutationQueues.byPath, reservation.key)
		}
		mutationQueues.Unlock()
	})
}

func mutationQueueKey(filePath string) (string, error) {
	resolved, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}
	if err := nodeNullPathError(resolved); err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(resolved)
	if err == nil {
		return realPath, nil
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return resolved, nil
	}
	if errors.Is(err, syscall.ELOOP) || strings.Contains(err.Error(), "too many links") {
		return "", nodeFilesystemError{code: "ELOOP", operation: "realpath", path: resolved}
	}
	return "", asNodeFilesystemErrorAt("realpath", resolved, err)
}
