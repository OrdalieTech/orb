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

func TestRunAuthCommandLogout(t *testing.T) {
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
	if code != 0 || stdout.String() != "Logged out of anthropic.\n" || stderr.Len() != 0 {
		t.Fatalf("logout = code %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "{}" {
		t.Fatalf("auth.json = %q, %v", contents, err)
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
