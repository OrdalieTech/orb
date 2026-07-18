package harness

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func requireFileErrorCode(t testing.TB, err error, code FileErrorCode) *FileError {
	t.Helper()
	var typed *FileError
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %v, want FileError(%s)", err, code)
	}
	return typed
}

func requireExecutionErrorCode(t testing.TB, err error, code ExecutionErrorCode) *ExecutionError {
	t.Helper()
	var typed *ExecutionError
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %v, want ExecutionError(%s)", err, code)
	}
	return typed
}

func TestNodeExecutionEnvFileSystemParity(t *testing.T) {
	root := t.TempDir()
	processCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeRoot, err := filepath.Rel(processCWD, root)
	if err != nil {
		t.Fatal(err)
	}
	env := NodeExecutionEnv{CWD: relativeRoot}
	ctx := context.Background()

	absolute, err := env.AbsolutePath(ctx, "nested/../file.txt")
	if err != nil || absolute != filepath.Join(root, "file.txt") || !filepath.IsAbs(absolute) {
		t.Fatalf("AbsolutePath = %q, %v", absolute, err)
	}
	joined, err := env.JoinPath(ctx, root, "nested", "..", "file.txt")
	if err != nil || joined != filepath.Join(root, "file.txt") {
		t.Fatalf("JoinPath = %q, %v", joined, err)
	}
	if err := env.WriteFile(ctx, "nested/file.txt", []byte("one\r\ntwo\nthree\n")); err != nil {
		t.Fatal(err)
	}
	if err := env.AppendFile(ctx, "nested/file.txt", []byte("four")); err != nil {
		t.Fatal(err)
	}
	text, err := env.ReadTextFile(ctx, "nested/file.txt")
	if err != nil || text != "one\r\ntwo\nthree\nfour" {
		t.Fatalf("ReadTextFile = %q, %v", text, err)
	}
	lines, err := env.ReadTextLines(ctx, "nested/file.txt", 2)
	if err != nil || len(lines) != 2 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("ReadTextLines = %#v, %v", lines, err)
	}
	if noLines, err := env.ReadTextLines(ctx, "nested/file.txt", 0); err != nil || len(noLines) != 0 {
		t.Fatalf("ReadTextLines(max=0) = %#v, %v", noLines, err)
	}
	binary, err := env.ReadBinaryFile(ctx, "nested/file.txt")
	if err != nil || string(binary) != text {
		t.Fatalf("ReadBinaryFile = %q, %v", binary, err)
	}
	info, err := env.FileInfo(ctx, "nested/file.txt")
	if err != nil || info.Kind != FileKindFile || info.Name != "file.txt" || info.Size != int64(len(binary)) || !filepath.IsAbs(info.Path) {
		t.Fatalf("FileInfo = %#v, %v", info, err)
	}
	entries, err := env.ListDir(ctx, "nested")
	if err != nil || len(entries) != 1 || entries[0].Name != "file.txt" {
		t.Fatalf("ListDir = %#v, %v", entries, err)
	}
	exists, err := env.Exists(ctx, "nested/file.txt")
	if err != nil || !exists {
		t.Fatalf("Exists = %v, %v", exists, err)
	}
	missing, err := env.Exists(ctx, "missing")
	if err != nil || missing {
		t.Fatalf("Exists(missing) = %v, %v", missing, err)
	}
	if err := env.CreateDir(ctx, "missing/child", false); err == nil {
		t.Fatal("non-recursive CreateDir below a missing parent succeeded")
	} else {
		_ = requireFileErrorCode(t, err, FileErrorNotFound)
	}
	if err := env.CreateDir(ctx, "tree/child", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Remove(ctx, "tree", false, false); err == nil {
		t.Fatal("non-recursive Remove of a non-empty directory succeeded")
	}
	if err := env.Remove(ctx, "tree", true, false); err != nil {
		t.Fatal(err)
	}
	if err := env.Remove(ctx, "missing", false, true); err != nil {
		t.Fatal(err)
	}
	if err := env.Remove(ctx, "missing", false, false); err == nil {
		t.Fatal("non-forced Remove of a missing path succeeded")
	}
}

func TestNodeExecutionEnvSymlinksCancellationAndTemps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available without privileges")
	}
	root := t.TempDir()
	env := NodeExecutionEnv{CWD: root}
	ctx := context.Background()
	if err := env.WriteFile(ctx, "target.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	info, err := env.FileInfo(ctx, "link.txt")
	if err != nil || info.Kind != FileKindSymlink {
		t.Fatalf("symlink FileInfo = %#v, %v", info, err)
	}
	canonical, err := env.CanonicalPath(ctx, "link.txt")
	if err != nil || canonical != filepath.Join(root, "target.txt") {
		t.Fatalf("CanonicalPath = %q, %v", canonical, err)
	}
	if _, err := env.ReadTextFile(ctx, "missing"); err == nil {
		t.Fatal("missing ReadTextFile succeeded")
	} else if typed := requireFileErrorCode(t, err, FileErrorNotFound); typed.Path != filepath.Join(root, "missing") {
		t.Fatalf("missing path = %q", typed.Path)
	}
	if _, err := env.ReadTextFile(ctx, "."); err == nil {
		t.Fatal("directory ReadTextFile succeeded")
	} else {
		_ = requireFileErrorCode(t, err, FileErrorIsDirectory)
	}
	if _, err := env.ListDir(ctx, "target.txt"); err == nil {
		t.Fatal("file ListDir succeeded")
	} else {
		_ = requireFileErrorCode(t, err, FileErrorNotDirectory)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	operations := []func() error{
		func() error { _, err := env.ReadTextFile(cancelled, "target.txt"); return err },
		func() error { _, err := env.ReadTextLines(cancelled, "target.txt", -1); return err },
		func() error { _, err := env.ReadBinaryFile(cancelled, "target.txt"); return err },
		func() error { return env.WriteFile(cancelled, "other.txt", []byte("no")) },
		func() error { _, err := env.ListDir(cancelled, "."); return err },
	}
	for _, operation := range operations {
		_ = requireFileErrorCode(t, operation(), FileErrorAborted)
	}

	tempDir, err := env.CreateTempDir(ctx, "node-env-test-")
	if err != nil || !strings.HasPrefix(filepath.Base(tempDir), "node-env-test-") {
		t.Fatalf("CreateTempDir = %q, %v", tempDir, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	tempFile, err := env.CreateTempFile(ctx, "prefix-", ".txt")
	if err != nil || !strings.HasPrefix(filepath.Base(tempFile), "prefix-") || !strings.HasSuffix(tempFile, ".txt") {
		t.Fatalf("CreateTempFile = %q, %v", tempFile, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(tempFile)) })
	if exists, existsErr := env.Exists(ctx, tempFile); existsErr != nil || !exists {
		t.Fatalf("temp file exists = %v, %v", exists, existsErr)
	}
	if err := env.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestNodeExecutionEnvShellParityAndFailures(t *testing.T) {
	root := t.TempDir()
	env := NodeExecutionEnv{CWD: root, ShellEnv: map[string]string{"BASE": "base"}}
	ctx := context.Background()
	var callbackMu strings.Builder
	result, err := env.Exec(ctx, `printf "$BASE:$EXTRA"; printf err >&2; exit 7`, ExecOptions{
		Env: map[string]string{"EXTRA": "extra"},
		OnStdout: func(chunk string) error {
			_, _ = callbackMu.WriteString("stdout:" + chunk)
			return nil
		},
		OnStderr: func(chunk string) error {
			_, _ = callbackMu.WriteString("stderr:" + chunk)
			return nil
		},
	})
	if err != nil || result.Stdout != "base:extra" || result.Stderr != "err" || result.ExitCode != 7 {
		t.Fatalf("Exec = %#v, %v", result, err)
	}
	if callbacks := callbackMu.String(); !strings.Contains(callbacks, "stdout:base:extra") || !strings.Contains(callbacks, "stderr:err") {
		t.Fatalf("callbacks = %q", callbacks)
	}

	for _, timeout := range []float64{0, -1, math.NaN(), math.Inf(1), maxExecutionTimeoutSeconds + 1} {
		if _, err := env.Exec(ctx, "true", ExecOptions{TimeoutSeconds: &timeout}); err == nil {
			t.Fatalf("timeout %v succeeded", timeout)
		} else {
			_ = requireExecutionErrorCode(t, err, ExecutionErrorTimeout)
		}
	}
	tiny := 0.01
	if _, err := env.Exec(ctx, "sleep 5", ExecOptions{TimeoutSeconds: &tiny}); err == nil {
		t.Fatal("timed execution succeeded")
	} else {
		_ = requireExecutionErrorCode(t, err, ExecutionErrorTimeout)
	}
	if _, err := env.Exec(ctx, "printf boom", ExecOptions{OnStdout: func(string) error { return errors.New("callback boom") }}); err == nil {
		t.Fatal("callback failure succeeded")
	} else if typed := requireExecutionErrorCode(t, err, ExecutionErrorCallback); typed.Error() != "callback boom" {
		t.Fatalf("callback error = %q", typed.Error())
	}
	aborted, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := env.Exec(aborted, "printf never", ExecOptions{}); err == nil {
		t.Fatal("pre-aborted execution succeeded")
	} else {
		_ = requireExecutionErrorCode(t, err, ExecutionErrorAborted)
	}
	live, cancelLive := context.WithCancel(ctx)
	if _, err := env.Exec(live, "printf ready; sleep 5", ExecOptions{OnStdout: func(string) error {
		cancelLive()
		return nil
	}}); err == nil {
		t.Fatal("cancelled live execution succeeded")
	} else {
		_ = requireExecutionErrorCode(t, err, ExecutionErrorAborted)
	}

	missing := NodeExecutionEnv{CWD: root, ShellPath: "missing-shell"}
	if _, err := missing.Exec(ctx, "true", ExecOptions{}); err == nil {
		t.Fatal("missing shell succeeded")
	} else {
		_ = requireExecutionErrorCode(t, err, ExecutionErrorShellUnavailable)
	}
	notExecutable := filepath.Join(root, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	spawnFailure := NodeExecutionEnv{CWD: root, ShellPath: notExecutable}
	if _, err := spawnFailure.Exec(ctx, "true", ExecOptions{}); err == nil {
		t.Fatal("non-executable shell succeeded")
	} else {
		_ = requireExecutionErrorCode(t, err, ExecutionErrorSpawn)
	}
}
