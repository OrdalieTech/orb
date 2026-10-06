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
	"github.com/OrdalieTech/orb/ai/auth/oauth"
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

// Under the native store, the sign-in device ID lives in the database's
// global settings and never creates settings.json on disk.
func TestDeviceIDUsesTheNativeSettingsStore(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	state, err := openNativeState(context.Background(), agentDir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	id, err := withDeviceID(state, nil).(oauth.DeviceIDSource).DeviceID()
	if err != nil || id == "" {
		t.Fatalf("device ID = %q, %v", id, err)
	}
	if _, err := os.Stat(filepath.Join(agentDir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("settings.json on disk: %v", err)
	}
	settings, err := state.Settings(agentDir, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if stored, err := settings.DeviceID(); err != nil || stored != id {
		t.Fatalf("stored device ID = %q, %v; want %q", stored, err, id)
	}
}
