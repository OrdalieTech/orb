package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/modes"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type f7Scenario struct {
	SchemaVersion int               `json:"schemaVersion"`
	FixedNow      int64             `json:"fixedNow"`
	CWD           string            `json:"cwd"`
	SessionID     string            `json:"sessionId"`
	SystemPrompt  string            `json:"systemPrompt"`
	TokenSize     int               `json:"tokenSize"`
	Responses     []json.RawMessage `json:"responses"`
	Steps         []f7Step          `json:"steps"`
}

type f7Step struct {
	Name              string `json:"name"`
	Input             string `json:"input"`
	Framing           string `json:"framing"`
	ExpectedLineCount int    `json:"expectedLineCount"`
}

type f7Host struct {
	session *codingagent.SessionRuntime
}

func (host *f7Host) Session() *codingagent.SessionRuntime { return host.session }
func (*f7Host) NewSession(string) (bool, error)           { return true, nil }
func (*f7Host) SwitchSession(string) (bool, error)        { return true, nil }
func (*f7Host) Fork(string, bool) (string, bool, error)   { return "", true, nil }
func (host *f7Host) Dispose()                             { host.session.Dispose() }

func TestF7RPCTranscriptMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "F7")
	if manifest.Family != "F7" || manifest.Generator != "conformance/extract/f7-rpc.ts" {
		t.Fatalf("unexpected F7 manifest: %+v", manifest)
	}
	var scenario f7Scenario
	runner.LoadJSON(t, "F7", "scenario.json", &scenario)
	if scenario.SchemaVersion != 1 || len(scenario.Steps) == 0 || len(scenario.Responses) != 1 {
		t.Fatalf("F7 scenario = version %d, steps %d, responses %d", scenario.SchemaVersion, len(scenario.Steps), len(scenario.Responses))
	}
	want, err := runner.ReadFixture("F7", "trace.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	wantLines := bytes.Split(bytes.TrimSuffix(want, []byte{'\n'}), []byte{'\n'})

	runtime := newF7Runtime(t, scenario)
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- modes.RunRPCMode(context.Background(), &f7Host{session: runtime}, modes.RPCModeOptions{
			Stdin: inputReader, Stdout: outputWriter, Stderr: &stderr,
		})
		_ = outputWriter.Close()
	}()

	lines := make(chan []byte)
	readErrors := make(chan error, 1)
	go readF7Output(outputReader, lines, readErrors)
	expectedIndex := 0
	for _, step := range scenario.Steps {
		framing := "\n"
		if step.Framing == "crlf" {
			framing = "\r\n"
		} else if step.Framing != "lf" {
			t.Fatalf("step %q has framing %q", step.Name, step.Framing)
		}
		if _, err := io.WriteString(inputWriter, step.Input+framing); err != nil {
			t.Fatalf("step %q input: %v", step.Name, err)
		}
		for range step.ExpectedLineCount {
			if expectedIndex >= len(wantLines) {
				t.Fatalf("step %q emitted beyond fixture", step.Name)
			}
			got := waitF7Line(t, step.Name, lines, readErrors)
			if diff := runner.ByteDiff(wantLines[expectedIndex], got); diff != "" {
				t.Fatalf("step %q, transcript line %d:\n%s", step.Name, expectedIndex+1, diff)
			}
			expectedIndex++
		}
	}
	if expectedIndex != len(wantLines) {
		t.Fatalf("consumed %d of %d F7 lines", expectedIndex, len(wantLines))
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case exitCode := <-done:
		if exitCode != 0 || stderr.Len() != 0 {
			t.Fatalf("RPC exit=%d stderr=%q", exitCode, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC mode did not stop after stdin EOF")
	}
}

func TestF7RPCTranscriptReplaysAgainstBinary(t *testing.T) {
	var scenario f7Scenario
	runner.LoadJSON(t, "F7", "scenario.json", &scenario)
	trace, err := runner.ReadFixture("F7", "trace.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	wantByStep := f7ExpectedLines(t, scenario, bytes.Split(bytes.TrimSuffix(trace, []byte{'\n'}), []byte{'\n'}))

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "pigo")
	build := exec.Command("go", "build", "-tags", "conformance", "-o", binary, "./cmd/pigo")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build pigo: %v\n%s", buildErr, output)
	}

	if scenario.CWD == "" {
		t.Fatal("F7 scenario has no cwd")
	}
	if err := os.MkdirAll(scenario.CWD, 0o755); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"compaction":{"enabled":false},"retry":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary,
		"--mode", "rpc",
	)
	command.Dir = scenario.CWD
	command.Env = append(os.Environ(),
		config.EnvAgentDir+"="+agentDir,
		"PIGO_F7_SCENARIO="+filepath.Join(repoRoot, "conformance", "fixtures", "F7", "scenario.json"),
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	lines := make(chan []byte)
	readErrors := make(chan error, 1)
	go readF7Output(stdout, lines, readErrors)
	for _, step := range scenario.Steps {
		framing := "\n"
		if step.Framing == "crlf" {
			framing = "\r\n"
		} else if step.Framing != "lf" {
			t.Fatalf("step %q has framing %q", step.Name, step.Framing)
		}
		if _, err := io.WriteString(stdin, step.Input+framing); err != nil {
			t.Fatalf("step %q input: %v", step.Name, err)
		}
		for index, want := range wantByStep[step.Name] {
			got := waitF7Line(t, step.Name, lines, readErrors)
			if diff := runner.ByteDiff(want, got); diff != "" {
				t.Fatalf("step %q, line %d:\n%s", step.Name, index+1, diff)
			}
		}
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil || stderr.Len() != 0 {
			t.Fatalf("RPC binary: %v, stderr=%q", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("RPC binary did not stop after stdin EOF")
	}
}

func f7ExpectedLines(t testing.TB, scenario f7Scenario, trace [][]byte) map[string][][]byte {
	t.Helper()
	result := make(map[string][][]byte, len(scenario.Steps))
	index := 0
	for _, step := range scenario.Steps {
		end := index + step.ExpectedLineCount
		if end > len(trace) {
			t.Fatalf("step %q ends at line %d of %d", step.Name, end, len(trace))
		}
		result[step.Name] = trace[index:end]
		index = end
	}
	if index != len(trace) {
		t.Fatalf("scenario accounts for %d of %d trace lines", index, len(trace))
	}
	return result
}

func newF7Runtime(t testing.TB, scenario f7Scenario) *codingagent.SessionRuntime {
	t.Helper()
	provider := faux.New(faux.Options{
		API: "faux", Provider: "faux", TokenSize: faux.FixedTokenSize(scenario.TokenSize),
		Now: func() int64 { return scenario.FixedNow },
	})
	responses := make([]faux.ResponseStep, len(scenario.Responses))
	for index, raw := range scenario.Responses {
		message, err := ai.UnmarshalMessage(raw)
		if err != nil {
			t.Fatalf("decode response %d: %v", index, err)
		}
		assistant, ok := message.(*ai.AssistantMessage)
		if !ok {
			t.Fatalf("response %d = %T", index, message)
		}
		responses[index] = assistant
	}
	provider.SetResponses(responses)

	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"compaction":{"enabled":false},"retry":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	nextEntryID := 0
	manager, err := sessionstore.InMemory(
		root,
		sessionstore.WithSessionID(scenario.SessionID),
		sessionstore.WithClock(func() time.Time { return time.UnixMilli(scenario.FixedNow).UTC() }),
		sessionstore.WithEntryIDGenerator(func() (string, error) {
			nextEntryID++
			return fmt.Sprintf("%08x", nextEntryID), nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	model := provider.GetModel()
	created := agent.NewAgent(
		provider.StreamSimple, agent.WithInitialState(agent.AgentState{
			Model: model, SystemPrompt: scenario.SystemPrompt, Messages: agent.AgentMessages{}, Tools: []agent.AgentTool{},
		}),
		agent.WithConvertToLLM(codingagent.ConvertToLLM),
		agent.WithClock(func() int64 { return scenario.FixedNow }),
	)
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, StreamFn: provider.StreamSimple,
		Clock: func() int64 { return scenario.FixedNow },
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			key := "faux-key"
			return &key, nil
		},
		AvailableModels: func() []ai.Model { return []ai.Model{*model} },
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func readF7Output(reader io.Reader, lines chan<- []byte, readErrors chan<- error) {
	defer close(lines)
	var pending []byte
	buffer := make([]byte, 4096)
	for {
		count, err := reader.Read(buffer)
		pending = append(pending, buffer[:count]...)
		for {
			newline := bytes.IndexByte(pending, '\n')
			if newline < 0 {
				break
			}
			line := bytes.Clone(pending[:newline])
			pending = pending[newline+1:]
			lines <- line
		}
		if err != nil {
			if err != io.EOF {
				readErrors <- err
			}
			if len(pending) != 0 {
				readErrors <- io.ErrUnexpectedEOF
			}
			return
		}
	}
}

func waitF7Line(t testing.TB, step string, lines <-chan []byte, readErrors <-chan error) []byte {
	t.Helper()
	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatalf("step %q: RPC stdout closed", step)
		}
		return line
	case err := <-readErrors:
		t.Fatalf("step %q: read RPC output: %v", step, err)
	case <-time.After(5 * time.Second):
		t.Fatalf("step %q: timed out waiting for RPC output", step)
	}
	return nil
}
