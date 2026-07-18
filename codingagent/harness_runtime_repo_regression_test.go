package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func TestRepoBoundHarnessRuntimeSupportsAllReplacementOperations(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	env := harness.NodeExecutionEnv{CWD: cwd}
	t.Cleanup(func() { _ = env.Cleanup() })
	repo := harness.NewJSONLSessionRepo(env, filepath.Join(cwd, "sessions"))
	source, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "source", CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	userID, err := source.AppendMessage(map[string]any{"role": "user", "content": "fork me", "timestamp": int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	sourceMetadata := source.Metadata()
	manager, err := sessionstore.FromHarnessStorage(source.Storage(), sessionstore.WithHarnessRepo(repo))
	if err != nil {
		t.Fatal(err)
	}
	provider := harnessRegressionFaux()
	runtime, err := NewAgentSessionRuntime(ctx, AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Dispose(ctx) })

	result, err := runtime.NewSession(ctx, nil)
	if err != nil || result.Cancelled {
		t.Fatalf("repo-backed new session = %#v, %v", result, err)
	}
	assertHarnessBackedRuntime(t, runtime, "new")

	result, err = runtime.SwitchSession(ctx, sourceMetadata.Path, nil)
	if err != nil || result.Cancelled {
		t.Fatalf("repo-backed resume = %#v, %v", result, err)
	}
	assertHarnessBackedRuntime(t, runtime, "resume")

	forked, err := runtime.Fork(ctx, userID, &extensions.ForkOptions{Position: extensions.ForkAt})
	if err != nil || forked.Cancelled {
		t.Fatalf("repo-backed fork = %#v, %v", forked, err)
	}
	assertHarnessBackedRuntime(t, runtime, "fork")

	contents, err := os.ReadFile(sourceMetadata.Path)
	if err != nil {
		t.Fatal(err)
	}
	importPath := filepath.Join(t.TempDir(), "imported.jsonl")
	if err := os.WriteFile(importPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = runtime.ImportFromJSONL(ctx, importPath, "")
	if err != nil || result.Cancelled {
		t.Fatalf("repo-backed import = %#v, %v", result, err)
	}
	assertHarnessBackedRuntime(t, runtime, "import")
}

func TestRepoBoundHarnessRuntimeSwitchAcceptsFutureSessionVersion(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	env := harness.NodeExecutionEnv{CWD: cwd}
	t.Cleanup(func() { _ = env.Cleanup() })
	repo := harness.NewJSONLSessionRepo(env, filepath.Join(cwd, "sessions"))
	current, err := repo.Create(ctx, harness.SessionCreateOptions{ID: "current", CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(current.Storage(), sessionstore.WithHarnessRepo(repo))
	if err != nil {
		t.Fatal(err)
	}
	provider := harnessRegressionFaux()
	runtime, err := NewAgentSessionRuntime(ctx, AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Dispose(ctx) })

	invalidPath := filepath.Join(filepath.Dir(current.Metadata().Path), "invalid.jsonl")
	invalidHeader := `{"type":"session","version":999,"id":"invalid","timestamp":"2026-07-18T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n"
	if err := os.WriteFile(invalidPath, []byte(invalidHeader), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.SwitchSession(ctx, invalidPath, nil)
	if err != nil || result.Cancelled {
		t.Fatalf("switch future-version session = %#v, %v", result, err)
	}
	header := runtime.Session().Manager().GetHeader()
	if header == nil || header.Version == nil || *header.Version != 999 {
		t.Fatalf("future-version header = %#v, want version 999", header)
	}
}

func TestRepoBoundHarnessRuntimePreservesUnknownFutureMembersAcrossManagerViews(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
	path := filepath.Join(filepath.Dir(runtime.Session().Manager().GetSessionFile()), "future-members.jsonl")
	headerLine := []byte(`{"futureHeader":{"beta":2,"alpha":1},"type":"session","version":999,"id":"future-members","timestamp":"2026-07-18T00:00:00.000Z","cwd":"` + cwd + `","futureTail":[3,2,1]}`)
	entryLine := []byte(`{"futureEntry":{"beta":2,"alpha":1},"type":"message","id":"future-entry","parentId":null,"timestamp":"2026-07-18T00:00:01.000Z","message":{"role":"user","content":"future","timestamp":1},"futureTail":true}`)
	thinkingLine := []byte(`{"type":"thinking_level_change","id":"future-thinking","parentId":"future-entry","timestamp":"2026-07-18T00:00:02.000Z","thinkingLevel":"off"}`)
	contents := bytes.Join([][]byte{headerLine, entryLine, thinkingLine, nil}, []byte{'\n'})
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.SwitchSession(ctx, path, nil)
	if err != nil || result.Cancelled {
		t.Fatalf("switch future-member session = %#v, %v", result, err)
	}
	manager := runtime.Session().Manager()
	assertJSON := func(t *testing.T, value any, want []byte) {
		t.Helper()
		got, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("unknown members changed\n got: %s\nwant: %s", got, want)
		}
	}

	t.Run("header", func(t *testing.T) {
		assertJSON(t, manager.GetHeader(), headerLine)
	})
	t.Run("entry", func(t *testing.T) {
		assertJSON(t, manager.GetEntry("future-entry"), entryLine)
	})
	t.Run("tree", func(t *testing.T) {
		tree := manager.GetTree()
		if len(tree) != 1 || tree[0].Entry.ID != "future-entry" {
			t.Fatalf("session tree roots = %#v, want future-entry", tree)
		}
		assertJSON(t, tree[0].Entry, entryLine)
	})
	t.Run("jsonl", func(t *testing.T) {
		got, jsonlErr := manager.JSONL()
		if jsonlErr != nil {
			t.Fatal(jsonlErr)
		}
		if !bytes.Equal(got, contents) {
			t.Fatalf("JSONL bytes changed\n got: %s\nwant: %s", got, contents)
		}
	})
}

func TestRepoBoundHarnessRuntimeImportsFutureSessionVersion(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
	inputPath := filepath.Join(t.TempDir(), "future-import.jsonl")
	if err := os.WriteFile(inputPath, futureHarnessSessionJSONL(cwd, "future-import", "future-import-user"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.ImportFromJSONL(ctx, inputPath, "")
	if err != nil || result.Cancelled {
		t.Fatalf("import future-version session = %#v, %v", result, err)
	}
	header := runtime.Session().Manager().GetHeader()
	if header == nil || header.Version == nil || *header.Version != 999 {
		t.Fatalf("imported future-version header = %#v, want version 999", header)
	}
	if entry := runtime.Session().Manager().GetEntry("future-import-user"); entry == nil {
		t.Fatal("imported future-version session lost its user entry")
	}
}

func TestRepoBoundHarnessRuntimeForksFutureSessionVersion(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
	path := filepath.Join(filepath.Dir(runtime.Session().Manager().GetSessionFile()), "future-fork.jsonl")
	if err := os.WriteFile(path, futureHarnessSessionJSONL(cwd, "future-fork", "future-fork-user"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := runtime.SwitchSession(ctx, path, nil); err != nil || result.Cancelled {
		t.Fatalf("switch future-version session = %#v, %v", result, err)
	}

	result, err := runtime.Fork(ctx, "future-fork-user", &extensions.ForkOptions{Position: extensions.ForkAt})
	if err != nil || result.Cancelled {
		t.Fatalf("fork future-version session = %#v, %v", result, err)
	}
	assertHarnessBackedRuntime(t, runtime, "future-version fork")
}

func TestRepoBoundHarnessRuntimeSkipsMalformedJSONLLines(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
	path := filepath.Join(filepath.Dir(runtime.Session().Manager().GetSessionFile()), "malformed-line.jsonl")
	contents := append([]byte(
		`{"type":"session","version":3,"id":"malformed-line","timestamp":"2026-07-18T00:00:00.000Z","cwd":"`+cwd+`"}`+"\n"+
			"not json\n",
	), []byte(`{"type":"message","id":"valid-user","parentId":null,"timestamp":"2026-07-18T00:00:01.000Z","message":{"role":"user","content":"keep me","timestamp":1}}`+"\n")...)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := runtime.SwitchSession(ctx, path, nil)
	if err != nil || result.Cancelled {
		t.Fatalf("switch session containing malformed line = %#v, %v", result, err)
	}
	if entry := runtime.Session().Manager().GetEntry("valid-user"); entry == nil {
		t.Fatal("valid entry after malformed JSONL line was not loaded")
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stored, []byte("not json\n")) {
		t.Fatal("resume rewrote the malformed line instead of leaving the source JSONL intact")
	}
}

func TestJSONLRuntimeDelayedFlushUsesExclusiveCreate(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	path := filepath.Join(cwd, "sessions", "delayed.jsonl")
	sentinel := []byte("created by another process\n")
	fileSystem := &delayedFlushCollisionFileSystem{
		NodeExecutionEnv: harness.NodeExecutionEnv{CWD: cwd},
		target:           path,
		sentinel:         sentinel,
	}
	repo := harness.NewJSONLSessionRepo(fileSystem, filepath.Join(cwd, "sessions-root"))
	session, err := repo.OpenRuntimePath(ctx, path, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.AppendMessage(map[string]any{"role": "user", "content": "pending", "timestamp": int64(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.AppendMessage(map[string]any{"role": "assistant", "content": "flush", "timestamp": int64(2)}); err == nil {
		t.Error("assistant flush overwrote a file created after the missing-path check")
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, sentinel) {
		t.Fatalf("exclusive-create collision changed existing file: got %q, want %q", stored, sentinel)
	}
}

func TestRepoBoundHarnessRuntimeUnsavedForkMatchesUpstreamError(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	runtime, _ := newFidelityHarnessRepoRuntime(t, cwd, nil)
	path := filepath.Join(filepath.Dir(runtime.Session().Manager().GetSessionFile()), "unsaved-fork.jsonl")
	if result, err := runtime.SwitchSession(ctx, path, nil); err != nil || result.Cancelled {
		t.Fatalf("switch missing session path = %#v, %v", result, err)
	}
	active := runtime.Session()
	userID, err := active.Manager().AppendMessage(map[string]any{"role": "user", "content": "fork before save", "timestamp": int64(1)})
	if err != nil {
		t.Fatal(err)
	}

	result, err := runtime.Fork(ctx, userID, &extensions.ForkOptions{Position: extensions.ForkAt})
	const want = "This session has not been saved yet. Wait for the first assistant response before cloning or forking it."
	if err == nil || err.Error() != want {
		t.Fatalf("unsaved fork = %#v, %v, want exact error %q", result, err, want)
	}
	if result.Cancelled {
		t.Fatal("unsaved fork was reported as cancelled")
	}
	if runtime.Session() != active {
		t.Fatal("failed unsaved fork replaced the active runtime")
	}
}

func futureHarnessSessionJSONL(cwd, id, userID string) []byte {
	return []byte(
		`{"type":"session","version":999,"id":"` + id + `","timestamp":"2026-07-18T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" +
			`{"type":"message","id":"` + userID + `","parentId":null,"timestamp":"2026-07-18T00:00:01.000Z","message":{"role":"user","content":"future","timestamp":1}}` + "\n",
	)
}

type delayedFlushCollisionFileSystem struct {
	harness.NodeExecutionEnv
	target       string
	sentinel     []byte
	targetChecks int
}

func (fileSystem *delayedFlushCollisionFileSystem) Exists(ctx context.Context, path string) (bool, error) {
	resolved, err := fileSystem.AbsolutePath(ctx, path)
	if err != nil {
		return false, err
	}
	if filepath.Clean(resolved) == filepath.Clean(fileSystem.target) {
		fileSystem.targetChecks++
		if fileSystem.targetChecks == 2 {
			if err := os.MkdirAll(filepath.Dir(fileSystem.target), 0o755); err != nil {
				return false, err
			}
			if err := os.WriteFile(fileSystem.target, fileSystem.sentinel, 0o600); err != nil {
				return false, err
			}
			return false, nil
		}
	}
	return fileSystem.NodeExecutionEnv.Exists(ctx, path)
}

func assertHarnessBackedRuntime(t *testing.T, runtime *AgentSessionRuntime, operation string) {
	t.Helper()
	if manager := runtime.Session().Manager(); manager == nil || !manager.IsHarnessBacked() {
		t.Fatalf("%s replacement detached the runtime from its harness repository", operation)
	}
}
