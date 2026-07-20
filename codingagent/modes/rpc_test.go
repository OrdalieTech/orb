package modes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

func TestReadStrictJSONLinesMatchesUpstreamFraming(t *testing.T) {
	input := "{\"value\":\"a\u2028b\u2029c\"}\r\n{\"final\":true}"
	lines := make(chan []byte)
	errors := make(chan error, 1)
	go readStrictJSONLines(strings.NewReader(input), lines, errors)
	var got []string
	for line := range lines {
		got = append(got, string(line))
	}
	select {
	case err := <-errors:
		if err != nil {
			t.Fatal(err)
		}
	default:
	}
	want := []string{"{\"value\":\"a\u2028b\u2029c\"}", "{\"final\":true}"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("strict lines = %#v, want %#v", got, want)
	}
}

func TestJavaScriptParseErrorMatchesV8(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "Unexpected end of JSON input"},
		{"not-json", `Unexpected token 'o', "not-json" is not valid JSON`},
		{"{", "Expected property name or '}' in JSON at position 1 (line 1 column 2)"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			var value any
			err := json.Unmarshal([]byte(test.input), &value)
			if err == nil {
				t.Fatal("malformed JSON parsed successfully")
			}
			if got := javascriptParseError([]byte(test.input), err); got != test.want {
				t.Fatalf("parse error = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRPCResponsePreservesExplicitEmptyID(t *testing.T) {
	withID, err := ai.Marshal(rpcSuccess("", true, "get_state"))
	if err != nil {
		t.Fatal(err)
	}
	if string(withID) != `{"id":"","type":"response","command":"get_state","success":true}` {
		t.Fatalf("explicit empty ID response = %s", withID)
	}
	withoutID, err := ai.Marshal(rpcError("", false, "parse", "bad"))
	if err != nil {
		t.Fatal(err)
	}
	if string(withoutID) != `{"type":"response","command":"parse","success":false,"error":"bad"}` {
		t.Fatalf("absent ID response = %s", withoutID)
	}
}

func TestRPCSessionNameTrimMatchesECMAScript(t *testing.T) {
	if !isJSTrimSpace('\ufeff') || !isJSTrimSpace('\u00a0') || isJSTrimSpace('\u0085') {
		t.Fatal("RPC session-name whitespace set differs from ECMAScript trim")
	}
}

func TestRPCSlashCommandWireOrderAndOptionalEmptyValues(t *testing.T) {
	empty := ""
	encoded, err := ai.Marshal(RPCSlashCommand{
		Name: "skill:test", Description: &empty, Source: "skill",
		SourceInfo: RPCSourceInfo{Path: "test/SKILL.md", Source: "skills", Scope: "project", Origin: "top-level", BaseDir: &empty},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"skill:test","description":"","source":"skill","sourceInfo":{"path":"test/SKILL.md","source":"skills","scope":"project","origin":"top-level","baseDir":""}}`
	if string(encoded) != want {
		t.Fatalf("slash command = %s, want %s", encoded, want)
	}
}

func TestRPCExtensionUIOptionalFieldsPreserveEmptyValues(t *testing.T) {
	requests := make(chan RPCExtensionUIRequest, 3)
	ui := newRPCExtensionUI(func(value any) error {
		requests <- value.(RPCExtensionUIRequest)
		return nil
	})
	defer ui.close()

	timeout := int64(1000)
	placeholder := ""
	inputDone := make(chan struct{})
	go func() {
		_, _ = ui.Input(context.Background(), "Input", &placeholder, &timeout)
		close(inputDone)
	}()
	input := <-requests
	encoded, err := ai.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"placeholder":""`)) {
		t.Fatalf("input request = %s", encoded)
	}
	ui.HandleResponse(RPCExtensionUIResponse{ID: input.ID, Cancelled: true})
	<-inputDone

	zeroTimeout := int64(0)
	selectDone := make(chan struct{})
	go func() {
		_, _ = ui.Select(context.Background(), "Select", []string{}, &zeroTimeout)
		close(selectDone)
	}()
	selectRequest := <-requests
	encoded, err = ai.Marshal(selectRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"options":[]`)) || !bytes.Contains(encoded, []byte(`"timeout":0`)) {
		t.Fatalf("select request = %s", encoded)
	}
	ui.HandleResponse(RPCExtensionUIResponse{ID: selectRequest.ID, Cancelled: true})
	<-selectDone

	if err := ui.SetWidget("empty", []string{}, ""); err != nil {
		t.Fatal(err)
	}
	widget := <-requests
	encoded, err = ai.Marshal(widget)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"widgetLines":[]`)) || bytes.Contains(encoded, []byte("widgetPlacement")) {
		t.Fatalf("widget request = %s", encoded)
	}
}

func TestRPCExtensionUIDialogRoundTrip(t *testing.T) {
	requests := make(chan RPCExtensionUIRequest, 1)
	ui := newRPCExtensionUI(func(value any) error {
		request, ok := value.(RPCExtensionUIRequest)
		if !ok {
			t.Fatalf("UI output = %T", value)
		}
		requests <- request
		return nil
	})
	defer ui.close()

	result := make(chan *string, 1)
	timeout := int64(250)
	go func() {
		value, err := ui.Select(context.Background(), "Pick", []string{"a", "b"}, &timeout)
		if err != nil {
			t.Errorf("select: %v", err)
		}
		result <- value
	}()
	request := <-requests
	encoded, err := ai.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Type    string   `json:"type"`
		ID      string   `json:"id"`
		Method  string   `json:"method"`
		Title   string   `json:"title"`
		Options []string `json:"options"`
		Timeout int64    `json:"timeout"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Type != "extension_ui_request" || wire.ID == "" || wire.Method != "select" || wire.Title != "Pick" || wire.Timeout != 250 || len(wire.Options) != 2 {
		t.Fatalf("select request = %s", encoded)
	}
	selected := "b"
	ui.HandleResponse(RPCExtensionUIResponse{Type: "extension_ui_response", ID: wire.ID, Value: &selected})
	if value := <-result; value == nil || *value != selected {
		t.Fatalf("select result = %#v", value)
	}
}

func TestRPCExtensionUIAlreadyCancelledDoesNotEmit(t *testing.T) {
	requests := make(chan any, 1)
	ui := newRPCExtensionUI(func(value any) error {
		requests <- value
		return nil
	})
	defer ui.close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if value, err := ui.Select(ctx, "Pick", []string{"a"}, nil); err != nil || value != nil {
		t.Fatalf("select = (%#v, %v)", value, err)
	}
	select {
	case request := <-requests:
		t.Fatalf("cancelled dialog emitted %#v", request)
	default:
	}
}

func TestRPCExtensionUIFireAndForgetWireOrder(t *testing.T) {
	var output bytes.Buffer
	ui := newRPCExtensionUI(func(value any) error {
		encoded, err := ai.Marshal(value)
		if err != nil {
			return err
		}
		output.Write(encoded)
		return nil
	})
	defer ui.close()

	if err := ui.SetTitle(""); err != nil {
		t.Fatal(err)
	}
	var request struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(output.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	want := `{"type":"extension_ui_request","id":"` + request.ID + `","method":"setTitle","title":""}`
	if output.String() != want {
		t.Fatalf("setTitle wire = %q, want %q", output.String(), want)
	}
}

func TestRPCPromptPreflightFailureEmitsExactlyOneResponse(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("preflight"))
	if err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}}))
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := RunRPCMode(context.Background(), &rpcTestHost{runtime: runtime}, RPCModeOptions{
		Stdin:  strings.NewReader("{\"id\":\"p\",\"type\":\"prompt\",\"message\":\"hello\"}\n"),
		Stdout: &stdout, Stderr: &stderr,
	})
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	lines := bytes.Split(bytes.TrimSuffix(stdout.Bytes(), []byte{'\n'}), []byte{'\n'})
	if len(lines) != 1 {
		t.Fatalf("preflight emitted %d lines: %s", len(lines), stdout.String())
	}
	var response RPCResponse
	if err := json.Unmarshal(lines[0], &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "p" || response.Command != "prompt" || response.Success || !strings.HasPrefix(response.Error, "No model selected.") {
		t.Fatalf("preflight response = %s", lines[0])
	}
}

func TestRPCGetAvailableThinkingLevels(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("thinking-levels"))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent:          agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}})),
		SessionManager: manager,
		Settings:       settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := RunRPCMode(context.Background(), &rpcTestHost{runtime: runtime}, RPCModeOptions{
		Stdin:  strings.NewReader("{\"id\":\"levels\",\"type\":\"get_available_thinking_levels\"}\n"),
		Stdout: &stdout, Stderr: &stderr,
	})
	want := "{\"id\":\"levels\",\"type\":\"response\",\"command\":\"get_available_thinking_levels\",\"success\":true,\"data\":{\"levels\":[\"off\"]}}\n"
	if exitCode != 0 || stderr.Len() != 0 || stdout.String() != want {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestRPCImmediateFollowUpAfterPromptResponseIsQueued(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("prompt-follow-up"))
	if err != nil {
		t.Fatal(err)
	}
	provider := faux.New(faux.Options{API: "faux", Provider: "faux", TokenSize: faux.FixedTokenSize(4)})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage("first"),
		faux.AssistantMessage("second"),
	})
	model := provider.GetModel()
	created := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{Model: model, Messages: agent.AgentMessages{}, Tools: []agent.AgentTool{}}),
		agent.WithStreamFn(provider.StreamSimple),
		agent.WithConvertToLLM(codingagent.ConvertToLLM),
	)
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			key := "fixture-key"
			return &key, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, inputWriter := io.Pipe()
	output := &immediateFollowUpWriter{input: inputWriter}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- RunRPCMode(context.Background(), &rpcTestHost{runtime: runtime}, RPCModeOptions{
			Stdin: input, Stdout: output, Stderr: &stderr,
		})
	}()
	if _, err := io.WriteString(inputWriter, `{"id":"p1","type":"prompt","message":"first"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for provider.State().CallCount != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := provider.State().CallCount; calls != 2 {
		t.Fatalf("provider calls = %d, want 2 queued turns", calls)
	}
	if err := runtime.WaitForIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case exitCode := <-done:
		if exitCode != 0 || stderr.Len() != 0 {
			t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RPC mode did not stop")
	}
	for _, id := range []string{"p1", "p2"} {
		if !bytes.Contains(output.Bytes(), []byte(`"id":"`+id+`","type":"response","command":"prompt","success":true`)) {
			t.Fatalf("missing successful %s response in %s", id, output.Bytes())
		}
	}
}

type rpcTestHost struct{ runtime *codingagent.SessionRuntime }

func (host *rpcTestHost) Session() *codingagent.SessionRuntime { return host.runtime }
func (*rpcTestHost) NewSession(string) (bool, error)           { return true, nil }
func (*rpcTestHost) SwitchSession(string) (bool, error)        { return true, nil }
func (*rpcTestHost) Fork(string, bool) (string, bool, error)   { return "", true, nil }
func (host *rpcTestHost) Dispose()                             { host.runtime.Dispose() }

func TestSerializedOutputStopsAfterWriterFailure(t *testing.T) {
	writer := &failRPCWriter{}
	output := newSerializedOutput(writer)
	output.writeLine([]byte(`{"first":true}`))
	output.writeLine([]byte(`{"second":true}`))
	err := output.closeAndWait()
	if err == nil || writer.calls != 1 {
		t.Fatalf("error = %v, writes = %d", err, writer.calls)
	}
}

func TestRPCEOFAbortsRunningCommandBeforeWaiting(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("eof"))
	if err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}}))
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- RunRPCMode(context.Background(), &rpcTestHost{runtime: runtime}, RPCModeOptions{
			Stdin:  strings.NewReader("{\"id\":\"b\",\"type\":\"bash\",\"command\":\"sleep 30\"}\n"),
			Stdout: &stdout, Stderr: &stderr,
		})
	}()
	select {
	case exitCode := <-done:
		if exitCode != 0 || stderr.Len() != 0 {
			t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC EOF waited for the uncancelled bash command")
	}
}

type failRPCWriter struct{ calls int }

func (writer *failRPCWriter) Write([]byte) (int, error) {
	writer.calls++
	return 0, io.ErrClosedPipe
}

type immediateFollowUpWriter struct {
	bytes.Buffer
	input    *io.PipeWriter
	injected bool
}

func (writer *immediateFollowUpWriter) Write(data []byte) (int, error) {
	count, err := writer.Buffer.Write(data)
	if err != nil || writer.injected || !bytes.Contains(data, []byte(`"id":"p1","type":"response","command":"prompt"`)) {
		return count, err
	}
	writer.injected = true
	if _, writeErr := io.WriteString(writer.input, `{"id":"p2","type":"prompt","message":"second","streamingBehavior":"followUp"}`+"\n"); writeErr != nil {
		return count, writeErr
	}
	// Keep the first response write active long enough for the input loop to
	// dispatch the follow-up before the agent marks itself streaming.
	time.Sleep(20 * time.Millisecond)
	return count, nil
}
