// Package memory is an in-memory harness.FileSystem port: POSIX virtual
// paths, files and directories, no symlinks. It never touches a host
// filesystem and holds no package-level state.
package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/engine/harness"
	textunicode "golang.org/x/text/encoding/unicode"
)

// ErrLimitExceeded is wrapped by the FileError returned when a write would
// exceed Options.MaxBytes or Options.MaxFiles.
var ErrLimitExceeded = errors.New("memory file system limit exceeded")

var (
	errIsDirectory  = errors.New("is a directory")
	errNotDirectory = errors.New("not a directory")
)

type Options struct {
	// Root is the working directory and the confinement boundary: paths
	// resolving outside it fail with permission_denied. Defaults to "/".
	Root string
	// MaxBytes bounds the total size of file contents; zero is unbounded.
	MaxBytes int64
	// MaxFiles bounds the number of regular files; zero is unbounded.
	MaxFiles int
	// Now stamps modification times; defaults to time.Now.
	Now func() time.Time
}

type node struct {
	dir   bool
	data  []byte
	mtime time.Time
}

// FileSystem is safe for concurrent use.
type FileSystem struct {
	root     string
	maxBytes int64
	maxFiles int
	now      func() time.Time

	mu    sync.Mutex
	nodes map[string]*node
	bytes int64
	files int
	seq   uint64
}

func New(options Options) *FileSystem {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	fsys := &FileSystem{root: "/", maxBytes: options.MaxBytes, maxFiles: options.MaxFiles, now: now, nodes: map[string]*node{"/": {dir: true, mtime: now()}}}
	fsys.root = fsys.resolve(options.Root)
	missing, _ := fsys.missingDirs(fsys.root)
	fsys.create(missing)
	return fsys
}

func fail(code harness.FileErrorCode, op, name string) error {
	var cause error
	switch code {
	case harness.FileErrorAborted:
		return &harness.FileError{Code: code, Path: name, Err: errors.New("aborted")}
	case harness.FileErrorNotFound:
		cause = fs.ErrNotExist
	case harness.FileErrorPermissionDenied:
		cause = fs.ErrPermission
	case harness.FileErrorNotDirectory:
		cause = errNotDirectory
	case harness.FileErrorIsDirectory:
		cause = errIsDirectory
	case harness.FileErrorInvalid:
		cause = fs.ErrInvalid
	default:
		cause = fs.ErrExist
	}
	return &harness.FileError{Code: code, Path: name, Err: &fs.PathError{Op: op, Path: name, Err: cause}}
}

// resolve maps host-flavored absolute paths (a Windows volume and
// separators) onto the virtual POSIX namespace.
func (fsys *FileSystem) resolve(name string) string {
	name = filepath.ToSlash(name[len(filepath.VolumeName(name)):])
	if !path.IsAbs(name) {
		name = path.Join(fsys.root, name)
	}
	return path.Clean(name)
}

func (fsys *FileSystem) check(ctx context.Context, op, name string) (string, error) {
	resolved := fsys.resolve(name)
	switch {
	case ctx != nil && ctx.Err() != nil:
		return "", fail(harness.FileErrorAborted, op, resolved)
	case strings.ContainsRune(name, 0):
		return "", fail(harness.FileErrorInvalid, op, resolved)
	case fsys.root != "/" && resolved != fsys.root && !strings.HasPrefix(resolved, fsys.root+"/"):
		return "", fail(harness.FileErrorPermissionDenied, op, resolved)
	}
	return resolved, nil
}

// lookup reports POSIX lookup failures: not_directory when the nearest
// existing ancestor is a file, not_found otherwise.
func (fsys *FileSystem) lookup(name string) (*node, harness.FileErrorCode) {
	if found := fsys.nodes[name]; found != nil {
		return found, ""
	}
	for dir := path.Dir(name); ; dir = path.Dir(dir) {
		if ancestor := fsys.nodes[dir]; ancestor != nil {
			if !ancestor.dir {
				return nil, harness.FileErrorNotDirectory
			}
			return nil, harness.FileErrorNotFound
		}
	}
}

func (fsys *FileSystem) lookupDir(name string) harness.FileErrorCode {
	found, code := fsys.lookup(name)
	if code == "" && !found.dir {
		return harness.FileErrorNotDirectory
	}
	return code
}

// missingDirs lists the directories a recursive create of name must add, or
// not_directory when name or an ancestor is a file.
func (fsys *FileSystem) missingDirs(name string) ([]string, harness.FileErrorCode) {
	var missing []string
	for dir := name; ; dir = path.Dir(dir) {
		if existing := fsys.nodes[dir]; existing != nil {
			if !existing.dir {
				return nil, harness.FileErrorNotDirectory
			}
			return missing, ""
		}
		missing = append(missing, dir)
	}
}

func (fsys *FileSystem) create(dirs []string) {
	stamp := fsys.now()
	for _, dir := range dirs {
		fsys.nodes[dir] = &node{dir: true, mtime: stamp}
	}
}

func (fsys *FileSystem) children(dir string) []string {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	var names []string
	for name := range fsys.nodes {
		if name != "/" && strings.HasPrefix(name, prefix) && !strings.Contains(name[len(prefix):], "/") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (fsys *FileSystem) descendants(dir string) []string {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	var names []string
	for name := range fsys.nodes {
		if name != "/" && strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names
}

func (fsys *FileSystem) WorkingDirectory() string { return fsys.root }

func (fsys *FileSystem) AbsolutePath(_ context.Context, name string) (string, error) {
	return fsys.resolve(name), nil
}

func (fsys *FileSystem) JoinPath(_ context.Context, parts ...string) (string, error) {
	return path.Join(parts...), nil
}

func (fsys *FileSystem) ReadBinaryFile(ctx context.Context, name string) ([]byte, error) {
	resolved, err := fsys.check(ctx, "open", name)
	if err != nil {
		return nil, err
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	found, code := fsys.lookup(resolved)
	if code != "" {
		return nil, fail(code, "open", resolved)
	}
	if found.dir {
		return nil, fail(harness.FileErrorIsDirectory, "read", resolved)
	}
	return slices.Clone(found.data), nil
}

func decodeText(data []byte) string {
	decoded, _ := textunicode.UTF8.NewDecoder().Bytes(data)
	return string(decoded)
}

func (fsys *FileSystem) ReadTextFile(ctx context.Context, name string) (string, error) {
	data, err := fsys.ReadBinaryFile(ctx, name)
	if err != nil {
		return "", err
	}
	return decodeText(data), nil
}

// ReadTextLines matches NodeExecutionEnv: maxLines <= 0 reads nothing and
// does not open the file.
func (fsys *FileSystem) ReadTextLines(ctx context.Context, name string, maxLines int) ([]string, error) {
	if _, err := fsys.check(ctx, "open", name); err != nil {
		return nil, err
	}
	if maxLines <= 0 {
		return []string{}, nil
	}
	data, err := fsys.ReadBinaryFile(ctx, name)
	if err != nil {
		return nil, err
	}
	text := decodeText(data)
	lines := []string{}
	for text != "" && len(lines) < maxLines {
		var line string
		line, text, _ = strings.Cut(text, "\n")
		lines = append(lines, strings.TrimSuffix(line, "\r"))
	}
	return lines, nil
}

func (fsys *FileSystem) WriteFile(ctx context.Context, name string, content []byte) error {
	return fsys.write(ctx, name, content, false)
}

func (fsys *FileSystem) AppendFile(ctx context.Context, name string, content []byte) error {
	return fsys.write(ctx, name, content, true)
}

// write validates ancestry and limits before creating parents, so a rejected
// write leaves no directories behind.
func (fsys *FileSystem) write(ctx context.Context, name string, content []byte, appendMode bool) error {
	resolved, err := fsys.check(ctx, "open", name)
	if err != nil {
		return err
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	existing := fsys.nodes[resolved]
	if existing != nil && existing.dir {
		return fail(harness.FileErrorIsDirectory, "open", resolved)
	}
	missing, code := fsys.missingDirs(path.Dir(resolved))
	if code != "" {
		return fail(code, "mkdir", resolved)
	}
	data := slices.Clone(content)
	bytes, files := fsys.bytes+int64(len(content)), fsys.files+1
	if existing != nil {
		if appendMode {
			data = append(slices.Clone(existing.data), content...)
		} else {
			bytes -= int64(len(existing.data))
		}
		files--
	}
	if (fsys.maxBytes > 0 && bytes > fsys.maxBytes) || (fsys.maxFiles > 0 && files > fsys.maxFiles) {
		return &harness.FileError{Code: harness.FileErrorUnknown, Path: resolved, Err: fmt.Errorf("%w: %d bytes and %d files allowed", ErrLimitExceeded, fsys.maxBytes, fsys.maxFiles)}
	}
	fsys.create(missing)
	fsys.nodes[resolved] = &node{data: data, mtime: fsys.now()}
	fsys.bytes, fsys.files = bytes, files
	return nil
}

// RenameFile follows rename(2): a file replaces a file, a directory replaces
// a missing or empty directory. Errors carry the source path like Node's.
func (fsys *FileSystem) RenameFile(ctx context.Context, from, to string) error {
	source, err := fsys.check(ctx, "rename", from)
	if err != nil {
		return err
	}
	destination, err := fsys.check(ctx, "rename", to)
	if err != nil {
		return err
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	moved, code := fsys.lookup(source)
	if code == "" {
		code = fsys.lookupDir(path.Dir(destination))
	}
	target := fsys.nodes[destination]
	switch {
	case code != "":
	case source == destination:
		return nil
	case source == fsys.root || source == "/":
		code = harness.FileErrorPermissionDenied
	case moved.dir && strings.HasPrefix(destination, source+"/"):
		code = harness.FileErrorInvalid
	case target != nil && !moved.dir && target.dir:
		code = harness.FileErrorIsDirectory
	case target != nil && moved.dir && !target.dir:
		code = harness.FileErrorNotDirectory
	case target != nil && moved.dir && len(fsys.children(destination)) > 0:
		code = harness.FileErrorUnknown
	}
	if code != "" {
		return fail(code, "rename", source)
	}
	if target != nil && !target.dir {
		fsys.bytes -= int64(len(target.data))
		fsys.files--
	}
	for _, name := range fsys.descendants(source) {
		fsys.nodes[destination+name[len(source):]] = fsys.nodes[name]
		delete(fsys.nodes, name)
	}
	fsys.nodes[destination] = moved
	delete(fsys.nodes, source)
	return nil
}

func fileInfo(name string, found *node) harness.FileInfo {
	info := harness.FileInfo{Path: name, Kind: harness.FileKindFile, Size: int64(len(found.data)), MTimeMS: float64(found.mtime.UnixNano()) / float64(time.Millisecond)}
	if name != "/" {
		info.Name = path.Base(name)
	}
	if found.dir {
		info.Kind = harness.FileKindDirectory
	}
	return info
}

func (fsys *FileSystem) FileInfo(ctx context.Context, name string) (harness.FileInfo, error) {
	resolved, err := fsys.check(ctx, "lstat", name)
	if err != nil {
		return harness.FileInfo{}, err
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	found, code := fsys.lookup(resolved)
	if code != "" {
		return harness.FileInfo{}, fail(code, "lstat", resolved)
	}
	return fileInfo(resolved, found), nil
}

// ListDir returns entries sorted by name.
func (fsys *FileSystem) ListDir(ctx context.Context, name string) ([]harness.FileInfo, error) {
	resolved, err := fsys.check(ctx, "scandir", name)
	if err != nil {
		return nil, err
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	if code := fsys.lookupDir(resolved); code != "" {
		return nil, fail(code, "scandir", resolved)
	}
	names := fsys.children(resolved)
	infos := make([]harness.FileInfo, len(names))
	for index, child := range names {
		infos[index] = fileInfo(child, fsys.nodes[child])
	}
	return infos, nil
}

func (fsys *FileSystem) CanonicalPath(ctx context.Context, name string) (string, error) {
	resolved, err := fsys.check(ctx, "realpath", name)
	if err != nil {
		return "", err
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	if _, code := fsys.lookup(resolved); code != "" {
		return "", fail(code, "realpath", resolved)
	}
	return resolved, nil
}

func (fsys *FileSystem) Exists(ctx context.Context, name string) (bool, error) {
	_, err := fsys.FileInfo(ctx, name)
	var typed *harness.FileError
	if errors.As(err, &typed) && typed.Code == harness.FileErrorNotFound {
		return false, nil
	}
	return err == nil, err
}

// CreateDir matches NodeExecutionEnv (Go's MkdirAll): a recursive create over
// an existing file reports not_directory; a non-recursive create over any
// existing entry reports unknown (EEXIST).
func (fsys *FileSystem) CreateDir(ctx context.Context, name string, recursive bool) error {
	resolved, err := fsys.check(ctx, "mkdir", name)
	if err != nil {
		return err
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	var missing []string
	code := harness.FileErrorUnknown
	switch {
	case recursive:
		missing, code = fsys.missingDirs(resolved)
	case fsys.nodes[resolved] == nil:
		missing, code = []string{resolved}, fsys.lookupDir(path.Dir(resolved))
	}
	if code != "" {
		return fail(code, "mkdir", resolved)
	}
	fsys.create(missing)
	return nil
}

// Remove matches NodeExecutionEnv: a non-recursive remove of any directory
// fails with unknown (Node's ERR_FS_EISDIR) and force ignores only missing
// paths. The root cannot be removed.
func (fsys *FileSystem) Remove(ctx context.Context, name string, recursive, force bool) error {
	resolved, err := fsys.check(ctx, "rm", name)
	if err != nil {
		return err
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	found, code := fsys.lookup(resolved)
	switch {
	case code == harness.FileErrorNotFound && force:
		return nil
	case code != "":
		return fail(code, "rm", resolved)
	case resolved == fsys.root || resolved == "/":
		return fail(harness.FileErrorPermissionDenied, "rm", resolved)
	case found.dir && !recursive:
		return &harness.FileError{Code: harness.FileErrorUnknown, Path: resolved, Err: fmt.Errorf("Path is a directory: rm returned EISDIR (is a directory) %s", resolved)} //nolint:staticcheck // Node's observable error text is capitalized.
	}
	for _, removed := range append(fsys.descendants(resolved), resolved) {
		if entry := fsys.nodes[removed]; !entry.dir {
			fsys.bytes -= int64(len(entry.data))
			fsys.files--
		}
		delete(fsys.nodes, removed)
	}
	return nil
}

// CreateTempDir creates directories under <Root>/tmp.
func (fsys *FileSystem) CreateTempDir(ctx context.Context, prefix string) (string, error) {
	if prefix == "" {
		prefix = "tmp-"
	}
	parent := path.Join(fsys.root, "tmp")
	if _, err := fsys.check(ctx, "mkdtemp", parent); err != nil {
		return "", err
	}
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	missing, code := fsys.missingDirs(parent)
	if code != "" {
		return "", fail(code, "mkdtemp", parent)
	}
	name := fsys.unique(parent, prefix, "")
	fsys.create(append(missing, name))
	return name, nil
}

func (fsys *FileSystem) unique(dir, prefix, suffix string) string {
	for {
		fsys.seq++
		if name := path.Join(dir, prefix+strconv.FormatUint(fsys.seq, 36)+suffix); fsys.nodes[name] == nil {
			return name
		}
	}
}

func (fsys *FileSystem) CreateTempFile(ctx context.Context, prefix, suffix string) (string, error) {
	dir, err := fsys.CreateTempDir(ctx, "tmp-")
	if err != nil {
		return "", err
	}
	fsys.mu.Lock()
	name := fsys.unique(dir, prefix, suffix)
	fsys.mu.Unlock()
	if err := fsys.WriteFile(ctx, name, nil); err != nil {
		return "", err
	}
	return name, nil
}

func (fsys *FileSystem) Cleanup() error { return nil }

// Snapshot returns a copy of every regular file's content keyed by path.
func (fsys *FileSystem) Snapshot() map[string]string {
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	files := make(map[string]string, fsys.files)
	for name, entry := range fsys.nodes {
		if !entry.dir {
			files[name] = string(entry.data)
		}
	}
	return files
}

var _ harness.FileSystem = (*FileSystem)(nil)
