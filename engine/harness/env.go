package harness

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	textunicode "golang.org/x/text/encoding/unicode"

	"github.com/OrdalieTech/orb/internal/nodepath"
	"github.com/OrdalieTech/orb/internal/proctree"
	"github.com/OrdalieTech/orb/internal/toolenv"
)

// NodeExecutionEnv is the pure-Go local filesystem and shell backend.
type NodeExecutionEnv struct {
	CWD       string
	ShellPath string
	ShellEnv  map[string]string

	childrenMu     sync.Mutex
	activeChildren map[int]struct{}
}

// LocalExecutionEnv is the platform-neutral Go name for NodeExecutionEnv.
type LocalExecutionEnv = NodeExecutionEnv

func (env *NodeExecutionEnv) WorkingDirectory() string {
	cwd := env.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return processAbsolute(cwd)
}

// processAbsolute resolves path against the process working directory, like
// Node's path.resolve; on win32 that also roots "\x" on the process drive.
func processAbsolute(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func (env *NodeExecutionEnv) resolve(path string) string {
	normalized := path
	// Malformed URLs stay ordinary paths so filesystem methods preserve their non-throwing contract.
	if expanded, err := nodepath.Expand(path); err == nil {
		normalized = expanded
	}
	if filepath.IsAbs(normalized) {
		return filepath.Clean(normalized)
	}
	// Node's win32 isAbsolute accepts a rooted path without a drive.
	if runtime.GOOS == "windows" && (strings.HasPrefix(normalized, `\`) || strings.HasPrefix(normalized, "/")) {
		return processAbsolute(normalized)
	}
	return filepath.Clean(filepath.Join(env.WorkingDirectory(), normalized))
}

func abortedFileError(ctx context.Context, path string) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	return &FileError{Code: FileErrorAborted, Path: path, Err: errors.New("aborted")}
}

// errNotDirectory is libuv's ENOTDIR where win32 only reports a missing path.
// Go's syscall.ENOTDIR there is ERROR_PATH_NOT_FOUND, which also matches
// fs.ErrNotExist, so it cannot carry the distinction.
var errNotDirectory = errors.New("not a directory")

func nodeOperationError(operation, path string, err error) error {
	if err == nil {
		return nil
	}
	var typed *FileError
	if errors.As(err, &typed) {
		return typed
	}
	code := FileErrorUnknown
	message := err.Error()
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = FileErrorAborted
		message = "aborted"
	case errors.Is(err, errNotDirectory), runtime.GOOS != "windows" && errors.Is(err, syscall.ENOTDIR):
		code = FileErrorNotDirectory
		message = fmt.Sprintf("ENOTDIR: not a directory, %s '%s'", operation, path)
	case errors.Is(err, fs.ErrNotExist):
		code = FileErrorNotFound
		message = fmt.Sprintf("ENOENT: no such file or directory, %s '%s'", operation, path)
	case errors.Is(err, fs.ErrPermission):
		code = FileErrorPermissionDenied
	// win32 reads of a directory handle fail with ERROR_INVALID_FUNCTION (errno 1
	// there), which libuv reports as EISDIR.
	case errors.Is(err, syscall.EISDIR), runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1)):
		code = FileErrorIsDirectory
		message = "EISDIR: illegal operation on a directory, read"
	case errors.Is(err, syscall.EINVAL):
		code = FileErrorInvalid
	}
	return &FileError{Code: code, Path: path, Err: errors.New(message)}
}

func nodeFileInfo(path string, info fs.FileInfo) (FileInfo, error) {
	var kind FileKind
	switch mode := info.Mode(); {
	case mode.IsRegular():
		kind = FileKindFile
	case mode.IsDir():
		kind = FileKindDirectory
	case mode&os.ModeSymlink != 0:
		kind = FileKindSymlink
	default:
		return FileInfo{}, &FileError{Code: FileErrorInvalid, Path: path, Err: errors.New("Unsupported file type")} //nolint:staticcheck // Upstream error text is observable.
	}
	trimmedPath := strings.TrimRight(path, string(filepath.Separator))
	name := ""
	if trimmedPath != "" {
		name = filepath.Base(trimmedPath)
	}
	return FileInfo{
		Name:    name,
		Path:    path,
		Kind:    kind,
		Size:    info.Size(),
		MTimeMS: float64(info.ModTime().UnixNano()) / float64(time.Millisecond),
	}, nil
}

func (env *NodeExecutionEnv) AbsolutePath(ctx context.Context, path string) (string, error) {
	return env.resolve(path), nil
}

func (env *NodeExecutionEnv) JoinPath(ctx context.Context, parts ...string) (string, error) {
	return filepath.Join(parts...), nil
}

func (env *NodeExecutionEnv) ReadTextFile(ctx context.Context, path string) (string, error) {
	contents, err := env.ReadBinaryFile(ctx, path)
	if err != nil {
		return "", err
	}
	decoded, _ := textunicode.UTF8.NewDecoder().Bytes(contents)
	return string(decoded), nil
}

func (env *NodeExecutionEnv) ReadTextLines(ctx context.Context, path string, maxLines int) ([]string, error) {
	resolved := env.resolve(path)
	if err := abortedFileError(ctx, resolved); err != nil {
		return nil, err
	}
	if maxLines <= 0 {
		return []string{}, nil
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, nodeOperationError("open", resolved, err)
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(textunicode.UTF8.NewDecoder().Reader(file))
	lines := make([]string, 0)
	for maxLines < 0 || len(lines) < maxLines {
		if err := abortedFileError(ctx, resolved); err != nil {
			return nil, err
		}
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			lines = append(lines, line)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, nodeOperationError("read", resolved, readErr)
		}
	}
	if err := abortedFileError(ctx, resolved); err != nil {
		return nil, err
	}
	return lines, nil
}

func (env *NodeExecutionEnv) ReadBinaryFile(ctx context.Context, path string) ([]byte, error) {
	resolved := env.resolve(path)
	if err := abortedFileError(ctx, resolved); err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		return nil, nodeOperationError("open", resolved, err)
	}
	if err := abortedFileError(ctx, resolved); err != nil {
		return nil, err
	}
	return contents, nil
}

func (env *NodeExecutionEnv) WriteFile(ctx context.Context, path string, content []byte) error {
	return env.write(ctx, path, content, false, true)
}

// WriteFileExclusive creates a new file without replacing a concurrent writer.
func (env *NodeExecutionEnv) WriteFileExclusive(ctx context.Context, path string, content []byte) error {
	return env.writeFlags(ctx, path, content, os.O_CREATE|os.O_EXCL|os.O_WRONLY, true)
}

func (env *NodeExecutionEnv) AppendFile(ctx context.Context, path string, content []byte) error {
	return env.write(ctx, path, content, true, false)
}

func (env *NodeExecutionEnv) write(ctx context.Context, path string, content []byte, appendMode, cancellable bool) error {
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	return env.writeFlags(ctx, path, content, flags, cancellable)
}

func (env *NodeExecutionEnv) writeFlags(ctx context.Context, path string, content []byte, flags int, cancellable bool) error {
	resolved := env.resolve(path)
	if cancellable {
		if err := abortedFileError(ctx, resolved); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		if env.fileAncestor(ctx, resolved) {
			err = errNotDirectory
		}
		return nodeOperationError("mkdir", resolved, err)
	}
	if cancellable {
		if err := abortedFileError(ctx, resolved); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(resolved, flags, 0o666)
	if err != nil {
		return nodeOperationError("open", resolved, err)
	}
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if writeErr != nil {
		return nodeOperationError("write", resolved, writeErr)
	}
	if closeErr != nil {
		return nodeOperationError("close", resolved, closeErr)
	}
	if cancellable {
		return abortedFileError(ctx, resolved)
	}
	return nil
}

func (env *NodeExecutionEnv) RenameFile(ctx context.Context, from, to string) error {
	resolvedFrom, resolvedTo := env.resolve(from), env.resolve(to)
	if err := os.Rename(resolvedFrom, resolvedTo); err != nil {
		return nodeOperationError("rename", resolvedFrom, err)
	}
	return nil
}

func (env *NodeExecutionEnv) FileInfo(ctx context.Context, path string) (FileInfo, error) {
	resolved := env.resolve(path)
	info, err := os.Lstat(resolved)
	if err != nil {
		return FileInfo{}, nodeOperationError("lstat", resolved, err)
	}
	return nodeFileInfo(resolved, info)
}

func (env *NodeExecutionEnv) ListDir(ctx context.Context, path string) ([]FileInfo, error) {
	resolved := env.resolve(path)
	if err := abortedFileError(ctx, resolved); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		// win32 cannot open a file as a directory and says "not found"; libuv's
		// scandir reports ENOTDIR there as on POSIX.
		if errors.Is(err, fs.ErrNotExist) {
			if info, infoErr := env.FileInfo(ctx, resolved); infoErr == nil && info.Kind == FileKindFile {
				err = errNotDirectory
			}
		}
		return nil, nodeOperationError("scandir", resolved, err)
	}
	infos := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		if err := abortedFileError(ctx, resolved); err != nil {
			return nil, err
		}
		entryPath := filepath.Join(resolved, entry.Name())
		info, statErr := os.Lstat(entryPath)
		if statErr != nil {
			return nil, nodeOperationError("lstat", entryPath, statErr)
		}
		converted, convertErr := nodeFileInfo(entryPath, info)
		if convertErr == nil {
			infos = append(infos, converted)
		}
	}
	return infos, nil
}

func (env *NodeExecutionEnv) CanonicalPath(ctx context.Context, path string) (string, error) {
	resolved := env.resolve(path)
	canonical, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", nodeOperationError("realpath", resolved, err)
	}
	absolute, err := filepath.Abs(canonical)
	if err != nil {
		return "", nodeOperationError("realpath", resolved, err)
	}
	return filepath.Clean(absolute), nil
}

func (env *NodeExecutionEnv) Exists(ctx context.Context, path string) (bool, error) {
	_, err := env.FileInfo(ctx, path)
	if err == nil {
		return true, nil
	}
	var typed *FileError
	if errors.As(err, &typed) && typed.Code == FileErrorNotFound {
		return false, nil
	}
	return false, err
}

func (env *NodeExecutionEnv) CreateDir(ctx context.Context, path string, recursive bool) error {
	resolved := env.resolve(path)
	var err error
	if recursive {
		err = os.MkdirAll(resolved, 0o755)
		if err != nil && env.fileAncestor(ctx, resolved) {
			err = errNotDirectory
		}
	} else {
		err = os.Mkdir(resolved, 0o755)
	}
	return nodeOperationError("mkdir", resolved, err)
}

// fileAncestor reports a regular file among path's ancestors. Node's recursive
// mkdir answers ENOTDIR for it on every platform, while win32 CreateDirectory
// only reports the path as not found.
func (env *NodeExecutionEnv) fileAncestor(ctx context.Context, path string) bool {
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		if info, err := env.FileInfo(ctx, current); err == nil {
			return info.Kind == FileKindFile
		}
		if parent := filepath.Dir(current); parent == current {
			return false
		}
	}
}

func (env *NodeExecutionEnv) Remove(ctx context.Context, path string, recursive, force bool) error {
	resolved := env.resolve(path)
	if !recursive {
		info, err := os.Lstat(resolved)
		if err != nil {
			if force && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return nodeOperationError("rm", resolved, err)
		}
		if info.IsDir() {
			return &FileError{
				Code: FileErrorUnknown, Path: resolved,
				Err: fmt.Errorf("Path is a directory: rm returned EISDIR (is a directory) %s", resolved), //nolint:staticcheck // Node's observable error text is capitalized.
			}
		}
	}
	if !force {
		if _, err := os.Lstat(resolved); err != nil {
			return nodeOperationError("rm", resolved, err)
		}
	}
	var err error
	if recursive {
		err = os.RemoveAll(resolved)
	} else {
		err = os.Remove(resolved)
	}
	if force && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return nodeOperationError("rm", resolved, err)
}

func (env *NodeExecutionEnv) CreateTempDir(ctx context.Context, prefix string) (string, error) {
	if prefix == "" {
		prefix = "tmp-"
	}
	path, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", nodeOperationError("mkdtemp", "", err)
	}
	return path, nil
}

func (env *NodeExecutionEnv) CreateTempFile(ctx context.Context, prefix, suffix string) (string, error) {
	dir, err := env.CreateTempDir(ctx, "tmp-")
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, prefix+"*"+suffix)
	if err != nil {
		return "", nodeOperationError("open", dir, err)
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		return "", nodeOperationError("close", path, closeErr)
	}
	return path, nil
}

func (env *NodeExecutionEnv) Cleanup() error {
	env.childrenMu.Lock()
	for pid := range env.activeChildren {
		_ = proctree.Kill(pid)
	}
	clear(env.activeChildren)
	env.childrenMu.Unlock()
	return nil
}

// Exec runs command through the shared shell executor; its failures become
// typed ExecutionErrors.
func (env *NodeExecutionEnv) Exec(ctx context.Context, command string, options ExecOptions) (ExecResult, error) {
	cwd := env.WorkingDirectory()
	if options.CWD != "" {
		cwd = env.resolve(options.CWD)
	}
	environment := toolenv.Merge(nil, options.Env)
	if options.InheritEnv == nil || *options.InheritEnv {
		environment = toolenv.Merge(toolenv.Environ(), env.ShellEnv, options.Env)
	}
	var stdout, stderr strings.Builder
	var callbackErr error
	pid := 0
	exitCode, err := proctree.Run(ctx, proctree.Command{
		Script: command, Dir: cwd, Env: environment, Timeout: options.TimeoutSeconds,
		Shell: func() (proctree.Shell, error) {
			shell, err := proctree.FindShell(env.ShellPath, os.Getenv)
			if err != nil {
				return shell, &ExecutionError{Code: ExecutionErrorShellUnavailable, Err: err}
			}
			return shell, nil
		},
		Started: func(started int) {
			pid = started
			env.childrenMu.Lock()
			if env.activeChildren == nil {
				env.activeChildren = make(map[int]struct{})
			}
			env.activeChildren[pid] = struct{}{}
			env.childrenMu.Unlock()
		},
		OnData: func(isStderr bool, chunk []byte) error {
			buffer, callback := &stdout, options.OnStdout
			if isStderr {
				buffer, callback = &stderr, options.OnStderr
			}
			buffer.Write(chunk)
			if callback == nil || callbackErr != nil {
				return nil
			}
			callbackErr = callback(string(chunk))
			return callbackErr
		},
	})
	env.childrenMu.Lock()
	delete(env.activeChildren, pid)
	env.childrenMu.Unlock()
	var failure *proctree.Error
	switch {
	case err == nil:
		return ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, nil
	case callbackErr != nil:
		return ExecResult{}, &ExecutionError{Code: ExecutionErrorCallback, Err: callbackErr}
	case errors.As(err, &failure):
		code := map[proctree.Kind]ExecutionErrorCode{proctree.Spawn: ExecutionErrorSpawn, proctree.Aborted: ExecutionErrorAborted, proctree.Timeout: ExecutionErrorTimeout}[failure.Kind]
		return ExecResult{}, &ExecutionError{Code: code, Err: failure.Err}
	}
	return ExecResult{}, err
}

var _ ExecutionEnv = (*NodeExecutionEnv)(nil)
