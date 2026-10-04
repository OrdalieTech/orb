package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGrepIgnoresMalformedRGRecordsAndContinues(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	path := filepath.Join(root, "context.txt")
	stdout := "not-json\n" + rgMatchEvent(t, path, 2, "match one\n")
	installFakeManagedTool(t, "rg", stdout, "", 0, "")

	result, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "match",
		"path":    path,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolResultText(t, result), "context.txt:2: match one"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestGrepAbortAfterSpawnStopsChild(t *testing.T) {
	requireUnixSearchTest(t)
	agentDir := t.TempDir()
	binDir := filepath.Join(agentDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	writeSearchExecutable(t, filepath.Join(binDir, "rg"), "#!/bin/sh\n: > "+shellSingleQuote(ready)+"\nexec sleep 5\n")
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	t.Setenv("PI_OFFLINE", "1")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	cwd := t.TempDir()
	go func() {
		_, err := NewGrepTool(cwd, nil).Execute(ctx, "call", map[string]any{"pattern": "x"}, nil)
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for rg helper")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errOperationAborted) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("grep did not stop its spawned child after abort")
	}
}
