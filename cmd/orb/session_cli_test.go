package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/agent/session/exporthtml"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/hosts/native"
	"github.com/OrdalieTech/orb/chat"
	"github.com/OrdalieTech/orb/connect/protocol"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/memory"
	"github.com/OrdalieTech/orb/storage/sqlite"
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

func TestFindLocalSessionByExactIDDoesNotRequireValidTranscriptBody(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "exact.jsonl")
	body := `{"type":"session","version":3,"id":"exact","timestamp":"2025-01-01T00:00:00.000Z","cwd":"` + project + `"}` + "\nnot-json\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := findLocalSessionByExactID("exact", project, root, filepath.Join(root, "agent")); got != path {
		t.Fatalf("exact header lookup = %q, want %q", got, path)
	}
	resolved, err := resolveSessionArgument("exact", project, root, filepath.Join(root, "agent"))
	if err != nil || resolved.kind != "local" || resolved.path != path {
		t.Fatalf("exact argument resolution = %+v, err %v", resolved, err)
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

func TestContextTUISessionSelectorAdapterPreservesLoadersAndResult(t *testing.T) {
	progressed := false
	loader := func(_ context.Context, update session.SessionListUpdateFunc) ([]session.SessionInfo, error) {
		update(session.SessionListUpdate{Loaded: 1, Total: 1})
		return []session.SessionInfo{{Path: "/current.jsonl"}}, nil
	}
	selector := newContextTUISessionSelector(context.Background(), func(_ context.Context, current, _ ContextSessionListLoader) (string, bool, error) {
		listed, err := current(context.Background(), func(update session.SessionListUpdate) {
			progressed = update.Loaded == 1 && update.Total == 1
		})
		if err != nil {
			return "", false, err
		}
		if len(listed) != 1 {
			return "", false, errors.New("context loader returned unexpected sessions")
		}
		return listed[0].Path, true, nil
	})
	path, selected, err := selector(loader, loader)
	if err != nil || !selected || path != "/current.jsonl" || !progressed {
		t.Fatalf("path=%q selected=%t progressed=%t err=%v", path, selected, progressed, err)
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
	dependencies := cliDependencies{createRuntime: func(string, CLIArgs, engine.AgentMessages) (runtimeInputs, error) {
		createdRuntime = true
		return runtimeInputs{}, nil
	}}

	for _, test := range []struct {
		name       string
		extension  string
		wantMarker string
	}{
		{name: "html", extension: ".html", wantMarker: `id="session-data"`},
		{name: "markdown output extension routes to the markdown exporter", extension: ".md", wantMarker: "- Session ID: `"},
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
			createRuntime: func(string, CLIArgs, engine.AgentMessages) (runtimeInputs, error) {
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
		}, cliDependencies{createRuntime: func(string, CLIArgs, engine.AgentMessages) (runtimeInputs, error) {
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
			createRuntime: func(cwd string, _ CLIArgs, _ engine.AgentMessages) (runtimeInputs, error) {
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
			createRuntime: func(string, CLIArgs, engine.AgentMessages) (runtimeInputs, error) {
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
	}, cliDependencies{createRuntime: func(cwd string, args CLIArgs, prior engine.AgentMessages) (runtimeInputs, error) {
		gotCWD = cwd
		return baseFactory(cwd, args, prior)
	}})
	if code != 0 || stdout.String() != "completed\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	return gotCWD
}

func TestNativeSessionMigrationRestartAndResume(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(root, "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ORB_BRIDGE_HOME", filepath.Join(root, "bridge"))
	t.Setenv("ORB_CHAT_DATA_DIR", "")
	cwd := t.TempDir()
	legacy, err := session.Create(cwd, "", session.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.AppendMessage(map[string]any{"role": "user", "content": "before migration"}); err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.AppendMessage(map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "saved"}}, "stopReason": "stop"}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(legacy.GetSessionFile())
	if err != nil {
		t.Fatal(err)
	}
	if state, err := openNativeState(ctx, agentDir, false); err == nil {
		_ = state.close()
		t.Fatal("legacy cutover without quiescence")
	}
	state, err := openNativeState(ctx, agentDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if code := runNativeCLI(ctx, []string{"--pi-files", "--help"}, cliStreams{Stdout: io.Discard, Stderr: io.Discard}); code == 0 {
		t.Fatal("compatibility mode reused migrated root")
	}
	id := legacy.GetSessionID()
	args := CLIArgs{Session: &id, native: state}
	manager, _, err := createCLISession(cwd, args, cliStreams{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetSessionFile() != "" || !manager.IsPersisted() {
		t.Fatal("native session has file authority")
	}
	if _, err = manager.AppendSessionInfo("After migration"); err != nil {
		t.Fatal(err)
	}
	if _, err := exporthtml.ExportSession(manager, exporthtml.Options{OutputPath: filepath.Join(root, "session.html")}); err != nil {
		t.Fatal(err)
	}
	if _, err := exporthtml.ExportSessionMarkdown(manager, filepath.Join(root, "session.md")); err != nil {
		t.Fatal(err)
	}

	other, err := openNativeState(ctx, agentDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.bindSession(manager); err == nil {
		t.Fatal("second process owner accepted")
	}
	if _, err := other.deleteSession(id); err == nil {
		t.Fatal("deleted an owned session")
	}
	_ = other.close()
	missing, requested := "missing", "new-id"
	if _, _, err := createCLISession(cwd, CLIArgs{Fork: &missing, SessionID: &requested, native: state}, cliStreams{}, nil); err == nil {
		t.Fatal("forked missing session")
	}
	backup := filepath.Join(root, "backup.db")
	if err := state.db.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	forked, err := state.sessions().Fork(ctx, harness.SessionMetadata{ID: id}, harness.SessionForkOptions{SessionCreateOptions: harness.SessionCreateOptions{CWD: cwd}, Position: harness.ForkAt})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.sessions().Delete(ctx, harness.SessionMetadata{ID: id}); err != nil {
		t.Fatal(err)
	}
	if count, err := state.db.RestoreSessions(ctx, backup, "personal"); err != nil || count != 1 {
		t.Fatalf("restore: %d %v", count, err)
	}
	if _, err := state.sessions().Open(ctx, forked.Metadata()); err != nil {
		t.Fatal("restore removed newer conversation", err)
	}
	if err = state.close(); err != nil {
		t.Fatal(err)
	}
	state, err = openNativeState(ctx, agentDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.close() }()
	args.native = state
	manager, _, err = createCLISession(cwd, args, cliStreams{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetSessionName() == nil || *manager.GetSessionName() != "After migration" {
		t.Fatal("restart lost native write")
	}
	after, err := os.ReadFile(legacy.GetSessionFile())
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("migration changed legacy backup", err)
	}
}

func TestNativeMigrationPreservesCapabilitiesAndFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ORB_BRIDGE_HOME", filepath.Join(root, "bridge"))
	t.Setenv("ORB_CHAT_DATA_DIR", "")
	t.Setenv("PI_OFFLINE", "1")
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(agentDir, "settings.json")
	settingsBytes := []byte(`{"apiKeys":{"test":"legacy-test-key"},"future":{"preserve":true}}`)
	if err := os.WriteFile(settingsPath, settingsBytes, 0600); err != nil {
		t.Fatal(err)
	}
	memoryStore, err := memory.NewFileStore(filepath.Join(agentDir, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := memoryStore.Append(ctx, memory.Item{Content: "remembered", Tags: []string{"project"}})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := memoryStore.Append(ctx, memory.Item{Content: "forgotten"})
	if err != nil {
		t.Fatal(err)
	}
	if err = memoryStore.Delete(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	dir, err := bridgeDir("personal")
	if err != nil {
		t.Fatal(err)
	}
	bridgePath := filepath.Join(dir, "state.json")
	store, err := native.OpenStore(bridgePath, protocol.MaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bridge.Open(store, true)
	if err != nil {
		t.Fatal(err)
	}
	peer := b.PeerID()
	_ = b.Close()
	_ = store.Close()
	originalBridge, err := os.ReadFile(bridgePath)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(agentDir, "chat", "telegram")
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	message := chat.Message{EventID: "pending", Text: "hello"}
	raw, _ := json.Marshal(map[string]any{"m": message})
	spoolPath := filepath.Join(dataDir, "spool.jsonl")
	originalSpool := append(append(append([]byte{}, raw...), '\n'), append(raw, '\n')...)
	originalSpool = append(originalSpool, []byte("{\"ack\":\"pending\"}\n")...)
	if err = os.WriteFile(spoolPath, originalSpool, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := openNativeState(ctx, agentDir, true)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := state.auth(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := credentials.Read(ctx, "test")
	if err != nil || credential == nil || credential.Key == nil || *credential.Key != "legacy-test-key" {
		t.Fatal("legacy auth missing", err)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err = state.accounts(agentDir, credentials).Add(bounded, "test", "default again", credential); err != nil {
		t.Fatal("nested credential transaction", err)
	}
	preferences := filepath.Join(root, "exported-settings.json")
	streams := cliStreams{Stdout: io.Discard, Stderr: io.Discard}
	if runNativeCLI(ctx, []string{"storage", "config", "export", "settings.json", preferences}, streams) != 0 {
		t.Fatal("configuration export failed")
	}
	if err := os.WriteFile(preferences, []byte(`{"theme":"light","future":{"preserve":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if runNativeCLI(ctx, []string{"storage", "config", "import", "settings.json", preferences}, streams) != 0 {
		t.Fatal("configuration import failed")
	}
	settings, err := state.settings(root, agentDir)
	if err != nil || settings.GetTheme() != "light" {
		t.Fatal("configuration import not authoritative", err)
	}
	settings.SetPluginEnabled("memory", true)
	if len(settings.DrainErrors()) != 0 {
		t.Fatal("native settings failed")
	}
	rows, err := state.memory().Query(ctx, memory.Filter{Tags: []string{"project"}})
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatal("memory migration", rows, err)
	}
	queue := state.db.Chat(state.chatNamespace(dataDir))
	pending, err := queue.Pending(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatal("spool migration", pending, err)
	}
	if err = queue.Ack(ctx, "pending"); err != nil {
		t.Fatal(err)
	}
	if err = queue.Put(ctx, chat.Message{EventID: "after-migration", Text: "new"}); err != nil {
		t.Fatal(err)
	}
	store, err = state.bridgeStore(bridgePath, protocol.MaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	b, err = bridge.Open(store, false)
	if err != nil || b.PeerID() != peer {
		t.Fatal("pairing identity changed", err)
	}
	remote, err := bridge.Open(&testBridgeStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = remote.Close() }()
	cache := state.db.Foreign("personal")
	ticket, err := cache.Begin(ctx, remote.PeerID())
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Put(ctx, ticket, sqlite.ForeignSession{Peer: remote.PeerID(), Namespace: "remote", ID: "session", Instance: "instance"}); err != nil {
		t.Fatal(err)
	}
	service := bridgeService{b: b, profile: "personal", ctx: context.WithValue(ctx, nativeStateKey{}, state)}
	params, _ := json.Marshal(map[string]string{"peer_id": remote.PeerID()})
	if _, err := service.admin(context.Background(), "block", params); err != nil {
		t.Fatal(err)
	}
	if previews, err := cache.List(ctx, remote.PeerID()); err != nil || len(previews) != 0 {
		t.Fatal("admin callback purged wrong database", err)
	}
	_ = b.Close()
	_ = store.Close()
	if err = state.close(); err != nil {
		t.Fatal(err)
	}
	state, err = openNativeState(ctx, agentDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.close() }()
	pending, err = state.db.Chat(state.chatNamespace(dataDir)).Pending(ctx)
	if err != nil || len(pending) != 1 || pending[0].EventID != "after-migration" {
		t.Fatal("restart replayed old spool", pending, err)
	}
	for path, want := range map[string][]byte{settingsPath: settingsBytes, bridgePath: originalBridge, spoolPath: originalSpool} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("original changed: %s, %v", path, err)
		}
	}
}

func TestNativeChatResetRetainsDeliveryHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ORB_BRIDGE_HOME", filepath.Join(root, "bridge"))
	t.Setenv("ORB_CHAT_DATA_DIR", "")
	t.Setenv("PI_OFFLINE", "1")
	state, err := openNativeState(ctx, agentDir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.close() }()
	settings, err := state.settings(root, agentDir)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := state.auth(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := state.models(agentDir, credentials, true)
	if err != nil {
		t.Fatal(err)
	}
	fauxProvider := faux.New(faux.Options{})
	newProvider := func() *chat.LocalProvider {
		p, err := chat.NewLocalProvider(root, chat.WithAgentDir(agentDir), chat.WithPersistence(func(key chat.ConversationKey) harness.SessionRepo { return state.db.Sessions("chat/" + key.String()) }, settings, registry), chat.WithSessionOptions(func(_ chat.ConversationKey, o *agent.AgentSessionOptions) {
			o.Model = fauxProvider.GetModel()
			o.StreamFn = fauxProvider.StreamSimple
		}))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	key := chat.ConversationKey{Platform: "faux", Account: "test", ChatID: "reset"}
	conversation, err := newProvider().Acquire(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	oldID := conversation.Manager.GetSessionID()
	marker := map[string]any{"eventId": "already-delivered", "phase": "delivered"}
	if _, err := conversation.Manager.AppendCustomEntry("orb.chat.turn", marker); err != nil {
		t.Fatal(err)
	}
	if err := conversation.Reset(); err != nil {
		t.Fatal(err)
	}
	newID := conversation.Manager.GetSessionID()
	if newID == oldID {
		t.Fatal("reset retained old session")
	}
	if err := conversation.Close(ctx); err != nil {
		t.Fatal(err)
	}
	conversation, err = newProvider().Acquire(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conversation.Close(ctx) }()
	if conversation.Manager.GetSessionID() != newID {
		t.Fatal("reopened old session")
	}
	data, err := conversation.Manager.JSONL()
	if err != nil || !bytes.Contains(data, []byte("already-delivered")) {
		t.Fatal("reset lost delivery tombstone", err)
	}
}
