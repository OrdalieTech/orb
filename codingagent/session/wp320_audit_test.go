package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCreateBranchedSessionRechainsAndRecreatesResolvedLabelsInOrder(t *testing.T) {
	t.Parallel()
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clockCalls := 0
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		value := start.Add(time.Duration(clockCalls) * time.Second)
		clockCalls++
		return value
	}
	manager, err := InMemory(
		t.TempDir(),
		WithSessionID("source"),
		WithClock(clock),
		WithSessionIDGenerator(func(time.Time) (string, error) { return "branched", nil }),
		WithEntryIDGenerator(sequenceIDGenerator(
			"00000001", "00000002", "00000003", "00000004", "00000005", "00000006",
			"00000007", "00000008", "00000009", "0000000a", "0000000b",
		)),
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.AppendMessage(map[string]any{"role": "user", "content": "one", "timestamp": 1})
	if err != nil {
		t.Fatal(err)
	}
	firstLabel := "first"
	if _, err := manager.AppendLabelChange(first, &firstLabel); err != nil {
		t.Fatal(err)
	}
	second, err := manager.AppendMessage(map[string]any{"role": "user", "content": "two", "timestamp": 2})
	if err != nil {
		t.Fatal(err)
	}
	secondLabel := "second"
	secondLabelID, err := manager.AppendLabelChange(second, &secondLabel)
	if err != nil {
		t.Fatal(err)
	}
	updated := "first-updated"
	if _, err := manager.AppendLabelChange(first, &updated); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendLabelChange(first, nil); err != nil {
		t.Fatal(err)
	}
	readded := "first-readded"
	readdedID, err := manager.AppendLabelChange(first, &readded)
	if err != nil {
		t.Fatal(err)
	}
	offPath, err := manager.AppendMessage(map[string]any{"role": "user", "content": "off path", "timestamp": 3})
	if err != nil {
		t.Fatal(err)
	}
	offPathLabel := "discarded"
	if _, err := manager.AppendLabelChange(offPath, &offPathLabel); err != nil {
		t.Fatal(err)
	}

	originalEntries := manager.GetEntries()
	timestamps := make(map[string]string, len(originalEntries))
	for _, entry := range originalEntries {
		timestamps[entry.ID] = entry.Timestamp
	}
	path, err := manager.CreateBranchedSession(second)
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("in-memory branch path = %q, want empty", path)
	}

	entries := manager.GetEntries()
	if len(entries) != 4 {
		t.Fatalf("branched entries = %+v", entries)
	}
	if entries[0].ID != first || entries[0].ParentID != nil {
		t.Fatalf("first retained entry = %+v", entries[0])
	}
	if entries[1].ID != second || entries[1].ParentID == nil || *entries[1].ParentID != first {
		t.Fatalf("entry after removed label was not re-chained: %+v", entries[1])
	}
	if entries[2].Type != "label" || entries[2].ID != "0000000a" || entries[2].TargetID != second ||
		entries[2].Label == nil || *entries[2].Label != secondLabel || entries[2].Timestamp != timestamps[secondLabelID] ||
		entries[2].ParentID == nil || *entries[2].ParentID != second {
		t.Fatalf("first recreated label = %+v", entries[2])
	}
	if entries[3].Type != "label" || entries[3].ID != "0000000b" || entries[3].TargetID != first ||
		entries[3].Label == nil || *entries[3].Label != readded || entries[3].Timestamp != timestamps[readdedID] ||
		entries[3].ParentID == nil || *entries[3].ParentID != entries[2].ID {
		t.Fatalf("second recreated label = %+v", entries[3])
	}
	if manager.GetLabel(offPath) != nil {
		t.Fatal("label outside retained path survived")
	}
	header := manager.GetHeader()
	if header == nil || header.ID != "branched" || header.Timestamp != formatTimestamp(start.Add(10*time.Second)) || header.ParentSession != nil {
		t.Fatalf("branched header = %+v", header)
	}
}

func TestCreateBranchedSessionPersistenceBoundaryAndParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	manager, err := Create(
		root,
		filepath.Join(root, "sessions"),
		WithSessionID("source"),
		WithClock(func() time.Time { return now }),
		WithSessionIDGenerator(func(time.Time) (string, error) { return "branched", nil }),
		WithEntryIDGenerator(sequenceIDGenerator("00000001", "00000002", "00000003")),
	)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := manager.AppendMessage(map[string]any{"role": "user", "content": "question", "timestamp": 1})
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := manager.GetSessionFile()
	branchedPath, err := manager.CreateBranchedSession(userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(branchedPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("user-only branch exists before assistant: %v", statErr)
	}
	header := manager.GetHeader()
	if header == nil || header.ParentSession == nil || *header.ParentSession != sourcePath {
		t.Fatalf("persistent branch header = %+v", header)
	}
	if _, err := manager.AppendMessage(map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "answer"}},
		"provider": "test", "model": "test", "timestamp": 2,
	}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(branchedPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 3 {
		t.Fatalf("persisted branch line count = %d, want one header and two messages\n%s", len(lines), content)
	}
	var headerCount int
	for _, line := range lines {
		var record struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Type == "session" {
			headerCount++
		}
	}
	if headerCount != 1 {
		t.Fatalf("header count = %d", headerCount)
	}
}

func TestForkFromReplacesOnlyHeaderAndCreatesExclusively(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sourceCWD := filepath.Join(root, "source")
	targetCWD := filepath.Join(root, "target")
	for _, dir := range []string{sourceCWD, targetCWD} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sourcePath := filepath.Join(root, "source.jsonl")
	messageLine := `{"z":1,"type":"message","future":{"b":2,"a":1},"id":"entry-1","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"user","content":"hello","timestamp":1},"a":2}`
	source := fmt.Sprintf(
		`{"futureHeader":true,"cwd":%q,"type":"session","id":"source","timestamp":"2025-01-01T00:00:00.000Z","version":3}`+"\n"+
			messageLine+"\n"+
			`{"type":"session","id":"nested-header","timestamp":"2025-01-01T00:00:02.000Z","cwd":"ignored"}`+"\n"+
			`[1,{"future":true}]`+"\n",
		filepath.ToSlash(sourceCWD),
	)
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	sessionDir := filepath.Join(root, "sessions")
	options := []Option{WithSessionID("forked"), WithClock(func() time.Time { return now })}
	forked, err := ForkFrom(sourcePath, targetCWD, sessionDir, options...)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(forked.GetSessionFile())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 3 {
		t.Fatalf("fork lines = %d, want replacement header plus two non-header values\n%s", len(lines), content)
	}
	expectedHeader := fmt.Sprintf(
		`{"type":"session","version":3,"id":"forked","timestamp":"2025-01-02T03:04:05.000Z","cwd":%q,"parentSession":%q}`,
		filepath.ToSlash(targetCWD), filepath.ToSlash(sourcePath),
	)
	if lines[0] != expectedHeader {
		t.Fatalf("fork header\n got: %s\nwant: %s", lines[0], expectedHeader)
	}
	if lines[1] != messageLine {
		t.Fatalf("unknown fields or member order changed\n got: %s\nwant: %s", lines[1], messageLine)
	}
	if lines[2] != `[1,{"future":true}]` {
		t.Fatalf("non-object entry changed: %s", lines[2])
	}

	before := append([]byte(nil), content...)
	if _, err := ForkFrom(sourcePath, targetCWD, sessionDir, options...); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second fork error = %v, want exclusive-create collision", err)
	}
	after, err := os.ReadFile(forked.GetSessionFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("exclusive-create collision modified the existing fork")
	}
}

func TestForkFromErrorsMatchUpstream(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	missing := filepath.Join(root, "missing.jsonl")
	if _, err := ForkFrom(missing, root, filepath.Join(root, "sessions")); err == nil ||
		err.Error() != "Cannot fork: source session file is empty or invalid: "+missing {
		t.Fatalf("missing source error = %v", err)
	}
	invalid := filepath.Join(root, "invalid.jsonl")
	if err := os.WriteFile(invalid, []byte(`{"type":"message","id":"orphan"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ForkFrom(invalid, root, filepath.Join(root, "sessions")); err == nil ||
		err.Error() != "Cannot fork: source session file is empty or invalid: "+invalid {
		t.Fatalf("invalid source error = %v", err)
	}
}

func TestListBuildsSearchMetadataAndUsesLatestNameClear(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	contents := "" +
		`{"type":"session","version":3,"id":"listed","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/fixture","parentSession":"/parent.jsonl"}` + "\n" +
		`{"type":"session_info","id":"name-1","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","name":"First name"}` + "\n" +
		`{"type":"message","id":"user","parentId":"name-1","timestamp":"1970-01-01T00:00:01.000Z","message":{"role":"user","content":"user search text","timestamp":5000}}` + "\n" +
		`{"type":"message","id":"assistant","parentId":"user","timestamp":"1970-01-01T00:00:06.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"assistant search text"}]}}` + "\n" +
		`{"type":"message","id":"tool","parentId":"assistant","timestamp":"1970-01-01T00:00:09.000Z","message":{"role":"toolResult","content":null,"timestamp":9000}}` + "\n" +
		`{"type":"message","id":"custom","parentId":"tool","timestamp":"1970-01-01T00:00:10.000Z","message":{"role":"custom","content":"not searchable","timestamp":10000}}` + "\n" +
		`{"type":"session_info","id":"name-2","parentId":"custom","timestamp":"2025-01-01T00:00:11.000Z","name":"  "}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.UnixMilli(99_000), time.UnixMilli(99_000)); err != nil {
		t.Fatal(err)
	}
	info := buildSessionInfo(path)
	if info == nil {
		t.Fatal("valid session was rejected")
	}
	if info.Name != nil || info.MessageCount != 4 || info.FirstMessage != "user search text" {
		t.Fatalf("session info = %+v", info)
	}
	if info.AllMessagesText != "user search text assistant search text" {
		t.Fatalf("search text = %q", info.AllMessagesText)
	}
	if info.Modified.UnixMilli() != 6000 {
		t.Fatalf("modified = %d, want latest user/assistant activity 6000", info.Modified.UnixMilli())
	}
	if info.ParentSessionPath == nil || *info.ParentSessionPath != "/parent.jsonl" {
		t.Fatalf("parent session = %v", info.ParentSessionPath)
	}
}

func TestListRejectsInvalidUserAssistantContentAndCountsProgress(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	projectA := filepath.Join(root, "project-a")
	projectB := filepath.Join(root, "project-b")
	for _, dir := range []string{projectA, projectB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeListedSession(t, filepath.Join(root, "a.jsonl"), "a", projectA,
		`{"role":"user","content":"kept","timestamp":1000}`)
	writeListedSession(t, filepath.Join(root, "b.jsonl"), "b", projectB,
		`{"role":"assistant","content":[{"type":"text","text":"other"}],"timestamp":2000}`)
	writeListedSession(t, filepath.Join(root, "null.jsonl"), "null", projectA,
		`{"role":"user","content":null,"timestamp":3000}`)
	writeListedSession(t, filepath.Join(root, "object.jsonl"), "object", projectA,
		`{"role":"assistant","content":{"type":"text","text":"bad"},"timestamp":4000}`)
	if err := os.Mkdir(filepath.Join(root, "directory.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	var progress [][2]int
	sessions := List(projectA, root, func(loaded, total int) {
		progress = append(progress, [2]int{loaded, total})
	})
	if len(sessions) != 1 || sessions[0].ID != "a" {
		t.Fatalf("filtered sessions = %+v", sessions)
	}
	if len(progress) != 5 || progress[len(progress)-1] != [2]int{5, 5} {
		t.Fatalf("progress must include filtered and invalid .jsonl paths: %+v", progress)
	}
	for index, update := range progress {
		if update != [2]int{index + 1, 5} {
			t.Fatalf("progress[%d] = %v", index, update)
		}
	}
}

func TestListOnlyFiltersWhenDirectoryDiffersFromDefault(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	projectA := filepath.Join(root, "project-a")
	projectB := filepath.Join(root, "project-b")
	for _, dir := range []string{projectA, projectB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defaultDir, err := DefaultSessionDir(projectA, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	writeListedSession(t, filepath.Join(defaultDir, "different-cwd.jsonl"), "different", projectB,
		`{"role":"user","content":"still listed","timestamp":1000}`)
	sessions := List(projectA, defaultDir, nil, WithAgentDir(agentDir))
	if len(sessions) != 1 || sessions[0].ID != "different" {
		t.Fatalf("explicit default directory was incorrectly cwd-filtered: %+v", sessions)
	}
}

func TestListAllDefaultTracksEveryCandidateAcrossProjectDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	projectA := filepath.Join(root, "project-a")
	projectB := filepath.Join(root, "project-b")
	for _, project := range []string{projectA, projectB} {
		if err := os.MkdirAll(project, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dirA, err := DefaultSessionDir(projectA, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	dirB, err := DefaultSessionDir(projectB, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	writeListedSession(t, filepath.Join(dirA, "older.jsonl"), "older", projectA,
		`{"role":"user","content":"older","timestamp":1000}`)
	writeListedSession(t, filepath.Join(dirB, "newer.jsonl"), "newer", projectB,
		`{"role":"assistant","content":[{"type":"text","text":"newer"}],"timestamp":2000}`)
	if err := os.WriteFile(filepath.Join(dirA, "invalid.jsonl"), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dirB, "directory.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	for index := range 8 {
		path := filepath.Join(dirA, fmt.Sprintf("invalid-%02d.jsonl", index))
		if err := os.WriteFile(path, []byte("not json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var progress [][2]int
	sessions := ListAll("", func(loaded, total int) {
		progress = append(progress, [2]int{loaded, total})
	}, WithAgentDir(agentDir))
	if len(sessions) != 2 || sessions[0].ID != "newer" || sessions[1].ID != "older" {
		t.Fatalf("all sessions = %+v", sessions)
	}
	if len(progress) != 12 || progress[len(progress)-1] != [2]int{12, 12} {
		t.Fatalf("cross-project progress = %+v", progress)
	}
}

func TestListModifiedFallbackPrecedence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tests := []struct {
		name      string
		header    string
		entry     string
		mtime     int64
		wantMilli int64
	}{
		{
			name: "epoch message falls back to header", header: "2025-01-01T00:00:00.000Z",
			entry: `{"role":"user","content":"epoch","timestamp":0}`, mtime: 99_000,
			wantMilli: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
		},
		{
			name: "missing message timestamp uses entry", header: "2025-01-01T00:00:00.000Z",
			entry: `{"role":"assistant","content":[]}`, mtime: 99_000, wantMilli: 7000,
		},
		{
			name: "invalid header falls back to mtime", header: "invalid",
			entry: `{"role":"user"}`, mtime: 99_000, wantMilli: 99_000,
		},
		{
			name: "positive fraction overrides header", header: "2025-01-01T00:00:00.000Z",
			entry: `{"role":"user","content":"fraction","timestamp":0.9}`, mtime: 99_000, wantMilli: 0,
		},
	}
	for index, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, fmt.Sprintf("%d.jsonl", index))
			contents := fmt.Sprintf(
				"{\"type\":\"session\",\"version\":3,\"id\":\"test\",\"timestamp\":%q,\"cwd\":\"/fixture\"}\n"+
					"{\"type\":\"message\",\"id\":\"message\",\"parentId\":null,\"timestamp\":\"1970-01-01T00:00:07.000Z\",\"message\":%s}\n",
				test.header, test.entry,
			)
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, time.UnixMilli(test.mtime), time.UnixMilli(test.mtime)); err != nil {
				t.Fatal(err)
			}
			info := buildSessionInfo(path)
			if info == nil || info.Modified.UnixMilli() != test.wantMilli {
				t.Fatalf("session info = %+v, want modified %d", info, test.wantMilli)
			}
		})
	}
}

func writeListedSession(t *testing.T, path, id, cwd, message string) {
	t.Helper()
	contents := fmt.Sprintf(
		"{\"type\":\"session\",\"version\":3,\"id\":%q,\"timestamp\":\"2025-01-01T00:00:00.000Z\",\"cwd\":%q}\n"+
			"{\"type\":\"message\",\"id\":\"message\",\"parentId\":null,\"timestamp\":\"2025-01-01T00:00:01.000Z\",\"message\":%s}\n",
		id, filepath.ToSlash(cwd), message,
	)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
