package harness

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	textunicode "golang.org/x/text/encoding/unicode"
)

const maxExecutionTimeoutSeconds = 2_147_483_647.0 / 1000.0

// NodeExecutionEnv is the pure-Go local filesystem and shell backend.
type NodeExecutionEnv struct {
	CWD       string
	ShellPath string
	ShellEnv  map[string]string
}

// LocalExecutionEnv is the platform-neutral Go name for NodeExecutionEnv.
type LocalExecutionEnv = NodeExecutionEnv

func (env NodeExecutionEnv) WorkingDirectory() string {
	cwd := env.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	absolute, err := filepath.Abs(cwd)
	if err == nil {
		cwd = absolute
	}
	return filepath.Clean(cwd)
}

func (env NodeExecutionEnv) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(filepath.Join(env.WorkingDirectory(), path))
}

func abortedFileError(ctx context.Context, path string) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	return &FileError{Code: FileErrorAborted, Path: path, Err: errors.New("aborted")}
}

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
	case errors.Is(err, fs.ErrNotExist):
		code = FileErrorNotFound
		message = fmt.Sprintf("ENOENT: no such file or directory, %s '%s'", operation, path)
	case errors.Is(err, fs.ErrPermission):
		code = FileErrorPermissionDenied
	case errors.Is(err, syscall.ENOTDIR):
		code = FileErrorNotDirectory
		message = fmt.Sprintf("ENOTDIR: not a directory, %s '%s'", operation, path)
	case errors.Is(err, syscall.EISDIR):
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

func (env NodeExecutionEnv) AbsolutePath(ctx context.Context, path string) (string, error) {
	return env.resolve(path), nil
}

func (env NodeExecutionEnv) JoinPath(ctx context.Context, parts ...string) (string, error) {
	return filepath.Join(parts...), nil
}

func (env NodeExecutionEnv) ReadTextFile(ctx context.Context, path string) (string, error) {
	contents, err := env.ReadBinaryFile(ctx, path)
	if err != nil {
		return "", err
	}
	decoded, _ := textunicode.UTF8.NewDecoder().Bytes(contents)
	return string(decoded), nil
}

func (env NodeExecutionEnv) ReadTextLines(ctx context.Context, path string, maxLines int) ([]string, error) {
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

func (env NodeExecutionEnv) ReadBinaryFile(ctx context.Context, path string) ([]byte, error) {
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

func (env NodeExecutionEnv) WriteFile(ctx context.Context, path string, content []byte) error {
	return env.write(ctx, path, content, false, true)
}

// WriteFileExclusive creates a new file without replacing a concurrent writer.
func (env NodeExecutionEnv) WriteFileExclusive(ctx context.Context, path string, content []byte) error {
	return env.writeFlags(ctx, path, content, os.O_CREATE|os.O_EXCL|os.O_WRONLY, true)
}

func (env NodeExecutionEnv) AppendFile(ctx context.Context, path string, content []byte) error {
	return env.write(ctx, path, content, true, false)
}

func (env NodeExecutionEnv) write(ctx context.Context, path string, content []byte, appendMode, cancellable bool) error {
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	return env.writeFlags(ctx, path, content, flags, cancellable)
}

func (env NodeExecutionEnv) writeFlags(ctx context.Context, path string, content []byte, flags int, cancellable bool) error {
	resolved := env.resolve(path)
	if cancellable {
		if err := abortedFileError(ctx, resolved); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
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

func (env NodeExecutionEnv) FileInfo(ctx context.Context, path string) (FileInfo, error) {
	resolved := env.resolve(path)
	info, err := os.Lstat(resolved)
	if err != nil {
		return FileInfo{}, nodeOperationError("lstat", resolved, err)
	}
	return nodeFileInfo(resolved, info)
}

func (env NodeExecutionEnv) ListDir(ctx context.Context, path string) ([]FileInfo, error) {
	resolved := env.resolve(path)
	if err := abortedFileError(ctx, resolved); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
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

func (env NodeExecutionEnv) CanonicalPath(ctx context.Context, path string) (string, error) {
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

func (env NodeExecutionEnv) Exists(ctx context.Context, path string) (bool, error) {
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

func (env NodeExecutionEnv) CreateDir(ctx context.Context, path string, recursive bool) error {
	resolved := env.resolve(path)
	var err error
	if recursive {
		err = os.MkdirAll(resolved, 0o755)
	} else {
		err = os.Mkdir(resolved, 0o755)
	}
	return nodeOperationError("mkdir", resolved, err)
}

func (env NodeExecutionEnv) Remove(ctx context.Context, path string, recursive, force bool) error {
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

func (env NodeExecutionEnv) CreateTempDir(ctx context.Context, prefix string) (string, error) {
	if prefix == "" {
		prefix = "tmp-"
	}
	path, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", nodeOperationError("mkdtemp", "", err)
	}
	return path, nil
}

func (env NodeExecutionEnv) CreateTempFile(ctx context.Context, prefix, suffix string) (string, error) {
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

func (env NodeExecutionEnv) Cleanup() error { return nil }

func (env NodeExecutionEnv) ResourceFileInfo(path string) (FileInfo, error) {
	return env.FileInfo(context.Background(), path)
}

func (env NodeExecutionEnv) ResourceListDir(path string) ([]FileInfo, error) {
	return env.ListDir(context.Background(), path)
}

func (env NodeExecutionEnv) ResourceReadTextFile(path string) (string, error) {
	return env.ReadTextFile(context.Background(), path)
}

func (env NodeExecutionEnv) ResourceCanonicalPath(path string) (string, error) {
	return env.CanonicalPath(context.Background(), path)
}

func (env NodeExecutionEnv) shell() (string, error) {
	if env.ShellPath != "" {
		info, err := os.Stat(env.ShellPath)
		if err != nil || info.IsDir() {
			return "", &ExecutionError{Code: ExecutionErrorShellUnavailable, Err: fmt.Errorf("Custom shell path not found: %s", env.ShellPath)} //nolint:staticcheck // Upstream error text is observable.
		}
		return env.ShellPath, nil
	}
	if info, err := os.Stat("/bin/bash"); err == nil && !info.IsDir() {
		return "/bin/bash", nil
	}
	if shell, err := exec.LookPath("bash"); err == nil {
		return shell, nil
	}
	if shell, err := exec.LookPath("sh"); err == nil {
		return shell, nil
	}
	return "", &ExecutionError{Code: ExecutionErrorShellUnavailable, Err: errors.New("No bash shell found")} //nolint:staticcheck // Upstream error text is observable.
}

func validateExecutionTimeout(seconds *float64) error {
	if seconds == nil {
		return nil
	}
	if math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds <= 0 {
		return &ExecutionError{Code: ExecutionErrorTimeout, Err: errors.New("Invalid timeout: must be a finite number of seconds")} //nolint:staticcheck // Upstream error text is observable.
	}
	if *seconds > maxExecutionTimeoutSeconds {
		return &ExecutionError{Code: ExecutionErrorTimeout, Err: fmt.Errorf("Invalid timeout: maximum is %s seconds", strconv.FormatFloat(maxExecutionTimeoutSeconds, 'f', 3, 64))} //nolint:staticcheck // Upstream error text is observable.
	}
	return nil
}

type callbackBuffer struct {
	mu         sync.Mutex
	buffer     bytes.Buffer
	callbackMu *sync.Mutex
	callback   func(string) error
	onError    func(error)
}

func (writer *callbackBuffer) Write(chunk []byte) (int, error) {
	writer.mu.Lock()
	_, _ = writer.buffer.Write(chunk)
	writer.mu.Unlock()
	if writer.callback != nil {
		writer.callbackMu.Lock()
		if err := writer.callback(string(chunk)); err != nil {
			writer.onError(err)
		}
		writer.callbackMu.Unlock()
	}
	return len(chunk), nil
}

func (writer *callbackBuffer) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.String()
}

func mergeExecutionEnvironment(base []string, layers ...map[string]string) []string {
	values := make(map[string]string, len(base))
	for _, pair := range base {
		if index := strings.IndexByte(pair, '='); index >= 0 {
			values[pair[:index]] = pair[index+1:]
		}
	}
	for _, layer := range layers {
		for key, value := range layer {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func (env NodeExecutionEnv) Exec(ctx context.Context, command string, options ExecOptions) (ExecResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return ExecResult{}, &ExecutionError{Code: ExecutionErrorAborted, Err: errors.New("aborted")}
	}
	if err := validateExecutionTimeout(options.TimeoutSeconds); err != nil {
		return ExecResult{}, err
	}
	shell, err := env.shell()
	if err != nil {
		return ExecResult{}, err
	}
	cwd := env.WorkingDirectory()
	if options.CWD != "" {
		cwd = env.resolve(options.CWD)
	}
	cmd := exec.Command(shell, "-c", command)
	cmd.Dir = cwd
	cmd.Env = mergeExecutionEnvironment(os.Environ(), env.ShellEnv, options.Env)
	configureProcessTree(cmd)

	callbackSignal := make(chan struct{}, 1)
	var callbackMu sync.Mutex
	var callbackCallMu sync.Mutex
	var callbackErr error
	recordCallbackError := func(err error) {
		callbackMu.Lock()
		if callbackErr == nil {
			callbackErr = err
			callbackSignal <- struct{}{}
		}
		callbackMu.Unlock()
	}
	stdout := &callbackBuffer{callbackMu: &callbackCallMu, callback: options.OnStdout, onError: recordCallbackError}
	stderr := &callbackBuffer{callbackMu: &callbackCallMu, callback: options.OnStderr, onError: recordCallbackError}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return ExecResult{}, &ExecutionError{Code: ExecutionErrorSpawn, Err: err}
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var timeout <-chan time.Time
	var timer *time.Timer
	if options.TimeoutSeconds != nil {
		timer = time.NewTimer(time.Duration(*options.TimeoutSeconds * float64(time.Second)))
		timeout = timer.C
	}
	timedOut := false
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		killProcessTree(cmd.Process)
		waitErr = <-done
	case <-timeout:
		timedOut = true
		killProcessTree(cmd.Process)
		waitErr = <-done
	case <-callbackSignal:
		killProcessTree(cmd.Process)
		waitErr = <-done
	}
	if timer != nil {
		timer.Stop()
	}

	callbackMu.Lock()
	streamErr := callbackErr
	callbackMu.Unlock()
	if streamErr != nil {
		return ExecResult{}, &ExecutionError{Code: ExecutionErrorCallback, Err: streamErr}
	}
	if timedOut {
		return ExecResult{}, &ExecutionError{Code: ExecutionErrorTimeout, Err: fmt.Errorf("timeout:%s", strconv.FormatFloat(*options.TimeoutSeconds, 'g', -1, 64))}
	}
	if ctx.Err() != nil {
		return ExecResult{}, &ExecutionError{Code: ExecutionErrorAborted, Err: errors.New("aborted")}
	}

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return ExecResult{}, &ExecutionError{Code: ExecutionErrorSpawn, Err: waitErr}
		}
		exitCode = exitErr.ExitCode()
		if exitCode < 0 {
			exitCode = 0
		}
	}
	return ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, nil
}

var _ ExecutionEnv = NodeExecutionEnv{}
var _ ResourceFileSystem = NodeExecutionEnv{}
