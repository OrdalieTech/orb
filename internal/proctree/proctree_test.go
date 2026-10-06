package proctree

import (
	"os"
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
