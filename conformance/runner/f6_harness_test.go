package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	agentharness "github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/ai"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type f6HarnessFixture struct {
	SchemaVersion int            `json:"schemaVersion"`
	Session       map[string]any `json:"session"`
	Env           map[string]any `json:"env"`
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
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	storage, err := agentharness.RehydrateJSONLSession(input, path)
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, err := storage.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if diff := runner.ByteDiff(input, gotBytes); diff != "" {
		t.Fatalf("rehydrated bytes changed before mutation:\n%s", diff)
	}

	got := observeF6HarnessStorage(t, storage, root)
	assertF6HarnessMap(t, fixture.Session["jsonl"].(map[string]any), got)
	baseEntries := storage.Entries()
	parent := "tools"
	if err := storage.AppendEntry(agentharness.SessionTreeEntry{
		Type: "custom", ID: "appended-fixed", ParentID: &parent,
		Timestamp: "2026-02-03T04:05:22.000Z", CustomType: "after-rehydrate",
		Data: json.RawMessage(`{"text":"<>&  "}`),
	}); err != nil {
		t.Fatal(err)
	}
	mutated, err := storage.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	appendLine, ok := fixture.Session["appendLine"].(string)
	if !ok {
		t.Fatalf("F6Harness appendLine has type %T", fixture.Session["appendLine"])
	}
	wantMutated := append(append([]byte(nil), input...), []byte(appendLine)...)
	if diff := runner.ByteDiff(wantMutated, mutated); diff != "" {
		t.Fatalf("rehydrated append diverged:\n%s", diff)
	}

	memory, err := agentharness.NewInMemorySessionStorage(baseEntries, agentharness.SessionMetadata{
		ID: "session-fixed", CreatedAt: "2026-02-03T04:05:06.789Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertF6HarnessMap(
		t,
		fixture.Session["memory"].(map[string]any),
		observeF6HarnessStorage(t, memory, root),
	)
}

func TestF6HarnessForkContextAndErrorsMatchUpstream(t *testing.T) {
	fixture := loadF6HarnessFixture(t)
	input, err := runner.ReadFixture("F6Harness", "session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	storage, err := agentharness.RehydrateJSONLSession(input, filepath.Join(root, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := agentharness.EntriesToFork(storage, "second-user", agentharness.ForkBefore)
	if err != nil {
		t.Fatal(err)
	}
	at, err := agentharness.EntriesToFork(storage, "model", agentharness.ForkAt)
	if err != nil {
		t.Fatal(err)
	}
	_, invalidFork := agentharness.EntriesToFork(storage, "main-assistant", agentharness.ForkBefore)
	gotForks := map[string]any{
		"beforeSecondUser":     f6HarnessEntryIDs(before),
		"atModel":              f6HarnessEntryIDs(at),
		"beforeAssistantError": f6HarnessSessionError(invalidFork, root),
	}
	assertF6HarnessJSONEqual(t, fixture.Session["forks"], gotForks)

	compaction := "compaction"
	pathEntries, err := storage.PathToRoot(&compaction)
	if err != nil {
		t.Fatal(err)
	}
	contextState := agentharness.BuildSessionContext(pathEntries)
	gotContext := f6HarnessContextObservation(t, contextState)
	assertF6HarnessMap(t, fixture.Session["compactedContext"].(map[string]any), gotContext)

	t.Run("branchSummaryContext", func(t *testing.T) {
		branchSummary := "branch-summary"
		branchEntries, err := storage.PathToRoot(&branchSummary)
		if err != nil {
			t.Fatal(err)
		}
		assertF6HarnessMap(
			t,
			fixture.Session["branchSummaryContext"].(map[string]any),
			f6HarnessContextObservation(t, agentharness.BuildSessionContext(branchEntries)),
		)
	})

	t.Run("emptyParentPath", func(t *testing.T) {
		emptyParent := "empty-parent"
		emptyParentEntries, err := storage.PathToRoot(&emptyParent)
		if err != nil {
			t.Fatal(err)
		}
		assertF6HarnessJSONEqual(t, fixture.Session["emptyParentPath"], f6HarnessEntryIDs(emptyParentEntries))
	})

	invalidCases := []struct {
		name    string
		content string
		leaf    bool
	}{
		{name: "missing-header"},
		{name: "unsupported-version", content: "{\"type\":\"session\",\"version\":2,\"id\":\"s\",\"timestamp\":\"t\",\"cwd\":\"/c\"}\n"},
		{name: "metadata-array", content: "{\"type\":\"session\",\"version\":3,\"id\":\"s\",\"timestamp\":\"t\",\"cwd\":\"/c\",\"metadata\":[]}\n"},
		{name: "invalid-entry", content: "{\"type\":\"session\",\"version\":3,\"id\":\"s\",\"timestamp\":\"t\",\"cwd\":\"/c\"}\n{\"type\":\"message\",\"id\":\"e\",\"parentId\":3,\"timestamp\":\"t\"}\n"},
		{name: "dangling-leaf", content: "{\"type\":\"session\",\"version\":3,\"id\":\"s\",\"timestamp\":\"t\",\"cwd\":\"/c\"}\n{\"type\":\"leaf\",\"id\":\"l\",\"parentId\":null,\"timestamp\":\"t\",\"targetId\":\"missing\"}\n", leaf: true},
	}
	gotInvalid := make([]any, 0, len(invalidCases))
	for _, test := range invalidCases {
		path := filepath.Join(root, test.name+".jsonl")
		loaded, openErr := agentharness.RehydrateJSONLSession([]byte(test.content), path)
		if openErr == nil && test.leaf {
			_, openErr = loaded.LeafID()
		}
		gotInvalid = append(gotInvalid, map[string]any{
			"name":  test.name,
			"error": f6HarnessSessionError(openErr, root),
		})
	}
	assertF6HarnessJSONEqual(t, fixture.Session["invalid"], gotInvalid)
}

func TestF6HarnessContextTransformsAndProjectorsMatchUpstream(t *testing.T) {
	fixture := loadF6HarnessFixture(t)
	entries := []agentharness.SessionTreeEntry{
		{Type: "message", ID: "transform-root", Timestamp: "2026-02-03T04:06:00.000Z", Message: json.RawMessage(`{"role":"user","content":[{"type":"text","text":"transform root"}],"timestamp":10}`)},
		{Type: "custom", ID: "constructor-custom", ParentID: f6HarnessStringPointer("transform-root"), Timestamp: "2026-02-03T04:06:01.000Z", CustomType: "constructor_state", Data: json.RawMessage(`{"label":"constructor"}`)},
		{Type: "message", ID: "constructor-drop", ParentID: f6HarnessStringPointer("constructor-custom"), Timestamp: "2026-02-03T04:06:02.000Z", Message: json.RawMessage(`{"role":"user","content":[{"type":"text","text":"constructor drop"}],"timestamp":11}`)},
		{Type: "custom", ID: "override-custom", ParentID: f6HarnessStringPointer("constructor-drop"), Timestamp: "2026-02-03T04:06:03.000Z", CustomType: "override_state", Data: json.RawMessage(`{"label":"override"}`)},
		{Type: "custom", ID: "call-custom", ParentID: f6HarnessStringPointer("override-custom"), Timestamp: "2026-02-03T04:06:04.000Z", CustomType: "call_state", Data: json.RawMessage(`{"label":"call"}`)},
		{Type: "message", ID: "call-drop", ParentID: f6HarnessStringPointer("call-custom"), Timestamp: "2026-02-03T04:06:05.000Z", Message: json.RawMessage(`{"role":"user","content":[{"type":"text","text":"call drop"}],"timestamp":12}`)},
		{Type: "message", ID: "transform-assistant", ParentID: f6HarnessStringPointer("call-drop"), Timestamp: "2026-02-03T04:06:06.000Z", Message: json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"transform answer"}],"api":"openai-responses","provider":"openai","model":"gpt-transform","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":13}`)},
	}
	storage, err := agentharness.NewInMemorySessionStorage(entries, agentharness.SessionMetadata{
		ID: "transform-session", CreatedAt: "2026-02-03T04:05:06.789Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	projectedUser := func(text string, timestamp int64) agent.AgentMessages {
		return agent.AgentMessages{json.RawMessage(
			fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":%q}],"timestamp":%d}`, text, timestamp),
		)}
	}
	constructorOptions := agentharness.SessionContextBuildOptions{
		EntryTransforms: []agentharness.ContextEntryTransform{
			func(input []agentharness.SessionTreeEntry) []agentharness.SessionTreeEntry {
				return f6HarnessDropEntry(input, "constructor-drop")
			},
		},
		EntryProjectors: map[string]agentharness.CustomEntryContextMessageProjector{
			"constructor_state": func(agentharness.SessionTreeEntry, int, []agentharness.SessionTreeEntry) agent.AgentMessages {
				return projectedUser("constructor projector", 20)
			},
			"override_state": func(agentharness.SessionTreeEntry, int, []agentharness.SessionTreeEntry) agent.AgentMessages {
				return projectedUser("constructor override", 21)
			},
		},
	}
	session := agentharness.NewSession(storage, constructorOptions)
	constructorContext, err := session.Context()
	if err != nil {
		t.Fatal(err)
	}
	perCallContext, err := session.Context(agentharness.SessionContextBuildOptions{
		EntryTransforms: []agentharness.ContextEntryTransform{
			func(input []agentharness.SessionTreeEntry) []agentharness.SessionTreeEntry {
				return f6HarnessDropEntry(input, "call-drop")
			},
		},
		EntryProjectors: map[string]agentharness.CustomEntryContextMessageProjector{
			"override_state": func(agentharness.SessionTreeEntry, int, []agentharness.SessionTreeEntry) agent.AgentMessages {
				return projectedUser("per-call override", 22)
			},
			"call_state": func(agentharness.SessionTreeEntry, int, []agentharness.SessionTreeEntry) agent.AgentMessages {
				return projectedUser("per-call projector", 23)
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{
		"constructorOnly":       f6HarnessContextObservation(t, constructorContext),
		"constructorAndPerCall": f6HarnessContextObservation(t, perCallContext),
	}
	want := fixture.Session["transformsAndProjectors"].(map[string]any)
	for _, name := range []string{"constructorOnly", "constructorAndPerCall"} {
		t.Run(name, func(t *testing.T) {
			assertF6HarnessMap(t, want[name].(map[string]any), got[name].(map[string]any))
		})
	}
}

func TestF6HarnessTypedEmptyActiveToolsMatchUpstream(t *testing.T) {
	fixture := loadF6HarnessFixture(t)
	want := fixture.Session["typedEmptyActiveTools"].(map[string]any)

	t.Run("harness session", func(t *testing.T) {
		storage, err := agentharness.NewInMemorySessionStorage(nil, agentharness.SessionMetadata{
			ID: "typed-active-tools", CreatedAt: "2026-02-03T04:05:06.789Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		session := agentharness.NewSession(storage)
		if _, err := session.AppendActiveToolsChange([]string{}); err != nil {
			t.Fatal(err)
		}
		entries := storage.Entries()
		contextState, err := session.Context()
		if err != nil {
			t.Fatal(err)
		}
		assertF6HarnessMap(t, want, f6HarnessTypedActiveToolsObservation(
			entries[0].Type, entries[0].ActiveToolNames, contextState.ActiveToolNames,
		))
	})

	t.Run("coding session manager", func(t *testing.T) {
		storage, err := agentharness.NewInMemorySessionStorage(nil, agentharness.SessionMetadata{
			ID: "typed-active-tools", CreatedAt: "2026-02-03T04:05:06.789Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		manager, err := sessionstore.FromHarnessStorage(storage, sessionstore.WithCwdOverride(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.AppendActiveToolsChange([]string{}); err != nil {
			t.Fatal(err)
		}
		entries := manager.GetEntries()
		contextState := manager.BuildSessionContext()
		assertF6HarnessMap(t, want, f6HarnessTypedActiveToolsObservation(
			entries[0].Type, entries[0].ActiveToolNames, contextState.ActiveToolNames,
		))
	})
}

func f6HarnessTypedActiveToolsObservation(entryType string, entryTools, contextTools []string) map[string]any {
	return map[string]any{
		"entry":   map[string]any{"type": entryType, "activeToolNames": entryTools},
		"context": map[string]any{"activeToolNames": contextTools},
	}
}

func f6HarnessDropEntry(entries []agentharness.SessionTreeEntry, id string) []agentharness.SessionTreeEntry {
	result := make([]agentharness.SessionTreeEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.ID != id {
			result = append(result, entry)
		}
	}
	return result
}

func TestF6HarnessSessionReposMatchUpstream(t *testing.T) {
	fixture := loadF6HarnessFixture(t)
	input, err := runner.ReadFixture("F6Harness", "session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := agentharness.RehydrateJSONLSession(input, filepath.Join(t.TempDir(), "seed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	entries := seed.Entries()[:3]
	ctx := context.Background()
	root := t.TempDir()

	memoryRepo := agentharness.NewInMemorySessionRepo()
	memorySource, err := memoryRepo.Create(ctx, agentharness.SessionCreateOptions{ID: "memory-source"})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := memorySource.Storage().AppendEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	memoryMetadata := memorySource.Metadata()
	memoryOpened, err := memoryRepo.Open(ctx, memoryMetadata)
	if err != nil {
		t.Fatal(err)
	}
	memoryBefore, err := memoryRepo.Fork(ctx, memoryMetadata, agentharness.SessionForkOptions{
		SessionCreateOptions: agentharness.SessionCreateOptions{ID: "memory-before"}, EntryID: "second-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryAt, err := memoryRepo.Fork(ctx, memoryMetadata, agentharness.SessionForkOptions{
		SessionCreateOptions: agentharness.SessionCreateOptions{ID: "memory-at"}, EntryID: "main-assistant", Position: agentharness.ForkAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryFull, err := memoryRepo.Fork(ctx, memoryMetadata, agentharness.SessionForkOptions{
		SessionCreateOptions: agentharness.SessionCreateOptions{ID: "memory-full"},
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryListed, err := memoryRepo.List(ctx, agentharness.SessionListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := memoryRepo.Delete(ctx, memoryMetadata); err != nil {
		t.Fatal(err)
	}
	_, memoryOpenAfterDelete := memoryRepo.Open(ctx, memoryMetadata)

	env := agentharness.NodeExecutionEnv{CWD: root}
	defer func() { _ = env.Cleanup() }()
	jsonlRepo := agentharness.NewJSONLSessionRepo(env, filepath.Join(root, "repo-sessions"))
	jsonlSource, err := jsonlRepo.Create(ctx, agentharness.SessionCreateOptions{
		ID: "jsonl-source", CWD: "/tmp/my-project",
		Metadata: json.RawMessage(`{ "10" : "ten", "2" : "two", "profile" : "reviewer", "nested" : { "z" : 1, "a" : 2 } }`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := jsonlSource.Storage().AppendEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	jsonlOther, err := jsonlRepo.Create(ctx, agentharness.SessionCreateOptions{ID: "jsonl-other", CWD: "/tmp/other-project"})
	if err != nil {
		t.Fatal(err)
	}
	jsonlMetadata, jsonlOtherMetadata := jsonlSource.Metadata(), jsonlOther.Metadata()
	jsonlOpened, err := jsonlRepo.Open(ctx, jsonlMetadata)
	if err != nil {
		t.Fatal(err)
	}
	jsonlListByCwd, err := jsonlRepo.List(ctx, agentharness.SessionListOptions{CWD: "/tmp/my-project"})
	if err != nil {
		t.Fatal(err)
	}
	jsonlListAll, err := jsonlRepo.List(ctx, agentharness.SessionListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	jsonlBefore, err := jsonlRepo.Fork(ctx, jsonlMetadata, agentharness.SessionForkOptions{
		SessionCreateOptions: agentharness.SessionCreateOptions{ID: "jsonl-before", CWD: "/tmp/target"}, EntryID: "second-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonlInherited, err := jsonlRepo.Fork(ctx, jsonlMetadata, agentharness.SessionForkOptions{
		SessionCreateOptions: agentharness.SessionCreateOptions{ID: "jsonl-inherited", CWD: "/tmp/target"},
	})
	if err != nil {
		t.Fatal(err)
	}
	overrideParent := "/fixture/override-parent.jsonl"
	jsonlOverridden, err := jsonlRepo.Fork(ctx, jsonlMetadata, agentharness.SessionForkOptions{
		SessionCreateOptions: agentharness.SessionCreateOptions{
			ID: "jsonl-overridden", CWD: "/tmp/target", ParentSessionPath: &overrideParent,
			Metadata: json.RawMessage(`{"profile":"writer"}`),
		},
	})
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
	_, jsonlOpenAfterDelete := jsonlRepo.Open(ctx, jsonlMetadata)

	wantRepos := fixture.Session["repos"].(map[string]any)
	wantJSONL := wantRepos["jsonl"].(map[string]any)
	noncanonicalBytes := []byte(wantJSONL["noncanonicalSourceBytes"].(string))
	noncanonicalPath := filepath.Join(root, "noncanonical-source.jsonl")
	if err := env.WriteFile(ctx, noncanonicalPath, noncanonicalBytes); err != nil {
		t.Fatal(err)
	}
	noncanonicalSession, err := jsonlRepo.Open(ctx, agentharness.SessionMetadata{Path: noncanonicalPath})
	if err != nil {
		t.Fatal(err)
	}
	noncanonicalMetadata := noncanonicalSession.Metadata()
	reserialized, err := jsonlRepo.Fork(ctx, noncanonicalMetadata, agentharness.SessionForkOptions{
		SessionCreateOptions: agentharness.SessionCreateOptions{ID: "jsonl-reserialized", CWD: "/tmp/reserialized"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reserializedMetadata := reserialized.Metadata()
	reserializedBytes, err := env.ReadTextFile(ctx, reserializedMetadata.Path)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]any{
		"memory": map[string]any{
			"sourceMetadata":   f6HarnessRepoMetadata(memoryMetadata, root),
			"openedSameObject": memoryOpened == memorySource,
			"listed":           f6HarnessRepoMetadataList(memoryListed, root),
			"beforeEntries":    f6HarnessEntries(memoryBefore.Entries()),
			"atEntries":        f6HarnessEntries(memoryAt.Entries()),
			"fullEntries":      f6HarnessEntries(memoryFull.Entries()),
			"openAfterDelete":  f6HarnessRepoError(memoryOpenAfterDelete, root),
		},
		"jsonl": map[string]any{
			"sourceMetadata":           f6HarnessRepoMetadata(jsonlMetadata, root),
			"otherMetadata":            f6HarnessRepoMetadata(jsonlOtherMetadata, root),
			"openedMetadata":           f6HarnessRepoMetadata(jsonlOpened.Metadata(), root),
			"openedEntries":            f6HarnessEntries(jsonlOpened.Entries()),
			"listByCwd":                f6HarnessRepoMetadataList(jsonlListByCwd, root),
			"listAll":                  f6HarnessRepoMetadataList(jsonlListAll, root),
			"encodedCwdDirectory":      filepath.Base(filepath.Dir(jsonlMetadata.Path)),
			"before":                   f6HarnessRepoFork(jsonlBefore, root),
			"inherited":                f6HarnessRepoFork(jsonlInherited, root),
			"overridden":               f6HarnessRepoFork(jsonlOverridden, root),
			"sourceExistsBeforeDelete": sourceExistsBeforeDelete,
			"sourceExistsAfterDelete":  sourceExistsAfterDelete,
			"openAfterDelete":          f6HarnessRepoError(jsonlOpenAfterDelete, root),
			"noncanonicalSourceBytes":  string(noncanonicalBytes),
			"noncanonicalMetadata":     f6HarnessRepoMetadata(noncanonicalMetadata, root),
			"reserializedMetadata":     f6HarnessRepoMetadata(reserializedMetadata, root),
			"reserializedBytes":        f6HarnessRepoJSONL(reserializedBytes, reserializedMetadata, root),
		},
	}
	for _, repoType := range []string{"memory", "jsonl"} {
		t.Run(repoType, func(t *testing.T) {
			assertF6HarnessMap(
				t,
				wantRepos[repoType].(map[string]any),
				got[repoType].(map[string]any),
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
	preTempDir, preTempDirErr := env.CreateTempDir(aborted, "pigo-aborted-")
	preTempFile, preTempFileErr := env.CreateTempFile(aborted, "aborted-", ".tmp")
	tempDir, tempDirErr := env.CreateTempDir(context.Background(), "pigo-harness-")
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
		"symlinkInfo":                 f6HarnessResult(map[string]any{"name": link.Name, "path": normalizeF6HarnessPath(link.Path, root), "kind": link.Kind, "size": link.Size}, linkErr, root),
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
			"createTempDir":  f6HarnessTempResult(preTempDir, preTempDirErr, "pigo-aborted-", "", root),
			"createTempFile": f6HarnessTempResult(preTempFile, preTempFileErr, "aborted-", ".tmp", root),
		},
		"invalidTimeout": f6HarnessResult(nil, invalidTimeoutErr, root),
		"timedOutExec":   f6HarnessResult(nil, timeoutErr, root),
		"callbackError":  f6HarnessResult(nil, callbackErr, root),
		"temp": map[string]any{
			"dirPrefix":  tempDirErr == nil && strings.HasPrefix(filepath.Base(tempDir), "pigo-harness-"),
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

func observeF6HarnessStorage(t *testing.T, storage agentharness.SessionStorage, root string) map[string]any {
	t.Helper()
	leaf, err := storage.LeafID()
	if err != nil {
		t.Fatal(err)
	}
	branch, err := storage.PathToRoot(leaf)
	if err != nil {
		t.Fatal(err)
	}
	session := agentharness.NewSession(storage)
	contextState, err := session.Context()
	if err != nil {
		t.Fatal(err)
	}
	name, hasName, err := session.Name()
	if err != nil {
		t.Fatal(err)
	}
	rootLabel, hasRoot := storage.Label("root-user")
	branchLabel, hasBranch := storage.Label("branch-user")
	labels := map[string]any{"root": nil, "branch": nil}
	if hasRoot {
		labels["root"] = rootLabel
	}
	if hasBranch {
		labels["branch"] = branchLabel
	}
	var leafValue any
	if leaf != nil {
		leafValue = *leaf
	}
	var nameValue any
	if hasName {
		nameValue = name
	}
	metadata := f6HarnessJSONValue(storage.Metadata())
	metadataMap := metadata.(map[string]any)
	if pathValue, ok := metadataMap["path"].(string); ok {
		metadataMap["path"] = normalizeF6HarnessPath(pathValue, root)
	}
	return map[string]any{
		"metadata":    metadataMap,
		"leafId":      leafValue,
		"entries":     f6HarnessEntries(storage.Entries()),
		"entryIds":    f6HarnessEntryIDs(storage.Entries()),
		"branchIds":   f6HarnessEntryIDs(branch),
		"messageIds":  f6HarnessEntryIDs(storage.EntriesByType("message")),
		"labels":      labels,
		"sessionName": nameValue,
		"context":     f6HarnessContextObservation(t, contextState),
	}
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

func f6HarnessEntries(entries []agentharness.SessionTreeEntry) []any {
	result := make([]any, len(entries))
	for index := range entries {
		result[index] = f6HarnessEntry(entries[index])
	}
	return result
}

func f6HarnessEntry(entry agentharness.SessionTreeEntry) map[string]any {
	value := map[string]any{
		"type": entry.Type, "id": entry.ID, "parentId": entry.ParentID, "timestamp": entry.Timestamp,
	}
	switch entry.Type {
	case "message":
		value["message"] = f6HarnessRaw(entry.Message)
	case "thinking_level_change":
		value["thinkingLevel"] = entry.ThinkingLevel
	case "model_change":
		value["provider"], value["modelId"] = entry.Provider, entry.ModelID
	case "active_tools_change":
		value["activeToolNames"] = entry.ActiveToolNames
	case "compaction":
		value["summary"], value["firstKeptEntryId"], value["tokensBefore"] = entry.Summary, entry.FirstKeptEntryID, entry.TokensBefore
		if len(entry.Details) != 0 {
			value["details"] = f6HarnessRaw(entry.Details)
		}
		if entry.FromHook != nil {
			value["fromHook"] = *entry.FromHook
		}
	case "branch_summary":
		value["fromId"], value["summary"] = entry.FromID, entry.Summary
		if len(entry.Details) != 0 {
			value["details"] = f6HarnessRaw(entry.Details)
		}
		if entry.FromHook != nil {
			value["fromHook"] = *entry.FromHook
		}
	case "custom":
		value["customType"] = entry.CustomType
		if len(entry.Data) != 0 {
			value["data"] = f6HarnessRaw(entry.Data)
		}
	case "custom_message":
		value["customType"], value["content"], value["display"] = entry.CustomType, f6HarnessRaw(entry.Content), entry.Display
		if len(entry.Details) != 0 {
			value["details"] = f6HarnessRaw(entry.Details)
		}
	case "label":
		value["targetId"] = entry.TargetID
		if entry.Label != nil {
			value["label"] = *entry.Label
		}
	case "session_info":
		value["name"] = entry.Name
	case "leaf":
		value["targetId"] = entry.TargetID
	}
	return value
}

func f6HarnessRaw(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

func f6HarnessStringPointer(value string) *string {
	return &value
}

var f6HarnessRepoTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-\d{3}Z_`)

func f6HarnessRepoMetadata(metadata agentharness.SessionMetadata, root string) map[string]any {
	value := map[string]any{
		"id": metadata.ID, "createdAt": metadata.CreatedAt,
	}
	if metadata.CWD != "" {
		value["cwd"] = metadata.CWD
	}
	if metadata.Path != "" {
		value["path"] = metadata.Path
	}
	if metadata.ParentSessionPath != nil {
		value["parentSessionPath"] = *metadata.ParentSessionPath
	}
	if len(metadata.Metadata) != 0 {
		value["metadata"] = f6HarnessRaw(metadata.Metadata)
	}
	return f6HarnessNormalizeRepoValue(value, root).(map[string]any)
}

func f6HarnessRepoMetadataList(metadata []agentharness.SessionMetadata, root string) []any {
	sorted := append([]agentharness.SessionMetadata(nil), metadata...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].ID < sorted[right].ID })
	result := make([]any, len(sorted))
	for index := range sorted {
		result[index] = f6HarnessRepoMetadata(sorted[index], root)
	}
	return result
}

func f6HarnessRepoFork(session *agentharness.Session, root string) map[string]any {
	return map[string]any{
		"metadata": f6HarnessRepoMetadata(session.Metadata(), root),
		"entries":  f6HarnessEntries(session.Entries()),
	}
}

func f6HarnessRepoError(err error, root string) any {
	return f6HarnessNormalizeRepoValue(f6HarnessSessionError(err, root), root)
}

func f6HarnessRepoJSONL(content string, metadata agentharness.SessionMetadata, root string) string {
	content = strings.ReplaceAll(content, metadata.CreatedAt, "<createdAt>")
	pathTimestamp := strings.NewReplacer(":", "-", ".", "-").Replace(metadata.CreatedAt)
	content = strings.ReplaceAll(content, pathTimestamp, "<createdAt>")
	return f6HarnessNormalizeRepoValue(content, root).(string)
}

func f6HarnessNormalizeRepoValue(value any, root string) any {
	switch typed := value.(type) {
	case string:
		return normalizeF6HarnessPath(f6HarnessRepoTimestamp.ReplaceAllString(typed, "<createdAt>_"), root)
	case []any:
		for index := range typed {
			typed[index] = f6HarnessNormalizeRepoValue(typed[index], root)
		}
	case map[string]any:
		for key := range typed {
			if key == "createdAt" {
				typed[key] = "<createdAt>"
			} else {
				typed[key] = f6HarnessNormalizeRepoValue(typed[key], root)
			}
		}
	}
	return value
}

func f6HarnessEntryIDs(entries []agentharness.SessionTreeEntry) []string {
	ids := make([]string, len(entries))
	for index := range entries {
		ids[index] = entries[index].ID
	}
	return ids
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
	return map[string]any{"ok": true, "value": normalizeF6HarnessValue(f6HarnessJSONValue(value), root)}
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
	result := map[string]any{"message": normalizeF6HarnessPath(err.Error(), root)}
	var fileError *agentharness.FileError
	var executionError *agentharness.ExecutionError
	switch {
	case errors.As(err, &fileError):
		result["code"] = fileError.Code
		if fileError.Path != "" {
			result["path"] = normalizeF6HarnessPath(fileError.Path, root)
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
	result := map[string]any{"message": normalizeF6HarnessPath(err.Error(), root)}
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

func normalizeF6HarnessValue(value any, root string) any {
	switch typed := value.(type) {
	case string:
		return normalizeF6HarnessPath(typed, root)
	case []any:
		for index := range typed {
			typed[index] = normalizeF6HarnessValue(typed[index], root)
		}
	case map[string]any:
		for key := range typed {
			typed[key] = normalizeF6HarnessValue(typed[key], root)
		}
	}
	return value
}

func normalizeF6HarnessPath(value, root string) string {
	return runner.ReplacePathAliases(filepath.ToSlash(value), filepath.ToSlash(root), "<fixture>")
}

func assertF6HarnessJSONEqual(t *testing.T, want, got any) {
	t.Helper()
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err = runner.CanonicalJSON(wantJSON)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err = runner.CanonicalJSON(gotJSON)
	if err != nil {
		t.Fatal(err)
	}
	if diff := runner.ByteDiff(wantJSON, gotJSON); diff != "" {
		t.Fatal(diff)
	}
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
			assertF6HarnessJSONEqual(t, want[key], value)
		})
	}
}
