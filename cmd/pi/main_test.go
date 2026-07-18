package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/session"
)

func TestApplySessionDefaultsRestoresOnlyRecordedThinkingLevel(t *testing.T) {
	message := json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hello"}]}`)

	t.Run("message without thinking entry uses configured default", func(t *testing.T) {
		args := CLIArgs{}
		context := session.SessionContext{Messages: []json.RawMessage{message}, ThinkingLevel: "off"}
		applySessionDefaults(&args, context, []session.SessionEntry{{Type: "message", Message: message}})
		if args.Thinking != nil {
			t.Fatalf("thinking = %q, want settings/default resolution", *args.Thinking)
		}
	})

	t.Run("message with thinking entry restores recorded level", func(t *testing.T) {
		args := CLIArgs{}
		context := session.SessionContext{Messages: []json.RawMessage{message}, ThinkingLevel: "high"}
		branch := []session.SessionEntry{
			{Type: "message", Message: message},
			{Type: "thinking_level_change", ThinkingLevel: "high"},
		}
		applySessionDefaults(&args, context, branch)
		if args.Thinking == nil || *args.Thinking != "high" {
			t.Fatalf("thinking = %v, want high", args.Thinking)
		}
	})

	t.Run("explicit CLI level wins", func(t *testing.T) {
		level := "low"
		args := CLIArgs{Thinking: &level}
		context := session.SessionContext{Messages: []json.RawMessage{message}, ThinkingLevel: "high"}
		applySessionDefaults(&args, context, []session.SessionEntry{{Type: "thinking_level_change", ThinkingLevel: "high"}})
		if args.Thinking == nil || *args.Thinking != "low" {
			t.Fatalf("thinking = %v, want low", args.Thinking)
		}
	})

	t.Run("provider-only CLI selection does not split the restored model pair", func(t *testing.T) {
		provider := "anthropic"
		args := CLIArgs{Provider: &provider}
		context := session.SessionContext{
			Messages: []json.RawMessage{message},
			Model:    &session.SessionModel{Provider: "openai", ModelID: "session-model"},
		}
		applySessionDefaults(&args, context, []session.SessionEntry{{Type: "message", Message: message}, {Type: "model_change"}})
		if args.Provider == nil || *args.Provider != "openai" || args.Model == nil || *args.Model != "session-model" || !args.RestoredModel {
			t.Fatalf("selection = %v/%v", args.Provider, args.Model)
		}
	})

	t.Run("empty CLI selection restores the session model pair", func(t *testing.T) {
		empty := ""
		args := CLIArgs{Provider: &empty, Model: &empty}
		context := session.SessionContext{
			Messages: []json.RawMessage{message},
			Model:    &session.SessionModel{Provider: "openai", ModelID: "session-model"},
		}
		applySessionDefaults(&args, context, []session.SessionEntry{{Type: "message", Message: message}, {Type: "model_change"}})
		if args.Provider == nil || *args.Provider != "openai" || args.Model == nil || *args.Model != "session-model" || !args.RestoredModel {
			t.Fatalf("selection = %v/%v", args.Provider, args.Model)
		}
	})

	t.Run("metadata-only session does not restore a model", func(t *testing.T) {
		args := CLIArgs{}
		context := session.SessionContext{Model: &session.SessionModel{Provider: "openai", ModelID: "session-model"}}
		applySessionDefaults(&args, context, []session.SessionEntry{{Type: "model_change"}})
		if args.Provider != nil || args.Model != nil {
			t.Fatalf("selection = %v/%v, want settings/default resolution", args.Provider, args.Model)
		}
	})

	t.Run("explicit model does not inherit the session provider", func(t *testing.T) {
		model := "gpt-cli"
		args := CLIArgs{Model: &model}
		context := session.SessionContext{Model: &session.SessionModel{Provider: "stale", ModelID: "session-model"}}
		applySessionDefaults(&args, context, []session.SessionEntry{{Type: "model_change"}})
		if args.Provider != nil || args.Model == nil || *args.Model != model {
			t.Fatalf("selection = %v/%v", args.Provider, args.Model)
		}
	})
}

func TestDecodeSessionMessagesPreservesCodingAgentRoles(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"hello","timestamp":1}`),
		json.RawMessage(`{"role":"custom","customType":"note","content":"remember","display":false,"timestamp":2}`),
		json.RawMessage(`{"role":"branchSummary","summary":"branch","fromId":"entry","timestamp":3}`),
		json.RawMessage(`{"role":"compactionSummary","summary":"compact","tokensBefore":4,"timestamp":5}`),
	}
	messages := decodeSessionMessages(raw)
	if len(messages) != len(raw) {
		t.Fatalf("decoded %d messages, want %d", len(messages), len(raw))
	}
	if _, ok := messages[0].(*ai.UserMessage); !ok {
		t.Fatalf("standard message = %T", messages[0])
	}
	for index := 1; index < len(messages); index++ {
		preserved, ok := messages[index].(json.RawMessage)
		if !ok || string(preserved) != string(raw[index]) {
			t.Fatalf("message %d = %T %s", index, messages[index], preserved)
		}
	}
}

func TestAppendInitialRuntimeStateChecksCurrentBranchForThinking(t *testing.T) {
	manager, err := session.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := manager.AppendMessage(&ai.UserMessage{Content: ai.NewUserText("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendThinkingLevelChange("high"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Branch(rootID); err != nil {
		t.Fatal(err)
	}

	prior := manager.BuildSessionContext()
	if err := appendInitialRuntimeState(manager, agent.AgentState{ThinkingLevel: "medium"}, prior); err != nil {
		t.Fatal(err)
	}
	branch := manager.GetBranch()
	if got := branch[len(branch)-1]; got.Type != "thinking_level_change" || got.ThinkingLevel != "medium" {
		t.Fatalf("current branch tail = %#v", got)
	}
}

func TestPersistAgentMessagesUsesRoleSpecificSessionEntries(t *testing.T) {
	manager, err := session.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sink := persistAgentMessages(manager)
	for _, message := range []agent.AgentMessage{
		&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
		json.RawMessage(`{"role":"custom","customType":"note","content":"remember","display":true,"details":{"x":1},"timestamp":2}`),
		json.RawMessage(`{"role":"branchSummary","summary":"skip","fromId":"root","timestamp":3}`),
	} {
		if err := sink(context.Background(), agent.MessageEndEvent{Message: message}); err != nil {
			t.Fatal(err)
		}
	}
	entries := manager.GetEntries()
	if len(entries) != 2 || entries[0].Type != "message" || entries[1].Type != "custom_message" {
		t.Fatalf("entries = %#v", entries)
	}
	custom := entries[1]
	if custom.CustomType != "note" || string(custom.Content) != `"remember"` || !custom.Display || string(custom.Details) != `{"x":1}` {
		t.Fatalf("custom entry = %#v", custom)
	}
}

func TestRunCLIRejectsAPIKeyWithoutExplicitModel(t *testing.T) {
	var stderr bytes.Buffer
	called := false
	code := runCLIWithDependencies(context.Background(), []string{"-p", "--api-key", "secret", "hello"}, cliStreams{
		Stdin:     strings.NewReader(""),
		Stdout:    io.Discard,
		Stderr:    &stderr,
		StdinTTY:  true,
		StdoutTTY: false,
	}, cliDependencies{createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
		called = true
		return runtimeInputs{}, nil
	}})
	if code != 1 || called || stderr.String() != "Error: --api-key requires a model to be specified via --model, --provider/--model, or --models\n" {
		t.Fatalf("code=%d called=%t stderr=%q", code, called, stderr.String())
	}
}

func TestRunCLIDispatchesAuthSubcommandsBeforeSessionSetup(t *testing.T) {
	called := false
	code := runCLIWithDependencies(context.Background(), []string{"logout", "anthropic"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
	}, cliDependencies{
		createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
			t.Fatal("auth command created an agent runtime")
			return runtimeInputs{}, nil
		},
		runAuth: func(_ context.Context, args CLIArgs, _ cliStreams) int {
			called = args.Command == "logout" && len(args.CommandArgs) == 1 && args.CommandArgs[0] == "anthropic"
			return 7
		},
	})
	if code != 7 || !called {
		t.Fatalf("auth dispatch = code %d, called %t", code, called)
	}
}

func TestRunCLIHelpAndListModelsRunAuthMigration(t *testing.T) {
	for _, argv := range [][]string{{"--help"}, {"--list-models"}} {
		t.Run(argv[0], func(t *testing.T) {
			agentDir := t.TempDir()
			t.Setenv(config.EnvAgentDir, agentDir)
			legacy := `{"anthropic":{"access":"access","refresh":"refresh","expires":4102444800000}}`
			if err := os.WriteFile(filepath.Join(agentDir, "oauth.json"), []byte(legacy), 0o600); err != nil {
				t.Fatal(err)
			}
			code := runCLIWithDependencies(context.Background(), argv, cliStreams{
				Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
			}, cliDependencies{})
			if code != 0 {
				t.Fatalf("%v exit code = %d", argv, code)
			}
			credential := config.ReadStoredCredential("anthropic", filepath.Join(agentDir, "auth.json"))
			if credential == nil || credential.Type != aiauth.CredentialOAuth || credential.Access != "access" {
				t.Fatalf("%v migration credential = %#v", argv, credential)
			}
			if _, err := os.Stat(filepath.Join(agentDir, "oauth.json.migrated")); err != nil {
				t.Fatalf("%v legacy rename: %v", argv, err)
			}
		})
	}
}

func TestRunCLIListModelsIsReadOnly(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	var stdout bytes.Buffer
	createdRuntime := false
	registry, err := config.NewModelRegistry(os.Getenv("PI_CODING_AGENT_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	code := runCLIWithDependencies(context.Background(), []string{"--list-models"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: io.Discard, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{
		createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
			createdRuntime = true
			return runtimeInputs{}, nil
		},
		loadModels: func(string) (*config.ModelRegistry, error) { return registry, nil },
	})
	if code != 0 || createdRuntime || stdout.Len() == 0 {
		t.Fatalf("code=%d createdRuntime=%t stdout=%q", code, createdRuntime, stdout.String())
	}
	entries, err := os.ReadDir(os.Getenv("PI_CODING_AGENT_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("--list-models created files: %#v", entries)
	}
}

func TestRunCLIListModelsWarnsAndContinuesOnMalformedModelsJSON(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(`{"providers":`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"--list-models"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{})
	if code != 0 || stdout.Len() == 0 || !strings.Contains(stderr.String(), "Warning: errors loading models.json:\nFailed to parse models.json") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunCLIModelUpdateDispatchAndConflicts(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Run("refresh", func(t *testing.T) {
		var stdout bytes.Buffer
		calls := 0
		code := runCLIWithDependencies(context.Background(), []string{"update", "--models"}, cliStreams{Stdout: &stdout, Stderr: io.Discard}, cliDependencies{
			refreshModels: func(context.Context, string) error { calls++; return nil },
		})
		if code != 0 || calls != 1 || stdout.String() != "Model catalogs refreshed\n" {
			t.Fatalf("code=%d calls=%d stdout=%q", code, calls, stdout.String())
		}
	})
	t.Run("conflict", func(t *testing.T) {
		var stderr bytes.Buffer
		calls := 0
		code := runCLIWithDependencies(context.Background(), []string{"update", "--models", "--self"}, cliStreams{Stdout: io.Discard, Stderr: &stderr}, cliDependencies{
			refreshModels: func(context.Context, string) error { calls++; return nil },
		})
		if code != 1 || calls != 0 || !strings.Contains(stderr.String(), "--models cannot be combined") {
			t.Fatalf("code=%d calls=%d stderr=%q", code, calls, stderr.String())
		}
	})
}

func TestRunCLIParserErrorVersionAndHelpPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		argv       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "parser error wins",
			argv:       []string{"--help", "--version", "-z", "--unknown"},
			wantCode:   1,
			wantStderr: "Error: Unknown option: -z\n",
		},
		{
			name:       "version wins help and deferred validation",
			argv:       []string{"--help", "--version", "--unknown", "--api-key", "secret"},
			wantStdout: version + "\n",
		},
		{
			name:       "help wins deferred validation",
			argv:       []string{"--help", "--unknown", "--api-key", "secret"},
			wantStdout: helpText,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			code := runCLIWithDependencies(context.Background(), test.argv, cliStreams{
				Stdin:     strings.NewReader(""),
				Stdout:    &stdout,
				Stderr:    &stderr,
				StdinTTY:  true,
				StdoutTTY: true,
			}, cliDependencies{createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
				called = true
				return runtimeInputs{}, nil
			}})
			if code != test.wantCode || called || stdout.String() != test.wantStdout || stderr.String() != test.wantStderr {
				t.Fatalf("code=%d called=%t stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunCLIReportsUnknownLongFlagsInMapOrder(t *testing.T) {
	var stderr bytes.Buffer
	called := false
	code := runCLIWithDependencies(context.Background(), []string{
		"-p",
		"--second=one",
		"--first", "value",
		"--second=two",
		"--api-key", "secret",
	}, cliStreams{
		Stdin:     strings.NewReader(""),
		Stdout:    io.Discard,
		Stderr:    &stderr,
		StdinTTY:  true,
		StdoutTTY: false,
	}, cliDependencies{createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
		called = true
		return runtimeInputs{}, nil
	}})
	want := "Error: Unknown options: --second, --first\n" +
		"Error: --api-key requires a model to be specified via --model, --provider/--model, or --models\n"
	if code != 1 || called || stderr.String() != want {
		t.Fatalf("code=%d called=%t stderr=%q", code, called, stderr.String())
	}
}

func TestRunCLIEmptyAPIKeyDoesNotRequireExplicitModel(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	provider := faux.New()
	var stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"-p", "--api-key", "", "--no-session"}, cliStreams{
		Stdin:     strings.NewReader(""),
		Stdout:    io.Discard,
		Stderr:    &stderr,
		StdinTTY:  true,
		StdoutTTY: false,
	}, cliDependencies{createRuntime: fauxRuntimeFactory(provider)})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunCLIPrintPersistsAndContinuesSession(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	sessionDir := filepath.Join(root, "sessions")
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	t.Setenv(config.EnvAgentDir, agentDir)
	attachment := filepath.Join(cwd, "prompt.txt")
	if err := os.WriteFile(attachment, []byte("file body"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage("first answer"),
		faux.AssistantMessage("second answer"),
	})
	dependencies := cliDependencies{createRuntime: fauxRuntimeFactory(provider)}
	var stdout, stderr bytes.Buffer
	exitCode := runCLIWithDependencies(context.Background(), []string{
		"-p", "@prompt.txt", "first prompt", "second prompt",
		"--session-dir", sessionDir,
		"--model", "faux-1",
	}, cliStreams{
		Stdin:     strings.NewReader(""),
		Stdout:    &stdout,
		Stderr:    &stderr,
		StdinTTY:  true,
		StdoutTTY: false,
	}, dependencies)
	if exitCode != 0 || stdout.String() != "second answer\n" || stderr.Len() != 0 {
		t.Fatalf("first run: exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}

	sessionFile := onlySessionFile(t, sessionDir)
	opened, err := session.Open(sessionFile, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	entries := opened.GetEntries()
	if len(entries) != 6 {
		t.Fatalf("first run entries = %d, want model/thinking changes plus four messages: %#v", len(entries), entries)
	}
	if entries[0].Type != "model_change" || entries[1].Type != "thinking_level_change" {
		t.Fatalf("initial entries = %#v", entries[:2])
	}
	firstUser := string(entries[2].Message)
	fileIndex := strings.Index(firstUser, attachment)
	bodyIndex := strings.Index(firstUser, "file body")
	promptIndex := strings.Index(firstUser, "first prompt")
	if fileIndex < 0 || bodyIndex <= fileIndex || promptIndex <= bodyIndex {
		t.Fatalf("initial user message = %s", firstUser)
	}
	if !strings.Contains(string(entries[4].Message), `"text":"second prompt"`) {
		t.Fatalf("second user message = %s", entries[4].Message)
	}

	provider = faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("continued")})
	stdout.Reset()
	stderr.Reset()
	exitCode = runCLIWithDependencies(context.Background(), []string{
		"-p", "-c", "third prompt",
		"--session-dir", sessionDir,
		"--model", "faux-1",
	}, cliStreams{
		Stdin:     strings.NewReader(""),
		Stdout:    &stdout,
		Stderr:    &stderr,
		StdinTTY:  true,
		StdoutTTY: false,
	}, cliDependencies{createRuntime: fauxRuntimeFactory(provider)})
	if exitCode != 0 || stdout.String() != "continued\n" || stderr.Len() != 0 {
		t.Fatalf("continue: exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if got := onlySessionFile(t, sessionDir); got != sessionFile {
		t.Fatalf("continue created %q instead of reopening %q", got, sessionFile)
	}
	reopened, err := session.Open(sessionFile, sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if entries := reopened.GetEntries(); len(entries) != 8 || !strings.Contains(string(entries[6].Message), `"text":"third prompt"`) {
		t.Fatalf("continued entries = %#v", entries)
	}
}

func TestRunCLIContinuesUpstreamTypeScriptSessionWithFullContext(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "F6", "write.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	t.Setenv(config.EnvAgentDir, agentDir)
	sessionDir, err := session.DefaultSessionDir(cwd, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(fixture, []byte("/fixture/project"), []byte(filepath.ToSlash(cwd)))
	sessionPath := filepath.Join(sessionDir, "2025-01-01T00-00-00-000Z_session-fixed.jsonl")
	if err := os.WriteFile(sessionPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}

	var requestContext ai.Context
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.Factory(func(
		_ context.Context,
		context ai.Context,
		_ *ai.StreamOptions,
		_ faux.State,
		_ *ai.Model,
	) (*ai.AssistantMessage, error) {
		requestContext = context
		return faux.AssistantMessage("continued"), nil
	})})
	var stdout, stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"-p", "-c", "new prompt", "--model", "faux-1"}, cliStreams{
		Stdin:     strings.NewReader(""),
		Stdout:    &stdout,
		Stderr:    &stderr,
		StdinTTY:  true,
		StdoutTTY: false,
	}, cliDependencies{createRuntime: fauxRuntimeFactory(provider)})
	if code != 0 || stdout.String() != "continued\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(requestContext.Messages) != 3 {
		t.Fatalf("provider context = %#v", requestContext.Messages)
	}
	if got := userMessageText(t, requestContext.Messages[0]); got != "hello <>&\u2028\u2029" {
		t.Fatalf("restored root message = %q", got)
	}
	if got := userMessageText(t, requestContext.Messages[1]); got != codingagent.BranchSummaryPrefix+"alternate branch"+codingagent.BranchSummarySuffix {
		t.Fatalf("restored branch summary = %q", got)
	}
	if got := userMessageText(t, requestContext.Messages[2]); got != "new prompt" {
		t.Fatalf("new prompt = %q", got)
	}
	if got := onlySessionFile(t, sessionDir); got != sessionPath {
		t.Fatalf("continued session = %q, want %q", got, sessionPath)
	}
}

func TestRunCLIDoesNotReadTerminalStdin(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("ok")})
	var stdout, stderr bytes.Buffer
	exitCode := runCLIWithDependencies(context.Background(), []string{"-p", "prompt", "--no-session", "--model", "faux-1"}, cliStreams{
		Stdin:     errorReader{},
		Stdout:    &stdout,
		Stderr:    &stderr,
		StdinTTY:  true,
		StdoutTTY: true,
	}, cliDependencies{createRuntime: fauxRuntimeFactory(provider)})
	if exitCode != 0 || stdout.String() != "ok\n" || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunCLIAutomaticallyUsesPrintModeForRedirectedOutput(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("automatic")})
	var stdout, stderr bytes.Buffer
	exitCode := runCLIWithDependencies(context.Background(), []string{"prompt", "--no-session", "--model", "faux-1"}, cliStreams{
		Stdin:     errorReader{},
		Stdout:    &stdout,
		Stderr:    &stderr,
		StdinTTY:  true,
		StdoutTTY: false,
	}, cliDependencies{createRuntime: fauxRuntimeFactory(provider)})
	if exitCode != 0 || stdout.String() != "automatic\n" || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestIsTerminalFileRejectsCharacterDevicesAndPipes(t *testing.T) {
	device, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = device.Close() }()
	if isTerminalFile(device) {
		t.Fatal("os.DevNull was treated as a terminal")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	if isTerminalFile(reader) || isTerminalFile(writer) {
		t.Fatal("pipe endpoint was treated as a terminal")
	}
}

func TestBuiltBinaryDispatchesHelp(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "pi")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, output)
	}
	command := exec.Command(binary, "--help")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run binary: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte("Usage: pi")) || !bytes.Contains(output, []byte("--continue")) {
		t.Fatalf("help output = %q", output)
	}
}

func fauxRuntimeFactory(provider *faux.Provider) func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
	return func(_ string, _ CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
		created := agent.NewAgent(
			agent.WithInitialState(agent.AgentState{
				SystemPrompt: "test",
				Model:        provider.GetModel(),
				Messages:     prior,
			}),
			agent.WithStreamFn(provider.StreamSimple),
			agent.WithConvertToLLM(codingagent.ConvertToLLM),
		)
		return runtimeInputs{Agent: created}, nil
	}
}

func userMessageText(t testing.TB, message ai.Message) string {
	t.Helper()
	user, ok := message.(*ai.UserMessage)
	if !ok {
		t.Fatalf("message = %T, want user", message)
	}
	if user.Content.Text != nil {
		return *user.Content.Text
	}
	if len(user.Content.Blocks) != 1 {
		t.Fatalf("content = %#v", user.Content)
	}
	text, ok := user.Content.Blocks[0].(*ai.TextContent)
	if !ok {
		t.Fatalf("content block = %T", user.Content.Blocks[0])
	}
	return text.Text
}

func onlySessionFile(t *testing.T, directory string) string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, filepath.Join(directory, entry.Name()))
		}
	}
	if len(files) != 1 {
		t.Fatalf("session files = %#v", files)
	}
	return files[0]
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("stdin was read") }

var _ io.Reader = errorReader{}
