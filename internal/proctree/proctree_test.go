package proctree

import (
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestPipeWaitRearmsGraceOnActivity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stdout, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		pipes := &pipeReaders{activity: make(chan struct{}), done: make(chan struct{})}
		done := make(chan struct{})
		go func() {
			pipes.wait(stdout, stderr)
			close(done)
		}()
		synctest.Wait()
		for range 3 {
			time.Sleep(exitStdioGrace - time.Nanosecond)
			pipes.activity <- struct{}{}
		}
		pipes.done <- struct{}{}
		pipes.done <- struct{}{}
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("pipe wait did not finish")
		}
	})
}

// A command's processes are the kernel's first choice when memory runs out,
// so a runaway script dies before the agent that ran it.
func TestRunMakesTheCommandTheFirstOOMVictim(t *testing.T) {
	if _, err := os.Stat("/proc/self/oom_score_adj"); err != nil {
		t.Skip("no oom_score_adj on this platform")
	}
	var output []byte
	code, err := Run(t.Context(), Command{
		Script: "sleep 0.1; cat /proc/self/oom_score_adj", Dir: t.TempDir(),
		Shell:  func() (Shell, error) { return Shell{Path: "/bin/sh", Args: []string{"-c"}}, nil },
		OnData: func(_ bool, chunk []byte) error { output = append(output, chunk...); return nil },
	})
	if err != nil || code != 0 || strings.TrimSpace(string(output)) != "1000" {
		t.Fatalf("code %d, err %v, oom_score_adj %q", code, err, output)
	}
}
