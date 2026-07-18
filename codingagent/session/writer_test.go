package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestManagerWritesExactCurrentMemberOrder(t *testing.T) {
	now := fixedTestTime(t)
	manager, err := InMemory(
		t.TempDir(),
		WithSessionID("session-fixed"),
		WithParentSession("/parent/session.jsonl"),
		WithClock(func() time.Time { return now }),
		WithEntryIDGenerator(sequenceIDGenerator(
			"00000001", "00000002", "00000003", "00000004",
			"00000005", "00000006", "00000007", "00000008",
			"00000009", "0000000a", "0000000b",
		)),
	)
	if err != nil {
		t.Fatal(err)
	}

	messageID, err := manager.AppendMessage(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: "hello <>&"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendThinkingLevelChange("high"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendModelChange("openai", "gpt-test"); err != nil {
		t.Fatal(err)
	}
	fromHook := false
	if _, err := manager.AppendCompaction(
		"summary <>&",
		messageID,
		12,
		OptionalEntryFields{
			Details: struct {
				Value string `json:"value"`
			}{Value: "<>&"},
			HasDetails: true,
			FromHook:   &fromHook,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendCustomEntry("empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendCustomEntry("null", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendCustomMessageEntry(
		"injected",
		json.RawMessage(`[{"type":"text","text":"custom"}]`),
		true,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendSessionInfo("  line one\r\nline two  "); err != nil {
		t.Fatal(err)
	}
	label := "checkpoint"
	if _, err := manager.AppendLabelChange(messageID, &label); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendLabelChange(messageID, nil); err != nil {
		t.Fatal(err)
	}
	trueValue := true
	if _, err := manager.BranchWithSummary(
		&messageID,
		"alternate",
		OptionalEntryFields{Details: nil, HasDetails: true, FromHook: &trueValue},
	); err != nil {
		t.Fatal(err)
	}

	got, err := manager.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	timestamp := "2025-01-02T03:04:05.006Z"
	cwd := manager.GetCWD()
	want := strings.Join([]string{
		fmt.Sprintf(`{"type":"session","version":3,"id":"session-fixed","timestamp":"%s","cwd":%q,"parentSession":"/parent/session.jsonl"}`, timestamp, cwd),
		fmt.Sprintf(`{"type":"message","id":"00000001","parentId":null,"timestamp":"%s","message":{"role":"user","content":"hello <>&"}}`, timestamp),
		fmt.Sprintf(`{"type":"thinking_level_change","id":"00000002","parentId":"00000001","timestamp":"%s","thinkingLevel":"high"}`, timestamp),
		fmt.Sprintf(`{"type":"model_change","id":"00000003","parentId":"00000002","timestamp":"%s","provider":"openai","modelId":"gpt-test"}`, timestamp),
		fmt.Sprintf(`{"type":"compaction","id":"00000004","parentId":"00000003","timestamp":"%s","summary":"summary <>&","firstKeptEntryId":"00000001","tokensBefore":12,"details":{"value":"<>&"},"fromHook":false}`, timestamp),
		fmt.Sprintf(`{"type":"custom","customType":"empty","id":"00000005","parentId":"00000004","timestamp":"%s"}`, timestamp),
		fmt.Sprintf(`{"type":"custom","customType":"null","data":null,"id":"00000006","parentId":"00000005","timestamp":"%s"}`, timestamp),
		fmt.Sprintf(`{"type":"custom_message","customType":"injected","content":[{"type":"text","text":"custom"}],"display":true,"details":null,"id":"00000007","parentId":"00000006","timestamp":"%s"}`, timestamp),
		fmt.Sprintf(`{"type":"session_info","id":"00000008","parentId":"00000007","timestamp":"%s","name":"line one line two"}`, timestamp),
		fmt.Sprintf(`{"type":"label","id":"00000009","parentId":"00000008","timestamp":"%s","targetId":"00000001","label":"checkpoint"}`, timestamp),
		fmt.Sprintf(`{"type":"label","id":"0000000a","parentId":"00000009","timestamp":"%s","targetId":"00000001"}`, timestamp),
		fmt.Sprintf(`{"type":"branch_summary","id":"0000000b","parentId":"00000001","timestamp":"%s","fromId":"00000001","summary":"alternate","details":null,"fromHook":true}`, timestamp),
	}, "\n") + "\n"
	if string(got) != want {
		t.Fatalf("writer mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRawNestedJSONIsRetained(t *testing.T) {
	manager, err := InMemory(
		t.TempDir(),
		WithSessionID("s"),
		WithClock(func() time.Time { return fixedTestTime(t) }),
		WithEntryIDGenerator(sequenceIDGenerator("00000001")),
	)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"role":"user", "content":"kept spacing"}`)
	if _, err := manager.AppendMessage(raw); err != nil {
		t.Fatal(err)
	}
	jsonl, err := manager.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jsonl), `"message":{"role":"user", "content":"kept spacing"}`) {
		t.Fatalf("raw nested JSON was rewritten: %s", jsonl)
	}
}

func TestInvalidRawJSONIsRejectedWithoutAppending(t *testing.T) {
	manager, err := InMemory(t.TempDir(), WithSessionID("s"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(json.RawMessage(`{"role":`)); err == nil {
		t.Fatal("invalid raw JSON was accepted")
	}
	if entries := manager.GetEntries(); len(entries) != 0 {
		t.Fatalf("invalid message appended %d entries", len(entries))
	}
}

func TestHeaderMetadataRoundTripsWithRawMemberOrder(t *testing.T) {
	version := CurrentVersion
	metadata := json.RawMessage(`{"z": 1,"nested":{"b":2, "a":1}}`)
	record := newHeaderRecord(SessionHeader{
		Type:      "session",
		Version:   &version,
		ID:        "session-id",
		Timestamp: "2026-07-18T00:00:00.000Z",
		CWD:       "/workspace",
		Metadata:  metadata,
	})
	encoded, err := record.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"session","version":3,"id":"session-id","timestamp":"2026-07-18T00:00:00.000Z","cwd":"/workspace","metadata":{"z": 1,"nested":{"b":2, "a":1}}}`
	if string(encoded) != want {
		t.Fatalf("header = %s, want %s", encoded, want)
	}

	parsed, err := parseFileEntryLine(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header == nil || string(parsed.Header.Metadata) != string(metadata) {
		t.Fatalf("parsed metadata = %s, want %s", parsed.Header.Metadata, metadata)
	}
	raw, ok := parsed.object.get("metadata")
	if !ok || string(raw) != string(metadata) {
		t.Fatalf("metadata member = %s, %v", raw, ok)
	}
	reencoded, err := parsed.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(reencoded) != want {
		t.Fatalf("reencoded header = %s, want %s", reencoded, want)
	}
}

func TestActiveToolsChangeUsesUpstreamWireShape(t *testing.T) {
	parentID := "parent"
	record := newEntryRecord(SessionEntry{
		Type:            "active_tools_change",
		ID:              "tools",
		ParentID:        &parentID,
		Timestamp:       "2026-07-18T00:00:01.000Z",
		ActiveToolNames: []string{"read", "bash <>&"},
	})
	encoded, err := record.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"active_tools_change","id":"tools","parentId":"parent","timestamp":"2026-07-18T00:00:01.000Z","activeToolNames":["read","bash <>&"]}`
	if string(encoded) != want {
		t.Fatalf("entry = %s, want %s", encoded, want)
	}

	parsed, err := parseFileEntryLine(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Entry == nil || len(parsed.Entry.ActiveToolNames) != 2 || parsed.Entry.ActiveToolNames[1] != "bash <>&" {
		t.Fatalf("active tools = %#v", parsed.Entry)
	}
}

func TestLeafTargetPreservesNullAndEmptyString(t *testing.T) {
	nullRecord := newEntryRecord(SessionEntry{
		Type:      "leaf",
		ID:        "leaf-null",
		Timestamp: "2026-07-18T00:00:02.000Z",
	})
	nullEncoded, err := nullRecord.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	nullWant := `{"type":"leaf","id":"leaf-null","parentId":null,"timestamp":"2026-07-18T00:00:02.000Z","targetId":null}`
	if string(nullEncoded) != nullWant {
		t.Fatalf("null leaf = %s, want %s", nullEncoded, nullWant)
	}
	nullParsed, err := parseFileEntryLine(nullEncoded)
	if err != nil {
		t.Fatal(err)
	}
	if nullParsed.Entry == nil || nullParsed.Entry.LeafTargetID != nil {
		t.Fatalf("parsed null leaf = %#v", nullParsed.Entry)
	}

	empty := ""
	emptyRecord := newEntryRecord(SessionEntry{
		Type:         "leaf",
		ID:           "leaf-empty",
		Timestamp:    "2026-07-18T00:00:03.000Z",
		LeafTargetID: &empty,
	})
	emptyEncoded, err := emptyRecord.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	emptyWant := `{"type":"leaf","id":"leaf-empty","parentId":null,"timestamp":"2026-07-18T00:00:03.000Z","targetId":""}`
	if string(emptyEncoded) != emptyWant {
		t.Fatalf("empty leaf = %s, want %s", emptyEncoded, emptyWant)
	}
	emptyParsed, err := parseFileEntryLine(emptyEncoded)
	if err != nil {
		t.Fatal(err)
	}
	if emptyParsed.Entry == nil || emptyParsed.Entry.LeafTargetID == nil || *emptyParsed.Entry.LeafTargetID != "" {
		t.Fatalf("parsed empty leaf = %#v", emptyParsed.Entry)
	}
}

func TestSessionStringHelpersPreserveUpstreamWhitespaceRules(t *testing.T) {
	if got := trimJSSpace("\ufeff\u00a0 value \u1680"); got != "value" {
		t.Fatalf("trimJSSpace() = %q", got)
	}
	if got := sanitizeSessionName("  line one\r\nline two\n\nline three  "); got != "line one line two line three" {
		t.Fatalf("sanitizeSessionName() = %q", got)
	}
}
