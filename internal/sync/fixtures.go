package upstreamsync

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

type fixtureSnapshot struct {
	size int64
	hash string
}

func compareFixtures(committed, generated string) ([]FixtureChange, error) {
	oldFiles, err := snapshotFiles(committed)
	if err != nil {
		return nil, fmt.Errorf("snapshot committed fixtures: %w", err)
	}
	newFiles, err := snapshotFiles(generated)
	if err != nil {
		return nil, fmt.Errorf("snapshot generated fixtures: %w", err)
	}
	paths := make([]string, 0, len(oldFiles)+len(newFiles))
	seen := make(map[string]struct{}, len(oldFiles)+len(newFiles))
	for filename := range oldFiles {
		seen[filename] = struct{}{}
		paths = append(paths, filename)
	}
	for filename := range newFiles {
		if _, exists := seen[filename]; !exists {
			paths = append(paths, filename)
		}
	}
	sort.Strings(paths)
	changes := make([]FixtureChange, 0)
	for _, filename := range paths {
		old, oldExists := oldFiles[filename]
		current, currentExists := newFiles[filename]
		if oldExists && currentExists && old.hash == current.hash {
			continue
		}
		change := FixtureChange{Path: filepath.ToSlash(filename), OldHash: "-", NewHash: "-"}
		switch {
		case !oldExists:
			change.Status = "A"
			change.NewBytes = current.size
			change.NewHash = current.hash
		case !currentExists:
			change.Status = "D"
			change.OldBytes = old.size
			change.OldHash = old.hash
		default:
			change.Status = "M"
			change.OldBytes = old.size
			change.NewBytes = current.size
			change.OldHash = old.hash
			change.NewHash = current.hash
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func snapshotFiles(root string) (map[string]fixtureSnapshot, error) {
	files := make(map[string]fixtureSnapshot)
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("fixture %s is not a regular file", filename)
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		files[relative] = fixtureSnapshot{size: info.Size(), hash: fmt.Sprintf("%x", digest[:6])}
		return nil
	})
	return files, err
}

func prepareConformanceCopy(root, fixtures string, lock Lock) (string, func(), error) {
	temporary, err := os.MkdirTemp("", "pigo-sync-test-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(temporary) }
	copyRoot := filepath.Join(temporary, "repo")
	if err := copyProject(root, copyRoot); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := writeLock(filepath.Join(copyRoot, "UPSTREAM.lock"), lock); err != nil {
		cleanup()
		return "", nil, err
	}
	destination := filepath.Join(copyRoot, "conformance", "fixtures")
	if err := os.RemoveAll(destination); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := copyTree(fixtures, destination); err != nil {
		cleanup()
		return "", nil, err
	}
	return copyRoot, cleanup, nil
}

func copyProject(source, destination string) error {
	skip := map[string]struct{}{`.git`: {}, `.tools`: {}, `.upstream`: {}}
	return copyTreeWithFilter(source, destination, func(relative string, entry fs.DirEntry) bool {
		if filepath.Dir(relative) != "." {
			return false
		}
		_, excluded := skip[entry.Name()]
		return excluded
	})
}

func copyTree(source, destination string) error {
	return copyTreeWithFilter(source, destination, nil)
}

func copyTreeWithFilter(source, destination string, skip func(string, fs.DirEntry) bool) error {
	return filepath.WalkDir(source, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		if relative == "." {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			switch {
			case entry.IsDir():
				return os.MkdirAll(destination, info.Mode().Perm())
			case info.Mode()&os.ModeSymlink != 0:
				linkTarget, err := os.Readlink(filename)
				if err != nil {
					return err
				}
				return os.Symlink(linkTarget, destination)
			case info.Mode().IsRegular():
				return copyFile(filename, destination, info.Mode().Perm())
			default:
				return fmt.Errorf("cannot copy special file %s", filename)
			}
		}
		if skip != nil && skip(relative, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(filename)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		case info.Mode().IsRegular():
			return copyFile(filename, target, info.Mode().Perm())
		default:
			return fmt.Errorf("cannot copy special file %s", filename)
		}
	})
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		_ = input.Close()
		return err
	}
	_, copyErr := io.Copy(output, input)
	outputCloseErr := output.Close()
	inputCloseErr := input.Close()
	if copyErr != nil {
		return copyErr
	}
	if outputCloseErr != nil {
		return outputCloseErr
	}
	return inputCloseErr
}

func promote(root, candidate string, lock Lock, green, descendant bool) error {
	if !green {
		return fmt.Errorf("%w: conformance is red", ErrPromotionUnsafe)
	}
	if !descendant {
		return fmt.Errorf("%w: target is not a descendant of the pinned commit", ErrPromotionUnsafe)
	}
	fixtures := filepath.Join(root, "conformance", "fixtures")
	parent := filepath.Dir(fixtures)
	staged, err := os.MkdirTemp(parent, ".fixtures-next-*")
	if err != nil {
		return err
	}
	removeStaged := true
	defer func() {
		if removeStaged {
			_ = os.RemoveAll(staged)
		}
	}()
	if err := copyTreeContents(candidate, staged); err != nil {
		return fmt.Errorf("stage generated fixtures: %w", err)
	}
	backup, err := os.MkdirTemp(parent, ".fixtures-backup-*")
	if err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	if err := os.Rename(fixtures, backup); err != nil {
		return fmt.Errorf("backup committed fixtures: %w", err)
	}
	restore := true
	defer func() {
		if restore {
			_ = os.RemoveAll(fixtures)
			_ = os.Rename(backup, fixtures)
		}
	}()
	if err := os.Rename(staged, fixtures); err != nil {
		return fmt.Errorf("install generated fixtures: %w", err)
	}
	removeStaged = false
	if err := writeLock(filepath.Join(root, "UPSTREAM.lock"), lock); err != nil {
		return err
	}
	restore = false
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove fixture backup: %w", err)
	}
	return nil
}

func copyTreeContents(source, destination string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
