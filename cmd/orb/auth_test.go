package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/codingagent/config"
)

func TestCredentialPrintCommandParsing(t *testing.T) {
	command, err := parseCredentialPrintCommand([]string{
		"auth", "print-bearer-token", "--provider", "kimi-coding", "--min-expiry", "30m", "--model", "kimi-for-coding",
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.kind != credentialPrintBearer || command.minExpiry == nil || *command.minExpiry != 30*time.Minute {
		t.Fatalf("command = %#v", command)
	}
	if strings.Join(command.args, " ") != "--provider kimi-coding --model kimi-for-coding" {
		t.Fatalf("command args = %q", command.args)
	}
	if _, err := parseCredentialPrintCommand([]string{"auth", "print-api-key", "--min-expiry", "30m"}); err == nil ||
		!strings.Contains(err.Error(), "only supported by print-bearer-token") {
		t.Fatalf("API-key min expiry error = %v", err)
	}
	for _, value := range []string{"", "30", "1d", "-1m", "1.5h"} {
		if _, err := parseCredentialDuration(value); err == nil {
			t.Fatalf("parseCredentialDuration(%q) succeeded", value)
		}
	}
}

func TestCredentialPrintAPIKeyWritesOnlyTheSecretToStdout(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	if err := os.WriteFile(
		filepath.Join(agentDir, "auth.json"),
		[]byte(`{"openai":{"type":"api_key","key":"test-api-key"}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	handled, code := handleCredentialPrintCommand(context.Background(), []string{
		"auth", "print-api-key", "--model", "gpt-5.5",
	}, cliStreams{Stdout: &stdout, Stderr: &stderr})
	if !handled || code != 0 || stdout.String() != "test-api-key\n" || stderr.Len() != 0 {
		t.Fatalf("credential print = handled %t, code %d, stdout %q, stderr %q", handled, code, stdout.String(), stderr.String())
	}
}

func TestCredentialPrintBearerReadsAuthorizationHeader(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	expires := time.Now().Add(2 * time.Hour).UnixMilli()
	credential := []byte(`{"kimi-coding":{"type":"oauth","access":"test-bearer-token","refresh":"refresh-token","expires":` +
		strconv.FormatInt(expires, 10) + `}}`)
	if err := os.WriteFile(filepath.Join(agentDir, "auth.json"), credential, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	handled, code := handleCredentialPrintCommand(context.Background(), []string{
		"auth", "print-bearer-token", "--provider", "kimi-coding", "--model", "kimi-for-coding",
	}, cliStreams{Stdout: &stdout, Stderr: &stderr})
	if !handled || code != 0 || stdout.String() != "test-bearer-token\n" || stderr.Len() != 0 {
		t.Fatalf("credential print = handled %t, code %d, stdout %q, stderr %q", handled, code, stdout.String(), stderr.String())
	}
}

func TestCredentialPrintRejectsOAuthAsAnAPIKey(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	expires := time.Now().Add(time.Hour).UnixMilli()
	credential := []byte(`{"openai-codex":{"type":"oauth","access":"not-printed","refresh":"refresh-token","expires":` +
		strconv.FormatInt(expires, 10) + `}}`)
	if err := os.WriteFile(filepath.Join(agentDir, "auth.json"), credential, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	handled, code := handleCredentialPrintCommand(context.Background(), []string{
		"auth", "print-api-key", "--provider", "openai-codex", "--model", "gpt-5.5",
	}, cliStreams{Stdout: &stdout, Stderr: &stderr})
	if !handled || code != 1 || stdout.Len() != 0 ||
		stderr.String() != "Error: Provider \"openai-codex\" is configured with OAuth, not an API key\n" {
		t.Fatalf("credential print = handled %t, code %d, stdout %q, stderr %q", handled, code, stdout.String(), stderr.String())
	}
}

func TestCredentialPrintHelpAndValidation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	handled, code := handleCredentialPrintCommand(context.Background(), []string{"auth", "--help"}, cliStreams{
		Stdout: &stdout, Stderr: &stderr,
	})
	if !handled || code != 0 || !strings.Contains(stdout.String(), "orb auth print-api-key") || stderr.Len() != 0 {
		t.Fatalf("auth help = handled %t, code %d, stdout %q, stderr %q", handled, code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	handled, code = handleCredentialPrintCommand(context.Background(), []string{"auth", "print-api-key"}, cliStreams{
		Stdout: &stdout, Stderr: &stderr,
	})
	if !handled || code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "requires --model") {
		t.Fatalf("missing model = handled %t, code %d, stdout %q, stderr %q", handled, code, stdout.String(), stderr.String())
	}
}

func TestCredentialPrintErrorBoundary(t *testing.T) {
	run := func(t *testing.T, argv []string, wantStderr string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		handled, code := handleCredentialPrintCommand(context.Background(), argv, cliStreams{
			Stdout: &stdout,
			Stderr: &stderr,
		})
		if !handled || code != 1 || stdout.Len() != 0 || stderr.String() != wantStderr {
			t.Fatalf(
				"credential print = handled %t, code %d, stdout %q, stderr %q; want code 1, empty stdout, stderr %q",
				handled,
				code,
				stdout.String(),
				stderr.String(),
				wantStderr,
			)
		}
	}

	t.Run("parse error", func(t *testing.T) {
		run(
			t,
			[]string{"auth", "unknown"},
			"Error: Unknown auth command \"unknown\". Use \"orb auth print-api-key\" or \"orb auth print-bearer-token\".\n",
		)
	})

	t.Run("validation error", func(t *testing.T) {
		run(
			t,
			[]string{"auth", "print-api-key"},
			"Error: Credential printing requires --model <model>\n",
		)
	})

	t.Run("setup error", func(t *testing.T) {
		agentDir := filepath.Join(t.TempDir(), "agent")
		if err := os.WriteFile(agentDir, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(config.EnvAgentDir, agentDir)
		run(
			t,
			[]string{"auth", "print-api-key", "--model", "gpt-5.5"},
			"Error: Failed to resolve credential\n",
		)
	})

	t.Run("resolution error", func(t *testing.T) {
		agentDir := t.TempDir()
		t.Setenv(config.EnvAgentDir, agentDir)
		expires := time.Now().Add(10 * time.Minute).UnixMilli()
		credential := []byte(`{"openrouter":{"type":"oauth","access":"not-printed","refresh":"","expires":` +
			strconv.FormatInt(expires, 10) + `}}`)
		if err := os.WriteFile(filepath.Join(agentDir, "auth.json"), credential, 0o600); err != nil {
			t.Fatal(err)
		}
		run(
			t,
			[]string{
				"auth", "print-bearer-token",
				"--provider", "openrouter",
				"--model", "anthropic/claude-opus-5",
			},
			"Error: Failed to resolve credential\n",
		)
	})
}

// LOG-m5: bare `orb logout` no longer silently defaults to anthropic; it
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
	if !strings.Contains(stderr.String(), "usage: orb logout <provider>") ||
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
