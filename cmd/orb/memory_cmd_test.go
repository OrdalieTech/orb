package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
)

func seedMemoryStore(t *testing.T, ctx context.Context, store memory.Store) {
	t.Helper()
	base := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	entries := []memory.Entry{
		{Kind: memory.KindUser, Text: "We decided to deploy OptChat memory on Tuesday.", Peer: "ana", Session: "s1", TS: base},
		{Kind: memory.KindTalk, Text: "Confirmed: OptChat memory uses a binary summary tree.", Peer: "orb", Session: "s1", TS: base.Add(time.Minute)},
		{Kind: memory.KindWork, Text: "Implemented filestore journal and SQLite store.", Peer: "orb", Session: "s1", TS: base.Add(2 * time.Minute)},
		{Kind: memory.KindNote, Text: "Release checklist verified with race detector.", Peer: "orb", Session: "s1", TS: base.Add(3 * time.Minute)},
	}
	for _, e := range entries {
		if _, err := store.AppendEntry(ctx, e); err != nil {
			t.Fatalf("AppendEntry: %v", err)
		}
	}
	if err := store.SaveProfile(ctx, []memory.ProfileEntry{
		{ID: "p1", Content: "Prefers concise Go code.", TS: base},
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
}

func runMemoryCLI(t *testing.T, ctx context.Context, argv []string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runCLI(ctx, argv, cliStreams{Stdout: &stdout, Stderr: &stderr})
	return code, stdout.String(), stderr.String()
}

func TestMemoryCLIHelp(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)

	code, out, errOut := runMemoryCLI(t, context.Background(), []string{"memory", "--help"})
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"Usage: orb memory", "status", "view", "compact", "search", "zoom"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help output missing %q:\n%s", want, out)
		}
	}
}

func TestMemoryCLISubcommandsFileBacked(t *testing.T) {
	ctx := context.Background()
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)

	store, err := filestore.NewFileStore(filepath.Join(agentDir, "memory"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	seedMemoryStore(t, ctx, store)

	// 1. status (text + json)
	code, out, errOut := runMemoryCLI(t, ctx, []string{"memory", "status"})
	if code != 0 || !strings.Contains(out, "Entries: 4") || !strings.Contains(out, "Profile: 1 entries") {
		t.Fatalf("status: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "status", "--json"})
	if code != 0 {
		t.Fatalf("status --json: code=%d err=%q", code, errOut)
	}
	var status memoryStatusReport
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("unmarshal status: %v (%s)", err, out)
	}
	if status.Entries != 4 || status.ProfileEntries != 1 || status.ViewBudgetBytes != memory.DefaultViewBytes {
		t.Fatalf("unexpected status report: %+v", status)
	}

	// 2. compact (text + json)
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "compact", "--json"})
	if code != 0 {
		t.Fatalf("compact --json: code=%d err=%q", code, errOut)
	}
	var compacted memoryCompactReport
	if err := json.Unmarshal([]byte(out), &compacted); err != nil {
		t.Fatalf("unmarshal compact: %v (%s)", err, out)
	}
	if compacted.BuiltNodes < 4 {
		t.Fatalf("expected at least 4 built nodes, got %d", compacted.BuiltNodes)
	}

	// Second compact should build 0 nodes.
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "compact"})
	if code != 0 || !strings.Contains(out, "Compacted 0 node(s).") {
		t.Fatalf("second compact: code=%d out=%q err=%q", code, out, errOut)
	}

	// 3. view (text + json)
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "view"})
	if code != 0 || !strings.Contains(out, "USER PROFILE") || !strings.Contains(out, "Prefers concise Go code.") || !strings.Contains(out, "<memory_view>") {
		t.Fatalf("view: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "view", "--view-bytes", "4096", "--json"})
	if code != 0 {
		t.Fatalf("view --json: code=%d err=%q", code, errOut)
	}
	var viewReport memoryViewReport
	if err := json.Unmarshal([]byte(out), &viewReport); err != nil {
		t.Fatalf("unmarshal view: %v (%s)", err, out)
	}
	if viewReport.EntryCount != 4 || viewReport.ViewBudgetBytes != 4096 || !strings.Contains(viewReport.Profile, "Prefers concise Go code.") {
		t.Fatalf("unexpected view report: %+v", viewReport)
	}

	// 4. search (text + json)
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "search", "OptChat"})
	if code != 0 || !strings.Contains(out, "OptChat") {
		t.Fatalf("search: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "search", "OptChat", "--limit", "1", "--json"})
	if code != 0 {
		t.Fatalf("search --json: code=%d err=%q", code, errOut)
	}
	var hits []memory.SearchHit
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatalf("unmarshal search: %v (%s)", err, out)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit with --limit 1, got %d", len(hits))
	}

	// 5. zoom: single entry id, id+n ref, id n args, --depth, --offset, --json
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "zoom", "0"})
	if code != 0 || !strings.Contains(out, "deploy OptChat memory on Tuesday") {
		t.Fatalf("zoom 0: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "zoom", "0+2", "--depth", "1", "--json"})
	if code != 0 {
		t.Fatalf("zoom 0+2 --json: code=%d err=%q", code, errOut)
	}
	var zoomed memoryZoomReport
	if err := json.Unmarshal([]byte(out), &zoomed); err != nil {
		t.Fatalf("unmarshal zoom: %v (%s)", err, out)
	}
	if zoomed.Ref != "0+2" || !strings.Contains(zoomed.Text, "0+1:") || !strings.Contains(zoomed.Text, "1+1:") {
		t.Fatalf("unexpected zoom report: %+v", zoomed)
	}
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "zoom", "0", "4", "--depth", "1"})
	if code != 0 || !strings.Contains(out, "0+2:") || !strings.Contains(out, "2+2:") {
		t.Fatalf("zoom 0 4: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = runMemoryCLI(t, ctx, []string{"memory", "zoom", "0", "--offset", "10"})
	if code != 0 || out == "" {
		t.Fatalf("zoom --offset: code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestMemoryCLINativeSQLite(t *testing.T) {
	ctx := context.Background()
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)

	state, err := openNativeState(ctx, agentDir, false)
	if err != nil {
		t.Fatalf("openNativeState: %v", err)
	}
	defer func() { _ = state.Close() }()

	seedMemoryStore(t, ctx, state.Memory())
	nativeCtx := context.WithValue(ctx, nativeStateKey{}, state)

	code, out, errOut := runMemoryCLI(t, nativeCtx, []string{"memory", "status", "--json"})
	if code != 0 {
		t.Fatalf("native status --json: code=%d err=%q", code, errOut)
	}
	var status memoryStatusReport
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("unmarshal status: %v (%s)", err, out)
	}
	if status.Entries != 4 || status.ProfileEntries != 1 {
		t.Fatalf("unexpected native status: %+v", status)
	}
}
