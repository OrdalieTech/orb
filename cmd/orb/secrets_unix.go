//go:build unix

package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/OrdalieTech/orb/internal/document"
)

// A team agent's entrypoint hands it credentials on file descriptors, never
// in its environment or in files its tools could open.

// loadSecrets reads KEY=VALUE lines from ORB_SECRETS_FD into this process's
// environment. /proc/self/environ, which the agent's read tool could open,
// shows only the environment the process started with.
func loadSecrets() error {
	file := inherited("ORB_SECRETS_FD")
	if file == nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	lines := bufio.NewScanner(file)
	lines.Buffer(nil, 1<<20)
	for lines.Scan() {
		if name, value, ok := strings.Cut(lines.Text(), "="); ok && name != "" {
			if err := os.Setenv(name, value); err != nil {
				return err
			}
		}
	}
	return lines.Err()
}

// authDescriptor is auth.json on ORB_AUTH_FD, a file the entrypoint opened as
// root: this process reads and rewrites it, and nothing it starts can open it,
// /proc/self/fd included, since reopening checks the file's own permissions.
// It wraps the descriptor once per process: the state can be opened twice (a
// first start migrates), and a second *os.File would leave the first to be
// collected, closing the descriptor under it.
var authDescriptor = sync.OnceValue(func() document.Document {
	if file := inherited("ORB_AUTH_FD"); file != nil {
		return &fdDocument{file: file}
	}
	return nil
})

// inherited opens the descriptor an environment variable names, closed on exec
// so no child inherits it.
func inherited(name string) *os.File {
	fd, err := strconv.Atoi(os.Getenv(name))
	if err != nil || fd < 3 {
		return nil
	}
	syscall.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), name)
}

type fdDocument struct {
	mu   sync.Mutex
	file *os.File
}

func (document *fdDocument) Read(context.Context) ([]byte, error) {
	document.mu.Lock()
	defer document.mu.Unlock()
	return document.read()
}

func (document *fdDocument) read() ([]byte, error) {
	data, err := io.ReadAll(io.NewSectionReader(document.file, 0, 1<<30))
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return data, err
}

func (document *fdDocument) Update(_ context.Context, update func([]byte) ([]byte, error)) error {
	document.mu.Lock()
	defer document.mu.Unlock()
	current, err := document.read()
	if err != nil {
		return err
	}
	next, err := update(current)
	if err != nil {
		return err
	}
	if _, err := document.file.WriteAt(next, 0); err != nil {
		return err
	}
	if err := document.file.Truncate(int64(len(next))); err != nil {
		return err
	}
	return document.file.Sync()
}
