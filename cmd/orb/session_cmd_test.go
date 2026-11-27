package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/platforms/native"
	"github.com/OrdalieTech/orb/platforms/native/sqlite"
)

func seedSessionCLIStore(t *testing.T) (agentDir, dirA, dirB string) {
	t.Helper()
	root := t.TempDir()
	agentDir = filepath.Join(root, "agent")
	dirA = filepath.Join(root, "proj-a")
	dirB = filepath.Join(root, "proj-b")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ORB_MITOSIS_HOME", filepath.Join(root, "mitosis"))
	t.Setenv("ORB_OFFLINE", "1")

	state, err := native.Open(t.Context(), agentDir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()

	now := time.Now().UTC()
	oldTS := now.Add(-14 * 24 * time.Hour).Format(time.RFC3339Nano)
	newTS := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)

	importOne := func(id, cwd, ts, text string) {
		t.Helper()
		cwdJSON, _ := json.Marshal(cwd)
		p := filepath.Join(root, id+".jsonl")
		data := fmt.Sprintf(
			"{\"type\":\"session\",\"version\":3,\"id\":%q,\"timestamp\":%q,\"cwd\":%s}\n"+
				"{\"type\":\"message\",\"id\":\"m1\",\"parentId\":null,\"timestamp\":%q,\"message\":{\"role\":\"user\",\"content\":%q,\"timestamp\":1790503201000}}\n",
			id, ts, string(cwdJSON), ts, text,
		)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := state.Sessions().OpenPath(t.Context(), p); err != nil {
			t.Fatal("import:", err)
		}
	}

	importOne("aaaa1111-1111-4111-8111-111111111111", dirA, oldTS, "old alpha work")
	importOne("bbbb2222-2222-4222-8222-222222222222", dirA, newTS, "recent beta work")
	importOne("cccc3333-3333-4333-8333-333333333333", dirB, newTS, "gamma deployment")
	return agentDir, dirA, dirB
}

func runSessionCLI(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runNativeCLI(t.Context(), argv, cliStreams{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return code, stdout.String(), stderr.String()
}

func TestSessionCLIHelpAndPiFilesError(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ORB_MITOSIS_HOME", filepath.Join(root, "mitosis"))
	t.Setenv("ORB_OFFLINE", "1")

	for _, args := range [][]string{{"session"}, {"session", "--help"}} {
		code, out, errOut := runSessionCLI(t, args...)
		if code != 0 || !strings.Contains(out, "Usage: orb session") {
			t.Fatalf("%v: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}

	// --pi-files with a fresh dir should reject `orb session list`.
	piRoot := t.TempDir()
	code, _, errOut := runSessionCLI(t, "--pi-files", "--agent-dir", filepath.Join(piRoot, "agent"), "--state-home", filepath.Join(piRoot, "state"), "session", "list")
	if code == 0 || !strings.Contains(errOut, "--pi-files") {
		t.Fatalf("--pi-files: code=%d err=%q", code, errOut)
	}
}

func TestSessionCLIListArchiveUnarchivePrivate(t *testing.T) {
	_, dirA, dirB := seedSessionCLIStore(t)

	// list --json returns all 3 active sessions.
	code, out, errOut := runSessionCLI(t, "session", "list", "--json")
	if code != 0 {
		t.Fatalf("list --json failed: %d %s", code, errOut)
	}
	var entries []sqlite.CatalogEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) != 3 {
		t.Fatalf("list --json = %q (%v)", out, err)
	}

	// list --cwd dirA --limit 1
	code, out, errOut = runSessionCLI(t, "session", "list", "--cwd", dirA, "--limit", "1", "--json")
	if code != 0 {
		t.Fatalf("list --cwd --limit failed: %d %s", code, errOut)
	}
	entries = nil
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) != 1 || entries[0].CWD != dirA {
		t.Fatalf("list --cwd --limit = %q (%v)", out, err)
	}

	// list --match gamma
	code, out, errOut = runSessionCLI(t, "session", "list", "--match", "gamma", "--json")
	if code != 0 {
		t.Fatalf("list --match failed: %d %s", code, errOut)
	}
	entries = nil
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) != 1 || entries[0].ID != "cccc3333-3333-4333-8333-333333333333" {
		t.Fatalf("list --match = %q (%v)", out, err)
	}

	// archive without filter refuses.
	code, _, errOut = runSessionCLI(t, "session", "archive")
	if code == 0 || !strings.Contains(errOut, "refusing to archive") {
		t.Fatalf("archive without filter: code=%d err=%q", code, errOut)
	}

	// archive --before 7d --dry-run previews aaaa1111 without mutating.
	code, out, errOut = runSessionCLI(t, "session", "archive", "--before", "7d", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("archive --before --dry-run failed: %d %s", code, errOut)
	}
	var archResp struct {
		Archived bool                  `json:"archived"`
		DryRun   bool                  `json:"dry_run"`
		Count    int                   `json:"count"`
		Sessions []sqlite.CatalogEntry `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &archResp); err != nil || !archResp.DryRun || archResp.Count != 1 || archResp.Sessions[0].ID != "aaaa1111-1111-4111-8111-111111111111" {
		t.Fatalf("dry-run response = %q (%v)", out, err)
	}
	// Still 3 active sessions after dry-run.
	code, out, _ = runSessionCLI(t, "session", "list", "--json")
	entries = nil
	if code != 0 || json.Unmarshal([]byte(out), &entries) != nil || len(entries) != 3 {
		t.Fatalf("after dry-run list = %q", out)
	}

	// archive --before 7d actually archives aaaa1111.
	code, out, errOut = runSessionCLI(t, "session", "archive", "--before", "7d")
	if code != 0 || !strings.Contains(out, "Archived 1 session(s).") {
		t.Fatalf("archive --before 7d: code=%d out=%q err=%q", code, out, errOut)
	}

	// archive by prefix (bbbb)
	code, out, errOut = runSessionCLI(t, "session", "archive", "bbbb")
	if code != 0 || !strings.Contains(out, "Archived 1 session(s).") {
		t.Fatalf("archive prefix: code=%d out=%q err=%q", code, out, errOut)
	}

	// list active -> 1, list --archived -> 2, list --all -> 3
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"session", "list", "--json"}, 1},
		{[]string{"session", "list", "--archived", "--json"}, 2},
		{[]string{"session", "list", "--all", "--json"}, 3},
	} {
		code, out, errOut = runSessionCLI(t, tc.args...)
		entries = nil
		if code != 0 || json.Unmarshal([]byte(out), &entries) != nil || len(entries) != tc.want {
			t.Fatalf("%v: code=%d len=%d want=%d out=%q err=%q", tc.args, code, len(entries), tc.want, out, errOut)
		}
	}

	// unarchive --cwd dirA unarchives both aaaa and bbbb.
	code, out, errOut = runSessionCLI(t, "session", "unarchive", "--cwd", dirA, "--json")
	if code != 0 {
		t.Fatalf("unarchive --cwd failed: %d %s", code, errOut)
	}
	archResp = struct {
		Archived bool                  `json:"archived"`
		DryRun   bool                  `json:"dry_run"`
		Count    int                   `json:"count"`
		Sessions []sqlite.CatalogEntry `json:"sessions"`
	}{}
	if err := json.Unmarshal([]byte(out), &archResp); err != nil || archResp.Archived || archResp.Count != 2 {
		t.Fatalf("unarchive --cwd = %q (%v)", out, err)
	}

	// archive --match gamma --cwd dirB
	code, out, errOut = runSessionCLI(t, "session", "archive", "--cwd", dirB, "--match", "gamma")
	if code != 0 || !strings.Contains(out, "Archived 1 session(s).") {
		t.Fatalf("archive --cwd --match: code=%d out=%q err=%q", code, out, errOut)
	}
	// unarchive by exact ID
	code, out, errOut = runSessionCLI(t, "session", "unarchive", "cccc3333-3333-4333-8333-333333333333")
	if code != 0 || !strings.Contains(out, "Unarchived 1 session(s).") {
		t.Fatalf("unarchive exact: code=%d out=%q err=%q", code, out, errOut)
	}

	// private on/off via prefix
	code, out, errOut = runSessionCLI(t, "session", "private", "cccc", "--json")
	if code != 0 {
		t.Fatalf("private failed: %d %s", code, errOut)
	}
	var privResp struct {
		LocalOnly bool                  `json:"local_only"`
		Count     int                   `json:"count"`
		Sessions  []sqlite.CatalogEntry `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &privResp); err != nil || !privResp.LocalOnly || privResp.Count != 1 || !privResp.Sessions[0].LocalOnly {
		t.Fatalf("private = %q (%v)", out, err)
	}

	code, out, errOut = runSessionCLI(t, "session", "private", "cccc", "--off", "--json")
	if code != 0 {
		t.Fatalf("private --off failed: %d %s", code, errOut)
	}
	privResp = struct {
		LocalOnly bool                  `json:"local_only"`
		Count     int                   `json:"count"`
		Sessions  []sqlite.CatalogEntry `json:"sessions"`
	}{}
	if err := json.Unmarshal([]byte(out), &privResp); err != nil || privResp.LocalOnly || privResp.Count != 1 || privResp.Sessions[0].LocalOnly {
		t.Fatalf("private --off = %q (%v)", out, err)
	}
}

func TestParseHumanDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"30d", 30 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
		{"24h", 24 * time.Hour},
		{"90m", 90 * time.Minute},
		{"1w3d", 10 * 24 * time.Hour},
	}
	for _, tc := range cases {
		got, err := parseHumanDuration(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("parseHumanDuration(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "0d", "-5h", "abc", "10x"} {
		if _, err := parseHumanDuration(bad); err == nil {
			t.Fatalf("parseHumanDuration(%q) succeeded, want error", bad)
		}
	}
}
