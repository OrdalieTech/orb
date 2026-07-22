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
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/chat"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/modes"
	"github.com/OrdalieTech/pigo/codingagent/session"
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

func TestRunCLIOfflineFlagSetsEnvironmentBeforeRuntimeCreation(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	t.Setenv("PI_OFFLINE", "0")
	called := false
	code := runCLIWithDependencies(context.Background(), []string{"--offline"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
		called = true
		if value := os.Getenv("PI_OFFLINE"); value != "1" {
			t.Fatalf("PI_OFFLINE = %q, want 1", value)
		}
		if value := os.Getenv("PI_SKIP_VERSION_CHECK"); value != "1" {
			t.Fatalf("PI_SKIP_VERSION_CHECK = %q, want 1", value)
		}
		return runtimeInputs{}, errors.New("stop after environment check")
	}})
	if code != 1 || !called {
		t.Fatalf("code=%d called=%t", code, called)
	}
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
		if request.Header.Get("User-Agent") != "pigo/1.2.3" || request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("headers = %#v", request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.2.4"}`))}, nil
	})}

	check := newStartupVersionCheck("1.2.3", client, latestReleaseURL, time.Second)
	ui := &versionNotificationUI{}
	check(context.Background(), ui)
	if requests != 1 || len(ui.messages) != 1 || ui.messages[0] != "pigo v1.2.4 is available. Run: pigo update" {
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

func TestRunCLIProvidesStartupVersionCheckToInteractiveMode(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	agentDir := filepath.Join(root, "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("PI_SKIP_VERSION_CHECK", "1")
	offline, hadOffline := os.LookupEnv("PI_OFFLINE")
	if err := os.Unsetenv("PI_OFFLINE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadOffline {
			_ = os.Setenv("PI_OFFLINE", offline)
		} else {
			_ = os.Unsetenv("PI_OFFLINE")
		}
	})
	wired := false
	refreshCalls := 0
	baseFactory := fauxRuntimeFactory(faux.New())
	code := runCLIWithDependencies(context.Background(), []string{"--no-session"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{
		createRuntime: func(cwd string, args CLIArgs, messages agent.AgentMessages) (runtimeInputs, error) {
			inputs, err := baseFactory(cwd, args, messages)
			if err != nil {
				return runtimeInputs{}, err
			}
			inputs.ModelRegistry, err = config.NewModelRegistry(agentDir)
			return inputs, err
		},
		refreshModels: func(context.Context, string) error {
			refreshCalls++
			return nil
		},
		runInteractive: func(ctx context.Context, runtime *codingagent.SessionRuntime, options modes.InteractiveModeOptions) int {
			defer runtime.Dispose()
			if options.StartupVersionCheck == nil {
				t.Fatal("StartupVersionCheck is nil")
			}
			if options.StartupModelRefresh == nil {
				t.Fatal("StartupModelRefresh is nil")
			}
			if refreshCalls != 0 {
				t.Fatalf("model refresh started before interactive mode: %d", refreshCalls)
			}
			options.StartupVersionCheck(ctx, extensions.NoopUI{})
			if err := options.StartupModelRefresh(ctx); err != nil {
				t.Fatal(err)
			}
			if refreshCalls != 1 {
				t.Fatalf("model refresh calls = %d, want 1", refreshCalls)
			}
			wired = true
			return 0
		},
	})
	if code != 0 || !wired {
		t.Fatalf("code=%d wired=%t", code, wired)
	}
}

func TestRunCLIVersionIncludesUpstreamIdentity(t *testing.T) {
	data, err := os.ReadFile("../../UPSTREAM.lock")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if code := runCLI(context.Background(), []string{"--version"}, cliStreams{Stdout: &stdout}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	want := fmt.Sprintf("pigo %s (upstream pi %s @ %.8s)\n", version, lock.Version, lock.Commit)
	if stdout.String() != want {
		t.Fatalf("version = %q, want %q", stdout.String(), want)
	}
}

func TestStartupModelRefreshModes(t *testing.T) {
	for _, test := range []struct {
		mode         string
		offline      bool
		allowNetwork bool
		want         bool
	}{
		{mode: "interactive", allowNetwork: true, want: true},
		{mode: "interactive"},
		{mode: "rpc", want: true},
		{mode: ""},
		{mode: "text"},
		{mode: "json"},
		{mode: "interactive", offline: true, allowNetwork: true},
		{mode: "rpc", offline: true},
	} {
		if got := startupModelRefreshEnabled(test.mode, test.offline, test.allowNetwork); got != test.want {
			t.Errorf("mode=%q offline=%t allowNetwork=%t: enabled=%t, want %t", test.mode, test.offline, test.allowNetwork, got, test.want)
		}
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

func TestRunCLIChatCommand(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("DISCORD_BOT_TOKEN", "")
	t.Setenv("PIGO_CHAT_ALLOWED_SENDERS", "")

	var stdout, stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"chat", "--help"}, cliStreams{
		Stdout: &stdout, Stderr: &stderr,
	}, cliDependencies{})
	if code != 0 || stdout.String() != chatHelpText || stderr.Len() != 0 {
		t.Fatalf("help: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	t.Setenv("PIGO_CHAT_ALLOWED_SENDERS", "123")
	code = runCLIWithDependencies(context.Background(), []string{"chat", "telegram"}, cliStreams{
		Stdout: &stdout, Stderr: &stderr,
	}, cliDependencies{})
	if code != 1 || stderr.String() != "Error: TELEGRAM_BOT_TOKEN is required\n" {
		t.Fatalf("missing token: code=%d stderr=%q", code, stderr.String())
	}

	stderr.Reset()
	code = runCLIWithDependencies(context.Background(), []string{"chat", "discord"}, cliStreams{
		Stdout: &stdout, Stderr: &stderr,
	}, cliDependencies{})
	if code != 1 || stderr.String() != "Error: DISCORD_BOT_TOKEN is required\n" {
		t.Fatalf("missing Discord token: code=%d stderr=%q", code, stderr.String())
	}

	stderr.Reset()
	code = runCLIWithDependencies(context.Background(), []string{"chat", "unknown"}, cliStreams{
		Stdout: &stdout, Stderr: &stderr,
	}, cliDependencies{})
	if code != 1 || stderr.String() != "Error: unsupported chat platform \"unknown\"\n" {
		t.Fatalf("unknown platform: code=%d stderr=%q", code, stderr.String())
	}

	authorize, err := chatAuthorizer("123, 456")
	if err != nil || authorize(chat.Message{SenderID: "456"}) != nil {
		t.Fatalf("allowed sender failed: %v", err)
	}
	if err := authorize(chat.Message{SenderID: "789"}); err == nil {
		t.Fatal("unexpectedly allowed sender 789")
	}

	t.Setenv("PIGO_CHAT_PATH", "missing-slash")
	ingress := webhookIngress("fixture", func(func(chat.Message) error) http.Handler { return http.NotFoundHandler() })
	if err := ingress(context.Background(), func(chat.Message) error { return nil }); err == nil {
		t.Fatal("webhook ingress accepted an invalid path")
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
	// Upstream runs listModels after full runtime creation (main.ts:747-764) so
	// extension-registered providers are listed; the run must stay read-only.
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	var stdout bytes.Buffer
	createdRuntime := false
	code := runCLIWithDependencies(context.Background(), []string{"--list-models"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: io.Discard, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{
		createRuntime: func(cwd string, args CLIArgs, messages agent.AgentMessages) (runtimeInputs, error) {
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
			wantStdout: versionOutput() + "\n",
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

func TestRunCLIRejectsUnknownExtensionFlagBeforeRuntime(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	var stderr bytes.Buffer
	called := false
	code := runCLIWithDependencies(context.Background(), []string{"-p", "--unknown"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr, StdinTTY: true,
	}, cliDependencies{createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
		called = true
		return runtimeInputs{}, nil
	}})
	if code != 1 || called || stderr.String() != "Error: Unknown option: --unknown\n" {
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

func TestRunCLIAllowsMissingModelOnlyForInteractiveRuntime(t *testing.T) {
	for _, test := range []struct {
		name        string
		argv        []string
		stdoutTTY   bool
		wantAllowed bool
	}{
		{name: "interactive", stdoutTTY: true, wantAllowed: true},
		{name: "redirected print", argv: []string{"prompt", "--no-session"}, stdoutTTY: false},
		{name: "explicit print", argv: []string{"-p", "prompt", "--no-session"}, stdoutTTY: true},
		{name: "json", argv: []string{"--mode", "json", "prompt", "--no-session"}, stdoutTTY: true},
		{name: "rpc", argv: []string{"--mode", "rpc", "--no-session"}, stdoutTTY: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
			called := false
			allowed := false
			code := runCLIWithDependencies(context.Background(), test.argv, cliStreams{
				Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
				StdinTTY: true, StdoutTTY: test.stdoutTTY,
			}, cliDependencies{createRuntime: func(_ string, args CLIArgs, _ agent.AgentMessages) (runtimeInputs, error) {
				called = true
				allowed = args.allowNoModel
				return runtimeInputs{}, errors.New("stop after runtime arguments")
			}})
			if code != 1 || !called || allowed != test.wantAllowed {
				t.Fatalf("code=%d called=%t allowNoModel=%t, want %t", code, called, allowed, test.wantAllowed)
			}
		})
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
	binary := filepath.Join(t.TempDir(), "pigo")
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
	if !bytes.Contains(output, []byte("Usage: pigo")) || !bytes.Contains(output, []byte("--continue")) {
		t.Fatalf("help output = %q", output)
	}
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
	binary := filepath.Join(temp, "pigo")
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
	if line := exchange("{\"id\":\"\",\"type\":\"get_messages\"}\n"); !bytes.HasPrefix(line, []byte(`{"id":"","type":"response"`)) || !bytes.Contains(line, []byte(`"messages":[]`)) {
		t.Fatalf("empty-ID messages response = %s", line)
	}
	if line := exchange("{\"id\":\"models\",\"type\":\"get_available_models\"}\n"); !bytes.Contains(line, []byte(`"models":[{`)) {
		t.Fatalf("available-models response = %s", line)
	}
	if line := exchange("{\"id\":\"unknown\",\"type\":\"missing\"}\n"); string(line) != `{"id":"unknown","type":"response","command":"missing","success":false,"error":"Unknown command: missing"}` {
		t.Fatalf("unknown response = %s", line)
	}
	if line := exchange("{\"id\":\"bash\",\"type\":\"bash\",\"command\":\"printf false-value\",\"excludeFromContext\":false}\n"); !bytes.Contains(line, []byte(`"output":"false-value"`)) {
		t.Fatalf("bash response = %s", line)
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

func TestRunCLIJSONHelpKeepsEventStdoutClean(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"--mode", "json", "--help"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{})
	if code != 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "Usage: pigo") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
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
			createRuntime := func(_ string, _ CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
				created := agent.NewAgent(
					provider.StreamSimple, agent.WithInitialState(agent.AgentState{
						SystemPrompt: "test", Model: provider.GetModel(), Messages: prior,
					}),
					agent.WithConvertToLLM(codingagent.ConvertToLLM),
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
			createRuntime := func(_ string, _ CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
				created := agent.NewAgent(
					provider.StreamSimple, agent.WithInitialState(agent.AgentState{
						SystemPrompt: "test", Model: provider.GetModel(), Messages: prior,
					}),
					agent.WithConvertToLLM(codingagent.ConvertToLLM),
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
	createRuntime := func(_ string, _ CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
		created := agent.NewAgent(
			provider.StreamSimple, agent.WithInitialState(agent.AgentState{
				SystemPrompt: "test", Model: provider.GetModel(), Messages: prior,
			}),
			agent.WithConvertToLLM(codingagent.ConvertToLLM),
		)
		return runtimeInputs{Agent: created, Extensions: registry}, nil
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
	createRuntime := func(_ string, _ CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
		created := agent.NewAgent(
			provider.StreamSimple, agent.WithInitialState(agent.AgentState{
				SystemPrompt: "test", Model: provider.GetModel(), Messages: prior,
			}),
			agent.WithConvertToLLM(codingagent.ConvertToLLM),
		)
		return runtimeInputs{Agent: created, Extensions: registry}, nil
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

func fauxRuntimeFactory(provider *faux.Provider) func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
	return func(_ string, _ CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
		created := agent.NewAgent(
			provider.StreamSimple, agent.WithInitialState(agent.AgentState{
				SystemPrompt: "test",
				Model:        provider.GetModel(),
				Messages:     prior,
			}),
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
