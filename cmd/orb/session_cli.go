package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/modes"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/plugins/claudesessions"
	"github.com/OrdalieTech/orb/plugins/codexsessions"
)

var errNoSessionSelected = errors.New("no session selected")

// importers open another agent's session as an Orb conversation, each with
// its plugin enabled; notFound is the error for an ID the agent doesn't know.
var importers = []struct {
	plugin   string
	open     func(id string, env []string, create func(cwd string) (*session.SessionManager, error)) (*session.SessionManager, error)
	notFound error
}{
	{claudesessions.Name, claudesessions.ImportClaudeCode, claudesessions.ErrNoClaudeCodeSession},
	{codexsessions.Name, codexsessions.ImportCodex, codexsessions.ErrNoCodexSession},
}

// importSession opens id through the first enabled importer that knows it,
// and returns no manager when none does.
func importSession(settings *config.SettingsManager, id string, create func(cwd string) (*session.SessionManager, error)) (*session.SessionManager, error) {
	for _, importer := range importers {
		if !settings.GetPlugins()[importer.plugin] {
			continue
		}
		if manager, err := importer.open(id, os.Environ(), create); !errors.Is(err, importer.notFound) {
			return manager, err
		}
	}
	return nil, nil
}

type SessionListLoader func(session.SessionListProgress) []session.SessionInfo

type ContextSessionListLoader func(context.Context, session.SessionListUpdateFunc) ([]session.SessionInfo, error)

type SessionSelector func(current, all SessionListLoader) (path string, selected bool, err error)

type ContextSessionSelector func(current, all ContextSessionListLoader) (path string, selected bool, err error)

func startupTUISessionSelector(ctx context.Context) SessionSelector {
	return func(current, all SessionListLoader) (string, bool, error) {
		return modes.RunSessionSelector(ctx, modes.SessionSelectorLoader(current), modes.SessionSelectorLoader(all))
	}
}

func startupContextTUISessionSelector(ctx context.Context) ContextSessionSelector {
	return func(current, all ContextSessionListLoader) (string, bool, error) {
		return modes.RunSessionSelectorContext(ctx, modes.SessionSelectorContextLoader(current), modes.SessionSelectorContextLoader(all))
	}
}

type resolvedSession struct {
	kind string
	path string
	cwd  string
	arg  string
}

func validateSessionFlags(args CLIArgs) []string {
	var validationErrors []string
	if hasCLIValue(args.Fork) {
		conflicts := make([]string, 0, 4)
		if hasCLIValue(args.Session) {
			conflicts = append(conflicts, "--session")
		}
		if args.Continue {
			conflicts = append(conflicts, "--continue")
		}
		if args.Resume {
			conflicts = append(conflicts, "--resume")
		}
		if args.NoSession {
			conflicts = append(conflicts, "--no-session")
		}
		if len(conflicts) > 0 {
			validationErrors = append(validationErrors, "--fork cannot be combined with "+strings.Join(conflicts, ", "))
		}
	}
	if args.SessionID != nil {
		conflicts := make([]string, 0, 3)
		if hasCLIValue(args.Session) {
			conflicts = append(conflicts, "--session")
		}
		if args.Continue {
			conflicts = append(conflicts, "--continue")
		}
		if args.Resume {
			conflicts = append(conflicts, "--resume")
		}
		if len(conflicts) > 0 {
			validationErrors = append(validationErrors, "--session-id cannot be combined with "+strings.Join(conflicts, ", "))
		}
		if err := session.AssertValidSessionID(*args.SessionID); err != nil {
			validationErrors = append(validationErrors, err.Error())
		}
	}
	return validationErrors
}

func hasCLIValue(value *string) bool { return value != nil && *value != "" }

func createCLISession(cwd string, args CLIArgs, streams cliStreams, selector SessionSelector, contextSelector ContextSessionSelector) (*session.SessionManager, session.SessionContext, error) {
	if args.native != nil && !args.NoSession {
		return createNativeSession(cwd, args, streams, selector, contextSelector)
	}
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return nil, session.SessionContext{}, err
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		return nil, session.SessionContext{}, err
	}
	cliSessionDir := ""
	if args.SessionDir != nil {
		cliSessionDir = *args.SessionDir
	}
	sessionDir, err := config.ResolveSessionDir(cliSessionDir, settings)
	if err != nil {
		return nil, session.SessionContext{}, err
	}
	managerOptions := []session.Option{session.WithAgentDir(agentDir)}
	if args.SessionID != nil {
		managerOptions = append(managerOptions, session.WithSessionID(*args.SessionID))
	}

	var manager *session.SessionManager
	switch {
	case args.NoSession:
		manager, err = session.InMemory(cwd, managerOptions...)
	case hasCLIValue(args.Fork):
		if args.SessionID != nil && findLocalSessionByExactID(*args.SessionID, cwd, sessionDir, agentDir) != "" {
			return nil, session.SessionContext{}, fmt.Errorf("Session already exists with id '%s'", *args.SessionID) //nolint:staticcheck // Upstream error capitalization is observable.
		}
		resolved, resolveErr := resolveSessionArgument(*args.Fork, cwd, sessionDir, agentDir)
		if resolveErr != nil {
			return nil, session.SessionContext{}, resolveErr
		}
		if resolved.kind == "not_found" {
			return nil, session.SessionContext{}, fmt.Errorf("No session found matching '%s'", resolved.arg) //nolint:staticcheck // Upstream error capitalization is observable.
		}
		manager, err = session.ForkFrom(resolved.path, cwd, sessionDir, managerOptions...)
	case hasCLIValue(args.Session):
		resolved, resolveErr := resolveSessionArgument(*args.Session, cwd, sessionDir, agentDir)
		if resolveErr != nil {
			return nil, session.SessionContext{}, resolveErr
		}
		openOptions := []session.Option{session.WithAgentDir(agentDir)}
		if args.clientCWD {
			openOptions = append(openOptions, session.WithCwdOverride(cwd))
		}
		switch {
		case resolved.kind == "not_found":
			manager, err = importSession(settings, resolved.arg, func(dir string) (*session.SessionManager, error) {
				return session.Create(dir, sessionDir, session.WithAgentDir(agentDir), session.WithSessionID(resolved.arg))
			})
			if err != nil {
				return nil, session.SessionContext{}, err
			}
			if manager != nil {
				break
			}
			return nil, session.SessionContext{}, fmt.Errorf("No session found matching '%s'", resolved.arg) //nolint:staticcheck // Upstream error capitalization is observable.
		case resolved.kind == "global" && !args.clientCWD:
			confirmed, confirmErr := confirmGlobalSessionFork(streams, resolved.cwd)
			if confirmErr != nil {
				return nil, session.SessionContext{}, confirmErr
			}
			if !confirmed {
				_, _ = fmt.Fprintln(streams.Stdout, "Aborted.")
				return nil, session.SessionContext{}, errNoSessionSelected
			}
			manager, err = session.ForkFrom(resolved.path, cwd, sessionDir, session.WithAgentDir(agentDir))
		default:
			manager, err = session.Open(resolved.path, sessionDir, openOptions...)
		}
	case args.Resume:
		var selectedPath string
		var selected bool
		var selectErr error
		if contextSelector != nil {
			selectedPath, selected, selectErr = contextSelector(
				func(ctx context.Context, update session.SessionListUpdateFunc) ([]session.SessionInfo, error) {
					return session.ListContext(ctx, cwd, sessionDir, update, session.WithAgentDir(agentDir))
				},
				func(ctx context.Context, update session.SessionListUpdateFunc) ([]session.SessionInfo, error) {
					return session.ListAllContext(ctx, sessionDir, update, session.WithAgentDir(agentDir))
				},
			)
		} else {
			if selector == nil {
				selector = startupTUISessionSelector(context.Background())
			}
			selectedPath, selected, selectErr = selector(
				func(progress session.SessionListProgress) []session.SessionInfo {
					return session.List(cwd, sessionDir, progress, session.WithAgentDir(agentDir))
				},
				func(progress session.SessionListProgress) []session.SessionInfo {
					return session.ListAll(sessionDir, progress, session.WithAgentDir(agentDir))
				},
			)
		}
		if selectErr != nil {
			return nil, session.SessionContext{}, selectErr
		}
		if !selected {
			_, _ = fmt.Fprintln(streams.Stdout, "No session selected")
			return nil, session.SessionContext{}, errNoSessionSelected
		}
		manager, err = session.Open(selectedPath, sessionDir, session.WithAgentDir(agentDir))
	case args.Continue:
		manager, err = session.ContinueRecent(cwd, sessionDir, session.WithAgentDir(agentDir))
	case args.SessionID != nil:
		if existing := findLocalSessionByExactID(*args.SessionID, cwd, sessionDir, agentDir); existing != "" {
			manager, err = session.Open(existing, sessionDir, session.WithAgentDir(agentDir))
		} else {
			_, _ = fmt.Fprintf(streams.Stderr, "Warning: No project session found with id '%s'; creating a new session with that id.\n", *args.SessionID)
			manager, err = session.Create(cwd, sessionDir, managerOptions...)
		}
	default:
		manager, err = session.Create(cwd, sessionDir, session.WithAgentDir(agentDir))
	}
	if err != nil {
		return nil, session.SessionContext{}, err
	}
	return manager, manager.BuildSessionContext(), nil
}

func formatMissingSessionCWDPrompt(issue *agent.MissingSessionCWDError) string {
	return "cwd from session file does not exist\n" + issue.SessionCWD + "\n\ncontinue in current cwd\n" + issue.FallbackCWD
}

func resolveSessionArgument(argument, cwd, sessionDir, agentDir string) (resolvedSession, error) {
	if strings.ContainsAny(argument, `/\`) || strings.HasSuffix(argument, ".jsonl") {
		path, err := config.NormalizePath(argument)
		if err != nil {
			return resolvedSession{}, err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
		return resolvedSession{kind: "path", path: path}, nil
	}
	if exact := findLocalSessionByExactID(argument, cwd, sessionDir, agentDir); exact != "" {
		return resolvedSession{kind: "local", path: exact}, nil
	}
	local := session.List(cwd, sessionDir, nil, session.WithAgentDir(agentDir))
	if match := matchSessionID(local, argument); match != nil {
		return resolvedSession{kind: "local", path: match.Path}, nil
	}
	all := session.ListAll(sessionDir, nil, session.WithAgentDir(agentDir))
	if match := matchSessionID(all, argument); match != nil {
		return resolvedSession{kind: "global", path: match.Path, cwd: match.CWD}, nil
	}
	return resolvedSession{kind: "not_found", arg: argument}, nil
}

func findLocalSessionByExactID(id, cwd, sessionDir, agentDir string) string {
	return session.FindByID(cwd, id, sessionDir, session.WithAgentDir(agentDir))
}

func matchSessionID(sessions []session.SessionInfo, value string) *session.SessionInfo {
	for index := range sessions {
		if sessions[index].ID == value {
			return &sessions[index]
		}
	}
	for index := range sessions {
		if strings.HasPrefix(sessions[index].ID, value) {
			return &sessions[index]
		}
	}
	return nil
}

func confirmGlobalSessionFork(streams cliStreams, sessionCWD string) (bool, error) {
	_, _ = fmt.Fprintln(streams.Stdout, "Session found in different project: "+sessionCWD)
	_, _ = io.WriteString(streams.Stdout, "Fork this session into current directory? [y/N] ")
	line, err := bufio.NewReader(streams.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
	return answer == "y" || answer == "yes", nil
}

func createNativeSession(cwd string, args CLIArgs, streams cliStreams, selector SessionSelector, contextSelector ContextSessionSelector) (*session.SessionManager, session.SessionContext, error) {
	ctx := context.Background()
	repo := args.native.sessions()
	var opened *harness.Session
	var err error
	reference := ""
	switch {
	case hasCLIValue(args.Fork):
		reference = *args.Fork
	case hasCLIValue(args.Session):
		reference = *args.Session
	case args.SessionID != nil:
		reference = *args.SessionID
	case args.Continue:
		rows, listErr := repo.List(ctx, harness.SessionListOptions{CWD: cwd})
		if listErr != nil {
			return nil, session.SessionContext{}, listErr
		}
		if len(rows) > 0 {
			reference = rows[0].ID
		}
	case args.Resume:
		var selected bool
		current := func(ctx context.Context, update session.SessionListUpdateFunc) ([]session.SessionInfo, error) {
			return repo.ListInfo(ctx, cwd, update)
		}
		all := func(ctx context.Context, update session.SessionListUpdateFunc) ([]session.SessionInfo, error) {
			return repo.ListInfo(ctx, "", update)
		}
		if contextSelector == nil && selector == nil {
			contextSelector = startupContextTUISessionSelector(ctx)
		}
		if contextSelector != nil {
			reference, selected, err = contextSelector(current, all)
		} else {
			reference, selected, err = selector(func(progress session.SessionListProgress) []session.SessionInfo {
				rows, _ := current(ctx, nil)
				return rows
			}, func(progress session.SessionListProgress) []session.SessionInfo {
				rows, _ := all(ctx, nil)
				return rows
			})
		}
		if err != nil {
			return nil, session.SessionContext{}, err
		}
		if !selected {
			return nil, session.SessionContext{}, errNoSessionSelected
		}
	}
	if reference != "" {
		opened, err = repo.OpenPath(ctx, reference)
	}
	if settings, settingsErr := args.native.settings(cwd, args.native.agentDir); hasCLIValue(args.Session) && errors.Is(err, fs.ErrNotExist) && settingsErr == nil {
		manager, importErr := importSession(settings, *args.Session, func(dir string) (*session.SessionManager, error) {
			created, err := repo.Create(ctx, harness.SessionCreateOptions{CWD: dir, ID: *args.Session})
			if err != nil {
				return nil, err
			}
			return session.FromHarnessStorage(created.Storage(), session.WithHarnessRepo(repo), session.WithAgentDir(args.native.agentDir))
		})
		if importErr != nil {
			return nil, session.SessionContext{}, importErr
		}
		if manager != nil {
			if err = args.native.bindSession(manager); err != nil {
				return nil, session.SessionContext{}, err
			}
			return manager, manager.BuildSessionContext(), nil
		}
	}
	if err != nil && (args.SessionID == nil || hasCLIValue(args.Fork) || hasCLIValue(args.Session) || !errors.Is(err, fs.ErrNotExist)) {
		return nil, session.SessionContext{}, err
	}
	createOptions := harness.SessionCreateOptions{CWD: cwd}
	if args.SessionID != nil {
		createOptions.ID = *args.SessionID
	}
	if hasCLIValue(args.Fork) {
		leaf, leafErr := opened.Storage().LeafID()
		if leafErr != nil {
			return nil, session.SessionContext{}, leafErr
		}
		entry := ""
		if leaf != nil {
			entry = *leaf
		}
		opened, err = repo.Fork(ctx, opened.Metadata(), harness.SessionForkOptions{SessionCreateOptions: createOptions, EntryID: entry, Position: harness.ForkAt})
	} else if opened == nil {
		opened, err = repo.Create(ctx, createOptions)
	}
	if err != nil {
		return nil, session.SessionContext{}, err
	}
	options := []session.Option{session.WithHarnessRepo(repo), session.WithAgentDir(args.native.agentDir)}
	if args.clientCWD {
		options = append(options, session.WithCwdOverride(cwd))
	}
	manager, err := session.FromHarnessStorage(opened.Storage(), options...)
	if err != nil {
		return nil, session.SessionContext{}, err
	}
	if err = args.native.bindSession(manager); err != nil {
		return nil, session.SessionContext{}, err
	}
	return manager, manager.BuildSessionContext(), nil
}
