package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	agentharness "github.com/OrdalieTech/orb/agent/harness"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/conformance/runner"
)

// f6FixedNowMS is the extract script's frozen clock ("2026-02-03T04:05:06.789Z").
const f6FixedNowMS = int64(1770091506789)

func f6V4Now() int64 { return f6FixedNowMS }

func f6HarnessString(value string) *string { return &value }

func f6HarnessInt(value int) *int { return &value }

func f6V4User(text string, timestamp int) string {
	return fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":"%s"}],"timestamp":%d}`, text, timestamp)
}

func f6V4Assistant(text string, timestamp int, stopReason string) string {
	return fmt.Sprintf(
		`{"role":"assistant","content":[{"type":"text","text":"%s"}],"api":"openai-responses","provider":"openai","model":"gpt-test",`+
			`"usage":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"totalTokens":3,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},`+
			`"stopReason":"%s","timestamp":%d}`, text, stopReason, timestamp)
}

const f6V4Usage = `{"input":100,"output":20,"cacheRead":30,"cacheWrite":10,"totalTokens":160,"cost":{"input":0.1,"output":0.2,"cacheRead":0.01,"cacheWrite":0.02,"total":0.33}}`

func TestF6HarnessPublicEntryCodecMatchesUpstreamJSONL(t *testing.T) {
	input, err := runner.ReadFixture("F6Harness", "session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(input), []byte{'\n'})
	header, err := agentharness.ParseSessionV4Header(lines[0], "<header>")
	if err != nil {
		t.Fatal(err)
	}
	encodedHeader, err := agentharness.MarshalSessionV4Header(header)
	if err != nil {
		t.Fatal(err)
	}
	if diff := runner.ByteDiff(lines[0], encodedHeader); diff != "" {
		t.Fatalf("fixture header changed:\n%s", diff)
	}
	seenKinds, seenEntryTypes, seenRecordTypes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for index, line := range lines[1:] {
		mutation, parseErr := agentharness.ParseSessionV4Mutation(line, "<entry>", index+2)
		if parseErr != nil {
			t.Fatalf("parse fixture line %d: %v", index+2, parseErr)
		}
		seenKinds[mutation.Kind] = true
		if mutation.Entry != nil {
			seenEntryTypes[mutation.Entry.Type] = true
		}
		if mutation.Record != nil {
			seenRecordTypes[mutation.Record.Type] = true
		}
		encoded, marshalErr := agentharness.MarshalSessionV4Mutation(mutation)
		if marshalErr != nil {
			t.Fatalf("marshal fixture line %d: %v", index+2, marshalErr)
		}
		if diff := runner.ByteDiff(line, encoded); diff != "" {
			t.Fatalf("fixture line %d changed:\n%s", index+2, diff)
		}
	}
	for _, kind := range []string{"entry", "record", "lane", "fact"} {
		if !seenKinds[kind] {
			t.Errorf("fixture did not exercise mutation kind %q", kind)
		}
	}
	for _, entryType := range []string{
		"message", "thinking_level_change", "model_change", "active_tools_change",
		"custom", "compaction", "branch_summary",
	} {
		if !seenEntryTypes[entryType] {
			t.Errorf("fixture did not exercise entry type %q", entryType)
		}
	}
	for _, recordType := range []string{"operation_started", "step_attempt", "usage", "operation_finished"} {
		if !seenRecordTypes[recordType] {
			t.Errorf("fixture did not exercise record type %q", recordType)
		}
	}
}

type f6HarnessFixture struct {
	SchemaVersion int            `json:"schemaVersion"`
	Session       map[string]any `json:"session"`
	Env           map[string]any `json:"env"`
}

// f6RunV4SessionScript replays the extract script's mutation sequence and
// returns its captured error observations.
func f6RunV4SessionScript(t *testing.T, storage agentharness.SessionV4Storage, root string) map[string]any {
	t.Helper()
	appendEntry := func(payload string, lane string) error {
		_, err := storage.AppendEntry(json.RawMessage(payload), lane)
		return err
	}
	mustAppend := func(payload string, lane string) {
		t.Helper()
		if err := appendEntry(payload, lane); err != nil {
			t.Fatal(err)
		}
	}
	mustRecord := func(payload string) {
		t.Helper()
		if _, err := storage.AppendRecord(json.RawMessage(payload)); err != nil {
			t.Fatal(err)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	mustAppend(`{"type":"message","id":"root-user","message":`+f6V4User("root <>&  ", 1)+`}`, "main")
	mustAppend(`{"type":"message","id":"main-assistant","message":`+f6V4Assistant("answer", 2, "stop")+`}`, "main")
	mustAppend(`{"type":"message","id":"second-user","message":`+f6V4User("continue", 3)+`}`, "main")
	mustAppend(`{"type":"thinking_level_change","id":"thinking","thinkingLevel":"high"}`, "main")
	mustAppend(`{"type":"model_change","id":"model","provider":"anthropic","modelId":"claude-test"}`, "main")
	mustAppend(`{"type":"active_tools_change","id":"tools","activeToolNames":["read","bash"]}`, "main")
	mustAppend(`{"type":"active_tools_change","id":"tools-empty","activeToolNames":[]}`, "main")
	mustAppend(`{"type":"custom","id":"custom","customType":"state","data":{"nested":[1,"two"]}}`, "main")
	mustAppend(`{"type":"compaction","id":"compaction","summary":"prior work","retainedTail":[`+f6V4User("retained tail", 4)+`],"tokensBefore":42.5,"details":{"readFiles":["a.go"]}}`, "main")
	mustAppend(`{"type":"branch_summary","id":"branch-summary","fromId":"compaction","summary":"discarded branch work","details":{"modifiedFiles":["b.go"]}}`, "main")
	must(storage.SetName("  fixture name  "))
	must(storage.SetLabel("root-user", f6HarnessString("  checkpoint  ")))
	must(storage.SetLabel("root-user", nil))
	must(storage.SetLabel("second-user", f6HarnessString("  branch point  ")))
	must(storage.CreateLane("branch", f6HarnessString("root-user")))
	mustAppend(`{"type":"message","id":"branch-user","message":`+f6V4User("branch", 5)+`}`, "branch")
	must(storage.CreateLane("idle", f6HarnessString("second-user")))
	must(storage.MoveLane("idle", nil))
	mustRecord(`{"type":"operation_started","id":"op-run","lane":"main","sourceLeafId":"branch-summary","intent":{"kind":"run","originalPrompt":[` +
		f6V4User("run prompt", 6) + `],"initialMessages":[{"type":"message","id":"queued-1","message":` + f6V4User("queued", 7) + `}]}}`)
	mustRecord(`{"type":"step_attempt","id":"step-1","lane":"main","runId":"op-run","step":"assistant","attempt":1,"resultEntryId":"main-assistant"}`)
	mustRecord(`{"type":"usage","id":"usage-1","lane":"main","cause":"assistant","runId":"op-run","entryId":"main-assistant","attempt":1,"stopReason":"stop","usage":` + f6V4Usage + `}`)
	_, conflictErr := storage.AppendRecord(json.RawMessage(
		`{"type":"operation_started","id":"op-run-2","lane":"main","sourceLeafId":null,"intent":{"kind":"navigation","targetId":null,"summarize":false}}`,
	))
	openBeforeFinish, err := storage.FindOpenOperations("main", nil)
	if err != nil {
		t.Fatal(err)
	}
	mustRecord(`{"type":"operation_finished","id":"finish-1","lane":"main","runId":"op-run","outcome":"completed"}`)

	duplicateErr := appendEntry(`{"type":"custom","id":"root-user","customType":"dup"}`, "main")
	missingLaneErr := appendEntry(`{"type":"custom","id":"orphan","customType":"x"}`, "missing-lane")
	laneExistsErr := storage.CreateLane("branch", nil)
	labelTargetErr := storage.SetLabel("ghost", f6HarnessString("x"))
	_, invalidLimitErr := storage.FindEntries(agentharness.SessionV4EntryQuery{Limit: f6HarnessInt(0)})

	return map[string]any{
		"openOperationConflict": f6HarnessSessionError(conflictErr, root),
		"openBeforeFinish":      f6V4RecordIDs(openBeforeFinish),
		"duplicateEntryId":      f6HarnessSessionError(duplicateErr, root),
		"missingLaneAppend":     f6HarnessSessionError(missingLaneErr, root),
		"laneAlreadyExists":     f6HarnessSessionError(laneExistsErr, root),
		"labelTargetMissing":    f6HarnessSessionError(labelTargetErr, root),
		"invalidLimit":          f6HarnessSessionError(invalidLimitErr, root),
	}
}

func f6V4EntryIDs(entries []agentharness.SessionV4Entry) []string {
	ids := make([]string, len(entries))
	for index := range entries {
		ids[index] = entries[index].ID
	}
	return ids
}

func f6V4RecordIDs(records []agentharness.SessionV4Record) []string {
	ids := make([]string, len(records))
	for index := range records {
		ids[index] = records[index].ID
	}
	return ids
}

func f6V4MainLeaf(storage agentharness.SessionV4Storage) *string {
	for _, pointer := range storage.Lanes() {
		if pointer.Lane == "main" {
			return pointer.LeafID
		}
	}
	return nil
}

func f6V4NameOrNil(storage agentharness.SessionV4Storage) any {
	if name, ok := storage.Name(); ok {
		return name
	}
	return nil
}

func f6V4LabelOrNil(storage agentharness.SessionV4Storage, id string) any {
	if label, ok := storage.Label(id); ok {
		return label
	}
	return nil
}

func f6ObserveV4Storage(t *testing.T, storage agentharness.SessionV4Storage, metadata any, root string) map[string]any {
	t.Helper()
	fail := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	lanes := storage.Lanes()
	mainLeaf := f6V4MainLeaf(storage)
	entries, err := storage.FindEntries(agentharness.SessionV4EntryQuery{Order: "oldestFirst"})
	fail(err)
	branch := []agentharness.SessionV4Entry{}
	branchToCompaction := []agentharness.SessionV4Entry{}
	if mainLeaf != nil {
		branch, err = storage.FindEntriesOnBranch(agentharness.SessionV4BranchQuery{
			SessionV4EntryQuery: agentharness.SessionV4EntryQuery{Order: "oldestFirst"}, Start: *mainLeaf,
		})
		fail(err)
		branchToCompaction, err = storage.FindEntriesOnBranch(agentharness.SessionV4BranchQuery{
			Start: *mainLeaf, StopAtType: "compaction",
		})
		fail(err)
	}
	messages, err := storage.FindEntries(agentharness.SessionV4EntryQuery{Type: "message", Order: "oldestFirst"})
	fail(err)
	customState, err := storage.FindEntries(agentharness.SessionV4EntryQuery{Type: "custom", CustomType: "state"})
	fail(err)
	pagedNewest, err := storage.FindEntries(agentharness.SessionV4EntryQuery{Limit: f6HarnessInt(3)})
	fail(err)
	records, err := storage.FindRecords(agentharness.SessionV4RecordQuery{})
	fail(err)
	usageRecords, err := storage.FindRecords(agentharness.SessionV4RecordQuery{Lane: "main", Type: "usage"})
	fail(err)
	openOperations, err := storage.FindOpenOperations("main", nil)
	fail(err)
	log, err := storage.Log(agentharness.SessionV4LogOptions{})
	fail(err)
	logAfterSeq, err := storage.Log(agentharness.SessionV4LogOptions{AfterSeq: f6HarnessInt(10), Limit: f6HarnessInt(3)})
	fail(err)
	logAfterSeqIDs := make([]int, len(logAfterSeq))
	for index := range logAfterSeq {
		logAfterSeqIDs[index] = logAfterSeq[index].Seq
	}
	return normalizeF6HarnessValue(map[string]any{
		"metadata":              f6HarnessJSONValue(metadata),
		"lanes":                 f6HarnessJSONValue(lanes),
		"entries":               f6HarnessJSONValue(entries),
		"entryIds":              f6V4EntryIDs(entries),
		"branchIds":             f6V4EntryIDs(branch),
		"messageIds":            f6V4EntryIDs(messages),
		"customStateIds":        f6V4EntryIDs(customState),
		"pagedNewestIds":        f6V4EntryIDs(pagedNewest),
		"branchToCompactionIds": f6V4EntryIDs(branchToCompaction),
		"records":               f6HarnessJSONValue(records),
		"usageRecordIds":        f6V4RecordIDs(usageRecords),
		"openOperations":        f6V4RecordIDs(openOperations),
		"log":                   f6HarnessJSONValue(log),
		"logAfterSeqIds":        logAfterSeqIDs,
		"stats":                 f6HarnessJSONValue(storage.Stats()),
		"name":                  f6V4NameOrNil(storage),
		"labels": map[string]any{
			"root":   f6V4LabelOrNil(storage, "root-user"),
			"second": f6V4LabelOrNil(storage, "second-user"),
		},
	}, root, "modifiedAt").(map[string]any)
}

func TestF6HarnessRehydratesUpstreamJSONLBytes(t *testing.T) {
	manifest := runner.LoadManifest(t, "F6Harness")
	if manifest.Family != "F6Harness" || manifest.Generator != "conformance/extract/f6-harness.ts" {
		t.Fatalf("unexpected F6Harness manifest: %+v", manifest)
	}
	fixture := loadF6HarnessFixture(t)
	input, err := runner.ReadFixture("F6Harness", "session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	env := agentharness.NodeExecutionEnv{CWD: root}
	defer func() { _ = env.Cleanup() }()
	path := filepath.Join(root, "session.jsonl")
	header := agentharness.SessionV4Header{
		ID: "session-fixed", CreatedAt: f6FixedNowMS, CWD: "/fixture/project",
		ParentSessionID: f6HarnessString("parent-session"),
		Metadata:        json.RawMessage(`{"profile":"reviewer","nested":{"enabled":true}}`),
	}
	storage, err := agentharness.CreateJSONLSessionV4Storage(ctx, &env, path, header)
	if err != nil {
		t.Fatal(err)
	}
	storage.Now = f6V4Now
	scriptErrors := f6RunV4SessionScript(t, storage, root)
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := runner.ByteDiff(input, written); diff != "" {
		t.Fatalf("replayed script bytes diverged from fixture:\n%s", diff)
	}
	runner.AssertCanonicalJSONEqual(t, fixture.Session["scriptErrors"], scriptErrors, "")

	reopened, err := agentharness.LoadJSONLSessionV4Storage(ctx, &env, path)
	if err != nil {
		t.Fatal(err)
	}
	assertF6HarnessMap(
		t,
		fixture.Session["jsonl"].(map[string]any),
		f6ObserveV4Storage(t, reopened, reopened.Metadata(), root),
	)

	memory := agentharness.NewInMemorySessionV4Storage(agentharness.SessionV4Metadata{
		ID: "session-fixed", CreatedAt: f6FixedNowMS, ParentSessionID: f6HarnessString("parent-session"),
	})
	memory.Now = f6V4Now
	memoryScriptErrors := f6RunV4SessionScript(t, memory, root)
	runner.AssertCanonicalJSONEqual(t, fixture.Session["memoryScriptErrors"], memoryScriptErrors, "")
	assertF6HarnessMap(
		t,
		fixture.Session["memory"].(map[string]any),
		f6ObserveV4Storage(t, memory, memory.Metadata(), root),
	)

	generated := 0
	session := agentharness.NewSessionV4(memory, func() string {
		generated++
		return fmt.Sprintf("gen-%d", generated)
	})
	appendedMessageID, err := session.AppendMessage(json.RawMessage(f6V4User("appended via session", 8)))
	if err != nil {
		t.Fatal(err)
	}
	viewCustomID, err := session.AppendCustomEntry("note", json.RawMessage(`{"via":"view"}`), "branch")
	if err != nil {
		t.Fatal(err)
	}
	newestMessage, err := session.FindEntryOnBranch(agentharness.SessionV4BranchQuery{
		SessionV4EntryQuery: agentharness.SessionV4EntryQuery{Type: "message"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mainLeaf, err := session.LeafID()
	if err != nil {
		t.Fatal(err)
	}
	branchLeaf, err := session.LeafID("branch")
	if err != nil {
		t.Fatal(err)
	}
	var newestMessageID any
	if newestMessage != nil {
		newestMessageID = newestMessage.ID
	}
	runner.AssertCanonicalJSONEqual(t, fixture.Session["sessionApi"], map[string]any{
		"appendedMessageId": appendedMessageID,
		"viewCustomId":      viewCustomID,
		"mainLeaf":          mainLeaf,
		"branchLeaf":        branchLeaf,
		"newestMessageId":   newestMessageID,
		"stats":             f6HarnessJSONValue(session.Stats()),
	}, "")

	reopened.Now = f6V4Now
	if _, err := reopened.AppendEntry(json.RawMessage(
		`{"type":"custom","id":"appended-fixed","customType":"after-rehydrate","data":{"text":"<>&  "}}`,
	), "main"); err != nil {
		t.Fatal(err)
	}
	mutated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	appendLine, ok := fixture.Session["appendLine"].(string)
	if !ok {
		t.Fatalf("F6Harness appendLine has type %T", fixture.Session["appendLine"])
	}
	wantMutated := append(append([]byte(nil), input...), []byte(appendLine)...)
	if diff := runner.ByteDiff(wantMutated, mutated); diff != "" {
		t.Fatalf("append diverged:\n%s", diff)
	}
}

func TestF6HarnessForkContextAndErrorsMatchUpstream(t *testing.T) {
	fixture := loadF6HarnessFixture(t)
	input, err := runner.ReadFixture("F6Harness", "session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	env := agentharness.NodeExecutionEnv{CWD: root}
	defer func() { _ = env.Cleanup() }()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, input, 0o666); err != nil {
		t.Fatal(err)
	}
	reopened, err := agentharness.LoadJSONLSessionV4Storage(ctx, &env, path)
	if err != nil {
		t.Fatal(err)
	}

	forkHeader := func(id string) agentharness.SessionV4Header {
		return agentharness.SessionV4Header{
			ID: id, CreatedAt: f6FixedNowMS, CWD: "/fixture/project", ParentSessionID: f6HarnessString("session-fixed"),
		}
	}
	observeFork := func(storage *agentharness.JSONLSessionV4Storage) map[string]any {
		entries, err := storage.FindEntries(agentharness.SessionV4EntryQuery{Order: "oldestFirst"})
		if err != nil {
			t.Fatal(err)
		}
		return normalizeF6HarnessValue(map[string]any{
			"entryIds": f6V4EntryIDs(entries),
			"lanes":    f6HarnessJSONValue(storage.Lanes()),
			"name":     f6V4NameOrNil(storage),
			"labels":   map[string]any{"second": f6V4LabelOrNil(storage, "second-user")},
		}, root, "modifiedAt").(map[string]any)
	}
	before, err := reopened.Fork(ctx, filepath.Join(root, "fork-before.jsonl"), forkHeader("fork-before"), agentharness.SessionV4ForkOptions{
		EntryID: f6HarnessString("second-user"), Position: agentharness.ForkBefore,
	})
	if err != nil {
		t.Fatal(err)
	}
	at, err := reopened.Fork(ctx, filepath.Join(root, "fork-at.jsonl"), forkHeader("fork-at"), agentharness.SessionV4ForkOptions{
		EntryID: f6HarnessString("main-assistant"), Position: agentharness.ForkAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := reopened.Fork(ctx, filepath.Join(root, "fork-tree.jsonl"), forkHeader("fork-tree"), agentharness.SessionV4ForkOptions{Scope: "tree"})
	if err != nil {
		t.Fatal(err)
	}
	treeBytes, err := os.ReadFile(filepath.Join(root, "fork-tree.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	_, invalidForkErr := reopened.Fork(ctx, filepath.Join(root, "fork-invalid.jsonl"), forkHeader("fork-invalid"), agentharness.SessionV4ForkOptions{
		EntryID: f6HarnessString("thinking"),
	})
	runner.AssertCanonicalJSONEqual(t, fixture.Session["forks"], map[string]any{
		"before":        observeFork(before),
		"at":            observeFork(at),
		"tree":          observeFork(tree),
		"treeBytes":     normalizeF6HarnessValue(string(treeBytes), root, "modifiedAt"),
		"invalidTarget": f6HarnessSessionError(invalidForkErr, root),
	}, "")

	mainLeaf := f6V4MainLeaf(reopened)
	branchPath, err := reopened.FindEntriesOnBranch(agentharness.SessionV4BranchQuery{
		SessionV4EntryQuery: agentharness.SessionV4EntryQuery{Order: "oldestFirst"}, Start: *mainLeaf,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertF6HarnessMap(
		t,
		fixture.Session["compactedContext"].(map[string]any),
		f6HarnessContextObservation(t, agentharness.BuildSessionV4Context(branchPath)),
	)

	const validHeader = `{"kind":"header","version":4,"id":"s","createdAt":0,"cwd":"/c"}`
	invalidCases := []struct {
		name    string
		content string
	}{
		{name: "missing-header", content: ""},
		{name: "v3-header", content: "{\"type\":\"session\",\"version\":3,\"id\":\"s\",\"timestamp\":\"t\",\"cwd\":\"/c\"}\n"},
		{name: "unsupported-version", content: "{\"kind\":\"header\",\"version\":5,\"id\":\"s\",\"createdAt\":0,\"cwd\":\"/c\"}\n"},
		{name: "metadata-array", content: "{\"kind\":\"header\",\"version\":4,\"id\":\"s\",\"createdAt\":0,\"cwd\":\"/c\",\"metadata\":[]}\n"},
		{name: "unknown-mutation", content: validHeader + "\n{\"kind\":\"bogus\",\"seq\":1}\n"},
		{name: "non-consecutive-seq", content: validHeader + "\n{\"kind\":\"entry\",\"lane\":\"main\",\"type\":\"custom\",\"id\":\"e\",\"customType\":\"x\",\"parentId\":null,\"seq\":2,\"timestamp\":0}\n"},
		{name: "missing-parent", content: validHeader + "\n{\"kind\":\"entry\",\"lane\":\"main\",\"type\":\"custom\",\"id\":\"e\",\"customType\":\"x\",\"parentId\":\"ghost\",\"seq\":1,\"timestamp\":0}\n"},
		{name: "dangling-lane", content: validHeader + "\n{\"kind\":\"lane\",\"seq\":1,\"lane\":\"side\",\"leafId\":\"missing\"}\n"},
	}
	gotInvalid := make([]any, 0, len(invalidCases))
	for _, test := range invalidCases {
		invalidPath := filepath.Join(root, test.name+".jsonl")
		if err := os.WriteFile(invalidPath, []byte(test.content), 0o666); err != nil {
			t.Fatal(err)
		}
		_, openErr := agentharness.LoadJSONLSessionV4Storage(ctx, &env, invalidPath)
		gotInvalid = append(gotInvalid, map[string]any{
			"name":    test.name,
			"content": test.content,
			"error":   f6HarnessSessionError(openErr, root),
		})
	}
	runner.AssertCanonicalJSONEqual(t, fixture.Session["invalid"], gotInvalid, "")

	const keptEntryLine = `{"kind":"entry","lane":"main","type":"custom","id":"kept","customType":"x","parentId":null,"seq":1,"timestamp":0}`
	repair := func(name, content string) map[string]any {
		repairPath := filepath.Join(root, name+".jsonl")
		if err := os.WriteFile(repairPath, []byte(content), 0o666); err != nil {
			t.Fatal(err)
		}
		repaired, err := agentharness.LoadJSONLSessionV4Storage(ctx, &env, repairPath)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := repaired.FindEntries(agentharness.SessionV4EntryQuery{Order: "oldestFirst"})
		if err != nil {
			t.Fatal(err)
		}
		repairedContent, err := os.ReadFile(repairPath)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{
			"content":         content,
			"entryIds":        f6V4EntryIDs(entries),
			"repairedContent": string(repairedContent),
		}
	}
	runner.AssertCanonicalJSONEqual(t, fixture.Session["repairs"], normalizeF6HarnessValue(map[string]any{
		"tornTail":         repair("torn-tail", validHeader+"\n"+keptEntryLine+"\n{\"kind\":\"en"),
		"unterminatedTail": repair("unterminated-tail", validHeader+"\n"+keptEntryLine),
	}, root, "modifiedAt"), "")
}

func TestF6HarnessContextTransformsAndProjectorsMatchUpstream(t *testing.T) {
	fixture := loadF6HarnessFixture(t)
	entryJSON := []string{
		fmt.Sprintf(`{"type":"message","id":"p-root","parentId":null,"seq":1,"timestamp":%d,"message":%s}`, f6FixedNowMS, f6V4User("transform root", 10)),
		fmt.Sprintf(`{"type":"custom","id":"p-constructor","parentId":"p-root","seq":2,"timestamp":%d,"customType":"constructor_state","data":{"label":"constructor"}}`, f6FixedNowMS),
		fmt.Sprintf(`{"type":"custom","id":"p-call","parentId":"p-constructor","seq":3,"timestamp":%d,"customType":"call_state"}`, f6FixedNowMS),
		fmt.Sprintf(`{"type":"custom","id":"p-drop","parentId":"p-call","seq":4,"timestamp":%d,"customType":"noise"}`, f6FixedNowMS),
		fmt.Sprintf(`{"type":"message","id":"p-deferred","parentId":"p-drop","seq":5,"timestamp":%d,"message":%s}`, f6FixedNowMS, f6V4Assistant("deferred work", 11, "deferred")),
		fmt.Sprintf(`{"type":"branch_summary","id":"p-empty-summary","parentId":"p-deferred","seq":6,"timestamp":%d,"fromId":"p-root","summary":""}`, f6FixedNowMS),
		fmt.Sprintf(`{"type":"message","id":"p-tail","parentId":"p-empty-summary","seq":7,"timestamp":%d,"message":%s}`, f6FixedNowMS, f6V4User("tail", 12)),
	}
	projectorPath := make([]agentharness.SessionV4Entry, len(entryJSON))
	for index, raw := range entryJSON {
		entry, err := agentharness.ParseSessionV4Entry([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		projectorPath[index] = entry
	}
	projectedUser := func(text string, timestamp int) agent.AgentMessages {
		return agent.AgentMessages{json.RawMessage(f6V4User(text, timestamp))}
	}
	got := map[string]any{
		"default": f6HarnessContextObservation(t, agentharness.BuildSessionV4Context(projectorPath)),
		"projected": f6HarnessContextObservation(t, agentharness.BuildSessionV4Context(projectorPath, agentharness.SessionV4ContextOptions{
			EntryTransforms: []agentharness.SessionV4ContextTransform{
				func(entries []agentharness.SessionV4Entry) []agentharness.SessionV4Entry {
					kept := make([]agentharness.SessionV4Entry, 0, len(entries))
					for _, entry := range entries {
						if entry.ID != "p-drop" {
							kept = append(kept, entry)
						}
					}
					return kept
				},
			},
			EntryProjectors: map[string]agentharness.SessionV4ContextProjector{
				"constructor_state": func(agentharness.SessionV4Entry, int, []agentharness.SessionV4Entry) agent.AgentMessages {
					return projectedUser("constructor projector", 20)
				},
				"call_state": func(agentharness.SessionV4Entry, int, []agentharness.SessionV4Entry) agent.AgentMessages {
					return projectedUser("call projector", 21)
				},
			},
		})),
	}
	want := fixture.Session["projectorContexts"].(map[string]any)
	for _, name := range []string{"default", "projected"} {
		t.Run(name, func(t *testing.T) {
			assertF6HarnessMap(t, want[name].(map[string]any), got[name].(map[string]any))
		})
	}
}

func TestF6HarnessTypedEmptyActiveToolsMatchUpstream(t *testing.T) {
	fixture := loadF6HarnessFixture(t)
	input, err := runner.ReadFixture("F6Harness", "session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	env := agentharness.NodeExecutionEnv{CWD: root}
	defer func() { _ = env.Cleanup() }()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, input, 0o666); err != nil {
		t.Fatal(err)
	}
	storage, err := agentharness.LoadJSONLSessionV4Storage(ctx, &env, path)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := storage.Entry("tools-empty")
	if !ok || entry.ActiveToolNames == nil || len(entry.ActiveToolNames) != 0 {
		t.Fatalf("tools-empty active tools = %#v, want explicit empty state", entry.ActiveToolNames)
	}
	var wantEntry any
	for _, candidate := range fixture.Session["jsonl"].(map[string]any)["entries"].([]any) {
		if candidate.(map[string]any)["id"] == "tools-empty" {
			wantEntry = candidate
		}
	}
	runner.AssertCanonicalJSONEqual(t, wantEntry, f6HarnessJSONValue(entry), "")

	mainLeaf := f6V4MainLeaf(storage)
	branchPath, err := storage.FindEntriesOnBranch(agentharness.SessionV4BranchQuery{
		SessionV4EntryQuery: agentharness.SessionV4EntryQuery{Order: "oldestFirst"}, Start: *mainLeaf,
	})
	if err != nil {
		t.Fatal(err)
	}
	contextState := agentharness.BuildSessionV4Context(branchPath)
	if contextState.ActiveToolNames == nil || len(contextState.ActiveToolNames) != 0 {
		t.Fatalf("context active tools = %#v, want explicit empty state", contextState.ActiveToolNames)
	}
	runner.AssertCanonicalJSONEqual(
		t,
		fixture.Session["compactedContext"].(map[string]any)["activeToolNames"],
		contextState.ActiveToolNames,
		"",
	)
}

func TestF6HarnessSessionReposMatchUpstream(t *testing.T) {
	fixture := loadF6HarnessFixture(t)
	ctx := context.Background()
	root := t.TempDir()
	env := agentharness.NodeExecutionEnv{CWD: root}
	defer func() { _ = env.Cleanup() }()
	provisioned := []string{
		`{"type":"message","id":"root-user","message":` + f6V4User("root", 1) + `}`,
		`{"type":"message","id":"main-assistant","message":` + f6V4Assistant("answer", 2, "stop") + `}`,
		`{"type":"message","id":"second-user","message":` + f6V4User("continue", 3) + `}`,
	}
	entriesOf := func(storage agentharness.SessionV4Storage) any {
		entries, err := storage.FindEntries(agentharness.SessionV4EntryQuery{Order: "oldestFirst"})
		if err != nil {
			t.Fatal(err)
		}
		return f6HarnessJSONValue(entries)
	}

	memoryRepo := agentharness.NewInMemorySessionV4Repo()
	memoryRepo.Now = f6V4Now
	memorySource, err := memoryRepo.Create(agentharness.SessionV4CreateOptions{ID: f6HarnessString("memory-source")})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range provisioned {
		if _, err := memorySource.AppendEntry(json.RawMessage(payload), "main"); err != nil {
			t.Fatal(err)
		}
	}
	memoryMetadata := memorySource.Metadata()
	memoryOpened, err := memoryRepo.Open(memoryMetadata)
	if err != nil {
		t.Fatal(err)
	}
	memoryFork := func(id string, options agentharness.SessionV4ForkOptions) *agentharness.InMemorySessionV4Storage {
		forked, err := memoryRepo.Fork(memoryMetadata, agentharness.SessionV4RepoForkOptions{
			SessionV4ForkOptions:   options,
			SessionV4CreateOptions: agentharness.SessionV4CreateOptions{ID: f6HarnessString(id)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return forked
	}
	memoryBefore := memoryFork("memory-before", agentharness.SessionV4ForkOptions{EntryID: f6HarnessString("second-user"), Position: agentharness.ForkBefore})
	memoryAt := memoryFork("memory-at", agentharness.SessionV4ForkOptions{EntryID: f6HarnessString("main-assistant"), Position: agentharness.ForkAt})
	memoryFull := memoryFork("memory-full", agentharness.SessionV4ForkOptions{})
	memoryTree := memoryFork("memory-tree", agentharness.SessionV4ForkOptions{Scope: "tree"})
	memoryListed := memoryRepo.List()
	sort.Slice(memoryListed, func(left, right int) bool { return memoryListed[left].ID < memoryListed[right].ID })
	_, memoryDuplicateErr := memoryRepo.Create(agentharness.SessionV4CreateOptions{ID: f6HarnessString("memory-source")})
	memoryRepo.Delete(memoryMetadata)
	_, memoryOpenAfterDeleteErr := memoryRepo.Open(memoryMetadata)

	jsonlRepo := agentharness.NewJSONLSessionV4Repo(&env, filepath.Join(root, "repo-sessions"))
	jsonlRepo.Now = f6V4Now
	jsonlSource, err := jsonlRepo.Create(ctx, agentharness.JSONLSessionV4CreateOptions{
		SessionV4CreateOptions: agentharness.SessionV4CreateOptions{ID: f6HarnessString("jsonl-source")},
		CWD:                    "/tmp/my-project",
		Metadata:               json.RawMessage(`{ "10" : "ten", "2" : "two", "profile" : "reviewer", "nested" : { "z" : 1, "a" : 2 } }`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range provisioned {
		if _, err := jsonlSource.AppendEntry(json.RawMessage(payload), "main"); err != nil {
			t.Fatal(err)
		}
	}
	jsonlOther, err := jsonlRepo.Create(ctx, agentharness.JSONLSessionV4CreateOptions{
		SessionV4CreateOptions: agentharness.SessionV4CreateOptions{ID: f6HarnessString("jsonl-other")},
		CWD:                    "/tmp/other-project",
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonlMetadata, jsonlOtherMetadata := jsonlSource.Metadata(), jsonlOther.Metadata()
	jsonlOpened, err := jsonlRepo.Open(ctx, jsonlMetadata)
	if err != nil {
		t.Fatal(err)
	}
	jsonlListByCwd, err := jsonlRepo.List(ctx, f6HarnessString("/tmp/my-project"))
	if err != nil {
		t.Fatal(err)
	}
	jsonlListAll, err := jsonlRepo.List(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	sortJSONLByID := func(metadata []agentharness.JSONLSessionV4Metadata) []agentharness.JSONLSessionV4Metadata {
		sorted := append([]agentharness.JSONLSessionV4Metadata(nil), metadata...)
		sort.Slice(sorted, func(left, right int) bool { return sorted[left].ID < sorted[right].ID })
		return sorted
	}
	jsonlFork := func(id string, options agentharness.SessionV4ForkOptions, metadata json.RawMessage, parentSessionID *string) *agentharness.JSONLSessionV4Storage {
		forked, err := jsonlRepo.Fork(ctx, jsonlMetadata, agentharness.JSONLSessionV4ForkOptions{
			SessionV4ForkOptions: options,
			JSONLSessionV4CreateOptions: agentharness.JSONLSessionV4CreateOptions{
				SessionV4CreateOptions: agentharness.SessionV4CreateOptions{ID: f6HarnessString(id), ParentSessionID: parentSessionID},
				CWD:                    "/tmp/target",
				Metadata:               metadata,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return forked
	}
	jsonlBefore := jsonlFork("jsonl-before", agentharness.SessionV4ForkOptions{EntryID: f6HarnessString("second-user")}, nil, nil)
	jsonlInherited := jsonlFork("jsonl-inherited", agentharness.SessionV4ForkOptions{}, nil, nil)
	jsonlTree := jsonlFork(
		"jsonl-tree", agentharness.SessionV4ForkOptions{Scope: "tree"},
		json.RawMessage(`{"profile":"writer"}`), f6HarnessString("override-parent"),
	)
	_, invalidIDErr := jsonlRepo.Create(ctx, agentharness.JSONLSessionV4CreateOptions{
		SessionV4CreateOptions: agentharness.SessionV4CreateOptions{ID: f6HarnessString("-bad-")},
		CWD:                    "/tmp/target",
	})
	_, duplicateIDErr := jsonlRepo.Create(ctx, agentharness.JSONLSessionV4CreateOptions{
		SessionV4CreateOptions: agentharness.SessionV4CreateOptions{ID: f6HarnessString("jsonl-source")},
		CWD:                    "/tmp/my-project",
	})
	sourceBytes, err := env.ReadTextFile(ctx, jsonlMetadata.Path)
	if err != nil {
		t.Fatal(err)
	}
	treeMetadata := jsonlTree.Metadata()
	treeBytes, err := env.ReadTextFile(ctx, treeMetadata.Path)
	if err != nil {
		t.Fatal(err)
	}
	sourceExistsBeforeDelete, err := env.Exists(ctx, jsonlMetadata.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonlRepo.Delete(ctx, jsonlMetadata); err != nil {
		t.Fatal(err)
	}
	sourceExistsAfterDelete, err := env.Exists(ctx, jsonlMetadata.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, jsonlOpenAfterDeleteErr := jsonlRepo.Open(ctx, jsonlMetadata)

	got := map[string]any{
		"memory": map[string]any{
			"sourceMetadata":  f6HarnessJSONValue(memoryMetadata),
			"openedMetadata":  f6HarnessJSONValue(memoryOpened.Metadata()),
			"listed":          f6HarnessJSONValue(memoryListed),
			"beforeEntries":   entriesOf(memoryBefore),
			"atEntries":       entriesOf(memoryAt),
			"fullEntries":     entriesOf(memoryFull),
			"treeEntries":     entriesOf(memoryTree),
			"treeLanes":       f6HarnessJSONValue(memoryTree.Lanes()),
			"duplicateCreate": f6HarnessSessionError(memoryDuplicateErr, root),
			"openAfterDelete": f6HarnessSessionError(memoryOpenAfterDeleteErr, root),
		},
		"jsonl": map[string]any{
			"sourceMetadata":      f6HarnessJSONValue(jsonlMetadata),
			"otherMetadata":       f6HarnessJSONValue(jsonlOtherMetadata),
			"openedMetadata":      f6HarnessJSONValue(jsonlOpened.Metadata()),
			"openedEntries":       entriesOf(jsonlOpened),
			"listByCwd":           f6HarnessJSONValue(sortJSONLByID(jsonlListByCwd)),
			"listAll":             f6HarnessJSONValue(sortJSONLByID(jsonlListAll)),
			"encodedCwdDirectory": filepath.Base(filepath.Dir(jsonlMetadata.Path)),
			"before": map[string]any{
				"metadata": f6HarnessJSONValue(jsonlBefore.Metadata()), "entries": entriesOf(jsonlBefore),
			},
			"inherited": map[string]any{
				"metadata": f6HarnessJSONValue(jsonlInherited.Metadata()), "entries": entriesOf(jsonlInherited),
			},
			"tree": map[string]any{
				"metadata": f6HarnessJSONValue(treeMetadata), "entries": entriesOf(jsonlTree),
				"lanes": f6HarnessJSONValue(jsonlTree.Lanes()),
			},
			"sourceBytes":              sourceBytes,
			"treeBytes":                treeBytes,
			"invalidIdCreate":          f6HarnessSessionError(invalidIDErr, root),
			"duplicateIdCreate":        f6HarnessSessionError(duplicateIDErr, root),
			"sourceExistsBeforeDelete": sourceExistsBeforeDelete,
			"sourceExistsAfterDelete":  sourceExistsAfterDelete,
			"openAfterDelete":          f6HarnessSessionError(jsonlOpenAfterDeleteErr, root),
		},
	}
	wantRepos := fixture.Session["repos"].(map[string]any)
	for _, repoType := range []string{"memory", "jsonl"} {
		t.Run(repoType, func(t *testing.T) {
			assertF6HarnessMap(
				t,
				wantRepos[repoType].(map[string]any),
				normalizeF6HarnessValue(got[repoType], root, "modifiedAt").(map[string]any),
			)
		})
	}
}

func TestF6HarnessNodeExecutionEnvironmentMatchesUpstream(t *testing.T) {
	fixture := loadF6HarnessFixture(t)
	root := t.TempDir()
	env := agentharness.NodeExecutionEnv{CWD: root, ShellEnv: map[string]string{"BASE_VALUE": "base"}}
	if err := env.WriteFile(context.Background(), "nested/lines.txt", []byte("one\r\ntwo\nthree\n")); err != nil {
		t.Fatal(err)
	}
	if err := env.WriteFile(context.Background(), "target.txt", []byte{0, 1, 2, 255}); err != nil {
		t.Fatal(err)
	}
	if err := env.CreateDir(context.Background(), "empty-remove", false); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(root, "target-link")); err != nil {
		t.Fatal(err)
	}

	abs, absErr := env.AbsolutePath(context.Background(), "nested/../target.txt")
	absAlready, absAlreadyErr := env.AbsolutePath(context.Background(), "/a/../b")
	joined, joinErr := env.JoinPath(context.Background(), root, "nested", "..", "target.txt")
	lines, linesErr := env.ReadTextLines(context.Background(), "nested/lines.txt", 2)
	negativeLines, negativeLinesErr := env.ReadTextLines(context.Background(), "nested/lines.txt", -1)
	binary, binaryErr := env.ReadBinaryFile(context.Background(), "target.txt")
	link, linkErr := env.FileInfo(context.Background(), "target-link")
	canonical, canonicalErr := env.CanonicalPath(context.Background(), "target-link")
	missing, existsErr := env.Exists(context.Background(), "missing")
	_, missingErr := env.ReadTextFile(context.Background(), "missing")
	_, directoryErr := env.ReadTextFile(context.Background(), "nested")
	_, listErr := env.ListDir(context.Background(), "target.txt")
	emptyDirectoryRemoveErr := env.Remove(context.Background(), "empty-remove", false, true)

	chunks := make([]string, 0, 2)
	execResult, execErr := env.Exec(context.Background(), `printf "out:$BASE_VALUE:$EXTRA"; printf "err" >&2; exit 7`, agentharness.ExecOptions{
		Env:      map[string]string{"EXTRA": "extra"},
		OnStdout: func(chunk string) error { chunks = append(chunks, "stdout:"+chunk); return nil },
		OnStderr: func(chunk string) error { chunks = append(chunks, "stderr:"+chunk); return nil },
	})
	signaledExecResult, signaledExecErr := env.Exec(context.Background(), "kill -9 $$", agentharness.ExecOptions{})
	sort.Strings(chunks)
	aborted, cancel := context.WithCancel(context.Background())
	cancel()
	_, abortErr := env.Exec(aborted, "printf never", agentharness.ExecOptions{})
	zero := 0.0
	_, invalidTimeoutErr := env.Exec(context.Background(), "printf never", agentharness.ExecOptions{TimeoutSeconds: &zero})
	tiny := 0.01
	_, timeoutErr := env.Exec(context.Background(), "sleep 1", agentharness.ExecOptions{TimeoutSeconds: &tiny})
	_, callbackErr := env.Exec(context.Background(), "printf boom", agentharness.ExecOptions{
		OnStdout: func(string) error { return errors.New("callback boom") },
	})
	if err := env.WriteFile(context.Background(), "abort/remove.txt", []byte("remove me")); err != nil {
		t.Fatal(err)
	}
	preAbs, preAbsErr := env.AbsolutePath(aborted, "/a/../b")
	preJoin, preJoinErr := env.JoinPath(aborted, root, "nested", "..", "target.txt")
	_, preReadTextErr := env.ReadTextFile(aborted, "target.txt")
	_, preReadLinesErr := env.ReadTextLines(aborted, "nested/lines.txt", -1)
	_, preReadBinaryErr := env.ReadBinaryFile(aborted, "target.txt")
	preWriteErr := env.WriteFile(aborted, "abort/blocked.txt", []byte("blocked"))
	preAppendErr := env.AppendFile(aborted, "abort/appended.txt", []byte("appended"))
	preInfo, preInfoErr := env.FileInfo(aborted, "target.txt")
	_, preListErr := env.ListDir(aborted, ".")
	preCanonical, preCanonicalErr := env.CanonicalPath(aborted, "target.txt")
	preExists, preExistsErr := env.Exists(aborted, "target.txt")
	preCreateDirErr := env.CreateDir(aborted, "abort/created", true)
	preRemoveErr := env.Remove(aborted, "abort/remove.txt", false, false)
	preTempDir, preTempDirErr := env.CreateTempDir(aborted, "orb-aborted-")
	preTempFile, preTempFileErr := env.CreateTempFile(aborted, "aborted-", ".tmp")
	tempDir, tempDirErr := env.CreateTempDir(context.Background(), "orb-harness-")
	tempFile, tempFileErr := env.CreateTempFile(context.Background(), "pre-", ".tmp")
	for _, created := range []struct {
		path string
		err  error
		file bool
	}{
		{path: preTempDir, err: preTempDirErr},
		{path: preTempFile, err: preTempFileErr, file: true},
		{path: tempDir, err: tempDirErr},
		{path: tempFile, err: tempFileErr, file: true},
	} {
		if created.err != nil || created.path == "" {
			continue
		}
		cleanupPath := created.path
		if created.file {
			cleanupPath = filepath.Dir(cleanupPath)
		}
		t.Cleanup(func() { _ = os.RemoveAll(cleanupPath) })
	}
	tempExists, tempExistsErr := env.Exists(context.Background(), tempFile)
	if err := env.Cleanup(); err != nil {
		t.Fatal(err)
	}

	got := map[string]any{
		"absolutePath":                f6HarnessResult(abs, absErr, root),
		"absolutePathAlreadyAbsolute": f6HarnessResult(absAlready, absAlreadyErr, root),
		"joinPath":                    f6HarnessResult(joined, joinErr, root),
		"readTextLines":               f6HarnessResult(lines, linesErr, root),
		"negativeMaxLines":            f6HarnessResult(negativeLines, negativeLinesErr, root),
		"readBinary":                  f6HarnessResult(f6HarnessBytes(binary), binaryErr, root),
		"symlinkInfo":                 f6HarnessResult(map[string]any{"name": link.Name, "path": runner.NormalizeFixturePath(link.Path, root), "kind": link.Kind, "size": link.Size}, linkErr, root),
		"symlinkCanonical":            f6HarnessResult(canonical, canonicalErr, root),
		"missingExists":               f6HarnessResult(missing, existsErr, root),
		"missingRead":                 f6HarnessResult(nil, missingErr, root),
		"directoryRead":               f6HarnessResult(nil, directoryErr, root),
		"listFile":                    f6HarnessResult(nil, listErr, root),
		"emptyDirectoryRemove":        f6HarnessVoidResult(emptyDirectoryRemoveErr, root),
		"exec":                        f6HarnessResult(execResult, execErr, root),
		"signaledExec":                f6HarnessResult(signaledExecResult, signaledExecErr, root),
		"callbackChunks":              chunks,
		"preAbortedExec":              f6HarnessResult(nil, abortErr, root),
		"preAborted": map[string]any{
			"absolutePath":   f6HarnessResult(preAbs, preAbsErr, root),
			"joinPath":       f6HarnessResult(preJoin, preJoinErr, root),
			"readTextFile":   f6HarnessResult(nil, preReadTextErr, root),
			"readTextLines":  f6HarnessResult(nil, preReadLinesErr, root),
			"readBinaryFile": f6HarnessResult(nil, preReadBinaryErr, root),
			"writeFile":      f6HarnessVoidResult(preWriteErr, root),
			"appendFile":     f6HarnessVoidResult(preAppendErr, root),
			"fileInfo":       f6HarnessStableFileInfoResult(preInfo, preInfoErr, root),
			"listDir":        f6HarnessResult(nil, preListErr, root),
			"canonicalPath":  f6HarnessResult(preCanonical, preCanonicalErr, root),
			"exists":         f6HarnessResult(preExists, preExistsErr, root),
			"createDir":      f6HarnessVoidResult(preCreateDirErr, root),
			"remove":         f6HarnessVoidResult(preRemoveErr, root),
			"createTempDir":  f6HarnessTempResult(preTempDir, preTempDirErr, "orb-aborted-", "", root),
			"createTempFile": f6HarnessTempResult(preTempFile, preTempFileErr, "aborted-", ".tmp", root),
		},
		"invalidTimeout": f6HarnessResult(nil, invalidTimeoutErr, root),
		"timedOutExec":   f6HarnessResult(nil, timeoutErr, root),
		"callbackError":  f6HarnessResult(nil, callbackErr, root),
		"temp": map[string]any{
			"dirPrefix":  tempDirErr == nil && strings.HasPrefix(filepath.Base(tempDir), "orb-harness-"),
			"filePrefix": tempFileErr == nil && strings.HasPrefix(filepath.Base(tempFile), "pre-"),
			"fileSuffix": tempFileErr == nil && strings.HasSuffix(tempFile, ".tmp"),
			"fileExists": tempExistsErr == nil && tempExists,
		},
	}
	assertF6HarnessMap(t, fixture.Env, got)
}

func loadF6HarnessFixture(t *testing.T) f6HarnessFixture {
	t.Helper()
	var fixture f6HarnessFixture
	runner.LoadJSON(t, "F6Harness", "observations.json", &fixture)
	if fixture.SchemaVersion != 1 {
		t.Fatalf("F6Harness schema version = %d", fixture.SchemaVersion)
	}
	return fixture
}

func f6HarnessContextObservation(t *testing.T, contextState agentharness.SessionContext) map[string]any {
	t.Helper()
	return map[string]any{
		"messages":        f6HarnessMessages(t, contextState.Messages),
		"roles":           f6HarnessMessageRoles(contextState.Messages),
		"thinkingLevel":   contextState.ThinkingLevel,
		"model":           contextState.Model,
		"activeToolNames": contextState.ActiveToolNames,
	}
}

func f6HarnessMessages(t *testing.T, messages []any) []any {
	t.Helper()
	result := make([]any, len(messages))
	for index, message := range messages {
		encoded, err := ai.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &result[index]); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func f6HarnessMessageRoles(messages []any) []any {
	roles := make([]any, len(messages))
	for index, message := range messages {
		encoded, err := ai.Marshal(message)
		if err != nil {
			continue
		}
		var envelope struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(encoded, &envelope) == nil && envelope.Role != "" {
			roles[index] = envelope.Role
		}
	}
	return roles
}

func f6HarnessBytes(value []byte) []int {
	result := make([]int, len(value))
	for index := range value {
		result[index] = int(value[index])
	}
	return result
}

func f6HarnessResult(value any, err error, root string) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": f6HarnessTypedError(err, root)}
	}
	return map[string]any{"ok": true, "value": normalizeF6HarnessValue(f6HarnessJSONValue(value), root, "")}
}

func f6HarnessVoidResult(err error, root string) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": f6HarnessTypedError(err, root)}
	}
	return map[string]any{"ok": true}
}

func f6HarnessStableFileInfoResult(info agentharness.FileInfo, err error, root string) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": f6HarnessTypedError(err, root)}
	}
	return f6HarnessResult(map[string]any{
		"name": info.Name, "path": info.Path, "kind": info.Kind, "size": info.Size,
	}, nil, root)
}

func f6HarnessTempResult(pathValue string, err error, prefix, suffix, root string) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": f6HarnessTypedError(err, root)}
	}
	return f6HarnessResult(
		strings.HasPrefix(filepath.Base(pathValue), prefix) && strings.HasSuffix(pathValue, suffix),
		nil,
		root,
	)
}

func f6HarnessTypedError(err error, root string) map[string]any {
	result := map[string]any{"message": runner.NormalizeFixturePath(err.Error(), root)}
	var fileError *agentharness.FileError
	var executionError *agentharness.ExecutionError
	switch {
	case errors.As(err, &fileError):
		result["code"] = fileError.Code
		if fileError.Path != "" {
			result["path"] = runner.NormalizeFixturePath(fileError.Path, root)
		}
	case errors.As(err, &executionError):
		result["code"] = executionError.Code
	}
	return result
}

func f6HarnessSessionError(err error, root string) any {
	if err == nil {
		return nil
	}
	result := map[string]any{"message": runner.NormalizeFixturePath(err.Error(), root)}
	var sessionError *agentharness.SessionError
	if errors.As(err, &sessionError) {
		result["code"] = sessionError.Code
	}
	return result
}

func f6HarnessJSONValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

// normalizeF6HarnessValue rewrites fixture roots recursively. redactKey
// values are masked verbatim; v4 observations redact "modifiedAt", mirroring
// the extract script's normalizeV4 (filesystem mtimes are the only
// nondeterministic v4 metadata).
func normalizeF6HarnessValue(value any, root, redactKey string) any {
	switch typed := value.(type) {
	case string:
		return runner.NormalizeFixturePath(typed, root)
	case []any:
		for index := range typed {
			typed[index] = normalizeF6HarnessValue(typed[index], root, redactKey)
		}
	case map[string]any:
		for key := range typed {
			if redactKey != "" && key == redactKey {
				typed[key] = "<" + redactKey + ">"
			} else {
				typed[key] = normalizeF6HarnessValue(typed[key], root, redactKey)
			}
		}
	}
	return value
}

func assertF6HarnessMap(t *testing.T, want, got map[string]any) {
	t.Helper()
	keys := make([]string, 0, len(want))
	for key := range want {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected observation %q", key)
		}
	}
	for _, key := range keys {
		key := key
		t.Run(key, func(t *testing.T) {
			value, ok := got[key]
			if !ok {
				t.Fatalf("missing observation %q", key)
			}
			wantMap, wantIsMap := want[key].(map[string]any)
			gotMap, gotIsMap := value.(map[string]any)
			if wantIsMap && gotIsMap {
				assertF6HarnessMap(t, wantMap, gotMap)
				return
			}
			runner.AssertCanonicalJSONEqual(t, want[key], value, "")
		})
	}
}
