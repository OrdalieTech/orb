package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
)

func loginJSONRows(t *testing.T, stdin string, args ...string) (rows []map[string]any, code int) {
	t.Helper()
	var out, errs bytes.Buffer
	code = runNativeCLI(t.Context(), append([]string{"login", "--json"}, args...), cliStreams{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errs})
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %q: %v (stderr %s)", line, err, errs.String())
		}
		rows = append(rows, row)
	}
	return rows, code
}

func TestLoginJSONListsAndRunsWhatSlashLoginOffers(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("PI_OFFLINE", "1")
	t.Setenv("GEMINI_API_KEY", "")
	find := func(rows []map[string]any, id, auth string) map[string]any {
		for _, row := range rows {
			if row["id"] == id && row["auth"] == auth {
				return row
			}
		}
		return nil
	}
	rows, code := loginJSONRows(t, "")
	if code != 0 || find(rows, "anthropic", "oauth") == nil || find(rows, "anthropic", "api_key") == nil || find(rows, "google", "api_key")["status"] != nil {
		t.Fatalf("listing: %d %v", code, rows)
	}
	// An API-key sign-in prompts for the secret, stores it where /login does, and says so.
	rows, code = loginJSONRows(t, "gm-test\n", "google")
	if code != 0 || rows[0]["type"] != "prompt" || rows[0]["kind"] != "secret" || rows[len(rows)-1]["type"] != "done" {
		t.Fatalf("api key: %d %v", code, rows)
	}
	rows, _ = loginJSONRows(t, "")
	if status, _ := find(rows, "google", "api_key")["status"].(map[string]any); status["source"] != "stored" {
		t.Fatalf("stored key not reported: %v", find(rows, "google", "api_key"))
	}
	// Closing stdin before answering cancels, and a provider without sign-in says why.
	if rows, code = loginJSONRows(t, "", "mistral"); code != 1 || rows[len(rows)-1]["message"] != "sign-in cancelled" {
		t.Fatalf("cancel: %d %v", code, rows)
	}
	if rows, code = loginJSONRows(t, "", "no-such-provider"); code != 1 || rows[0]["type"] != "error" {
		t.Fatalf("unknown: %d %v", code, rows)
	}
}
