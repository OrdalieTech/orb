//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/codingagent/config"
)

// --list-models is metadata-only: the resource loader must not run, so a
// skills directory handed to it is never read. The marker is a FIFO planted as
// the skills dir's ignore file — skill discovery reads ignore files, and
// opening the FIFO for read parks the reader until the test's non-blocking
// write-open succeeds, which can only happen while such a reader exists. The
// dir arrives via --skill because only the resource loader consumes explicit
// skill paths; package resolution (kept on the metadata path for extension
// discovery) never sees them.
func TestListModelsDoesNotReadSkillsDirectory(t *testing.T) {
	cwd := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(cwd)

	skillsDir := filepath.Join(cwd, "myskills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: probe\ndescription: probe skill\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(skillsDir, ".gitignore")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	var stdout bytes.Buffer
	go func() {
		done <- runCLIWithDependencies(context.Background(), []string{"--list-models", "--skill", skillsDir}, cliStreams{
			Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{}, StdinTTY: true, StdoutTTY: true,
		}, cliDependencies{})
	}()

	skillsRead := false
	deadline := time.After(60 * time.Second)
	for {
		select {
		case code := <-done:
			if code != 0 {
				t.Fatalf("exit=%d stdout=%q", code, stdout.String())
			}
			if skillsRead {
				t.Fatal("--list-models read the skills directory (ignore-file FIFO was opened)")
			}
			return
		case <-deadline:
			t.Fatal("--list-models did not finish; a reader may be parked on the skills FIFO")
		default:
		}
		// A write-open succeeds only while a reader holds the FIFO open; closing
		// immediately hands that reader EOF so the run is never left hanging.
		if writer, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			skillsRead = true
			_ = writer.Close()
		}
		time.Sleep(2 * time.Millisecond)
	}
}
