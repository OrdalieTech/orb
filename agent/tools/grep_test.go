package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/internal/truncate"
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

func TestGrepTakesContextFromRipgrepEvents(t *testing.T) {
	requireUnixSearchTest(t)
	root := searchTreeRoot(t)
	events := func(kinds string, lines ...string) string {
		encoded := make([]string, len(lines))
		for index, line := range lines {
			kind := map[byte]string{'m': "match", 'c': "context"}[kinds[index]]
			encoded[index] = rgLineEvent(t, kind, "./rg-context.txt", index+1, line+"\n")
		}
		return strings.Join(encoded, "\n")
	}
	for _, test := range []struct {
		name, stdout string
		limit        float64
		want         string
	}{{
		// A match inside the last allowed match's trailing context shows as context (#9276).
		name:   "overlapping context",
		stdout: events("cmcmmcm", "a", "match one", "b", "match two", "match three", "c", "match four"),
		limit:  2,
		want: "rg-context.txt-1- a\nrg-context.txt:2: match one\nrg-context.txt-3- b\nrg-context.txt:4: match two\n" +
			"rg-context.txt-5- match three\n\n[2 matches limit reached. Use limit=4 for more, or refine pattern]",
	}, {
		name:   "exactly limit matches",
		stdout: events("mcmc", "match one", "x", "match two", "y"),
		limit:  2.5,
		want:   "rg-context.txt:1: match one\nrg-context.txt-2- x\nrg-context.txt:3: match two\nrg-context.txt-4- y",
	}, {
		name:   "lone CR kept, CRLF stripped",
		stdout: events("mc", "one\rtwo match\r", "next\r"),
		limit:  100,
		want:   "rg-context.txt:1: one\rtwo match\nrg-context.txt-2- next",
	}, {
		name:   "surrogate pair kept whole",
		stdout: events("m", strings.Repeat("a", truncate.GrepMaxLineLength-1)+"\U0001F600"),
		limit:  100,
		want: "rg-context.txt:1: " + strings.Repeat("a", truncate.GrepMaxLineLength-1) +
			"... [truncated]\n\n[Some lines truncated to 500 chars. Use read tool to see full lines]",
	}} {
		t.Run(test.name, func(t *testing.T) {
			installFakeManagedTool(t, "rg", test.stdout, "", 0, "")
			result, err := NewGrepTool(root, nil).Execute(context.Background(), "call", map[string]any{
				"pattern": "match", "context": 1.0, "limit": test.limit,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := toolResultText(t, result); got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
		})
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
	t.Setenv("ORB_AGENT_DIR", agentDir)
	t.Setenv("ORB_OFFLINE", "1")

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
