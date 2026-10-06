package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
)

func TestMain(m *testing.M) {
	_ = os.Unsetenv("HERDR_ENV")
	os.Exit(m.Run())
}

type versionNotificationUI struct {
	extensions.NoopUI
	messages []string
}

func (ui *versionNotificationUI) Notify(message string, _ extensions.NotificationType) {
	ui.messages = append(ui.messages, message)
}

type versionRoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip versionRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestStartupVersionCheckNotifiesAndHonorsNetworkCeilings(t *testing.T) {
	t.Setenv("PI_SKIP_VERSION_CHECK", "")
	t.Setenv("PI_OFFLINE", "")
	if !isNewerPackageVersion("v5.0.0-beta.20", "5.0.0-beta.9") || isNewerPackageVersion("v1.2.3", "1.2.3") {
		t.Fatal("semver precedence mismatch")
	}
	requests := 0
	client := &http.Client{Transport: versionRoundTrip(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.String() != latestReleaseURL {
			t.Errorf("URL = %q", request.URL)
		}
		if request.Header.Get("User-Agent") != "orb/1.2.3" || request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("headers = %#v", request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.2.4"}`))}, nil
	})}

	check := newStartupVersionCheck("1.2.3", client, latestReleaseURL, time.Second)
	ui := &versionNotificationUI{}
	check(context.Background(), ui)
	if requests != 1 || len(ui.messages) != 1 || ui.messages[0] != "orb v1.2.4 is available. Run: orb update" {
		t.Fatalf("requests=%d notifications=%q", requests, ui.messages)
	}

	for _, variable := range []string{"PI_SKIP_VERSION_CHECK", "PI_OFFLINE"} {
		t.Setenv(variable, "1")
		check(context.Background(), &versionNotificationUI{})
		t.Setenv(variable, "")
	}
	if requests != 1 {
		t.Fatalf("request made while disabled: %d", requests)
	}

	bounded := false
	blocked := &http.Client{Transport: versionRoundTrip(func(request *http.Request) (*http.Response, error) {
		_, bounded = request.Context().Deadline()
		return nil, context.Canceled
	})}
	newStartupVersionCheck("1.2.3", blocked, latestReleaseURL, 20*time.Millisecond)(context.Background(), &versionNotificationUI{})
	if !bounded {
		t.Fatal("request context has no deadline")
	}
}

func TestStartupModelRefreshIsNonBlockingAndRefreshesRegisteredProviders(t *testing.T) {
	original, present := os.LookupEnv("PI_OFFLINE")
	if err := os.Unsetenv("PI_OFFLINE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv("PI_OFFLINE", original)
		} else {
			_ = os.Unsetenv("PI_OFFLINE")
		}
	})
	agentDir := t.TempDir()
	registry, err := config.NewModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	providerRefresh := make(chan bool, 2)
	if err := registry.RegisterProviderConfig("startup", extensions.ProviderConfig{
		APIKey: "key",
		RefreshModels: func(ctx extensions.RefreshModelsContext) ([]extensions.ProviderModelConfig, error) {
			providerRefresh <- ctx.AllowNetwork
			return nil, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case allowNetwork := <-providerRefresh:
		if allowNetwork {
			t.Fatal("registration refresh allowed network access")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registration refresh did not run")
	}

	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		startStartupModelRefresh(context.Background(), "interactive", false, true, agentDir, registry, func(context.Context, string) error {
			close(started)
			<-release
			return errors.New("catalog unavailable")
		})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("startup refresh blocked the caller")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("startup refresh did not run")
	}
	close(release)
	select {
	case allowNetwork := <-providerRefresh:
		if !allowNetwork {
			t.Fatal("startup reload disabled registered provider network access")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup reload did not refresh registered providers")
	}
}

func TestRPCStartupModelRefreshWithPresentFalseEnvIsCacheOnly(t *testing.T) {
	t.Setenv("PI_OFFLINE", "0")
	registry, err := config.NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	providerRefresh := make(chan bool, 2)
	if err := registry.RegisterProviderConfig("startup", extensions.ProviderConfig{
		APIKey: "key",
		RefreshModels: func(ctx extensions.RefreshModelsContext) ([]extensions.ProviderModelConfig, error) {
			providerRefresh <- ctx.AllowNetwork
			return nil, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-providerRefresh:
	case <-time.After(2 * time.Second):
		t.Fatal("registration refresh did not run")
	}
	catalogRefresh := make(chan struct{}, 1)
	startStartupModelRefresh(context.Background(), "rpc", false, false, t.TempDir(), registry, func(context.Context, string) error {
		catalogRefresh <- struct{}{}
		return nil
	})
	select {
	case allowNetwork := <-providerRefresh:
		if allowNetwork {
			t.Fatal("cache-only refresh allowed provider network access")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cache-only provider refresh did not run")
	}
	select {
	case <-catalogRefresh:
		t.Fatal("cache-only refresh fetched the catalog")
	default:
	}
}

func TestRunCLIListModelsIsReadOnly(t *testing.T) {
	// Models are listed after full runtime creation so extension-registered
	// providers are listed; the run must stay read-only.
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer
	createdRuntime := false
	code := runCLIWithDependencies(context.Background(), []string{"--list-models"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: io.Discard, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{
		createRuntime: func(cwd string, args CLIArgs, messages engine.AgentMessages) (runtimeInputs, error) {
			createdRuntime = true
			return createRuntimeInputs(cwd, args, messages)
		},
	})
	if code != 0 || !createdRuntime || stdout.Len() == 0 {
		t.Fatalf("code=%d createdRuntime=%t stdout=%q", code, createdRuntime, stdout.String())
	}
	// Runtime creation writes only benign config (auth.json); it must never
	// persist a session for a metadata-only command.
	if _, err := os.Stat(filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "sessions")); !os.IsNotExist(err) {
		t.Fatalf("--list-models persisted a session (stat err = %v)", err)
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
	encodedAttachment, err := json.Marshal(attachment)
	if err != nil {
		t.Fatal(err)
	}
	fileIndex := strings.Index(firstUser, strings.Trim(string(encodedAttachment), `"`))
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
	if got := userMessageText(t, requestContext.Messages[1]); got != agent.BranchSummaryPrefix+"alternate branch"+agent.BranchSummarySuffix {
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

// builtBinaryPath names a test-built CLI; Windows only executes files with a PATHEXT extension.
func builtBinaryPath(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "orb.exe")
	}
	return filepath.Join(dir, "orb")
}

func TestBuiltBinaryServesRPCConversation(t *testing.T) {
	temp := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("completion path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"id\":\"chatcmpl_rpc\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"faux-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"RPC binary \"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(writer, "data: {\"id\":\"chatcmpl_rpc\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"faux-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"complete.\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	binary := builtBinaryPath(temp)
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, output)
	}
	project := filepath.Join(temp, "project")
	agentDir := filepath.Join(temp, "agent")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	models := `{"providers":{"faux":{"baseUrl":` + fmt.Sprintf("%q", server.URL+"/v1") + `,"api":"openai-completions","apiKey":"dummy","models":[{"id":"faux-1","name":"Faux Model","reasoning":false,"input":["text","image"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":128000,"maxTokens":16384}]}}}`
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(models), 0o600); err != nil {
		t.Fatal(err)
	}

	rpcContext, cancelRPC := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRPC()
	command := exec.CommandContext(rpcContext, binary, "--mode", "rpc", "--no-session", "--provider", "faux", "--model", "faux-1")
	command.Dir = project
	command.Env = append(os.Environ(), config.EnvAgentDir+"="+agentDir)
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
	reader := bufio.NewReader(stdout)
	exchange := func(input string) []byte {
		t.Helper()
		if _, writeErr := io.WriteString(stdin, input); writeErr != nil {
			t.Fatal(writeErr)
		}
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			t.Fatalf("read RPC response: %v; stderr=%q", readErr, stderr.String())
		}
		return bytes.TrimSuffix(line, []byte{'\n'})
	}
	if line := exchange("\n"); string(line) != `{"type":"response","command":"parse","success":false,"error":"Failed to parse command: Unexpected end of JSON input"}` {
		t.Fatalf("parse response = %s", line)
	}
	stateLine := exchange("{\"id\":\"state\",\"type\":\"get_state\"}\r\n")
	var state struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
		Data    struct {
			SessionID string    `json:"sessionId"`
			Model     *ai.Model `json:"model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stateLine, &state); err != nil {
		t.Fatal(err)
	}
	if state.ID != "state" || !state.Success || state.Data.SessionID == "" || state.Data.Model == nil || state.Data.Model.ID != "faux-1" {
		t.Fatalf("state response = %s", stateLine)
	}
	if line := exchange("{\"id\":\"\",\"type\":\"get_messages\"}\n"); string(line) != `{"id":"","type":"response","command":"get_messages","success":true,"data":{"messages":[]}}` {
		t.Fatalf("empty-ID messages response = %s", line)
	}
	if line := exchange("{\"id\":\"models\",\"type\":\"get_available_models\"}\n"); !bytes.Contains(line, []byte(`"models":[{`)) {
		t.Fatalf("available-models response = %s", line)
	}
	if line := exchange("{\"id\":\"unknown\",\"type\":\"missing\"}\n"); string(line) != `{"id":"unknown","type":"response","command":"missing","success":false,"error":"Unknown command: missing"}` {
		t.Fatalf("unknown response = %s", line)
	}
	if line := exchange("{\"id\":\"bash\",\"type\":\"bash\",\"command\":\"printf false-value\",\"excludeFromContext\":false}\n"); string(line) != `{"type":"bash_execution_update","id":"bash","delta":"false-value"}` {
		t.Fatalf("bash execution update = %s", line)
	}
	bashResponse, bashReadErr := reader.ReadBytes('\n')
	if bashReadErr != nil {
		t.Fatalf("read bash response: %v; stderr=%q", bashReadErr, stderr.String())
	}
	if !bytes.Contains(bashResponse, []byte(`"output":"false-value"`)) {
		t.Fatalf("bash response = %s", bashResponse)
	}
	if line := exchange("{\"id\":\"entries\",\"type\":\"get_entries\"}\n"); !bytes.Contains(line, []byte(`"excludeFromContext":false`)) {
		t.Fatalf("explicit-false bash entry = %s", line)
	}
	if _, err := io.WriteString(stdin, "{\"id\":\"prompt\",\"type\":\"prompt\",\"message\":\"Say complete.\"}\n"); err != nil {
		t.Fatal(err)
	}
	seenPromptResponse, seenAssistant, seenSettled := false, false, false
	for range 32 {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			t.Fatalf("read prompt event: %v; stderr=%q", readErr, stderr.String())
		}
		seenPromptResponse = seenPromptResponse || bytes.Contains(line, []byte(`"id":"prompt","type":"response","command":"prompt","success":true`))
		seenAssistant = seenAssistant || bytes.Contains(line, []byte(`"type":"message_end"`)) && bytes.Contains(line, []byte(`RPC binary complete.`))
		if bytes.Contains(line, []byte(`"type":"agent_settled"`)) {
			seenSettled = true
			break
		}
	}
	if !seenPromptResponse || !seenAssistant || !seenSettled {
		t.Fatalf("prompt lifecycle = response %v, assistant %v, settled %v", seenPromptResponse, seenAssistant, seenSettled)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil || stderr.Len() != 0 {
		t.Fatalf("RPC binary exit: %v; stderr=%q", err, stderr.String())
	}
}

func TestRunCLIHeadlessModesBindSessionReplacementLifecycle(t *testing.T) {
	for _, test := range []struct {
		name string
		argv []string
	}{
		{name: "print", argv: []string{"-p", "--no-session", "--model", "faux-1", "/replace-session"}},
		{name: "json", argv: []string{"--mode", "json", "--no-session", "--model", "faux-1", "/replace-session"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
			provider := faux.New()
			registry := extensions.NewRegistry(root)
			var events []string
			if err := registry.Register("<headless-session-lifecycle>", func(api extensions.API) error {
				api.On(extensions.EventSessionBeforeSwitch, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
					event := raw.(extensions.SessionBeforeSwitchEvent)
					events = append(events, "before:"+string(event.Reason))
					return nil, nil
				})
				api.On(extensions.EventSessionShutdown, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
					event := raw.(extensions.SessionShutdownEvent)
					events = append(events, "shutdown:"+string(event.Reason))
					return nil, nil
				})
				api.On(extensions.EventSessionStart, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
					event := raw.(extensions.SessionStartEvent)
					events = append(events, "start:"+string(event.Reason))
					return nil, nil
				})
				api.RegisterCommand("replace-session", extensions.Command{
					Handler: func(ctx context.Context, _ string, commandContext extensions.CommandContext) error {
						result, err := commandContext.NewSession(ctx, &extensions.NewSessionOptions{
							WithSession: func(context.Context, extensions.ReplacedSessionContext) error {
								events = append(events, "with-session")
								return nil
							},
						})
						if err != nil {
							return err
						}
						if result.Cancelled {
							return errors.New("replacement was cancelled")
						}
						return nil
					},
				})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			createRuntime := func(_ string, _ CLIArgs, prior engine.AgentMessages) (runtimeInputs, error) {
				created := engine.NewAgent(
					provider.StreamSimple, engine.WithInitialState(engine.AgentState{
						SystemPrompt: "test", Model: provider.GetModel(), Messages: prior,
					}),
					engine.WithConvertToLLM(agent.ConvertToLLM),
				)
				return runtimeInputs{Agent: created, Extensions: registry}, nil
			}
			var stdout, stderr bytes.Buffer
			code := runCLIWithDependencies(context.Background(), test.argv, cliStreams{
				Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
				StdinTTY: true, StdoutTTY: false,
			}, cliDependencies{createRuntime: createRuntime})
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			want := "start:startup,before:new,shutdown:new,start:new,with-session,shutdown:quit"
			if got := strings.Join(events, ","); got != want {
				t.Fatalf("session lifecycle = %q, want %q", got, want)
			}
		})
	}
}

func TestRunCLIHeadlessModesContinuePromptingReplacementSession(t *testing.T) {
	for _, test := range []struct {
		name string
		argv []string
	}{
		{name: "print", argv: []string{"-p", "--no-session", "--model", "faux-1", "/replace-session", "second prompt"}},
		{name: "json", argv: []string{"--mode", "json", "--no-session", "--model", "faux-1", "/replace-session", "second prompt"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
			provider := faux.New()
			provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("replacement answer")})
			registry := extensions.NewRegistry(root)
			var replacementID, promptedSessionID string
			if err := registry.Register("<headless-rebind>", func(api extensions.API) error {
				api.On(extensions.EventBeforeAgentStart, func(_ context.Context, _ extensions.Event, extensionContext extensions.Context) (any, error) {
					promptedSessionID = extensionContext.SessionManager().GetSessionID()
					return nil, nil
				})
				api.RegisterCommand("replace-session", extensions.Command{
					Handler: func(ctx context.Context, _ string, commandContext extensions.CommandContext) error {
						result, err := commandContext.NewSession(ctx, &extensions.NewSessionOptions{
							WithSession: func(_ context.Context, replaced extensions.ReplacedSessionContext) error {
								replacementID = replaced.SessionManager().GetSessionID()
								return nil
							},
						})
						if err != nil {
							return err
						}
						if result.Cancelled {
							return errors.New("replacement was cancelled")
						}
						return nil
					},
				})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			createRuntime := func(_ string, _ CLIArgs, prior engine.AgentMessages) (runtimeInputs, error) {
				created := engine.NewAgent(
					provider.StreamSimple, engine.WithInitialState(engine.AgentState{
						SystemPrompt: "test", Model: provider.GetModel(), Messages: prior,
					}),
					engine.WithConvertToLLM(agent.ConvertToLLM),
				)
				return runtimeInputs{Agent: created, Extensions: registry}, nil
			}
			var stdout, stderr bytes.Buffer
			code := runCLIWithDependencies(context.Background(), test.argv, cliStreams{
				Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
				StdinTTY: true, StdoutTTY: false,
			}, cliDependencies{createRuntime: createRuntime})
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if replacementID == "" {
				t.Fatal("replacement withSession callback was not called")
			}
			if promptedSessionID != replacementID {
				t.Fatalf("second prompt session = %q, want replacement %q", promptedSessionID, replacementID)
			}
			if !strings.Contains(stdout.String(), "replacement answer") {
				t.Fatalf("headless output missed replacement response: %q", stdout.String())
			}
		})
	}
}

func TestRunCLIJSONMovesEventSubscriptionBeforeReplacementWithSession(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("replacement stream")})
	registry := extensions.NewRegistry(root)
	withSessionCalled := false
	if err := registry.Register("<json-rebind>", func(api extensions.API) error {
		api.RegisterCommand("replace-and-prompt", extensions.Command{
			Handler: func(ctx context.Context, _ string, commandContext extensions.CommandContext) error {
				_, err := commandContext.NewSession(ctx, &extensions.NewSessionOptions{
					WithSession: func(ctx context.Context, replaced extensions.ReplacedSessionContext) error {
						withSessionCalled = true
						return replaced.SendUserMessage(ctx, ai.NewUserText("prompt from replacement"), nil)
					},
				})
				return err
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	createRuntime := func(_ string, _ CLIArgs, prior engine.AgentMessages) (runtimeInputs, error) {
		created := engine.NewAgent(
			provider.StreamSimple, engine.WithInitialState(engine.AgentState{
				SystemPrompt: "test", Model: provider.GetModel(), Messages: prior,
			}),
			engine.WithConvertToLLM(agent.ConvertToLLM),
		)
		fresh, err := registry.Fresh(root) // each runtime gets its own instances, as createRuntimeInputs builds them
		return runtimeInputs{Agent: created, Extensions: fresh}, err
	}
	var stdout, stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{
		"--mode", "json", "--no-session", "--model", "faux-1", "/replace-and-prompt",
	}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		StdinTTY: true, StdoutTTY: false,
	}, cliDependencies{createRuntime: createRuntime})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !withSessionCalled {
		t.Fatal("replacement withSession callback was not called")
	}
	if !strings.Contains(stdout.String(), `"text":"replacement stream"`) {
		t.Fatalf("JSON stream missed replacement session events: %q", stdout.String())
	}
}

func TestRunCLIRPCMovesEventSubscriptionForExtensionInitiatedReplacement(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("RPC replacement stream")})
	registry := extensions.NewRegistry(root)
	withSessionDone := make(chan struct{})
	if err := registry.Register("<rpc-extension-rebind>", func(api extensions.API) error {
		api.RegisterCommand("replace-and-prompt", extensions.Command{
			Handler: func(ctx context.Context, _ string, commandContext extensions.CommandContext) error {
				_, err := commandContext.NewSession(ctx, &extensions.NewSessionOptions{
					WithSession: func(ctx context.Context, replaced extensions.ReplacedSessionContext) error {
						err := replaced.SendUserMessage(ctx, ai.NewUserText("RPC prompt from replacement"), nil)
						close(withSessionDone)
						return err
					},
				})
				return err
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	createRuntime := func(_ string, _ CLIArgs, prior engine.AgentMessages) (runtimeInputs, error) {
		created := engine.NewAgent(
			provider.StreamSimple, engine.WithInitialState(engine.AgentState{
				SystemPrompt: "test", Model: provider.GetModel(), Messages: prior,
			}),
			engine.WithConvertToLLM(agent.ConvertToLLM),
		)
		fresh, err := registry.Fresh(root)
		return runtimeInputs{Agent: created, Extensions: fresh}, err
	}
	input, inputWriter := io.Pipe()
	output, outputWriter := io.Pipe()
	var stderr bytes.Buffer
	rpcContext, cancelRPC := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRPC()
	done := make(chan int, 1)
	go func() {
		code := runCLIWithDependencies(rpcContext, []string{
			"--mode", "rpc", "--no-session", "--model", "faux-1",
		}, cliStreams{
			Stdin: input, Stdout: outputWriter, Stderr: &stderr,
			StdinTTY: true, StdoutTTY: false,
		}, cliDependencies{createRuntime: createRuntime})
		_ = outputWriter.Close()
		done <- code
	}()
	if _, err := io.WriteString(inputWriter, `{"id":"replace","type":"prompt","message":"/replace-and-prompt"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(output)
	lines := make(chan []byte, 64)
	readErrors := make(chan error, 1)
	go func() {
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				lines <- line
			}
			if err != nil {
				readErrors <- err
				return
			}
		}
	}()
	select {
	case <-withSessionDone:
	case <-rpcContext.Done():
		t.Fatal("extension-initiated replacement prompt did not finish")
	}
	if _, err := io.WriteString(inputWriter, `{"id":"barrier","type":"get_state"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	seenPromptResponse, seenReplacementAssistant, seenSettled := false, false, false
	for {
		select {
		case line := <-lines:
			seenPromptResponse = seenPromptResponse || bytes.Contains(line, []byte(`"id":"replace","type":"response","command":"prompt","success":true`))
			seenReplacementAssistant = seenReplacementAssistant ||
				bytes.Contains(line, []byte(`"type":"message_end"`)) &&
					bytes.Contains(line, []byte(`RPC replacement stream`))
			seenSettled = seenSettled || bytes.Contains(line, []byte(`"type":"agent_settled"`))
			if bytes.Contains(line, []byte(`"id":"barrier","type":"response","command":"get_state","success":true`)) {
				goto streamComplete
			}
		case err := <-readErrors:
			t.Fatalf("read RPC replacement events: %v", err)
		case <-rpcContext.Done():
			t.Fatal("RPC replacement event stream timed out")
		}
	}

streamComplete:
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
	case <-rpcContext.Done():
		t.Fatal("RPC mode did not stop")
	}
	if !seenPromptResponse || !seenReplacementAssistant || !seenSettled {
		t.Fatalf(
			"RPC replacement stream = response %t, assistant %t, settled %t",
			seenPromptResponse, seenReplacementAssistant, seenSettled,
		)
	}
}

func fauxRuntimeFactory(provider *faux.Provider) func(string, CLIArgs, engine.AgentMessages) (runtimeInputs, error) {
	return func(_ string, _ CLIArgs, prior engine.AgentMessages) (runtimeInputs, error) {
		created := engine.NewAgent(
			provider.StreamSimple, engine.WithInitialState(engine.AgentState{
				SystemPrompt: "test",
				Model:        provider.GetModel(),
				Messages:     prior,
			}),
			engine.WithConvertToLLM(agent.ConvertToLLM),
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
