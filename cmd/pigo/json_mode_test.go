package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/modes"
	"github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type f3SessionFixtures struct {
	SchemaVersion int                 `json:"schemaVersion"`
	FixedNow      int64               `json:"fixedNow"`
	Scenarios     []f3SessionScenario `json:"scenarios"`
}

type f3SessionScenario struct {
	Name                    string            `json:"name"`
	Trace                   string            `json:"trace"`
	FixedNow                int64             `json:"fixedNow"`
	SystemPrompt            string            `json:"systemPrompt"`
	InitialMessage          string            `json:"initialMessage"`
	Messages                []string          `json:"messages"`
	TokenSize               int               `json:"tokenSize"`
	Settings                json.RawMessage   `json:"settings"`
	Queue                   *f3SessionQueue   `json:"queue"`
	CompactAfterFirstPrompt bool              `json:"compactAfterFirstPrompt"`
	Responses               []json.RawMessage `json:"responses"`
	ExpectedExitCode        int               `json:"expectedExitCode"`
	RequiredEventTypes      []string          `json:"requiredEventTypes"`
}

type f3SessionQueue struct {
	Steering []string `json:"steering"`
	FollowUp []string `json:"followUp"`
}

type fixturePrintSession struct {
	runtime  *codingagent.SessionRuntime
	scenario f3SessionScenario
	prompts  int
}

func (fixture *fixturePrintSession) Prompt(ctx context.Context, input any, images ...*ai.ImageContent) error {
	index := fixture.prompts
	fixture.prompts++
	if index == 0 && fixture.scenario.Queue != nil {
		for _, message := range fixture.scenario.Queue.Steering {
			if err := fixture.runtime.Steer(message); err != nil {
				return err
			}
		}
		for _, message := range fixture.scenario.Queue.FollowUp {
			if err := fixture.runtime.FollowUp(message); err != nil {
				return err
			}
		}
	}
	if err := fixture.runtime.Prompt(ctx, input, images...); err != nil {
		return err
	}
	if index == 0 && fixture.scenario.CompactAfterFirstPrompt {
		_, _ = fixture.runtime.Compact(ctx, "")
	}
	return nil
}

func (fixture *fixturePrintSession) Abort() { fixture.runtime.Abort() }

func (fixture *fixturePrintSession) State() agent.AgentState { return fixture.runtime.State() }

func (fixture *fixturePrintSession) Subscribe(listener func(any)) func() {
	return fixture.runtime.Subscribe(listener)
}

func TestJSONPrintModeMatchesUpstreamRunPrintModeFixtures(t *testing.T) {
	fixtures := loadF3SessionFixtures(t)
	if fixtures.SchemaVersion != 2 {
		t.Fatalf("F3-session schema version = %d, want 2", fixtures.SchemaVersion)
	}
	for _, scenario := range fixtures.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			want, err := runner.ReadFixture("F3-session", scenario.Trace)
			if err != nil {
				t.Fatal(err)
			}
			lines := runner.LoadJSONLines(t, "F3-session", scenario.Trace)
			header := decodeStrictSessionHeader(t, lines[0], "/fixture/project")
			headerTime, err := time.Parse(time.RFC3339Nano, header.Timestamp)
			if err != nil {
				t.Fatal(err)
			}
			runtime, manager := newF3SessionRuntime(t, scenario, headerTime)
			wrapped := &fixturePrintSession{runtime: runtime, scenario: scenario}
			var stdout, stderr bytes.Buffer
			exitCode := modes.RunPrintMode(context.Background(), wrapped, modes.PrintModeOptions{
				Mode: modes.PrintOutputJSON, Messages: scenario.Messages, InitialMessage: scenario.InitialMessage,
				SessionHeader: manager.GetHeader(), Stdout: &stdout, Stderr: &stderr,
			})
			if exitCode != scenario.ExpectedExitCode || stderr.Len() != 0 {
				t.Fatalf("exit=%d want=%d stderr=%q", exitCode, scenario.ExpectedExitCode, stderr.String())
			}
			if !bytes.Equal(stdout.Bytes(), want) {
				t.Fatalf("JSON trace mismatch:\n%s", runner.ByteDiff(want, stdout.Bytes()))
			}
		})
	}
	if !strings.Contains(helpText, "pigo login <provider>") || !strings.Contains(helpText, "anthropic, openai-codex, github-copilot, xai") {
		t.Fatalf("headless OAuth help is incomplete: %q", helpText)
	}
}

func loadF3SessionFixtures(t testing.TB) f3SessionFixtures {
	t.Helper()
	var fixtures f3SessionFixtures
	runner.LoadJSON(t, "F3-session", "scenarios.json", &fixtures)
	return fixtures
}

func newF3SessionRuntime(t testing.TB, scenario f3SessionScenario, headerTime time.Time) (*codingagent.SessionRuntime, *session.SessionManager) {
	t.Helper()
	agentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), scenario.Settings, 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager("/fixture/project", config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory("/fixture/project",
		session.WithSessionID("fixture-json-"+scenario.Name),
		session.WithClock(func() time.Time { return headerTime }),
	)
	if err != nil {
		t.Fatal(err)
	}
	now := func() int64 { return scenario.FixedNow }
	provider := f3ScenarioProvider(t, scenario)
	created := agent.NewAgent(
		provider.StreamSimple, agent.WithInitialState(agent.AgentState{
			Model: provider.GetModel(), SystemPrompt: scenario.SystemPrompt, Messages: agent.AgentMessages{}, Tools: []agent.AgentTool{},
		}),
		agent.WithConvertToLLM(codingagent.ConvertToLLM),
		agent.WithClock(now),
	)
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings, StreamFn: provider.StreamSimple,
		Sleep: func(context.Context, time.Duration) error { return nil }, Clock: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	return runtime, manager
}

type strictSessionHeader struct {
	Type          string  `json:"type"`
	Version       int     `json:"version"`
	ID            string  `json:"id"`
	Timestamp     string  `json:"timestamp"`
	CWD           string  `json:"cwd"`
	ParentSession *string `json:"parentSession,omitempty"`
}

func decodeStrictSessionHeader(t testing.TB, raw []byte, expectedCWD string) strictSessionHeader {
	t.Helper()
	header, err := parseStrictSessionHeader(raw, expectedCWD)
	if err != nil {
		t.Fatal(err)
	}
	return header
}

func parseStrictSessionHeader(raw []byte, expectedCWD string) (strictSessionHeader, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var header strictSessionHeader
	if err := decoder.Decode(&header); err != nil {
		return strictSessionHeader{}, fmt.Errorf("decode session header: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return strictSessionHeader{}, fmt.Errorf("session header trailer: %v", err)
	}
	if header.Type != "session" || header.Version != session.CurrentVersion || header.ID == "" || header.CWD != expectedCWD {
		return strictSessionHeader{}, fmt.Errorf("invalid session header: %#v", header)
	}
	parsed, err := time.Parse(time.RFC3339Nano, header.Timestamp)
	if err != nil || parsed.Location() != time.UTC || parsed.Format("2006-01-02T15:04:05.000Z") != header.Timestamp {
		return strictSessionHeader{}, fmt.Errorf("session header timestamp = %q, parse=%v", header.Timestamp, err)
	}
	reencoded, err := ai.Marshal(header)
	if err != nil {
		return strictSessionHeader{}, err
	}
	if !bytes.Equal(reencoded, raw) {
		return strictSessionHeader{}, fmt.Errorf("session header member order or encoding changed: %s", runner.ByteDiff(raw, reencoded))
	}
	return header, nil
}

func canonicalizeCLIHeader(t testing.TB, got []byte, actualCWD string, expected strictSessionHeader) []byte {
	t.Helper()
	lines, err := runner.DecodeJSONLines(got)
	if err != nil {
		t.Fatal(err)
	}
	header := decodeStrictSessionHeader(t, lines[0], actualCWD)
	header.ID = expected.ID
	header.Timestamp = expected.Timestamp
	header.CWD = expected.CWD
	encoded, err := ai.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	output.Write(encoded)
	output.WriteByte('\n')
	for _, line := range lines[1:] {
		output.Write(line)
		output.WriteByte('\n')
	}
	return output.Bytes()
}

func f3ScenarioByName(t testing.TB, name string) f3SessionScenario {
	t.Helper()
	for _, scenario := range loadF3SessionFixtures(t).Scenarios {
		if scenario.Name == name {
			return scenario
		}
	}
	t.Fatalf("missing F3-session scenario %q", name)
	return f3SessionScenario{}
}
func TestCLIJSONModeMatchesMultiplePromptFixture(t *testing.T) {
	scenario := f3ScenarioByName(t, "multiple-prompts")
	want, err := runner.ReadFixture("F3-session", scenario.Trace)
	if err != nil {
		t.Fatal(err)
	}
	expectedLines := runner.LoadJSONLines(t, "F3-session", scenario.Trace)
	expectedHeader := decodeStrictSessionHeader(t, expectedLines[0], "/fixture/project")
	project := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), "agent")
	t.Chdir(project)
	t.Setenv(config.EnvAgentDir, agentDir)
	provider := f3ScenarioProvider(t, scenario)
	settings := f3ScenarioSettings(t, project, agentDir, scenario.Settings)

	var stdout, stderr bytes.Buffer
	argv := []string{"--mode", "json", "--no-session", "--model", "faux-1"}
	argv = append(argv, scenario.InitialMessage)
	argv = append(argv, scenario.Messages...)
	exitCode := runCLIWithDependencies(context.Background(), argv, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, StdinTTY: true, StdoutTTY: false,
	}, cliDependencies{createRuntime: f3RuntimeFactory(scenario, provider, settings)})
	if exitCode != scenario.ExpectedExitCode || stderr.Len() != 0 {
		t.Fatalf("exit=%d want=%d stderr=%q", exitCode, scenario.ExpectedExitCode, stderr.String())
	}
	canonical := canonicalizeCLIHeader(t, stdout.Bytes(), filepath.ToSlash(project), expectedHeader)
	if !bytes.Equal(canonical, want) {
		t.Fatalf("CLI JSON trace mismatch:\n%s", runner.ByteDiff(want, canonical))
	}
	if provider.PendingResponseCount() != 0 {
		t.Fatalf("pending faux responses = %d", provider.PendingResponseCount())
	}
}

func TestCLIJSONModeMergesPipedStdinAndRunsRemainingPrompts(t *testing.T) {
	project := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), "agent")
	t.Chdir(project)
	t.Setenv(config.EnvAgentDir, agentDir)
	now := int64(1_700_000_100_321)
	provider := faux.New(faux.Options{API: "faux", Provider: "faux", Now: func() int64 { return now }})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage("first", faux.AssistantMessageOptions{Timestamp: &now}),
		faux.AssistantMessage("second", faux.AssistantMessageOptions{Timestamp: &now}),
	})
	settings := f3ScenarioSettings(t, project, agentDir, json.RawMessage(`{"compaction":{"enabled":false},"retry":{"enabled":false}}`))
	scenario := f3SessionScenario{FixedNow: now, SystemPrompt: "stdin fixture"}
	var stdout, stderr bytes.Buffer
	exitCode := runCLIWithDependencies(context.Background(), []string{
		"--mode", "json", "--no-session", "--model", "faux-1", "cli prompt", "next prompt",
	}, cliStreams{
		Stdin: strings.NewReader("  piped input\n"), Stdout: &stdout, Stderr: &stderr, StdinTTY: false, StdoutTTY: false,
	}, cliDependencies{createRuntime: f3RuntimeFactory(scenario, provider, settings)})
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
	lines, err := runner.DecodeJSONLines(stdout.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	decodeStrictSessionHeader(t, lines[0], filepath.ToSlash(project))
	if got, want := userPromptsFromJSONEvents(t, lines[1:]), []string{"piped inputcli prompt", "next prompt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("user prompts = %#v, want %#v", got, want)
	}
}

func TestCLIJSONModeResumeAndForkHeaders(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv(config.EnvAgentDir, agentDir)
	sessionDir, err := session.DefaultSessionDir(project, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	source := createCLIStoredSession(t, project, sessionDir, "json-source")
	selector := func(current, _ SessionListLoader) (string, bool, error) {
		listed := current(nil)
		for _, candidate := range listed {
			if candidate.ID == source.GetSessionID() {
				return candidate.Path, true, nil
			}
		}
		return "", false, fmt.Errorf("source session absent from %#v", listed)
	}

	for _, test := range []struct {
		name       string
		argv       []string
		selector   SessionSelector
		wantID     string
		wantParent *string
	}{
		{name: "fork", argv: []string{"--mode", "json", "--fork", "json-source", "fork prompt", "--model", "faux-1"}, wantParent: stringValue(source.GetSessionFile())},
		{name: "resume", argv: []string{"--mode", "json", "--resume", "resume prompt", "--model", "faux-1"}, selector: selector, wantID: source.GetSessionID()},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := faux.New(faux.Options{API: "faux", Provider: "faux"})
			provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage(test.name + " complete")})
			base := fauxRuntimeFactory(provider)
			var prior agent.AgentMessages
			var stdout, stderr bytes.Buffer
			exitCode := runCLIWithDependencies(context.Background(), test.argv, cliStreams{
				Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, StdinTTY: true, StdoutTTY: false,
			}, cliDependencies{
				selectSession: test.selector,
				createRuntime: func(cwd string, args CLIArgs, messages agent.AgentMessages) (runtimeInputs, error) {
					prior = append(agent.AgentMessages(nil), messages...)
					return base(cwd, args, messages)
				},
			})
			if exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
			}
			lines, err := runner.DecodeJSONLines(stdout.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			header := decodeStrictSessionHeader(t, lines[0], filepath.ToSlash(project))
			if len(prior) != 2 {
				t.Fatalf("restored messages = %d, want 2", len(prior))
			}
			if test.wantID != "" && header.ID != test.wantID {
				t.Fatalf("header id = %q, want %q", header.ID, test.wantID)
			}
			if test.name == "fork" && header.ID == source.GetSessionID() {
				t.Fatalf("fork reused source id %q", header.ID)
			}
			if !reflect.DeepEqual(header.ParentSession, test.wantParent) {
				t.Fatalf("header parent = %#v, want %#v", header.ParentSession, test.wantParent)
			}
		})
	}
}

func TestCLIJSONModeKeepsMetadataOffStdout(t *testing.T) {
	for _, test := range []struct {
		name        string
		argv        []string
		wantText    string
		wantStderr  bool
		wantRuntime bool
	}{
		{name: "plain help", argv: []string{"--help"}, wantText: "Usage: pigo", wantStderr: false},
		{name: "json help", argv: []string{"--mode", "json", "--help"}, wantText: "Usage: pigo", wantStderr: true},
		// Upstream lists models after full runtime creation (main.ts:747-764).
		{name: "plain model list", argv: []string{"--list-models"}, wantText: "No models available", wantStderr: false, wantRuntime: true},
		{name: "json model list", argv: []string{"--mode", "json", "--list-models"}, wantText: "No models available", wantStderr: true, wantRuntime: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			agentDir := t.TempDir()
			t.Setenv(config.EnvAgentDir, agentDir)
			registry, err := config.NewModelRegistry(agentDir)
			if err != nil {
				t.Fatal(err)
			}
			createdRuntime := false
			var stdout, stderr bytes.Buffer
			exitCode := runCLIWithDependencies(context.Background(), test.argv, cliStreams{
				Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, StdinTTY: true, StdoutTTY: true,
			}, cliDependencies{
				loadModels: func(string) (*config.ModelRegistry, error) { return registry, nil },
				createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
					createdRuntime = true
					return runtimeInputs{}, nil
				},
			})
			if exitCode != 0 || createdRuntime != test.wantRuntime {
				t.Fatalf("exit=%d createdRuntime=%t want %t", exitCode, createdRuntime, test.wantRuntime)
			}
			if test.wantStderr {
				if stdout.Len() != 0 || !strings.Contains(stderr.String(), test.wantText) {
					t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
			} else if stderr.Len() != 0 || !strings.Contains(stdout.String(), test.wantText) {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func f3ScenarioProvider(t testing.TB, scenario f3SessionScenario) *faux.Provider {
	t.Helper()
	now := func() int64 { return scenario.FixedNow }
	provider := faux.New(faux.Options{
		API: "faux", Provider: "faux", TokenSize: faux.FixedTokenSize(scenario.TokenSize), Now: now,
	})
	responses := make([]faux.ResponseStep, 0, len(scenario.Responses))
	for index, raw := range scenario.Responses {
		message, err := ai.UnmarshalMessage(raw)
		if err != nil {
			t.Fatalf("response %d: %v", index, err)
		}
		assistant, ok := message.(*ai.AssistantMessage)
		if !ok {
			t.Fatalf("response %d = %T, want assistant", index, message)
		}
		responses = append(responses, assistant)
	}
	provider.SetResponses(responses)
	return provider
}

func f3ScenarioSettings(t testing.TB, cwd, agentDir string, raw json.RawMessage) *config.SettingsManager {
	t.Helper()
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func f3RuntimeFactory(scenario f3SessionScenario, provider *faux.Provider, settings *config.SettingsManager) func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
	return func(_ string, _ CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
		now := func() int64 { return scenario.FixedNow }
		// The F3 goldens were extracted from upstream's runPrintMode harness,
		// whose raw Agent carries no session id; disable faux's session-keyed
		// prompt-cache emulation so the CLI path (which sets the stream
		// session id like upstream createAgentSession) matches them.
		noCache := ai.CacheRetentionNone
		created := agent.NewAgent(
			provider.StreamSimple, agent.WithInitialState(agent.AgentState{
				Model: provider.GetModel(), SystemPrompt: scenario.SystemPrompt, Messages: prior, Tools: []agent.AgentTool{},
			}),
			agent.WithConvertToLLM(codingagent.ConvertToLLM),
			agent.WithClock(now),
			agent.WithSimpleStreamOptions(ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{CacheRetention: &noCache}}),
		)
		return runtimeInputs{Agent: created, Settings: settings}, nil
	}
}

func userPromptsFromJSONEvents(t testing.TB, lines []json.RawMessage) []string {
	t.Helper()
	prompts := make([]string, 0)
	for index, raw := range lines {
		var event struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("event %d: %v", index, err)
		}
		if event.Type != "message_start" || len(event.Message) == 0 {
			continue
		}
		message, err := ai.UnmarshalMessage(event.Message)
		if err != nil {
			t.Fatalf("event %d message: %v", index, err)
		}
		user, ok := message.(*ai.UserMessage)
		if !ok {
			continue
		}
		if user.Content.Text != nil {
			prompts = append(prompts, *user.Content.Text)
			continue
		}
		var text strings.Builder
		for _, block := range user.Content.Blocks {
			if content, ok := block.(*ai.TextContent); ok {
				text.WriteString(content.Text)
			}
		}
		prompts = append(prompts, text.String())
	}
	return prompts
}

func TestStrictSessionHeaderRejectsUnknownFields(t *testing.T) {
	if _, err := parseStrictSessionHeader([]byte(`{"type":"session","version":3,"id":"id","timestamp":"2026-07-18T00:00:00.000Z","cwd":"/fixture/project","extra":true}`), "/fixture/project"); err == nil {
		t.Fatal("header with an unknown field was accepted")
	}
}
