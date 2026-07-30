package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestParseArgsVerboseFlag(t *testing.T) {
	// Regression: --verbose used to fall into the unknown-long-flag branch,
	// swallowing the following argument as its value and failing startup with
	// "Unknown option: --verbose" (upstream accepts it: args.ts:178-179).
	args := ParseArgs([]string{"--verbose", "hi"})
	if !args.Verbose {
		t.Fatal("--verbose not parsed")
	}
	if len(args.UnknownFlags) != 0 {
		t.Fatalf("--verbose treated as unknown flag: %#v", args.UnknownFlags)
	}
	if len(args.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", args.Diagnostics)
	}
	if len(args.Messages) != 1 || args.Messages[0] != "hi" {
		t.Fatalf("messages = %#v", args.Messages)
	}
}

func TestHelpTextListsVerbose(t *testing.T) {
	if !strings.Contains(helpText, "--verbose") {
		t.Fatal("help text does not document --verbose (args.ts:273)")
	}
}

func TestRPCModeRejectsFileArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"--mode", "rpc", "@missing.txt"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, cliDependencies{})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if got, want := stderr.String(), "Error: @file arguments are not supported in RPC mode\n"; got != want {
		t.Fatalf("stderr = %q, want %q (main.ts:546-549)", got, want)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestUnstampedVersionIsDev(t *testing.T) {
	// The unstamped default must not carry a semver, so a dev build can never
	// print a stale release number; goreleaser stamps releases via ldflags.
	if version != "dev" {
		t.Fatalf("unstamped version = %q, want %q", version, "dev")
	}
}

func TestStartupDiagnosticsColoredOnTTY(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	run := func(stdoutTTY bool) string {
		var stdout, stderr bytes.Buffer
		code := runCLIWithDependencies(context.Background(), []string{"-z"}, cliStreams{
			Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, StdoutTTY: stdoutTTY, StderrTTY: !stdoutTTY,
		}, cliDependencies{})
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		return stderr.String()
	}
	// Upstream's default chalk keys color support on STDOUT even for lines it
	// writes to stderr (chalk/source/index.js stdoutColor), so a TTY stderr
	// alone stays plain.
	if got, want := run(true), "\x1b[31mError: Unknown option: -z\x1b[39m\n"; got != want {
		t.Fatalf("stdout-TTY stderr = %q, want %q (main.ts:511-514)", got, want)
	}
	if got, want := run(false), "Error: Unknown option: -z\n"; got != want {
		t.Fatalf("non-TTY-stdout stderr = %q, want %q", got, want)
	}
}

func TestStartupDiagnosticsRespectNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var stdout, stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"-z"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, StderrTTY: true,
	}, cliDependencies{})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if got, want := stderr.String(), "Error: Unknown option: -z\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}
