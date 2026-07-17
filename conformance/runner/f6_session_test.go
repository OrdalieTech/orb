package runner_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type f6Fixture struct {
	SchemaVersion  int               `json:"schemaVersion"`
	ParseCases     []f6ParseCase     `json:"parseCases"`
	MigrationCases []f6MigrationCase `json:"migrationCases"`
	InvalidUTF8    struct {
		InputBase64 string          `json:"inputBase64"`
		Expected    json.RawMessage `json:"expected"`
	} `json:"invalidUTF8"`
	LazyPersistence struct {
		PreAssistantExists  bool `json:"preAssistantExists"`
		PostAssistantExists bool `json:"postAssistantExists"`
	} `json:"lazyPersistence"`
}

type f6ParseCase struct {
	Name     string          `json:"name"`
	Input    string          `json:"input"`
	Expected json.RawMessage `json:"expected"`
}

type f6MigrationCase struct {
	Name     string `json:"name"`
	Input    string `json:"input"`
	Expected string `json:"expected"`
}

type f6Projection struct {
	Header      json.RawMessage   `json:"header"`
	LeafID      *string           `json:"leafId"`
	EntryTypes  []string          `json:"entryTypes"`
	BranchIDs   []string          `json:"branchIds"`
	SessionName *string           `json:"sessionName"`
	Entries     []json.RawMessage `json:"entries"`
}

func TestF6SessionParsingMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "F6")
	if manifest.Family != "F6" || manifest.Generator != "conformance/extract/f6-session.ts" {
		t.Fatalf("unexpected F6 manifest: %+v", manifest)
	}

	fixture := loadF6Fixture(t)
	for _, fixtureCase := range fixture.ParseCases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			entries := session.ParseSessionEntries(fixtureCase.Input)
			got, err := f6MarshalJSONArray(entries)
			if err != nil {
				t.Fatalf("marshal parsed entries: %v", err)
			}
			wantCanonical, err := runner.CanonicalJSON(fixtureCase.Expected)
			if err != nil {
				t.Fatalf("canonicalize fixture: %v", err)
			}
			gotCanonical, err := runner.CanonicalJSON(got)
			if err != nil {
				t.Fatalf("canonicalize Go result: %v", err)
			}
			if diff := runner.ByteDiff(wantCanonical, gotCanonical); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestF6SessionMigrationMatchesUpstream(t *testing.T) {
	fixture := loadF6Fixture(t)
	for _, fixtureCase := range fixture.MigrationCases {
		fixtureCase := fixtureCase
		t.Run(fixtureCase.Name, func(t *testing.T) {
			entries := session.ParseSessionEntries(fixtureCase.Input)
			nextID := 0
			changed, err := session.MigrateSessionEntries(entries, func() (string, error) {
				nextID++
				return fmt.Sprintf("%08x", nextID), nil
			})
			if err != nil {
				t.Fatalf("migrate entries: %v", err)
			}
			if wantChanged := f6InputVersion(fixtureCase.Input) < session.CurrentVersion; changed != wantChanged {
				t.Fatalf("migration changed = %t, want %t", changed, wantChanged)
			}
			got, err := f6MarshalJSONL(entries)
			if err != nil {
				t.Fatalf("marshal migrated entries: %v", err)
			}
			if diff := runner.ByteDiff([]byte(fixtureCase.Expected), got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestF6SessionInvalidUTF8DecodingMatchesUpstream(t *testing.T) {
	fixture := loadF6Fixture(t)
	contents, err := base64.StdEncoding.DecodeString(fixture.InvalidUTF8.InputBase64)
	if err != nil {
		t.Fatalf("decode invalid-UTF8 fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "invalid-utf8.jsonl")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write invalid-UTF8 fixture: %v", err)
	}
	entries, err := session.LoadEntriesFromFile(path)
	if err != nil {
		t.Fatalf("load invalid-UTF8 fixture: %v", err)
	}
	got, err := f6MarshalJSONArray(entries)
	if err != nil {
		t.Fatalf("marshal invalid-UTF8 entries: %v", err)
	}
	wantCanonical, err := runner.CanonicalJSON(fixture.InvalidUTF8.Expected)
	if err != nil {
		t.Fatalf("canonicalize invalid-UTF8 fixture: %v", err)
	}
	gotCanonical, err := runner.CanonicalJSON(got)
	if err != nil {
		t.Fatalf("canonicalize invalid-UTF8 result: %v", err)
	}
	if diff := runner.ByteDiff(wantCanonical, gotCanonical); diff != "" {
		t.Fatal(diff)
	}
}

func TestF6SessionWriteAndProjectionMatchUpstream(t *testing.T) {
	fixture := loadF6Fixture(t)
	wantJSONL, err := runner.ReadFixture("F6", "write.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	wantProjection, err := runner.ReadFixture("F6", "projection.json")
	if err != nil {
		t.Fatal(err)
	}

	gotJSONL, preAssistantExists, postAssistantExists := f6BuildWrittenSession(t)
	if preAssistantExists != fixture.LazyPersistence.PreAssistantExists ||
		postAssistantExists != fixture.LazyPersistence.PostAssistantExists {
		t.Fatalf(
			"lazy persistence = pre %t, post %t; want pre %t, post %t",
			preAssistantExists,
			postAssistantExists,
			fixture.LazyPersistence.PreAssistantExists,
			fixture.LazyPersistence.PostAssistantExists,
		)
	}
	if diff := runner.ByteDiff(wantJSONL, gotJSONL); diff != "" {
		t.Fatalf("session write mismatch:\n%s", diff)
	}

	sessionPath := filepath.Join(t.TempDir(), "go-session.jsonl")
	if err := os.WriteFile(sessionPath, gotJSONL, 0o600); err != nil {
		t.Fatalf("write Go session: %v", err)
	}
	manager, err := session.Open(sessionPath, "")
	if err != nil {
		t.Fatalf("open Go session: %v", err)
	}
	projection, err := f6ProjectManager(manager)
	if err != nil {
		t.Fatalf("project Go session: %v", err)
	}
	gotProjection, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("marshal Go projection: %v", err)
	}
	f6CompareProjection(t, wantProjection, gotProjection, "Go")

	if os.Getenv("PI_GO_F6_TS_VERIFY") == "1" {
		tsProjection := f6VerifyWithUpstream(t, sessionPath)
		f6CompareProjection(t, wantProjection, tsProjection, "upstream TypeScript")
	}
}

func loadF6Fixture(t testing.TB) f6Fixture {
	t.Helper()
	var fixture f6Fixture
	runner.LoadJSON(t, "F6", "cases.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.ParseCases) != 6 || len(fixture.MigrationCases) != 6 || fixture.InvalidUTF8.InputBase64 == "" || len(fixture.InvalidUTF8.Expected) == 0 {
		t.Fatalf(
			"F6 fixture header = version %d, parse cases %d, migration cases %d",
			fixture.SchemaVersion,
			len(fixture.ParseCases),
			len(fixture.MigrationCases),
		)
	}
	if fixture.LazyPersistence.PreAssistantExists || !fixture.LazyPersistence.PostAssistantExists {
		t.Fatalf("unexpected F6 lazy-persistence fixture: %+v", fixture.LazyPersistence)
	}
	return fixture
}

func f6MarshalJSONArray(entries []*session.FileEntry) ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('[')
	for index, entry := range entries {
		if index > 0 {
			output.WriteByte(',')
		}
		encoded, err := entry.MarshalJSON()
		if err != nil {
			return nil, err
		}
		output.Write(encoded)
	}
	output.WriteByte(']')
	return output.Bytes(), nil
}

func f6MarshalJSONL(entries []*session.FileEntry) ([]byte, error) {
	var output bytes.Buffer
	for _, entry := range entries {
		encoded, err := entry.MarshalJSON()
		if err != nil {
			return nil, err
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

func f6InputVersion(input string) int {
	entries := session.ParseSessionEntries(input)
	for _, entry := range entries {
		if entry.Header == nil {
			continue
		}
		if entry.Header.Version == nil {
			return 1
		}
		return *entry.Header.Version
	}
	return 1
}

func f6BuildWrittenSession(t testing.TB) ([]byte, bool, bool) {
	t.Helper()
	fixtureRoot := t.TempDir()
	projectDir := filepath.Join(fixtureRoot, "project")
	sessionDir := filepath.Join(fixtureRoot, "sessions")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("create fixture project: %v", err)
	}

	clockValue := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)
	clock := func() time.Time {
		clockValue = clockValue.Add(time.Second)
		return clockValue
	}
	nextID := 0
	manager, err := session.Create(
		projectDir,
		sessionDir,
		session.WithSessionID("session-fixed"),
		session.WithClock(clock),
		session.WithEntryIDGenerator(func() (string, error) {
			nextID++
			return fmt.Sprintf("%08x", nextID), nil
		}),
	)
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}

	userID, err := manager.AppendMessage(&ai.UserMessage{
		Content:   ai.NewUserText("hello <>&\u2028\u2029"),
		Timestamp: 1,
	})
	if err != nil {
		t.Fatalf("append user message: %v", err)
	}
	_, err = manager.AppendThinkingLevelChange("high")
	f6MustAppend(t, "thinking-level change", err)
	_, err = manager.AppendModelChange("openai", "gpt-test")
	f6MustAppend(t, "model change", err)
	_, err = manager.AppendCustomEntry("state-empty")
	f6MustAppend(t, "empty custom entry", err)
	_, err = manager.AppendCustomEntry("state-null", nil)
	f6MustAppend(t, "null custom entry", err)
	_, err = manager.AppendCustomMessageEntry(
		"injected",
		[]any{
			&ai.TextContent{Text: "custom"},
			&ai.ImageContent{Data: "AA==", MimeType: "image/png"},
		},
		false,
	)
	f6MustAppend(t, "custom message", err)
	_, err = manager.AppendSessionInfo("  line one\r\nline two  ")
	f6MustAppend(t, "session info", err)
	_, err = manager.AppendSessionInfo("\ufeff\u0085edge\u0085\ufeff")
	f6MustAppend(t, "ECMAScript whitespace session info", err)

	preAssistantExists := f6PathExists(t, manager.GetSessionFile())
	_, err = manager.AppendMessage(&ai.AssistantMessage{
		Content:    ai.AssistantContent{&ai.TextContent{Text: "answer"}},
		API:        ai.APIOpenAIResponses,
		Provider:   "openai",
		Model:      "gpt-test",
		Usage:      ai.Usage{Input: 1, Output: 2, TotalTokens: 3, Cost: ai.Cost{}},
		StopReason: ai.StopReasonStop,
		Timestamp:  2,
	})
	f6MustAppend(t, "assistant message", err)
	postAssistantExists := f6PathExists(t, manager.GetSessionFile())

	fromHook := false
	_, err = manager.AppendCompaction(
		"compact",
		userID,
		99,
		session.OptionalEntryFields{
			Details:    map[string]any{"files": []string{"a.go"}},
			HasDetails: true,
			FromHook:   &fromHook,
		},
	)
	f6MustAppend(t, "compaction", err)
	_, err = manager.BranchWithSummary(&userID, "alternate branch")
	f6MustAppend(t, "branch summary", err)
	checkpoint := "checkpoint"
	_, err = manager.AppendLabelChange(userID, &checkpoint)
	f6MustAppend(t, "set label", err)
	_, err = manager.AppendLabelChange(userID, nil)
	f6MustAppend(t, "clear label", err)

	jsonl, err := manager.JSONL()
	if err != nil {
		t.Fatalf("marshal manager JSONL: %v", err)
	}
	jsonl = bytes.ReplaceAll(jsonl, []byte(filepath.ToSlash(projectDir)), []byte("/fixture/project"))
	return jsonl, preAssistantExists, postAssistantExists
}

func f6MustAppend(t testing.TB, operation string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("append %s: %v", operation, err)
	}
}

func f6PathExists(t testing.TB, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("stat session path: %v", err)
	return false
}

func f6ProjectManager(manager *session.SessionManager) (f6Projection, error) {
	projection := f6Projection{
		EntryTypes: make([]string, 0),
		BranchIDs:  make([]string, 0),
		Entries:    make([]json.RawMessage, 0),
	}
	if header := manager.GetHeader(); header != nil {
		encoded, err := header.MarshalJSON()
		if err != nil {
			return f6Projection{}, err
		}
		projection.Header = encoded
	}
	for _, entry := range manager.GetEntries() {
		encoded, err := entry.MarshalJSON()
		if err != nil {
			return f6Projection{}, err
		}
		projection.EntryTypes = append(projection.EntryTypes, entry.Type)
		projection.Entries = append(projection.Entries, encoded)
	}
	projection.LeafID = manager.GetLeafID()
	projection.SessionName = manager.GetSessionName()
	for _, entry := range manager.GetBranch() {
		projection.BranchIDs = append(projection.BranchIDs, entry.ID)
	}
	return projection, nil
}

func f6CompareProjection(t testing.TB, want, got []byte, source string) {
	t.Helper()
	wantCanonical, err := runner.CanonicalJSON(want)
	if err != nil {
		t.Fatalf("canonicalize projection fixture: %v", err)
	}
	gotCanonical, err := runner.CanonicalJSON(got)
	if err != nil {
		t.Fatalf("canonicalize %s projection: %v", source, err)
	}
	if diff := runner.ByteDiff(wantCanonical, gotCanonical); diff != "" {
		t.Fatalf("%s projection mismatch:\n%s", source, diff)
	}
}

func f6VerifyWithUpstream(t testing.TB, sessionPath string) []byte {
	t.Helper()
	fixtureRoot := runner.FixtureRoot()
	repoRoot := filepath.Clean(filepath.Join(fixtureRoot, "..", ".."))
	upstreamRoot := filepath.Join(repoRoot, ".upstream")
	script := filepath.Join(repoRoot, "conformance", "extract", "f6-verify.ts")
	command := exec.Command("node", "--import", "tsx", script, sessionPath)
	command.Dir = upstreamRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("verify Go session with upstream TypeScript: %v\n%s", err, output)
	}
	return output
}
