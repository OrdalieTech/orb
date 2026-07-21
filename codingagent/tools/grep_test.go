package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/internal/truncate"
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

func TestGrepFractionalLimitUsesUpstreamThresholdAndNotice(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	path := filepath.Join(root, "context.txt")
	stdout := strings.Join([]string{
		rgMatchEvent(t, path, 2, "match one\n"),
		rgMatchEvent(t, path, 5, "match two\n"),
		rgMatchEvent(t, path, 6, "after two\n"),
	}, "\n")
	installFakeManagedTool(t, "rg", stdout, "", 0, "")

	result, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "match",
		"path":    path,
		"limit":   1.5,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "context.txt:2: match one\ncontext.txt:5: match two\n\n[1.5 matches limit reached. Use limit=3 for more, or refine pattern]"
	if got := toolResultText(t, result); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	details, ok := result.Details.(GrepToolDetails)
	if !ok || details.MatchLimitReached == nil || *details.MatchLimitReached != 1.5 {
		t.Fatalf("details = %#v", result.Details)
	}
}

func TestGrepAppliesGlobalByteLimitAfterMatchFormatting(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	path := filepath.Join(root, "visible.txt")
	line := strings.Repeat("x", truncate.GrepMaxLineLength)
	events := make([]string, 110)
	for index := range events {
		events[index] = rgMatchEvent(t, path, index+1, line+"\n")
	}
	installFakeManagedTool(t, "rg", strings.Join(events, "\n"), "", 0, "")

	result, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
		"pattern": "x",
		"path":    path,
		"limit":   1000,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	details, ok := result.Details.(GrepToolDetails)
	if !ok || details.Truncation == nil || !details.Truncation.Truncated {
		t.Fatalf("details = %#v", result.Details)
	}
	if details.MatchLimitReached != nil || details.LinesTruncated {
		t.Fatalf("unexpected secondary truncation details: %#v", details)
	}
	if got := toolResultText(t, result); !strings.HasSuffix(got, "\n\n[50.0KB limit reached]") {
		t.Fatalf("output lacks exact byte-limit notice: %q", got[len(got)-min(len(got), 100):])
	}
}

func TestGrepExitAndAvailabilityErrorsMatchUpstream(t *testing.T) {
	requireUnixSearchTest(t)
	t.Run("rg error", func(t *testing.T) {
		root := searchTreeRoot(t)
		installFakeManagedTool(t, "rg", "", "regex parse error", 2, "")
		_, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
			"pattern": "[",
			"path":    root,
		}, nil)
		if err == nil || err.Error() != "regex parse error" {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		root := searchTreeRoot(t)
		installFakeManagedTool(t, "rg", "", "", 1, "")
		result, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
			"pattern": "absent",
			"path":    root,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := toolResultText(t, result); got != "No matches found" || result.Details != nil {
			t.Fatalf("result = %q, details %#v", got, result.Details)
		}
	})

	t.Run("unavailable offline", func(t *testing.T) {
		t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
		t.Setenv("PI_OFFLINE", "1")
		t.Setenv("PATH", "")
		_, err := NewGrepTool(t.TempDir(), nil).Execute(context.Background(), "call", map[string]any{"pattern": "x"}, nil)
		want := "ripgrep (rg) is not available and could not be downloaded"
		if err == nil || err.Error() != want {
			t.Fatalf("error = %v, want %q", err, want)
		}
	})
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

func TestGrepAbortBeforeSpawnPreservesUpstreamMissedSignalQuirk(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	path := filepath.Join(root, "context.txt")
	installFakeManagedTool(t, "rg", rgMatchEvent(t, path, 2, "match one\n"), "", 0, "")
	operations := &blockingGrepOperations{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result agent.AgentToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := NewGrepTool(root, &GrepToolOptions{Operations: operations}).Execute(ctx, "call", map[string]any{
			"pattern": "match", "path": path,
		}, nil)
		done <- outcome{result: result, err: err}
	}()
	<-operations.started
	cancel()
	close(operations.release)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if text := toolResultText(t, got.result); text != "context.txt:2: match one" {
			t.Fatalf("result = %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("grep did not finish after the pre-spawn abort window")
	}
}

type blockingGrepOperations struct {
	started chan struct{}
	release chan struct{}
}

func (operations *blockingGrepOperations) IsDirectory(context.Context, string) (bool, error) {
	close(operations.started)
	<-operations.release
	return false, nil
}

func (*blockingGrepOperations) ReadFile(context.Context, string) (string, error) {
	return "", nil
}

func TestSearchNumberFormattingMatchesJavaScript(t *testing.T) {
	for _, testCase := range []struct {
		value float64
		want  string
	}{
		{value: math.Inf(1), want: "Infinity"},
		{value: math.Inf(-1), want: "-Infinity"},
		{value: 1.5, want: "1.5"},
		{value: math.Copysign(0, -1), want: "0"},
	} {
		t.Run(fmt.Sprint(testCase.value), func(t *testing.T) {
			if got := formatSearchNumber(testCase.value); got != testCase.want {
				t.Fatalf("formatSearchNumber(%v) = %q, want %q", testCase.value, got, testCase.want)
			}
		})
	}
}
