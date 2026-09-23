package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/platforms/memory"
)

// FileSystem is the persisted FS port: a platforms/memory working copy whose
// every mutation is written through to KV before it returns, so an evicted
// or restarted object reopens the same tree. Reads never touch storage.
type FileSystem struct {
	*memory.FileSystem
	kv        KV
	namespace string

	// mu serializes each mutation with its write-through; entries mirrors
	// what storage holds (the root chain is implicit and never stored).
	mu      sync.Mutex
	entries map[string]*entry
	// failed latches a write-through failure: the working copy is then ahead
	// of storage, so later mutations fail until the object restarts.
	failed error
	// restoring is the modification time memory stamps while reopening.
	restoring time.Time
}

type entry struct {
	Dir   bool    `json:"dir,omitempty"`
	Size  int64   `json:"size,omitempty"`
	MTime float64 `json:"mtime"`
	// tail caches the last partial chunk of a file once it is appended to,
	// so journal appends rewrite one chunk without reading storage.
	tail []byte
}

// OpenFileSystem restores the tree stored under namespace. options configure
// the working copy; its clock is kept for new writes.
func OpenFileSystem(ctx context.Context, kv KV, namespace string, options memory.Options) (*FileSystem, error) {
	fsys := &FileSystem{kv: kv, namespace: namespace, entries: map[string]*entry{}}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	options.Now = func() time.Time {
		if !fsys.restoring.IsZero() {
			return fsys.restoring
		}
		return now()
	}
	fsys.FileSystem = memory.New(options)
	for dir := fsys.WorkingDirectory(); ; dir = path.Dir(dir) {
		fsys.entries[dir] = &entry{Dir: true}
		if dir == "/" {
			break
		}
	}
	if err := fsys.restore(ctx); err != nil {
		return nil, err
	}
	return fsys, nil
}

func (fsys *FileSystem) restore(ctx context.Context) error {
	metas, err := fsys.kv.List(ctx, fsys.namespace+"m")
	if err != nil {
		return fmt.Errorf("worker: list %s: %w", fsys.namespace, err)
	}
	stored := make(map[string]*entry, len(metas))
	for key, value := range metas {
		var restored entry
		if err := json.Unmarshal(value, &restored); err != nil {
			return fmt.Errorf("worker: corrupt entry %q: %w", key, err)
		}
		stored[strings.TrimPrefix(key, fsys.namespace+"m")] = &restored
	}
	defer func() { fsys.restoring = time.Time{} }()
	// Sorted order restores every directory before its children.
	for _, name := range slices.Sorted(maps.Keys(stored)) {
		restored := stored[name]
		fsys.restoring = time.UnixMicro(int64(restored.MTime * 1000))
		if restored.Dir {
			err = fsys.FileSystem.CreateDir(ctx, name, true)
		} else {
			var data []byte
			if data, err = fsys.readChunks(ctx, name, restored.Size); err == nil {
				err = fsys.FileSystem.WriteFile(ctx, name, data)
			}
		}
		if err != nil {
			return fmt.Errorf("worker: restore %s: %w", name, err)
		}
		fsys.entries[name] = restored
	}
	return nil
}

func (fsys *FileSystem) readChunks(ctx context.Context, name string, size int64) ([]byte, error) {
	keys := make([]string, chunkCount(size))
	for index := range keys {
		keys[index] = chunkKey(fsys.namespace, name, index)
	}
	values, err := fsys.kv.Get(ctx, keys)
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, size)
	for _, key := range keys {
		chunk, ok := values[key]
		if !ok {
			return nil, fmt.Errorf("missing chunk %q", key)
		}
		data = append(data, chunk...)
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("stored %d bytes, metadata says %d", len(data), size)
	}
	return data, nil
}

// batch collects one mutation's write-through.
type batch struct {
	fsys *FileSystem
	put  map[string][]byte
	del  map[string]bool
}

func (fsys *FileSystem) batch() *batch {
	return &batch{fsys: fsys, put: map[string][]byte{}, del: map[string]bool{}}
}

func (b *batch) set(key string, value []byte) {
	b.put[key] = value
	delete(b.del, key)
}

func (b *batch) remove(key string) {
	if _, pending := b.put[key]; !pending {
		b.del[key] = true
	}
}

func (b *batch) meta(name string, stored *entry) {
	data, _ := json.Marshal(stored)
	b.set(metaKey(b.fsys.namespace, name), data)
	b.fsys.entries[name] = stored
}

// forget drops name's keys; chunks a later set rewrites are kept.
func (b *batch) forget(name string) {
	stored := b.fsys.entries[name]
	delete(b.fsys.entries, name)
	b.remove(metaKey(b.fsys.namespace, name))
	for index := range chunkCount(stored.Size) {
		b.remove(chunkKey(b.fsys.namespace, name, index))
	}
}

// file stores data as name's whole content, replacing any previous chunks.
func (b *batch) file(name string, data []byte, mtime float64) {
	if previous := b.fsys.entries[name]; previous != nil {
		for index := chunkCount(int64(len(data))); index < chunkCount(previous.Size); index++ {
			b.remove(chunkKey(b.fsys.namespace, name, index))
		}
	}
	for index := range chunkCount(int64(len(data))) {
		b.set(chunkKey(b.fsys.namespace, name, index), data[index*chunkSize:min(len(data), (index+1)*chunkSize)])
	}
	b.meta(name, &entry{Size: int64(len(data)), MTime: mtime})
}

// parents stores the directories above name that storage does not hold yet.
func (b *batch) parents(ctx context.Context, name string) error {
	for dir := path.Dir(name); b.fsys.entries[dir] == nil; dir = path.Dir(dir) {
		info, err := b.fsys.FileInfo(ctx, dir)
		if err != nil {
			return err
		}
		b.meta(dir, &entry{Dir: true, MTime: info.MTimeMS})
	}
	return nil
}

func (b *batch) commit(ctx context.Context) error {
	if len(b.put) == 0 && len(b.del) == 0 {
		return nil
	}
	return b.fsys.kv.Write(ctx, b.put, slices.Sorted(maps.Keys(b.del)))
}

// persist runs one mutation on the working copy and writes its effect
// through. Storage writes outlive the caller's cancellation: once memory has
// changed, storage must follow.
func (fsys *FileSystem) persist(ctx context.Context, mutate func() error, record func(context.Context, *batch) error) error {
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	if fsys.failed != nil {
		return fsys.failed
	}
	if err := mutate(); err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	changes := fsys.batch()
	err := record(ctx, changes)
	if err == nil {
		err = changes.commit(ctx)
	}
	if err != nil {
		fsys.failed = &harness.FileError{Code: harness.FileErrorUnknown, Err: fmt.Errorf("worker: %s is ahead of storage until the object restarts: %w", fsys.namespace, err)}
		return fsys.failed
	}
	return nil
}

func (fsys *FileSystem) resolve(ctx context.Context, name string) string {
	resolved, _ := fsys.AbsolutePath(ctx, name)
	return resolved
}

func (fsys *FileSystem) WriteFile(ctx context.Context, name string, content []byte) error {
	return fsys.persist(ctx, func() error { return fsys.FileSystem.WriteFile(ctx, name, content) },
		func(ctx context.Context, changes *batch) error {
			return changes.written(ctx, fsys.resolve(ctx, name), content)
		})
}

// written records data, or name's current content when nil, in full.
func (b *batch) written(ctx context.Context, name string, data []byte) error {
	if err := b.parents(ctx, name); err != nil {
		return err
	}
	info, err := b.fsys.FileInfo(ctx, name)
	if err == nil && data == nil {
		data, err = b.fsys.ReadBinaryFile(ctx, name)
	}
	if err != nil {
		return err
	}
	b.file(name, data, info.MTimeMS)
	return nil
}

func (fsys *FileSystem) AppendFile(ctx context.Context, name string, content []byte) error {
	return fsys.persist(ctx, func() error { return fsys.FileSystem.AppendFile(ctx, name, content) },
		func(ctx context.Context, changes *batch) error {
			resolved := fsys.resolve(ctx, name)
			previous := fsys.entries[resolved]
			if previous == nil {
				return changes.written(ctx, resolved, content)
			}
			return changes.appended(ctx, resolved, previous, content)
		})
}

// appended rewrites only the chunks from the old last chunk onward.
func (b *batch) appended(ctx context.Context, name string, previous *entry, content []byte) error {
	info, err := b.fsys.FileInfo(ctx, name)
	if err != nil {
		return err
	}
	first := int(previous.Size / chunkSize)
	tail := previous.tail
	if tail == nil && previous.Size%chunkSize != 0 {
		key := chunkKey(b.fsys.namespace, name, first)
		values, err := b.fsys.kv.Get(ctx, []string{key})
		if err != nil {
			return &harness.FileError{Code: harness.FileErrorUnknown, Path: name, Err: err}
		}
		if tail = values[key]; int64(len(tail)) != previous.Size%chunkSize {
			return &harness.FileError{Code: harness.FileErrorUnknown, Path: name, Err: errors.New("worker: stored tail chunk does not match its metadata")}
		}
	}
	data := append(slices.Clip(tail), content...)
	for offset := 0; offset < len(data); offset += chunkSize {
		b.set(chunkKey(b.fsys.namespace, name, first+offset/chunkSize), data[offset:min(len(data), offset+chunkSize)])
	}
	next := &entry{Size: previous.Size + int64(len(content)), MTime: info.MTimeMS}
	next.tail = slices.Clone(data[len(data)-len(data)%chunkSize:])
	b.meta(name, next)
	return nil
}

func (fsys *FileSystem) CreateDir(ctx context.Context, name string, recursive bool) error {
	return fsys.persist(ctx, func() error { return fsys.FileSystem.CreateDir(ctx, name, recursive) },
		func(ctx context.Context, changes *batch) error {
			return changes.parents(ctx, path.Join(fsys.resolve(ctx, name), "child"))
		})
}

func (fsys *FileSystem) Remove(ctx context.Context, name string, recursive, force bool) error {
	return fsys.persist(ctx, func() error { return fsys.FileSystem.Remove(ctx, name, recursive, force) },
		func(ctx context.Context, changes *batch) error {
			resolved := fsys.resolve(ctx, name)
			for stored := range fsys.entries {
				if within(stored, resolved) {
					changes.forget(stored)
				}
			}
			return nil
		})
}

func (fsys *FileSystem) RenameFile(ctx context.Context, from, to string) error {
	return fsys.persist(ctx, func() error { return fsys.FileSystem.RenameFile(ctx, from, to) },
		func(ctx context.Context, changes *batch) error {
			source, destination := fsys.resolve(ctx, from), fsys.resolve(ctx, to)
			if source == destination {
				return nil
			}
			moved := map[string]*entry{}
			for stored, value := range fsys.entries {
				switch {
				case within(stored, destination):
					changes.forget(stored)
				case within(stored, source):
					moved[destination+stored[len(source):]] = value
					changes.forget(stored)
				}
			}
			for _, name := range slices.Sorted(maps.Keys(moved)) {
				if value := moved[name]; value.Dir {
					changes.meta(name, &entry{Dir: true, MTime: value.MTime})
					continue
				}
				data, err := fsys.ReadBinaryFile(ctx, name)
				if err != nil {
					return err
				}
				changes.file(name, data, moved[name].MTime)
			}
			return nil
		})
}

func (fsys *FileSystem) CreateTempDir(ctx context.Context, prefix string) (string, error) {
	var name string
	err := fsys.persist(ctx, func() (err error) {
		name, err = fsys.FileSystem.CreateTempDir(ctx, prefix)
		return err
	}, func(ctx context.Context, changes *batch) error { return changes.parents(ctx, path.Join(name, "child")) })
	return name, err
}

func (fsys *FileSystem) CreateTempFile(ctx context.Context, prefix, suffix string) (string, error) {
	var name string
	err := fsys.persist(ctx, func() (err error) {
		name, err = fsys.FileSystem.CreateTempFile(ctx, prefix, suffix)
		return err
	}, func(ctx context.Context, changes *batch) error { return changes.written(ctx, name, nil) })
	return name, err
}

func within(name, root string) bool {
	return name == root || strings.HasPrefix(name, strings.TrimSuffix(root, "/")+"/")
}

var _ harness.FileSystem = (*FileSystem)(nil)
