package exporthtml

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/session"
)

func TestPreRenderCustomToolsMatchesUpstreamSelectionAndMerge(t *testing.T) {
	t.Parallel()
	empty := ""
	renderer := &fakeToolHTMLRenderer{
		calls: map[string]*string{
			"custom-1": stringPointer("<call>"),
			"empty-1":  &empty,
			"cross-1":  stringPointer("<cross-call>"),
			"nil-1":    stringPointer("<nil-result-call>"),
		},
		results: map[string]*ToolHTMLRenderResult{
			"custom-1": {Collapsed: stringPointer("<collapsed>"), Expanded: stringPointer("<expanded>")},
			"orphan-1": {Collapsed: stringPointer("<fallback>")},
			"cross-1":  {Expanded: stringPointer("<cross-expanded>")},
			"nil-1":    nil,
		},
	}
	entries := []session.SessionEntry{
		messageEntry(`{"role":"assistant","content":[{"type":"toolCall","id":"custom-1","name":"custom","arguments":{"count":2}},{"type":"toolCall","id":"builtin-1","name":"bash","arguments":{"command":"true"}},{"type":"toolCall","id":"empty-1","name":"empty","arguments":{}}]}`),
		messageEntry(`{"role":"toolResult","toolCallId":"custom-1","toolName":"custom","content":[{"type":"text","text":"result"}],"details":{"source":"test"},"isError":true}`),
		messageEntry(`{"role":"toolResult","toolCallId":"orphan-1","toolName":"fallback","content":[],"isError":false}`),
		messageEntry(`{"role":"toolResult","toolCallId":"builtin-1","toolName":"bash","content":[]}`),
		messageEntry(`{"role":"assistant","content":[{"type":"toolCall","id":"cross-1","name":"custom","arguments":{}},{"type":"toolCall","id":"nil-1","name":"custom","arguments":{}}]}`),
		messageEntry(`{"role":"toolResult","toolCallId":"cross-1","toolName":"read","content":[]}`),
		messageEntry(`{"role":"toolResult","toolCallId":"nil-1","toolName":"custom","content":[]}`),
	}

	got := preRenderCustomTools(entries, renderer)
	want := map[string]RenderedToolHTML{
		"custom-1": {CallHTML: stringPointer("<call>"), ResultHTMLCollapsed: stringPointer("<collapsed>"), ResultHTMLExpanded: stringPointer("<expanded>")},
		"orphan-1": {ResultHTMLCollapsed: stringPointer("<fallback>")},
		"cross-1":  {CallHTML: stringPointer("<cross-call>"), ResultHTMLExpanded: stringPointer("<cross-expanded>")},
		"nil-1":    {CallHTML: stringPointer("<nil-result-call>")},
	}
	if gotMap := renderedToolsMap(got); !reflect.DeepEqual(gotMap, want) {
		t.Fatalf("rendered tools = %#v, want %#v", got, want)
	}
	if gotOrder := renderedToolIDs(got); !reflect.DeepEqual(gotOrder, []string{"custom-1", "orphan-1", "cross-1", "nil-1"}) {
		t.Fatalf("rendered tool insertion order = %v", gotOrder)
	}
	if gotCalls := renderer.callIDs(); !reflect.DeepEqual(gotCalls, []string{"custom-1", "empty-1", "cross-1", "nil-1"}) {
		t.Fatalf("render calls = %v", gotCalls)
	}
	if gotResults := renderer.resultIDs(); !reflect.DeepEqual(gotResults, []string{"custom-1", "orphan-1", "cross-1", "nil-1"}) {
		t.Fatalf("render results = %v", gotResults)
	}
	firstResult := renderer.resultRecords[0]
	if firstResult.toolName != "custom" || !firstResult.isError {
		t.Fatalf("first result metadata = %+v", firstResult)
	}
	if !reflect.DeepEqual(firstResult.content, []any{map[string]any{"type": "text", "text": "result"}}) {
		t.Fatalf("decoded result content = %#v", firstResult.content)
	}
	if !reflect.DeepEqual(firstResult.details, map[string]any{"source": "test"}) {
		t.Fatalf("decoded result details = %#v", firstResult.details)
	}
	if !reflect.DeepEqual(renderer.callRecords[0].arguments, map[string]any{"count": float64(2)}) {
		t.Fatalf("decoded call arguments = %#v", renderer.callRecords[0].arguments)
	}
}

func TestPreRenderCustomToolsPreservesEmptyResultObjectAndOmitsEmptyMap(t *testing.T) {
	t.Parallel()
	emptyResult := &fakeToolHTMLRenderer{results: map[string]*ToolHTMLRenderResult{"orphan": {}}}
	got := preRenderCustomTools([]session.SessionEntry{
		messageEntry(`{"role":"toolResult","toolCallId":"orphan","toolName":"custom","content":[]}`),
	}, emptyResult)
	if gotMap := renderedToolsMap(got); !reflect.DeepEqual(gotMap, map[string]RenderedToolHTML{"orphan": {}}) {
		t.Fatalf("empty result object = %#v", got)
	}

	emptyCall := ""
	omitted := &fakeToolHTMLRenderer{calls: map[string]*string{"empty": &emptyCall}}
	got = preRenderCustomTools([]session.SessionEntry{
		messageEntry(`{"role":"assistant","content":[{"type":"toolCall","id":"builtin","name":"read","arguments":{}},{"type":"toolCall","id":"empty","name":"custom","arguments":{}}]}`),
		messageEntry(`{"role":"toolResult","toolCallId":"builtin","toolName":"read","content":[]}`),
	}, omitted)
	if got != nil {
		t.Fatalf("empty rendered map = %#v, want nil", got)
	}
}

func TestRenderedToolsPayloadPreservesPlainObjectInsertionOrder(t *testing.T) {
	t.Parallel()
	renderer := &fakeToolHTMLRenderer{calls: map[string]*string{
		"z-call": stringPointer("z"),
		"a-call": stringPointer("a"),
	}}
	entries := []session.SessionEntry{messageEntry(
		`{"role":"assistant","content":[{"type":"toolCall","id":"z-call","name":"custom","arguments":{}},{"type":"toolCall","id":"a-call","name":"custom","arguments":{}}]}`,
	)}
	html, err := generateHTML(sessionData{
		Header:  &session.SessionHeader{Type: "session", ID: "order", Timestamp: "2025-01-01T00:00:00.000Z", CWD: "/tmp"},
		Entries: entries, RenderedTools: preRenderCustomTools(entries, renderer),
	}, "dark", nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeHTMLSessionPayload(t, []byte(html))
	want := `"renderedTools":{"z-call":{"callHtml":"z"},"a-call":{"callHtml":"a"}}`
	if !strings.Contains(string(payload), want) {
		t.Fatalf("payload lost upstream insertion order:\n%s", payload)
	}
}

func TestLiveExportEmbedsRenderedToolsWithExactFieldNames(t *testing.T) {
	root := t.TempDir()
	manager, err := session.Create(root, filepath.Join(root, "sessions"), session.WithSessionID("render-session"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(map[string]any{
		"role": "assistant",
		"content": []any{map[string]any{
			"type": "toolCall", "id": "call-1", "name": "custom", "arguments": map[string]any{"value": 1},
		}},
		"api": "openai-responses", "provider": "openai", "model": "gpt-test",
		"usage": map[string]any{}, "stopReason": "stop", "timestamp": 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(map[string]any{
		"role": "toolResult", "toolCallId": "call-1", "toolName": "custom",
		"content": []any{map[string]any{"type": "text", "text": "done"}}, "isError": false, "timestamp": 2,
	}); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeToolHTMLRenderer{
		calls: map[string]*string{"call-1": stringPointer("<call>")},
		results: map[string]*ToolHTMLRenderResult{
			"call-1": {Collapsed: stringPointer("<collapsed>"), Expanded: stringPointer("<expanded>")},
		},
	}
	output := filepath.Join(root, "live.html")
	if _, err := ExportSession(manager, Options{OutputPath: output, ThemeName: "dark", ToolRenderer: renderer}); err != nil {
		t.Fatal(err)
	}
	payload := readSessionPayload(t, output)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if got, want := string(envelope["renderedTools"]), `{"call-1":{"callHtml":"<call>","resultHtmlCollapsed":"<collapsed>","resultHtmlExpanded":"<expanded>"}}`; got != want {
		t.Fatalf("renderedTools JSON = %s, want %s", got, want)
	}
}

func TestFileExportIgnoresLiveRendererAndState(t *testing.T) {
	renderer := &fakeToolHTMLRenderer{calls: map[string]*string{"call-1": stringPointer("<call>")}}
	root := t.TempDir()
	output := filepath.Join(root, "file.html")
	systemPrompt := "live only"
	if _, err := ExportFromFile(fixturePath(t), Options{
		OutputPath: output, ThemeName: "dark", SystemPrompt: &systemPrompt,
		Tools: json.RawMessage(`[{"name":"live"}]`), ToolRenderer: renderer,
	}); err != nil {
		t.Fatal(err)
	}
	if len(renderer.callRecords) != 0 || len(renderer.resultRecords) != 0 {
		t.Fatalf("file export invoked live renderer: calls=%v results=%v", renderer.callRecords, renderer.resultRecords)
	}
	payload := readSessionPayload(t, output)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"systemPrompt", "tools", "renderedTools"} {
		if _, ok := envelope[field]; ok {
			t.Errorf("file export retained live field %q", field)
		}
	}
}

type fakeToolHTMLRenderer struct {
	calls         map[string]*string
	results       map[string]*ToolHTMLRenderResult
	callRecords   []fakeCallRecord
	resultRecords []fakeResultRecord
}

type fakeCallRecord struct {
	toolCallID string
	toolName   string
	arguments  any
}

type fakeResultRecord struct {
	toolCallID string
	toolName   string
	content    any
	details    any
	isError    bool
}

func (renderer *fakeToolHTMLRenderer) RenderCall(toolCallID, toolName string, arguments any) *string {
	renderer.callRecords = append(renderer.callRecords, fakeCallRecord{toolCallID, toolName, arguments})
	return renderer.calls[toolCallID]
}

func (renderer *fakeToolHTMLRenderer) RenderResult(
	toolCallID, toolName string,
	content, details any,
	isError bool,
) *ToolHTMLRenderResult {
	renderer.resultRecords = append(renderer.resultRecords, fakeResultRecord{toolCallID, toolName, content, details, isError})
	return renderer.results[toolCallID]
}

func (renderer *fakeToolHTMLRenderer) callIDs() []string {
	result := make([]string, len(renderer.callRecords))
	for index, record := range renderer.callRecords {
		result[index] = record.toolCallID
	}
	return result
}

func (renderer *fakeToolHTMLRenderer) resultIDs() []string {
	result := make([]string, len(renderer.resultRecords))
	for index, record := range renderer.resultRecords {
		result[index] = record.toolCallID
	}
	return result
}

func messageEntry(message string) session.SessionEntry {
	return session.SessionEntry{Type: "message", Message: json.RawMessage(message)}
}

func stringPointer(value string) *string {
	return &value
}

func readSessionPayload(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return decodeHTMLSessionPayload(t, contents)
}

func decodeHTMLSessionPayload(t *testing.T, contents []byte) []byte {
	t.Helper()
	match := regexp.MustCompile(`<script id="session-data" type="application/json">([^<]+)</script>`).FindSubmatch(contents)
	if len(match) != 2 {
		t.Fatal("session data script was not found")
	}
	payload, err := base64.StdEncoding.DecodeString(string(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func renderedToolsMap(collection *renderedToolCollection) map[string]RenderedToolHTML {
	if collection == nil {
		return nil
	}
	result := make(map[string]RenderedToolHTML, collection.len())
	for _, record := range collection.records {
		result[record.id] = record.html
	}
	return result
}

func renderedToolIDs(collection *renderedToolCollection) []string {
	if collection == nil {
		return nil
	}
	result := make([]string, len(collection.records))
	for index, record := range collection.records {
		result[index] = record.id
	}
	return result
}
