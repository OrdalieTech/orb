package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/session"
)

var errNoSessionSelected = errors.New("no session selected")

type SessionListLoader func(session.SessionListProgress) []session.SessionInfo

type SessionSelector func(current, all SessionListLoader) (path string, selected bool, err error)

type resolvedSession struct {
	kind string
	path string
	cwd  string
	arg  string
}

type MissingSessionCWDError struct {
	StoredCWD   string
	SessionFile string
	CurrentCWD  string
}

func (issue *MissingSessionCWDError) Error() string {
	return fmt.Sprintf(
		"Stored session working directory does not exist: %s\nSession file: %s\nCurrent working directory: %s",
		issue.StoredCWD, issue.SessionFile, issue.CurrentCWD,
	)
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

func createCLISession(
	cwd string,
	args CLIArgs,
	streams cliStreams,
	selector SessionSelector,
) (*session.SessionManager, session.SessionContext, error) {
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
		switch resolved.kind {
		case "not_found":
			return nil, session.SessionContext{}, fmt.Errorf("No session found matching '%s'", resolved.arg) //nolint:staticcheck // Upstream error capitalization is observable.
		case "global":
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
			manager, err = session.Open(resolved.path, sessionDir, session.WithAgentDir(agentDir))
		}
	case args.Resume:
		if selector == nil {
			selector = terminalSessionSelector(streams)
		}
		selectedPath, selected, selectErr := selector(
			func(progress session.SessionListProgress) []session.SessionInfo {
				return session.List(cwd, sessionDir, progress, session.WithAgentDir(agentDir))
			},
			func(progress session.SessionListProgress) []session.SessionInfo {
				return session.ListAll(sessionDir, progress, session.WithAgentDir(agentDir))
			},
		)
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
	if manager.GetSessionFile() != "" {
		if _, statErr := os.Stat(manager.GetCWD()); errors.Is(statErr, os.ErrNotExist) {
			return nil, session.SessionContext{}, &MissingSessionCWDError{
				StoredCWD: manager.GetCWD(), SessionFile: manager.GetSessionFile(), CurrentCWD: cwd,
			}
		}
	}
	return manager, manager.BuildSessionContext(), nil
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
	for _, info := range session.List(cwd, sessionDir, nil, session.WithAgentDir(agentDir)) {
		if info.ID == id {
			return info.Path
		}
	}
	return ""
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
	answer := strings.TrimSuffix(line, "\n")
	answer = strings.ToLower(strings.TrimSuffix(answer, "\r"))
	return answer == "y" || answer == "yes", nil
}

func terminalSessionSelector(streams cliStreams) SessionSelector {
	// TODO(WP-450): replace this startup fallback with the TUI session picker and missing-cwd recovery flow.
	return func(current, all SessionListLoader) (string, bool, error) {
		if !streams.StdinTTY {
			return "", false, errors.New("--resume requires an interactive terminal; use --session <path|id>")
		}
		reader := bufio.NewReader(streams.Stdin)
		showAll := false
		for {
			sessions := current(nil)
			scope := "current project"
			if showAll {
				sessions = all(nil)
				scope = "all projects"
			}
			if len(sessions) == 0 && !showAll {
				showAll = true
				continue
			}
			if len(sessions) == 0 {
				return "", false, nil
			}
			_, _ = fmt.Fprintf(streams.Stdout, "Select a session (%s; type a to toggle):\n", scope)
			for index, info := range sessions {
				label := info.FirstMessage
				if info.Name != nil {
					label = *info.Name
				}
				_, _ = fmt.Fprintf(streams.Stdout, "  %d) %s  %s\n", index+1, info.ID, label)
			}
			_, _ = io.WriteString(streams.Stdout, "> ")
			line, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return "", false, err
			}
			selection := strings.TrimSpace(line)
			if selection == "" {
				return "", false, nil
			}
			if strings.EqualFold(selection, "a") {
				showAll = !showAll
				continue
			}
			if index, parseErr := strconv.Atoi(selection); parseErr == nil && index >= 1 && index <= len(sessions) {
				return sessions[index-1].Path, true, nil
			}
			if match := matchSessionID(sessions, selection); match != nil {
				return match.Path, true, nil
			}
			return "", false, fmt.Errorf("No session found matching '%s'", selection) //nolint:staticcheck // Upstream error capitalization is observable.
		}
	}
}
