package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Files delegates tool writes through os.Root so symlink changes cannot escape
// the writable roots between authorization and the filesystem operation.
// Reads remain unrestricted, matching the native command sandbox.
type Files struct{ WritableRoots []string }

func (f Files) ReadFile(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.ReadFile(name)
}
func (f Files) Access(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, _, err := f.open(name)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	_, err = os.Stat(name)
	return err
}
func (f Files) WriteFile(ctx context.Context, name, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, relative, err := f.open(name)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.WriteFile(relative, []byte(content), 0666)
}
func (f Files) MkdirAll(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, relative, err := f.open(name)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.MkdirAll(relative, 0777)
}
func (f Files) open(name string) (*os.Root, string, error) {
	for _, allowed := range f.WritableRoots {
		relative, err := filepath.Rel(allowed, name)
		if err != nil || !filepath.IsLocal(relative) {
			continue
		}
		root, err := os.OpenRoot(allowed)
		return root, relative, err
	}
	return nil, "", fmt.Errorf("sandbox: write outside writable roots: %s", name)
}
