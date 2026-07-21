package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent/config"
)

// LOG-m5: bare `pigo logout` no longer silently defaults to anthropic; it
// lists the stored credentials and requires an explicit provider argument.
func TestLOGm5BareLogoutListsStoredCredentials(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	path := filepath.Join(agentDir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"anthropic":{"type":"oauth","refresh":"r","access":"a","expires":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runAuthCommand(context.Background(), CLIArgs{Command: "logout"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	})
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("bare logout = code %d, stdout %q", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), "usage: pigo logout <provider>") ||
		!strings.Contains(stderr.String(), "Stored credentials: anthropic") {
		t.Fatalf("bare logout stderr = %q", stderr.String())
	}
	contents, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(contents), "anthropic") {
		t.Fatalf("bare logout removed a credential: %q, %v", contents, err)
	}

	stdout.Reset()
	stderr.Reset()
	code = runAuthCommand(context.Background(), CLIArgs{Command: "logout", CommandArgs: []string{"anthropic"}}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	})
	if code != 0 || stdout.String() != "Logged out of anthropic.\n" || stderr.Len() != 0 {
		t.Fatalf("explicit logout = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	contents, err = os.ReadFile(path)
	if err != nil || string(contents) != "{}" {
		t.Fatalf("auth.json = %q, %v", contents, err)
	}
}

// LOG-m5: bare logout with nothing stored says so instead of failing on a
// phantom provider.
func TestLOGm5BareLogoutWithoutStoredCredentials(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	var stdout, stderr bytes.Buffer
	code := runAuthCommand(context.Background(), CLIArgs{Command: "logout"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	})
	if code != 1 || !strings.Contains(stderr.String(), "No stored credentials.") {
		t.Fatalf("empty bare logout = code %d, stderr %q", code, stderr.String())
	}
}

func TestRunAuthCommandLogoutAcceptsExplicitAnthropic(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	var stdout, stderr bytes.Buffer
	code := runAuthCommand(context.Background(), CLIArgs{Command: "logout", CommandArgs: []string{"anthropic"}}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	})
	if code != 0 || stdout.String() != "Logged out of anthropic.\n" || stderr.Len() != 0 {
		t.Fatalf("logout = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestRunAuthCommandRejectsUnsupportedProvider(t *testing.T) {
	var stderr bytes.Buffer
	code := runAuthCommand(context.Background(), CLIArgs{Command: "login", CommandArgs: []string{"openai"}}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &stderr,
	})
	if code != 1 || !strings.Contains(stderr.String(), `provider "openai" does not support headless login yet`) {
		t.Fatalf("unsupported login = code %d, stderr %q", code, stderr.String())
	}
}

func TestHeadlessAuthInteraction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	interaction := newHeadlessAuthInteraction(strings.NewReader("answer\n"), &stdout, &stderr)
	interaction.Notify(auth.AuthEvent{Type: auth.EventAuthURL, URL: "https://example.test", Instructions: "Open it."})
	answer, err := interaction.Prompt(context.Background(), auth.AuthPrompt{Type: auth.PromptManualCode, Message: "Paste code:"})
	if err != nil || answer != "answer" {
		t.Fatalf("prompt = %q, %v", answer, err)
	}
	if stdout.String() != "Open it.\nhttps://example.test\n" || stderr.String() != "Paste code:\n" {
		t.Fatalf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

// LOG-m5: headless PromptSelect prints the numbered options and maps a
// numbered (or literal-id) answer back to the option id.
func TestLOGm5HeadlessPromptSelectListsNumberedOptions(t *testing.T) {
	options := []auth.PromptOption{
		{ID: "max", Label: "Claude Pro/Max", Description: "Subscription"},
		{ID: "console", Label: "Console account"},
	}
	var stdout, stderr bytes.Buffer
	interaction := newHeadlessAuthInteraction(strings.NewReader("2\n"), &stdout, &stderr)
	answer, err := interaction.Prompt(context.Background(), auth.AuthPrompt{
		Type: auth.PromptSelect, Message: "Choose login method:", Options: options,
	})
	if err != nil || answer != "console" {
		t.Fatalf("numbered select = %q, %v", answer, err)
	}
	wantPrompt := "Choose login method:\n  1) Claude Pro/Max — Subscription\n  2) Console account\n"
	if stderr.String() != wantPrompt {
		t.Fatalf("select prompt = %q, want %q", stderr.String(), wantPrompt)
	}

	interaction = newHeadlessAuthInteraction(strings.NewReader("MAX\n"), &bytes.Buffer{}, &bytes.Buffer{})
	answer, err = interaction.Prompt(context.Background(), auth.AuthPrompt{
		Type: auth.PromptSelect, Message: "Choose:", Options: options,
	})
	if err != nil || answer != "max" {
		t.Fatalf("literal-id select = %q, %v", answer, err)
	}

	interaction = newHeadlessAuthInteraction(strings.NewReader("7\n"), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err = interaction.Prompt(context.Background(), auth.AuthPrompt{
		Type: auth.PromptSelect, Message: "Choose:", Options: options,
	}); err == nil || !strings.Contains(err.Error(), `invalid selection "7"`) {
		t.Fatalf("out-of-range select error = %v", err)
	}
}
