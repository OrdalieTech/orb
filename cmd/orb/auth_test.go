package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
)

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
