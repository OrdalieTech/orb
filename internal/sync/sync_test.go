package upstreamsync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMirrorMappingPrefersFileRowsAndExpandsBraces(t *testing.T) {
	mirror, err := parseMirror([]byte(`# MIRROR

| Upstream | pigo |
|---|---|
| ` + "`packages/ai/src/`" + ` | ` + "`ai/`" + ` |

| Upstream file | pigo file | WP |
|---|---|---|
| ` + "`packages/ai/src/types.ts`" + ` | ` + "`ai/types.go`, `codingagent/messages.go`" + ` | WP-110 |
| ` + "`packages/agent/src/harness/compaction/{compaction,branch-summarization,utils}.ts`" + ` | ` + "`agent/harness/compaction.go`" + ` | WP-310 |
`))
	if err != nil {
		t.Fatal(err)
	}
	targets, wps := mirror.lookup("packages/ai/src/types.ts")
	if want := []string{"ai/types.go", "codingagent/messages.go"}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("specific targets = %v, want %v", targets, want)
	}
	if !reflect.DeepEqual(wps, []string{"WP-110"}) {
		t.Fatalf("specific WPs = %v", wps)
	}
	targets, wps = mirror.lookup("packages/ai/src/new-feature.ts")
	if !reflect.DeepEqual(targets, []string{"ai/"}) || len(wps) != 0 {
		t.Fatalf("baseline mapping = %v %v", targets, wps)
	}
	targets, wps = mirror.lookup("packages/agent/src/harness/compaction/branch-summarization.ts")
	if !reflect.DeepEqual(targets, []string{"agent/harness/compaction.go"}) || !reflect.DeepEqual(wps, []string{"WP-310"}) {
		t.Fatalf("brace mapping = %v %v", targets, wps)
	}
}

func TestClassificationFlagsWireAndPublicSurfaces(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{filename: "packages/ai/src/types.ts", want: ClassWire},
		{filename: "packages/ai/src/api/openai-responses.ts", want: ClassWire},
		{filename: "packages/coding-agent/src/core/session-manager.ts", want: ClassWire},
		{filename: "packages/agent/src/harness/session/session.ts", want: ClassWire},
		{filename: "packages/coding-agent/src/core/extensions/types.ts", want: ClassAPI},
		{filename: "packages/coding-agent/src/core/agent-session.ts", want: ClassAPI},
		{filename: "packages/ai/src/providers/openai.ts", want: ClassAPI},
		{filename: "packages/coding-agent/docs/session-format.md", want: ClassDocs},
		{filename: "packages/coding-agent/src/core/usage-totals.ts", want: ClassFeature},
	}
	for _, testCase := range tests {
		if got := classifyChange(testCase.filename, ""); got != testCase.want {
			t.Errorf("classify %s = %s, want %s", testCase.filename, got, testCase.want)
		}
	}
}

func TestCompareFixturesReportsAddedModifiedAndDeleted(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := t.TempDir()
	writeTestFile(t, filepath.Join(oldRoot, "same.json"), "same\n")
	writeTestFile(t, filepath.Join(newRoot, "same.json"), "same\n")
	writeTestFile(t, filepath.Join(oldRoot, "changed.json"), "old\n")
	writeTestFile(t, filepath.Join(newRoot, "changed.json"), "new value\n")
	writeTestFile(t, filepath.Join(oldRoot, "deleted.json"), "deleted\n")
	writeTestFile(t, filepath.Join(newRoot, "added.json"), "added\n")
	changes, err := compareFixtures(oldRoot, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]string)
	for _, change := range changes {
		statuses[change.Path] = change.Status
	}
	want := map[string]string{"added.json": "A", "changed.json": "M", "deleted.json": "D"}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("fixture changes = %v, want %v", statuses, want)
	}
}

func TestDryRunAgainstKnownNewerCommitProducesReadableReport(t *testing.T) {
	fixture := newSyncFixture(t)
	result, err := Run(context.Background(), fixture.config(false, false))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Green || result.TargetCommit != fixture.target {
		t.Fatalf("result = green:%v target:%s", result.Green, result.TargetCommit)
	}
	if len(result.Changes) != 1 || result.Changes[0].Classification != ClassWire {
		t.Fatalf("changes = %+v", result.Changes)
	}
	for _, fragment := range []string{"Status: **GREEN**", "wire-format", "WP-110", "Fixture regeneration", "Proposed work items", "Not attempted (dry run)"} {
		if !strings.Contains(result.Report, fragment) {
			t.Errorf("report does not contain %q", fragment)
		}
	}
	assertPinnedState(t, fixture)
	if _, err := os.Stat(result.ReportPath); err != nil {
		t.Fatalf("report was not written: %v", err)
	}
}

func TestLockBumpRefusesRedConformance(t *testing.T) {
	fixture := newSyncFixture(t)
	config := fixture.config(true, true)
	config.conformance = func(context.Context, string, string, Lock) (string, error) {
		return "--- FAIL: TestWire\nFAIL\n", errors.New("exit status 1")
	}
	result, err := Run(context.Background(), config)
	if !errors.Is(err, ErrPromotionUnsafe) || !errors.Is(err, ErrRed) {
		t.Fatalf("Run error = %v, want promotion refusal and red", err)
	}
	if result.Green || !strings.Contains(result.Report, "Status: **RED**") || !strings.Contains(result.Report, "Promotion: Refused") {
		t.Fatalf("red report = %s", result.Report)
	}
	assertPinnedState(t, fixture)
}

func TestCandidateConformanceCopyUsesTargetLock(t *testing.T) {
	fixture := newSyncFixture(t)
	config := fixture.config(false, false)
	config.conformance = func(_ context.Context, root, fixtures string, lock Lock) (string, error) {
		copyRoot, cleanup, err := prepareConformanceCopy(root, fixtures, lock)
		if err != nil {
			return "", err
		}
		defer cleanup()
		candidate, err := readLock(filepath.Join(copyRoot, "UPSTREAM.lock"))
		if err != nil {
			return "", err
		}
		if candidate.Commit != fixture.target {
			return "", errors.New("conformance copy kept pinned lock")
		}
		return "ok\n", nil
	}
	if _, err := Run(context.Background(), config); err != nil {
		t.Fatal(err)
	}
}

func TestGreenLockBumpPromotesFixturesAndLock(t *testing.T) {
	fixture := newSyncFixture(t)
	result, err := Run(context.Background(), fixture.config(true, false))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Green || !strings.Contains(result.Promotion, "Promoted") {
		t.Fatalf("promotion result = %+v", result)
	}
	lock, err := readLock(filepath.Join(fixture.root, "UPSTREAM.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Commit != fixture.target || lock.SyncedAt != "2026-07-18" {
		t.Fatalf("promoted lock = %+v", lock)
	}
	data, err := os.ReadFile(filepath.Join(fixture.root, "conformance", "fixtures", "F1", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new fixture\n" {
		t.Fatalf("promoted fixture = %q", data)
	}
}

type syncFixture struct {
	root     string
	upstream string
	base     string
	target   string
}

func newSyncFixture(t *testing.T) syncFixture {
	t.Helper()
	root := t.TempDir()
	upstream := filepath.Join(root, ".upstream")
	if err := os.MkdirAll(filepath.Join(upstream, "packages", "ai", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(upstream, "packages", "coding-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, upstream, "init", "-b", "main")
	gitTest(t, upstream, "config", "user.name", "Sync Test")
	gitTest(t, upstream, "config", "user.email", "sync@example.test")
	writeTestFile(t, filepath.Join(upstream, "packages", "ai", "src", "types.ts"), "export interface Message { type: string }\n")
	writeTestFile(t, filepath.Join(upstream, "packages", "coding-agent", "package.json"), "{\"version\":\"1.0.0\"}\n")
	gitTest(t, upstream, "add", ".")
	gitTest(t, upstream, "commit", "-m", "base")
	base := strings.TrimSpace(gitTest(t, upstream, "rev-parse", "HEAD"))
	writeTestFile(t, filepath.Join(upstream, "packages", "ai", "src", "types.ts"), "export interface Message { type: string; usage: number }\n")
	gitTest(t, upstream, "add", ".")
	gitTest(t, upstream, "commit", "-m", "add usage to message wire format")
	target := strings.TrimSpace(gitTest(t, upstream, "rev-parse", "HEAD"))
	gitTest(t, upstream, "checkout", "--detach", base)

	writeTestFile(t, filepath.Join(root, ".gitignore"), ".upstream/\n")
	writeTestFile(t, filepath.Join(root, "UPSTREAM.lock"), "{\n  \"repo\": \""+upstream+"\",\n  \"commit\": \""+base+"\",\n  \"version\": \"1.0.0\",\n  \"syncedAt\": \"2026-07-17\"\n}\n")
	writeTestFile(t, filepath.Join(root, "docs", "MIRROR.md"), "| Upstream file | pigo file | WP |\n|---|---|---|\n| `packages/ai/src/types.ts` | `ai/types.go` | WP-110 |\n")
	writeTestFile(t, filepath.Join(root, "conformance", "fixtures", "F1", "cases.json"), "old fixture\n")
	gitTest(t, root, "init", "-b", "main")
	gitTest(t, root, "config", "user.name", "Sync Test")
	gitTest(t, root, "config", "user.email", "sync@example.test")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-m", "pigo base")
	return syncFixture{root: root, upstream: upstream, base: base, target: target}
}

func (fixture syncFixture) config(bump, red bool) Config {
	config := Config{
		Root:        fixture.root,
		UpstreamDir: fixture.upstream,
		Target:      fixture.target,
		ReportPath:  filepath.Join("docs", "sync", "reports", "2026-07-18.md"),
		Fetch:       false,
		DryRun:      !bump,
		Bump:        bump,
		Now:         time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC),
	}
	config.generate = func(_ context.Context, _, _, output, _ string) (string, error) {
		if err := writeTestFileRaw(filepath.Join(output, "F1", "cases.json"), "new fixture\n"); err != nil {
			return "", err
		}
		return "generated F1\n", nil
	}
	config.conformance = func(context.Context, string, string, Lock) (string, error) {
		if red {
			return "FAIL\n", errors.New("exit status 1")
		}
		return "ok github.com/OrdalieTech/pigo/conformance/runner\n", nil
	}
	return config
}

func assertPinnedState(t *testing.T, fixture syncFixture) {
	t.Helper()
	lock, err := readLock(filepath.Join(fixture.root, "UPSTREAM.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Commit != fixture.base {
		t.Fatalf("lock commit = %s, want %s", lock.Commit, fixture.base)
	}
	data, err := os.ReadFile(filepath.Join(fixture.root, "conformance", "fixtures", "F1", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old fixture\n" {
		t.Fatalf("committed fixture changed: %q", data)
	}
	head := strings.TrimSpace(gitTest(t, fixture.upstream, "rev-parse", "HEAD"))
	if head != fixture.base {
		t.Fatalf("upstream HEAD = %s, want restored %s", head, fixture.base)
	}
}

func gitTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func writeTestFile(t *testing.T, filename, content string) {
	t.Helper()
	if err := writeTestFileRaw(filename, content); err != nil {
		t.Fatal(err)
	}
}

func writeTestFileRaw(filename, content string) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filename, []byte(content), 0o644)
}
