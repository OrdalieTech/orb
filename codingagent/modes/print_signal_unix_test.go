//go:build !windows

package modes

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
)

func TestRunPrintModeSubprocessExitsWithSignalCode(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestRunPrintModeSignalHelper$")
	command.Env = append(os.Environ(),
		"PI_GO_PRINT_SIGNAL_HELPER=1",
		"PI_GO_PRINT_SIGNAL_READY="+ready,
	)
	output := &lockedBuffer{}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper did not become ready: %s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := command.Wait()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 143 {
		t.Fatalf("wait error = %v, output = %q", err, output.String())
	}
	if output.String() != "" {
		t.Fatalf("signal shutdown output = %q", output.String())
	}
}

func TestRunPrintModeSignalHelper(t *testing.T) {
	if os.Getenv("PI_GO_PRINT_SIGNAL_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("unused")})
	session := newPrintAgent(provider)
	session.Subscribe(func(ctx context.Context, _ agent.AgentEvent) error {
		if err := os.WriteFile(os.Getenv("PI_GO_PRINT_SIGNAL_READY"), []byte("ready"), 0o600); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	code := RunPrintMode(context.Background(), session, PrintModeOptions{
		InitialMessage: "block",
	})
	os.Exit(code)
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer []byte
}

func (buffer *lockedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	buffer.buffer = append(buffer.buffer, value...)
	return len(value), nil
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return string(buffer.buffer)
}
