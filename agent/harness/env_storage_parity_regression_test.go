package harness_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	harness "github.com/OrdalieTech/pigo/agent/harness"
)

func TestNodeExecutionEnvChecksRelativeCustomShellFromProcessCWDBeforeSpawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test shell is a POSIX script")
	}
	processCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	shellDir, err := os.MkdirTemp(processCWD, ".relative-shell-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shellDir) })
	shellPath := filepath.Join(shellDir, "custom-shell")
	if err := os.WriteFile(shellPath, []byte("#!/bin/sh\nexec /bin/sh \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	relativeShellPath, err := filepath.Rel(processCWD, shellPath)
	if err != nil {
		t.Fatal(err)
	}

	env := harness.NodeExecutionEnv{CWD: t.TempDir(), ShellPath: relativeShellPath}
	_, err = env.Exec(context.Background(), "printf process-cwd-shell", harness.ExecOptions{})
	var executionError *harness.ExecutionError
	if !errors.As(err, &executionError) || executionError.Code != harness.ExecutionErrorSpawn {
		t.Fatalf("relative custom shell error = %T %v, want spawn_error after process-cwd existence check", err, err)
	}
}

type appendFailureFileSystem struct {
	harness.NodeExecutionEnv
}

func (appendFailureFileSystem) AppendFile(context.Context, string, []byte) error {
	return errors.New("disk full")
}

func TestJSONLSessionStorageSetLeafReportsLeafAppendFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fileSystem := appendFailureFileSystem{NodeExecutionEnv: harness.NodeExecutionEnv{CWD: root}}
	repo := harness.NewJSONLSessionRepo(fileSystem, filepath.Join(root, "sessions"))
	session, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "leaf-errors", CWD: root})
	if err != nil {
		t.Fatal(err)
	}

	_, err = session.MoveTo(nil, nil)
	if err == nil {
		t.Fatal("SetLeaf succeeded after append failure")
	}
	var sessionError *harness.SessionError
	if !errors.As(err, &sessionError) || sessionError.Code != harness.SessionErrorStorage {
		t.Fatalf("error = %T %v, want SessionError(storage)", err, err)
	}
	const prefix = "Failed to append session leaf "
	message := err.Error()
	if !strings.HasPrefix(message, prefix) || !strings.HasSuffix(message, ": disk full") {
		t.Fatalf("SetLeaf append error = %q, want %q + entry id + %q", message, prefix, ": disk full")
	}
	id := strings.TrimSuffix(strings.TrimPrefix(message, prefix), ": disk full")
	if len(id) != 8 {
		t.Fatalf("SetLeaf append error entry id = %q, want eight hex characters", id)
	}
}
