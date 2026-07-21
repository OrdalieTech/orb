package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/session"
)

func TestResolveSessionArgumentPrefersLocalExactThenPrefix(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := session.DefaultSessionDir(project, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	createCLIStoredSession(t, project, dir, "abcdef01")
	createCLIStoredSession(t, project, dir, "abc99999")

	exact, err := resolveSessionArgument("abc99999", project, "", agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if exact.kind != "local" || !strings.Contains(exact.path, "abc99999") {
		t.Fatalf("exact resolution = %+v", exact)
	}
	prefix, err := resolveSessionArgument("abcdef", project, "", agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if prefix.kind != "local" || !strings.Contains(prefix.path, "abcdef01") {
		t.Fatalf("prefix resolution = %+v", prefix)
	}
	path, err := resolveSessionArgument("relative.jsonl", project, "", agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if path.kind != "path" || path.path != filepath.Join(project, "relative.jsonl") {
		t.Fatalf("path resolution = %+v", path)
	}
	if _, err := resolveSessionArgument("file://remote/tmp/session.jsonl", project, "", agentDir); err == nil {
		t.Fatal("remote file URL was accepted")
	}
}

func TestCreateCLISessionForkResumeAndExactID(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)
	dir, err := session.DefaultSessionDir(project, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	source := createCLIStoredSession(t, project, dir, "source-id")

	selector := func(current, _ SessionListLoader) (string, bool, error) {
		listed := current(nil)
		if len(listed) != 1 {
			t.Fatalf("current sessions = %#v", listed)
		}
		return listed[0].Path, true, nil
	}
	streams := cliStreams{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, StdinTTY: true, StdoutTTY: true}
	manager, _, err := createCLISession(project, CLIArgs{Resume: true}, streams, selector)
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetSessionFile() != source.GetSessionFile() {
		t.Fatalf("resume opened %q, want %q", manager.GetSessionFile(), source.GetSessionFile())
	}

	forkArg := "source"
	forked, _, err := createCLISession(project, CLIArgs{Fork: &forkArg}, streams, nil)
	if err != nil {
		t.Fatal(err)
	}
	header := forked.GetHeader()
	if header == nil || header.ParentSession == nil || *header.ParentSession != source.GetSessionFile() {
		t.Fatalf("fork header = %+v", header)
	}

	exactID := "exact-new"
	var warning bytes.Buffer
	created, _, err := createCLISession(project, CLIArgs{SessionID: &exactID}, cliStreams{Stderr: &warning}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.GetSessionID() != exactID || !strings.Contains(warning.String(), "creating a new session") {
		t.Fatalf("exact-id create = %q warning %q", created.GetSessionID(), warning.String())
	}
}

func TestTUISessionSelectorAdapterPreservesLoadersAndResult(t *testing.T) {
	currentProgress, allProgress := false, false
	current := func(progress session.SessionListProgress) []session.SessionInfo {
		progress(1, 2)
		return []session.SessionInfo{{Path: "/current.jsonl"}}
	}
	all := func(progress session.SessionListProgress) []session.SessionInfo {
		progress(3, 4)
		return []session.SessionInfo{{Path: "/all.jsonl"}}
	}
	runnerCalled := false
	selector := newTUISessionSelector(context.Background(), func(_ context.Context, gotCurrent, gotAll SessionListLoader) (string, bool, error) {
		runnerCalled = true
		if listed := gotCurrent(func(loaded, total int) { currentProgress = loaded == 1 && total == 2 }); len(listed) != 1 || listed[0].Path != "/current.jsonl" {
			t.Fatalf("current sessions = %#v", listed)
		}
		if listed := gotAll(func(loaded, total int) { allProgress = loaded == 3 && total == 4 }); len(listed) != 1 || listed[0].Path != "/all.jsonl" {
			t.Fatalf("all sessions = %#v", listed)
		}
		return "/selected.jsonl", true, nil
	})
	path, selected, err := selector(current, all)
	if err != nil || !selected || path != "/selected.jsonl" || !runnerCalled || !currentProgress || !allProgress {
		t.Fatalf("path=%q selected=%t err=%v called=%t progress=%t/%t", path, selected, err, runnerCalled, currentProgress, allProgress)
	}
}

func TestValidateSessionFlagsMatchesUpstreamConflicts(t *testing.T) {
	fork, selected, sessionID := "source", "target", "id"
	validationErrors := validateSessionFlags(CLIArgs{
		Fork: &fork, Session: &selected, Continue: true, Resume: true, NoSession: true, SessionID: &sessionID,
	})
	if len(validationErrors) != 2 || validationErrors[0] != "--fork cannot be combined with --session, --continue, --resume, --no-session" ||
		validationErrors[1] != "--session-id cannot be combined with --session, --continue, --resume" {
		t.Fatalf("validation errors = %#v", validationErrors)
	}
	empty := ""
	if got := validateSessionFlags(CLIArgs{Fork: &empty, Session: &empty, Continue: true}); len(got) != 0 {
		t.Fatalf("empty string flags are falsey upstream, got errors %#v", got)
	}
}

func TestRunCLISessionValidationPrecedesHelpAndStopsAtFirstError(t *testing.T) {
	fork, selected := "source", "target"
	var stdout, stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{
		"--help", "--fork", fork, "--session", selected, "--session-id", ".invalid",
	}, cliStreams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, cliDependencies{})
	want := "Error: --fork cannot be combined with --session\n"
	if code != 1 || stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("code=%d stdout=%q stderr=%q, want stderr %q", code, stdout.String(), stderr.String(), want)
	}

	stdout.Reset()
	stderr.Reset()
	code = runCLIWithDependencies(context.Background(), []string{
		"--help", "--session-id", ".invalid",
	}, cliStreams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, cliDependencies{})
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "Session id must be non-empty") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestConfirmGlobalSessionForkReadsNonTerminalInputLikeUpstream(t *testing.T) {
	var output bytes.Buffer
	confirmed, err := confirmGlobalSessionFork(cliStreams{
		Stdin: strings.NewReader("yes\n"), Stdout: &output, StdinTTY: false,
	}, "/other/project")
	if err != nil || !confirmed {
		t.Fatalf("confirmed=%t err=%v output=%q", confirmed, err, output.String())
	}
	confirmed, err = confirmGlobalSessionFork(cliStreams{
		Stdin: strings.NewReader(" y \n"), Stdout: io.Discard,
	}, "/other/project")
	if err != nil || confirmed {
		t.Fatalf("space-padded answer confirmed=%t err=%v", confirmed, err)
	}
}

func TestCreateCLISessionConfirmsGlobalIDBeforeForking(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "current")
	other := filepath.Join(root, "other")
	agentDir := filepath.Join(root, "agent")
	for _, directory := range []string{current, other} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(config.EnvAgentDir, agentDir)
	otherSessionDir, err := session.DefaultSessionDir(other, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	source := createCLIStoredSession(t, other, otherSessionDir, "global-source")
	argument := "global"

	var output bytes.Buffer
	forked, _, err := createCLISession(current, CLIArgs{Session: &argument}, cliStreams{
		Stdin: strings.NewReader("yes\n"), Stdout: &output,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	header := forked.GetHeader()
	if header == nil || header.CWD != current || header.ParentSession == nil || *header.ParentSession != source.GetSessionFile() {
		t.Fatalf("forked global header = %+v", header)
	}
	if !strings.Contains(output.String(), "Session found in different project: "+other) {
		t.Fatalf("confirmation output = %q", output.String())
	}

	output.Reset()
	_, _, err = createCLISession(current, CLIArgs{Session: &argument}, cliStreams{
		Stdin: strings.NewReader("no\n"), Stdout: &output,
	}, nil)
	if !errors.Is(err, errNoSessionSelected) || !strings.HasSuffix(output.String(), "Aborted.\n") {
		t.Fatalf("declined global session err=%v output=%q", err, output.String())
	}
}

func TestRunCLIExportRoutesBeforeRuntime(t *testing.T) {
	root := t.TempDir()
	manager := createCLIStoredSession(t, root, filepath.Join(root, "sessions"), "export-route")
	createdRuntime := false
	dependencies := cliDependencies{createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
		createdRuntime = true
		return runtimeInputs{}, nil
	}}

	for _, test := range []struct {
		name       string
		extension  string
		wantMarker string
	}{
		{name: "html", extension: ".html", wantMarker: `id="session-data"`},
		{name: "output extension does not change format", extension: ".md", wantMarker: `id="session-data"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputPath := filepath.Join(root, test.name+test.extension)
			var stdout, stderr bytes.Buffer
			code := runCLIWithDependencies(context.Background(), []string{
				"--export", manager.GetSessionFile(), outputPath,
			}, cliStreams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, dependencies)
			if code != 0 || stderr.Len() != 0 || stdout.String() != "Exported to: "+outputPath+"\n" {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			contents, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(contents), test.wantMarker) {
				t.Fatalf("export %q does not contain %q", outputPath, test.wantMarker)
			}
		})
	}
	if createdRuntime {
		t.Fatal("export initialized the agent runtime")
	}
}

func TestRunCLIResumeRoutesBeforeInteractiveDispatch(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv(config.EnvAgentDir, agentDir)
	dir, err := session.DefaultSessionDir(project, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	source := createCLIStoredSession(t, project, dir, "resume-route")

	selected := false
	selector := func(current, _ SessionListLoader) (string, bool, error) {
		selected = true
		listed := current(nil)
		return listed[0].Path, true, nil
	}

	t.Run("print mode continues selected session", func(t *testing.T) {
		provider := faux.New()
		provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("resumed")})
		var stdout, stderr bytes.Buffer
		code := runCLIWithDependencies(context.Background(), []string{"-p", "-r", "next", "--model", "faux-1"}, cliStreams{
			Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, StdinTTY: true, StdoutTTY: false,
		}, cliDependencies{createRuntime: fauxRuntimeFactory(provider), selectSession: selector})
		if code != 0 || stdout.String() != "resumed\n" || stderr.Len() != 0 || !selected {
			t.Fatalf("code=%d selected=%t stdout=%q stderr=%q", code, selected, stdout.String(), stderr.String())
		}
		reopened, openErr := session.Open(source.GetSessionFile(), dir)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if len(reopened.GetEntries()) <= len(source.GetEntries()) {
			t.Fatalf("resume did not append to selected session: %#v", reopened.GetEntries())
		}
	})

	t.Run("bare resume selects before TUI initialization", func(t *testing.T) {
		var stderr bytes.Buffer
		createdRuntime := false
		selected = false
		code := runCLIWithDependencies(context.Background(), []string{"-r"}, cliStreams{
			Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr, StdinTTY: true, StdoutTTY: true,
		}, cliDependencies{
			selectSession: selector,
			createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
				createdRuntime = true
				return runtimeInputs{}, errors.New("interactive fixture stop")
			},
		})
		if code != 1 || !selected || !createdRuntime || !strings.Contains(stderr.String(), "interactive fixture stop") {
			t.Fatalf("code=%d selected=%t createdRuntime=%t stderr=%q", code, selected, createdRuntime, stderr.String())
		}
	})

	t.Run("selector cancellation exits zero", func(t *testing.T) {
		var stdout bytes.Buffer
		code := runCLIWithDependencies(context.Background(), []string{"-r"}, cliStreams{
			Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: io.Discard, StdinTTY: true, StdoutTTY: true,
		}, cliDependencies{selectSession: func(SessionListLoader, SessionListLoader) (string, bool, error) {
			return "", false, nil
		}})
		if code != 0 || stdout.String() != "No session selected\n" {
			t.Fatalf("code=%d stdout=%q", code, stdout.String())
		}
	})
}

func TestRunCLISessionSelectionForkExactIDAndNameEndToEnd(t *testing.T) {
	for _, selection := range []struct {
		name     string
		argument func(*session.SessionManager) string
	}{
		{name: "local id prefix", argument: func(*session.SessionManager) string { return "source" }},
		{name: "direct path", argument: func(manager *session.SessionManager) string { return manager.GetSessionFile() }},
	} {
		t.Run("session "+selection.name, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "project")
			agentDir := filepath.Join(root, "agent")
			if err := os.MkdirAll(project, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(project)
			t.Setenv(config.EnvAgentDir, agentDir)
			dir, err := session.DefaultSessionDir(project, agentDir)
			if err != nil {
				t.Fatal(err)
			}
			source := createCLIStoredSession(t, project, dir, "source-session")
			before := len(source.GetEntries())
			gotCWD := runCLIFauxSessionCommand(t, []string{
				"--session", selection.argument(source), "-p", "next", "--model", "faux-1",
			})
			if gotCWD != project {
				t.Fatalf("runtime cwd = %q, want %q", gotCWD, project)
			}
			reopened, err := session.Open(source.GetSessionFile(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(reopened.GetEntries()) <= before {
				t.Fatalf("selected session was not appended: %#v", reopened.GetEntries())
			}
		})
	}

	t.Run("fork and name", func(t *testing.T) {
		root := t.TempDir()
		current := filepath.Join(root, "current")
		sourceCWD := filepath.Join(root, "source")
		agentDir := filepath.Join(root, "agent")
		for _, directory := range []string{current, sourceCWD} {
			if err := os.MkdirAll(directory, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Chdir(current)
		t.Setenv(config.EnvAgentDir, agentDir)
		sourceDir, err := session.DefaultSessionDir(sourceCWD, agentDir)
		if err != nil {
			t.Fatal(err)
		}
		source := createCLIStoredSession(t, sourceCWD, sourceDir, "fork-source")
		gotCWD := runCLIFauxSessionCommand(t, []string{
			"--fork", source.GetSessionFile(), "-n", "Forked session", "-p", "next", "--model", "faux-1",
		})
		if gotCWD != current {
			t.Fatalf("runtime cwd = %q, want %q", gotCWD, current)
		}
		currentDir, err := session.DefaultSessionDir(current, agentDir)
		if err != nil {
			t.Fatal(err)
		}
		listed := session.List(current, currentDir, nil, session.WithAgentDir(agentDir))
		if len(listed) != 1 {
			t.Fatalf("forked sessions = %#v", listed)
		}
		forked, err := session.Open(listed[0].Path, currentDir)
		if err != nil {
			t.Fatal(err)
		}
		header := forked.GetHeader()
		name := forked.GetSessionName()
		if header == nil || header.CWD != current || header.ParentSession == nil || *header.ParentSession != source.GetSessionFile() ||
			name == nil || *name != "Forked session" {
			t.Fatalf("forked header/name = %+v/%v", header, name)
		}
	})

	t.Run("exact id reuses project session", func(t *testing.T) {
		root := t.TempDir()
		project := filepath.Join(root, "project")
		agentDir := filepath.Join(root, "agent")
		if err := os.MkdirAll(project, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(project)
		t.Setenv(config.EnvAgentDir, agentDir)
		dir, err := session.DefaultSessionDir(project, agentDir)
		if err != nil {
			t.Fatal(err)
		}
		source := createCLIStoredSession(t, project, dir, "exact-session")
		before := len(source.GetEntries())
		gotCWD := runCLIFauxSessionCommand(t, []string{
			"--session-id", "exact-session", "-p", "next", "--model", "faux-1",
		})
		if gotCWD != project {
			t.Fatalf("runtime cwd = %q, want %q", gotCWD, project)
		}
		listed := session.List(project, dir, nil, session.WithAgentDir(agentDir))
		if len(listed) != 1 || listed[0].ID != "exact-session" {
			t.Fatalf("exact-id sessions = %#v", listed)
		}
		reopened, err := session.Open(source.GetSessionFile(), dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(reopened.GetEntries()) <= before {
			t.Fatalf("exact-id session was not reused: %#v", reopened.GetEntries())
		}
	})
}

func TestMissingSessionCWDReturnsStructuredIssue(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "missing-project")
	current := filepath.Join(root, "current")
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(current, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := session.DefaultSessionDir(project, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	stored := createCLIStoredSession(t, project, dir, "missing-cwd")
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)
	path := stored.GetSessionFile()
	manager, _, err := createCLISession(current, CLIArgs{Session: &path}, cliStreams{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	issue := getMissingSessionCWDIssue(manager, current)
	if issue == nil || issue.StoredCWD != project || issue.SessionFile != path || issue.CurrentCWD != current {
		t.Fatalf("missing cwd issue = %#v", issue)
	}
}

func TestRunCLIMissingSessionCWDModeSplit(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "missing-project")
	current := filepath.Join(root, "current")
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(current, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := session.DefaultSessionDir(project, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	stored := createCLIStoredSession(t, project, dir, "missing-cwd-mode-split")
	path := stored.GetSessionFile()
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}
	t.Chdir(current)
	t.Setenv(config.EnvAgentDir, agentDir)

	t.Run("headless reports the structured error before runtime creation", func(t *testing.T) {
		created := false
		var stderr bytes.Buffer
		code := runCLIWithDependencies(context.Background(), []string{"-p", "--session", path, "prompt"}, cliStreams{
			Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr, StdinTTY: true,
		}, cliDependencies{createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
			created = true
			return runtimeInputs{}, nil
		}})
		if code != 1 || created || !strings.Contains(stderr.String(), "Stored session working directory does not exist: "+project) {
			t.Fatalf("code=%d created=%t stderr=%q", code, created, stderr.String())
		}
	})

	t.Run("interactive continues in current cwd after selector confirmation", func(t *testing.T) {
		selected := false
		createdCWD := ""
		var stderr bytes.Buffer
		code := runCLIWithDependencies(context.Background(), []string{"--session", path}, cliStreams{
			Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr, StdinTTY: true, StdoutTTY: true,
		}, cliDependencies{
			selectMissingSessionCWD: func(_ context.Context, issue *MissingSessionCWDError) (string, bool, error) {
				selected = true
				if issue.StoredCWD != project || issue.CurrentCWD != current || issue.SessionFile != path {
					t.Fatalf("selector issue = %#v", issue)
				}
				return current, true, nil
			},
			createRuntime: func(cwd string, _ CLIArgs, _ agent.AgentMessages) (runtimeInputs, error) {
				createdCWD = cwd
				return runtimeInputs{}, errors.New("interactive fixture stop")
			},
		})
		if code != 1 || !selected || createdCWD != current || !strings.Contains(stderr.String(), "interactive fixture stop") {
			t.Fatalf("code=%d selected=%t cwd=%q stderr=%q", code, selected, createdCWD, stderr.String())
		}
	})

	t.Run("interactive cancellation exits without creating a runtime", func(t *testing.T) {
		created := false
		code := runCLIWithDependencies(context.Background(), []string{"--session", path}, cliStreams{
			Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, StdinTTY: true, StdoutTTY: true,
		}, cliDependencies{
			selectMissingSessionCWD: func(context.Context, *MissingSessionCWDError) (string, bool, error) {
				return "", false, nil
			},
			createRuntime: func(string, CLIArgs, agent.AgentMessages) (runtimeInputs, error) {
				created = true
				return runtimeInputs{}, nil
			},
		})
		if code != 0 || created {
			t.Fatalf("code=%d created=%t", code, created)
		}
	})
}

func createCLIStoredSession(t *testing.T, cwd, sessionDir, id string) *session.SessionManager {
	t.Helper()
	manager, err := session.Create(cwd, sessionDir, session.WithSessionID(id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(map[string]any{"role": "user", "content": id, "timestamp": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "answer"}},
		"api": "openai-responses", "provider": "openai", "model": "gpt-test", "usage": map[string]any{},
		"stopReason": "stop", "timestamp": 2,
	}); err != nil {
		t.Fatal(err)
	}
	return manager
}

func runCLIFauxSessionCommand(t *testing.T, argv []string) string {
	t.Helper()
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("completed")})
	baseFactory := fauxRuntimeFactory(provider)
	gotCWD := ""
	var stdout, stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), argv, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, StdinTTY: true,
	}, cliDependencies{createRuntime: func(cwd string, args CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
		gotCWD = cwd
		return baseFactory(cwd, args, prior)
	}})
	if code != 0 || stdout.String() != "completed\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	return gotCWD
}
