package runner_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/session/exporthtml"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type f6WP320Fixture struct {
	SchemaVersion int `json:"schemaVersion"`
	Tree          struct {
		Before      json.RawMessage `json:"before"`
		Branched    json.RawMessage `json:"branched"`
		Persistence struct {
			UserOnly         json.RawMessage `json:"userOnly"`
			AssistantPresent json.RawMessage `json:"assistantPresent"`
		} `json:"persistence"`
	} `json:"tree"`
	Fork struct {
		Source   string `json:"source"`
		Expected string `json:"expected"`
		Errors   struct {
			InvalidID struct {
				Message string  `json:"message"`
				Code    *string `json:"code"`
			} `json:"invalidID"`
			EmptySource struct {
				Message string  `json:"message"`
				Code    *string `json:"code"`
			} `json:"emptySource"`
			Collision struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"collision"`
			CollisionTolerance string `json:"collisionTolerance"`
		} `json:"errors"`
	} `json:"fork"`
	List struct {
		Files                  map[string]string `json:"files"`
		ProjectA               string            `json:"projectA"`
		Current                json.RawMessage   `json:"current"`
		All                    json.RawMessage   `json:"all"`
		CurrentProgress        json.RawMessage   `json:"currentProgress"`
		AllProgress            json.RawMessage   `json:"allProgress"`
		InvalidContentRejected bool              `json:"invalidContentRejected"`
	} `json:"list"`
	Export struct {
		Input             string            `json:"input"`
		SessionDataBase64 string            `json:"sessionDataBase64"`
		SessionDataJSON   string            `json:"sessionDataJSON"`
		AssetHashes       map[string]string `json:"assetHashes"`
		HTMLSHA256        string            `json:"htmlSha256"`
		HTMLBytes         int               `json:"htmlBytes"`
		PlaceholderCounts map[string]int    `json:"placeholderCounts"`
		SelfContained     bool              `json:"selfContained"`
		RawPayloadExposed bool              `json:"rawPayloadExposed"`
		SecurityMarkers   map[string]bool   `json:"securityMarkers"`
		WhitespaceMarkers map[string]bool   `json:"whitespaceMarkers"`
		ThemeMarkers      map[string]bool   `json:"themeMarkers"`
		DOMProjection     json.RawMessage   `json:"domProjection"`
		DOMTolerances     []string          `json:"domTolerances"`
	} `json:"export"`
}

func TestF6CreateBranchedSessionMatchesUpstream(t *testing.T) {
	fixture := loadF6WP320Fixture(t)
	clockValue := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Millisecond)
	clock := func() time.Time {
		clockValue = clockValue.Add(time.Millisecond)
		return clockValue
	}
	nextID := 0
	manager, err := session.InMemory(
		"/fixture/project",
		session.WithSessionID("tree-source"),
		session.WithClock(clock),
		session.WithSessionIDGenerator(func(time.Time) (string, error) { return "tree-branched", nil }),
		session.WithEntryIDGenerator(func() (string, error) {
			nextID++
			return fmt.Sprintf("%08x", nextID), nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	root, err := manager.AppendMessage(map[string]any{"role": "user", "content": "root", "timestamp": 1})
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := manager.AppendMessage(f6WP320Assistant("answer", 2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(map[string]any{"role": "user", "content": "abandoned", "timestamp": 3}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Branch(assistant); err != nil {
		t.Fatal(err)
	}
	alternate, err := manager.AppendMessage(map[string]any{"role": "user", "content": "alternate", "timestamp": 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		target string
		label  *string
	}{
		{root, f6String("root-first")},
		{alternate, f6String("alternate-first")},
		{root, nil},
		{root, f6String("root-readded")},
		{alternate, f6String("alternate-updated")},
	} {
		if _, err := manager.AppendLabelChange(change.target, change.label); err != nil {
			t.Fatal(err)
		}
	}

	labelTimes := f6WP320LabelTimes(manager.GetEntries())
	f6CompareCanonical(t, fixture.Tree.Before, f6WP320TreeProjection(t, manager, "tree-source", labelTimes), "tree before extraction")
	if _, err := manager.CreateBranchedSession(alternate); err != nil {
		t.Fatal(err)
	}
	f6CompareCanonical(t, fixture.Tree.Branched, f6WP320TreeProjection(t, manager, "tree-branched", labelTimes), "tree after extraction")
}

func TestF6CreateBranchedSessionPersistenceMatchesUpstream(t *testing.T) {
	fixture := loadF6WP320Fixture(t)
	root := t.TempDir()
	userOnly := f6WP320BranchPersistence(t, filepath.Join(root, "user"), false)
	assistantPresent := f6WP320BranchPersistence(t, filepath.Join(root, "assistant"), true)
	f6CompareCanonical(t, fixture.Tree.Persistence.UserOnly, userOnly, "user-only branch persistence")
	f6CompareCanonical(t, fixture.Tree.Persistence.AssistantPresent, assistantPresent, "assistant-present branch persistence")
}

func TestF6ForkFromMatchesUpstream(t *testing.T) {
	fixture := loadF6WP320Fixture(t)
	root := t.TempDir()
	sourceCWD := filepath.Join(root, "source")
	targetCWD := filepath.Join(root, "target")
	sessionDir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(targetCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, "source.jsonl")
	source := strings.ReplaceAll(fixture.Fork.Source, "/fixture/source", filepath.ToSlash(sourceCWD))
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2025, 1, 2, 3, 4, 5, 6*int(time.Millisecond), time.UTC)
	forked, err := session.ForkFrom(
		sourcePath, targetCWD, sessionDir,
		session.WithSessionID("fork-session"),
		session.WithClock(func() time.Time { return fixed }),
	)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := forked.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	actual = bytes.ReplaceAll(actual, []byte("2025-01-02T03:04:05.006Z"), []byte("2025-01-01T00:00:00.000Z"))
	actual = bytes.ReplaceAll(actual, []byte(filepath.ToSlash(targetCWD)), []byte("/fixture/target"))
	actual = bytes.ReplaceAll(actual, []byte(filepath.ToSlash(sourcePath)), []byte("/fixture/source.jsonl"))
	if diff := runner.ByteDiff([]byte(fixture.Fork.Expected), actual); diff != "" {
		t.Fatalf("forked JSONL mismatch:\n%s", diff)
	}

	_, err = session.ForkFrom(sourcePath, targetCWD, sessionDir, session.WithSessionID(".invalid"))
	if err == nil || err.Error() != fixture.Fork.Errors.InvalidID.Message {
		t.Fatalf("invalid-id error = %v, want %q", err, fixture.Fork.Errors.InvalidID.Message)
	}
	emptyPath := filepath.Join(root, "empty.jsonl")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = session.ForkFrom(emptyPath, targetCWD, sessionDir, session.WithSessionID("empty-source"))
	wantEmpty := strings.ReplaceAll(fixture.Fork.Errors.EmptySource.Message, "<tmp>", filepath.ToSlash(root))
	if err == nil || filepath.ToSlash(err.Error()) != wantEmpty {
		t.Fatalf("empty-source error = %v, want %q", err, wantEmpty)
	}
	_, err = session.ForkFrom(
		sourcePath, targetCWD, sessionDir,
		session.WithSessionID("fork-session"),
		session.WithClock(func() time.Time { return fixed }),
	)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("collision error = %v, want %s classification", err, fixture.Fork.Errors.Collision.Code)
	}
	wantBasename := "2025-01-02T03-04-05-006Z_fork-session.jsonl"
	wantUpstreamCollision := "EEXIST: file already exists, open '<tmp>/sessions/" + wantBasename + "'"
	if fixture.Fork.Errors.Collision.Message != wantUpstreamCollision {
		t.Fatalf("upstream collision message = %q, want %q", fixture.Fork.Errors.Collision.Message, wantUpstreamCollision)
	}
	if !strings.Contains(filepath.ToSlash(err.Error()), wantBasename) || fixture.Fork.Errors.CollisionTolerance == "" {
		t.Fatalf("collision error = %v; tolerance = %q", err, fixture.Fork.Errors.CollisionTolerance)
	}
}

func TestF6SessionListingMatchesUpstream(t *testing.T) {
	fixture := loadF6WP320Fixture(t)
	root := t.TempDir()
	flatDir := filepath.Join(root, "flat")
	if err := os.MkdirAll(flatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range fixture.List.Files {
		materialized := strings.ReplaceAll(contents, "<tmp>", filepath.ToSlash(root))
		file := filepath.Join(flatDir, name)
		if err := os.WriteFile(file, []byte(materialized), 0o600); err != nil {
			t.Fatal(err)
		}
		mtime := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(file, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(flatDir, "ignored.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectA := strings.ReplaceAll(fixture.List.ProjectA, "<tmp>", filepath.ToSlash(root))
	var currentProgress, allProgress []map[string]int
	options := []session.Option{session.WithAgentDir(filepath.Join(root, "agent"))}
	current := session.List(projectA, flatDir, func(loaded, total int) {
		currentProgress = append(currentProgress, map[string]int{"loaded": loaded, "total": total})
	}, options...)
	all := session.ListAll(flatDir, func(loaded, total int) {
		allProgress = append(allProgress, map[string]int{"loaded": loaded, "total": total})
	}, options...)

	currentProjection := f6WP320SessionInfoProjection(current, root)
	allProjection := f6WP320SessionInfoProjection(all, root)
	f6CompareCanonical(t, fixture.List.Current, currentProjection, "current-project session list")
	f6CompareCanonical(t, fixture.List.All, allProjection, "all session list")
	f6CompareCanonical(t, fixture.List.CurrentProgress, currentProgress, "current-project list progress")
	f6CompareCanonical(t, fixture.List.AllProgress, allProgress, "all-list progress")
	invalidPresent := false
	for _, info := range all {
		invalidPresent = invalidPresent || info.ID == "invalid-content"
	}
	if got := !invalidPresent; got != fixture.List.InvalidContentRejected {
		t.Fatalf("invalid-content rejected = %t, want %t", got, fixture.List.InvalidContentRejected)
	}
}

func TestF6HTMLExportMatchesUpstreamBytesAndStructure(t *testing.T) {
	fixture := loadF6WP320Fixture(t)
	root := t.TempDir()
	inputPath := filepath.Join(root, "input.jsonl")
	outputPath := filepath.Join(root, "output.html")
	if err := os.WriteFile(inputPath, []byte(fixture.Export.Input), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := exporthtml.ExportFromFile(inputPath, exporthtml.Options{OutputPath: outputPath, ThemeName: "dark"}); err != nil {
		t.Fatal(err)
	}
	htmlBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	if len(htmlBytes) != fixture.Export.HTMLBytes || f6SHA256(htmlBytes) != fixture.Export.HTMLSHA256 {
		t.Fatalf("HTML bytes/hash = %d/%s, want %d/%s", len(htmlBytes), f6SHA256(htmlBytes), fixture.Export.HTMLBytes, fixture.Export.HTMLSHA256)
	}
	sessionMatch := f6WP320SessionDataPattern.FindStringSubmatch(html)
	if len(sessionMatch) != 2 {
		t.Fatal("HTML export did not contain session data")
	}
	decoded, err := base64.StdEncoding.DecodeString(sessionMatch[1])
	if err != nil {
		t.Fatal(err)
	}
	if sessionMatch[1] != fixture.Export.SessionDataBase64 || string(decoded) != fixture.Export.SessionDataJSON {
		t.Fatal("embedded session payload differs from upstream")
	}

	actual := f6WP320HTMLProjection(t, html)
	if !equalStringMap(actual.AssetHashes, fixture.Export.AssetHashes) {
		t.Fatalf("asset hashes = %#v, want %#v", actual.AssetHashes, fixture.Export.AssetHashes)
	}
	if !equalIntMap(actual.PlaceholderCounts, fixture.Export.PlaceholderCounts) {
		t.Fatalf("placeholder counts = %#v, want %#v", actual.PlaceholderCounts, fixture.Export.PlaceholderCounts)
	}
	if actual.SelfContained != fixture.Export.SelfContained || actual.RawPayloadExposed != fixture.Export.RawPayloadExposed {
		t.Fatalf("self-contained/raw payload = %t/%t, want %t/%t", actual.SelfContained, actual.RawPayloadExposed, fixture.Export.SelfContained, fixture.Export.RawPayloadExposed)
	}
	if !equalBoolMap(actual.SecurityMarkers, fixture.Export.SecurityMarkers) ||
		!equalBoolMap(actual.WhitespaceMarkers, fixture.Export.WhitespaceMarkers) ||
		!equalBoolMap(actual.ThemeMarkers, fixture.Export.ThemeMarkers) {
		t.Fatalf("HTML marker projection differs from upstream")
	}
	f6CompareCanonical(t, fixture.Export.DOMProjection, actual.DOMProjection, "HTML DOM projection")
	if len(fixture.Export.DOMTolerances) != 3 {
		t.Fatalf("DOM tolerances are not documented: %#v", fixture.Export.DOMTolerances)
	}
}

func loadF6WP320Fixture(t testing.TB) f6WP320Fixture {
	t.Helper()
	var fixture f6WP320Fixture
	runner.LoadJSON(t, "F6", "tree-export.json", &fixture)
	if fixture.SchemaVersion != 2 || len(fixture.Tree.Before) == 0 || fixture.Fork.Source == "" ||
		len(fixture.List.Files) != 5 || fixture.Export.HTMLSHA256 == "" || len(fixture.Export.AssetHashes) != 5 {
		t.Fatalf("invalid WP-320 F6 fixture: %+v", fixture)
	}
	return fixture
}

func f6WP320Assistant(text string, timestamp int) map[string]any {
	return map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}},
		"api": "openai-responses", "provider": "openai", "model": "gpt-test",
		"usage": map[string]any{
			"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2,
			"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0},
		},
		"stopReason": "stop", "timestamp": timestamp,
	}
}

func f6String(value string) *string { return &value }

func f6WP320LabelTimes(entries []session.SessionEntry) map[string]string {
	result := make(map[string]string)
	next := 0
	for _, entry := range entries {
		if entry.Type != "label" {
			continue
		}
		next++
		result[entry.Timestamp] = fmt.Sprintf("label-time-%d", next)
	}
	return result
}

func f6WP320TreeProjection(
	t testing.TB,
	manager *session.SessionManager,
	sessionID string,
	labelTimes map[string]string,
) map[string]any {
	t.Helper()
	entries := manager.GetEntries()
	idMap := make(map[string]string, len(entries))
	for index, entry := range entries {
		idMap[entry.ID] = fmt.Sprintf("%08x", index+1)
	}
	normalizedEntries := make([]map[string]any, 0, len(entries))
	for index, entry := range entries {
		normalizedEntries = append(normalizedEntries, f6WP320NormalizeEntry(t, entry, idMap, labelTimes, index))
	}
	headerBytes, err := manager.GetHeader().MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatal(err)
	}
	header["id"] = sessionID
	header["timestamp"] = "session-time"
	header["cwd"] = "/fixture/project"
	delete(header, "parentSession")
	branch := manager.GetBranch()
	branchIDs := make([]string, 0, len(branch))
	for _, entry := range branch {
		branchIDs = append(branchIDs, idMap[entry.ID])
	}
	var leafID any
	if leaf := manager.GetLeafID(); leaf != nil {
		leafID = idMap[*leaf]
	}
	tree := manager.GetTree()
	normalizedTree := make([]map[string]any, 0, len(tree))
	for _, node := range tree {
		normalizedTree = append(normalizedTree, f6WP320NormalizeNode(t, node, entries, idMap, labelTimes))
	}
	return map[string]any{
		"header": header, "leafId": leafID, "branchIds": branchIDs,
		"entries": normalizedEntries, "tree": normalizedTree,
	}
}

func f6WP320NormalizeEntry(
	t testing.TB,
	entry session.SessionEntry,
	idMap, labelTimes map[string]string,
	index int,
) map[string]any {
	t.Helper()
	encoded, err := entry.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "parentId", "targetId", "fromId", "firstKeptEntryId"} {
		if value, ok := normalized[key].(string); ok {
			normalized[key] = idMap[value]
		}
	}
	if entry.Type == "label" {
		if token, ok := labelTimes[entry.Timestamp]; ok {
			normalized["timestamp"] = token
		} else {
			normalized["timestamp"] = "unmapped-label-timestamp"
		}
	} else {
		normalized["timestamp"] = fmt.Sprintf("entry-time-%d", index+1)
	}
	return normalized
}

func f6WP320NormalizeNode(
	t testing.TB,
	node *session.SessionTreeNode,
	entries []session.SessionEntry,
	idMap, labelTimes map[string]string,
) map[string]any {
	t.Helper()
	index := -1
	for candidate := range entries {
		if entries[candidate].ID == node.Entry.ID {
			index = candidate
			break
		}
	}
	children := make([]map[string]any, 0, len(node.Children))
	for _, child := range node.Children {
		children = append(children, f6WP320NormalizeNode(t, child, entries, idMap, labelTimes))
	}
	result := map[string]any{
		"entry":    f6WP320NormalizeEntry(t, node.Entry, idMap, labelTimes, index),
		"children": children,
	}
	if node.Label != nil {
		result["label"] = *node.Label
	}
	if node.LabelTimestamp != nil {
		if token, ok := labelTimes[*node.LabelTimestamp]; ok {
			result["labelTimestamp"] = token
		} else {
			result["labelTimestamp"] = "unmapped-label-timestamp"
		}
	}
	return result
}

func f6WP320BranchPersistence(t testing.TB, root string, includeAssistant bool) map[string]any {
	t.Helper()
	project := filepath.Join(root, "project")
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	manager, err := session.Create(project, sessions, session.WithSessionID(map[bool]string{false: "user-source", true: "assistant-source"}[includeAssistant]))
	if err != nil {
		t.Fatal(err)
	}
	user, err := manager.AppendMessage(map[string]any{"role": "user", "content": "root", "timestamp": 1})
	if err != nil {
		t.Fatal(err)
	}
	leaf := user
	if includeAssistant {
		leaf, err = manager.AppendMessage(f6WP320Assistant("answer", 2))
		if err != nil {
			t.Fatal(err)
		}
	}
	label := "checkpoint"
	if _, err := manager.AppendLabelChange(user, &label); err != nil {
		t.Fatal(err)
	}
	originalFile := manager.GetSessionFile()
	originalExists := fileExists(originalFile)
	branchedFile, err := manager.CreateBranchedSession(leaf)
	if err != nil {
		t.Fatal(err)
	}
	branchExists := fileExists(branchedFile)
	afterAssistantExists := branchExists
	if !includeAssistant {
		if _, err := manager.AppendMessage(f6WP320Assistant("first assistant after branch", 3)); err != nil {
			t.Fatal(err)
		}
		afterAssistantExists = fileExists(branchedFile)
	}
	var parsed []map[string]any
	if afterAssistantExists {
		raw, err := os.ReadFile(branchedFile)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatal(err)
			}
			parsed = append(parsed, entry)
		}
	}
	headerCount := 0
	entryTypes := make([]any, 0, len(parsed))
	retained := make([]map[string]any, 0, len(parsed))
	for _, entry := range parsed {
		entryTypes = append(entryTypes, entry["type"])
		if entry["type"] == "session" {
			headerCount++
		} else {
			retained = append(retained, entry)
		}
	}
	parentChainValid := true
	for index, entry := range retained {
		var expected any
		if index > 0 {
			expected = retained[index-1]["id"]
		}
		if entry["parentId"] != expected {
			parentChainValid = false
		}
	}
	return map[string]any{
		"originalExists": originalExists, "branchExists": branchExists,
		"afterAssistantExists": afterAssistantExists, "headerCount": headerCount,
		"entryTypes": entryTypes, "parentChainValid": parentChainValid,
	}
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func f6WP320SessionInfoProjection(infos []session.SessionInfo, root string) []map[string]any {
	result := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		var name, parent any
		if info.Name != nil {
			name = *info.Name
		}
		if info.ParentSessionPath != nil {
			parent = strings.ReplaceAll(filepath.ToSlash(*info.ParentSessionPath), filepath.ToSlash(root), "<tmp>")
		}
		result = append(result, map[string]any{
			"path": filepath.Base(info.Path), "id": info.ID,
			"cwd":  strings.ReplaceAll(filepath.ToSlash(info.CWD), filepath.ToSlash(root), "<tmp>"),
			"name": name, "parentSessionPath": parent,
			"created": f6JSDate(info.Created), "modified": f6JSDate(info.Modified),
			"messageCount": info.MessageCount, "firstMessage": info.FirstMessage,
			"allMessagesText": info.AllMessagesText,
		})
	}
	return result
}

func f6JSDate(value time.Time) string {
	if value.IsZero() {
		return "Invalid Date"
	}
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

type f6HTMLProjection struct {
	AssetHashes       map[string]string
	PlaceholderCounts map[string]int
	SelfContained     bool
	RawPayloadExposed bool
	SecurityMarkers   map[string]bool
	WhitespaceMarkers map[string]bool
	ThemeMarkers      map[string]bool
	DOMProjection     any
}

var (
	f6WP320SessionDataPattern = regexp.MustCompile(`<script id="session-data" type="application/json">([^<]+)</script>`)
	f6WP320StyleBodyPattern   = regexp.MustCompile(`(?is)(<style[^>]*>).*?(</style>)`)
	f6WP320ScriptBodyPattern  = regexp.MustCompile(`(?is)(<script[^>]*>).*?(</script>)`)
	f6WP320TagPattern         = regexp.MustCompile(`<(\/)?([A-Za-z][A-Za-z0-9-]*)([^>]*)>`)
	f6WP320AttributePattern   = regexp.MustCompile(`([A-Za-z_:][A-Za-z0-9_:.-]*)=(?:"([^"]*)"|'([^']*)')`)
	f6WP320ExternalPattern    = regexp.MustCompile(`(?i)<script[^>]+src=|<link[^>]+href=`)
)

func f6WP320HTMLProjection(t testing.TB, html string) f6HTMLProjection {
	t.Helper()
	repoRoot := filepath.Clean(filepath.Join(runner.FixtureRoot(), "..", ".."))
	assetRoot := filepath.Join(repoRoot, "codingagent", "session", "exporthtml", "assets")
	assetFiles := map[string]string{
		"templateHTML": "template.html", "templateCSS": "template.css", "templateJS": "template.js",
		"markedJS": filepath.Join("vendor", "marked.min.js"), "highlightJS": filepath.Join("vendor", "highlight.min.js"),
	}
	assetHashes := make(map[string]string, len(assetFiles))
	for name, relative := range assetFiles {
		contents, err := os.ReadFile(filepath.Join(assetRoot, relative))
		if err != nil {
			t.Fatal(err)
		}
		assetHashes[name] = f6SHA256(contents)
	}
	css, err := os.ReadFile(filepath.Join(assetRoot, "template.css"))
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := os.ReadFile(filepath.Join(assetRoot, "template.js"))
	if err != nil {
		t.Fatal(err)
	}
	placeholders := []string{"CSS", "JS", "SESSION_DATA", "MARKED_JS", "HIGHLIGHT_JS", "THEME_VARS", "BODY_BG", "CONTAINER_BG", "INFO_BG"}
	placeholderCounts := make(map[string]int, len(placeholders))
	for _, name := range placeholders {
		placeholderCounts[name] = strings.Count(html, "{{"+name+"}}")
	}
	securityMarkers := make(map[string]bool)
	for _, marker := range []string{
		"sanitizeMarkdownUrl(token.href)", "escapeHtml(href)", "replace(/[\\x00-\\x1f\\x7f]/g, '')",
		"parseSkillBlock", "safeMarkedParse(skillBlock.content)", "escapeHtml(img.mimeType",
		"escapeHtml(img.data || '')", "escapeHtml(entry.id)",
	} {
		securityMarkers[marker] = bytes.Contains(renderer, []byte(marker))
	}
	whitespaceMarkers := map[string]bool{
		"outputLinePreWrap":       regexp.MustCompile(`(?s)\.output-preview > div:not\(\.expand-hint\),\s*\.output-full > div:not\(\.expand-hint\) \{.*?white-space:\s*pre-wrap;`).Match(css),
		"ansiLinePre":             regexp.MustCompile(`(?s)\.ansi-line\s*\{.*?white-space:\s*pre;`).Match(css),
		"containerDoesNotPreWrap": !regexp.MustCompile(`(?s)\.output-preview,\s*\.output-full\s*\{.*?white-space:\s*pre-wrap;`).Match(css),
	}
	withoutBodies := f6WP320StyleBodyPattern.ReplaceAllString(html, "$1$2")
	withoutBodies = f6WP320ScriptBodyPattern.ReplaceAllString(withoutBodies, "$1$2")
	tagMatches := f6WP320TagPattern.FindAllStringSubmatch(withoutBodies, -1)
	tags := make([]map[string]any, 0, len(tagMatches))
	allowed := map[string]bool{"id": true, "class": true, "type": true, "role": true, "aria-orientation": true, "aria-label": true}
	for _, tagMatch := range tagMatches {
		attrs := make(map[string]string)
		for _, attrMatch := range f6WP320AttributePattern.FindAllStringSubmatch(tagMatch[3], -1) {
			if !allowed[attrMatch[1]] {
				continue
			}
			value := attrMatch[2]
			if value == "" {
				value = attrMatch[3]
			}
			attrs[attrMatch[1]] = value
		}
		tags = append(tags, map[string]any{"closing": tagMatch[1] != "", "name": strings.ToLower(tagMatch[2]), "attrs": attrs})
	}
	return f6HTMLProjection{
		AssetHashes: assetHashes, PlaceholderCounts: placeholderCounts,
		SelfContained:     !f6WP320ExternalPattern.MatchString(html),
		RawPayloadExposed: strings.Contains(html, `<script>alert("xss-fixture")</script>`),
		SecurityMarkers:   securityMarkers, WhitespaceMarkers: whitespaceMarkers,
		ThemeMarkers: map[string]bool{
			"variablesResolved":           !strings.Contains(html, "{{THEME_VARS}}"),
			"bodyBackgroundResolved":      !strings.Contains(html, "{{BODY_BG}}"),
			"exportPageBackgroundPresent": strings.Contains(html, "--exportPageBg:"),
		},
		DOMProjection: tags,
	}
}

func f6SHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func f6CompareCanonical(t testing.TB, want json.RawMessage, got any, label string) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical, err := runner.CanonicalJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	gotCanonical, err := runner.CanonicalJSON(gotJSON)
	if err != nil {
		t.Fatal(err)
	}
	if diff := runner.ByteDiff(wantCanonical, gotCanonical); diff != "" {
		t.Fatalf("%s mismatch:\n%s", label, diff)
	}
}

func equalStringMap(left, right map[string]string) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func equalIntMap(left, right map[string]int) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func equalBoolMap(left, right map[string]bool) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}
