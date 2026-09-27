//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// In a terminal, a migration blocked by an Orb still on the previous version
// offers to stop it, and continues once it has exited.
func TestMigrationOffersToStopTheOldOrb(t *testing.T) {
	process := exec.Command("sleep", "30")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = process.Wait(); close(done) }()
	t.Cleanup(func() { _ = process.Process.Kill(); <-done })
	pid := process.Process.Pid
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n-axo) echo '%d %d orb';;\n-o) echo 'orb --old';;\n*) kill -0 %d 2>/dev/null || exit 1; echo 'orb HOME=/home';;\nesac\n", os.Getuid(), pid, pid)
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/bin:/usr/bin")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	previous := procfs
	procfs = filepath.Join(bin, "no-procfs")
	t.Cleanup(func() { procfs = previous })
	var stderr strings.Builder
	streams := cliStreams{Stdin: strings.NewReader("n\n"), Stderr: &stderr, StdinTTY: true, StderrTTY: true}
	if err := stopLegacyWriters(context.Background(), bin, streams); err == nil || !strings.Contains(stderr.String(), "orb --old") {
		t.Fatalf("declined: %v %q", err, stderr.String())
	}
	streams.Stdin = strings.NewReader("y\n")
	if err := stopLegacyWriters(context.Background(), bin, streams); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("the old Orb still runs")
	}
}
