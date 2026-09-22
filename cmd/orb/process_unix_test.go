//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMigrationProcessExitsDuringInspection(t *testing.T) {
	process := exec.Command("sleep", "30")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = -axo ]; then echo '%d %d orb'; else exit 1; fi\n", os.Getuid(), process.Process.Pid)
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	previous := procfs
	procfs = filepath.Join(bin, "no-procfs")
	t.Cleanup(func() { procfs = previous })
	if err := requireOfflineMigration(context.Background(), bin); err == nil {
		t.Fatal("allowed migration without inspecting a live process")
	}
	_ = process.Process.Kill()
	_ = process.Wait()
	if err := requireOfflineMigration(context.Background(), bin); err != nil {
		t.Fatal("exited process blocked migration:", err)
	}
}
