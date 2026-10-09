package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/nodepath"
	"github.com/OrdalieTech/orb/platforms/native"
	"github.com/OrdalieTech/orb/platforms/native/teamenv"
	"github.com/OrdalieTech/orb/tui"
)

// stopLegacyWriters clears the way for this version's one-time migration: in
// a terminal Orb names each previous-version Orb still writing and offers to
// stop it.
func stopLegacyWriters(ctx context.Context, agentDir string, streams cliStreams) error {
	var confirm func(int, string) bool
	if streams.StdinTTY && streams.StderrTTY {
		confirm = func(pid int, described string) bool {
			if described != "" {
				described = " (" + described + ")"
			}
			_, _ = fmt.Fprintf(streams.Stderr, "This Orb moves your conversations and settings into its database once. Orb process %d%s still runs the previous version and would keep writing where this one no longer reads.\nStop it and continue? It loses any turn in progress. [y/N] ", pid, described)
			answer, _ := bufio.NewReader(streams.Stdin).ReadString('\n')
			return strings.EqualFold(strings.TrimSpace(answer), "y")
		}
	}
	return native.StopLegacyWriters(ctx, agentDir, confirm)
}

type nativeStateKey struct{}

func stateFromContext(ctx context.Context) *native.State {
	state, _ := ctx.Value(nativeStateKey{}).(*native.State)
	return state
}

func openNativeState(ctx context.Context, agentDir string, migrate bool, sessionDirs ...string) (*native.State, error) {
	return native.Open(ctx, agentDir, migrate, teamenv.AuthDocument(), sessionDirs...)
}

func nativeKeybindings(state *native.State) (tui.KeybindingsConfig, error) {
	if state == nil {
		return nil, nil
	}
	data, err := state.Document(filepath.Join(state.AgentDir, "keybindings.json")).Read(context.Background())
	if err != nil {
		return nil, err
	}
	if bindings := tui.ParseKeybindings(data); bindings != nil {
		return bindings, nil
	}
	return tui.KeybindingsConfig{}, nil
}

// configureChild builds an extension's child agent on state when it shares
// the agent dir.
func configureChild(state *native.State) func(*agent.AgentSessionOptions) error {
	return func(options *agent.AgentSessionOptions) error {
		if state == nil || options.AgentDir != state.AgentDir {
			return nil
		}
		var err error
		if options.Settings, err = state.Settings(options.CWD, state.AgentDir); err != nil {
			return err
		}
		if options.ModelRegistry == nil {
			credentials, err := state.Auth(state.AgentDir)
			if err != nil {
				return err
			}
			if options.ModelRegistry, err = state.Models(state.AgentDir, state.Accounts(state.AgentDir, credentials), os.Getenv("ORB_OFFLINE") != ""); err != nil {
				return err
			}
		}
		if options.SessionManager == nil {
			options.SessionManager, err = state.ChildSession(options.CWD)
		}
		return err
	}
}

func migrateAuthForContext(ctx context.Context, agentDir string) ([]string, error) {
	if stateFromContext(ctx) != nil {
		return nil, nil
	}
	return config.MigrateAuthToAuthJSON(agentDir)
}
func authStorageLocation(ctx context.Context, agentDir string, auth *config.AuthStorage) string {
	if stateFromContext(ctx) == nil {
		return auth.Path()
	}
	path, _ := native.Path(agentDir)
	return path
}

// nativeRootOptions consumes only global prefixes, never strings belonging to a
// runtime flag or a prompt. Validate all values before changing the environment.
func nativeRootOptions(argv []string) ([]string, bool, error) {
	values := map[string]string{}
	files := false
	index := 0
	for index < len(argv) {
		name := ""
		switch argv[index] {
		case "--pi-files":
			files = true
			index++
			continue
		case "--agent-dir":
			name = config.EnvAgentDir
		case "--state-home":
			name = "ORB_STATE_HOME"
		case "--bridge-home":
			name = "ORB_BRIDGE_HOME"
		default:
			goto apply
		}
		if index+1 >= len(argv) || argv[index+1] == "" || strings.HasPrefix(argv[index+1], "--") {
			return nil, false, fmt.Errorf("%s requires a directory", argv[index])
		}
		path, err := nodepath.Expand(argv[index+1])
		if err == nil {
			path, err = filepath.Abs(path)
		}
		if err != nil {
			return nil, false, err
		}
		values[name] = path
		index += 2
	}
apply:
	for name, value := range values {
		if err := os.Setenv(name, value); err != nil {
			return nil, false, err
		}
	}
	return argv[index:], files, nil
}

func runNativeCLI(ctx context.Context, argv []string, streams cliStreams) int {
	argv, files, err := nativeRootOptions(argv)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if len(argv) > 0 && (argv[0] == "--version" || argv[0] == "-v") {
		return runCLI(ctx, argv, streams)
	}
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if files {
		migrated, err := native.Migrated(ctx, agentDir)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		if migrated {
			return reportCLIError(streams.Stderr, errors.New("this root uses native SQLite; select a separate ORB_AGENT_DIR for Pi-file compatibility"))
		}
		return runCLI(ctx, argv, streams)
	}
	migrate := len(argv) > 1 && argv[0] == "storage" && argv[1] == "migrate"
	if migrate {
		if err = native.RequireOfflineMigration(ctx, agentDir); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
	}
	var sessionDirs []string
	if parsed := ParseArgs(argv); parsed.SessionDir != nil {
		return reportCLIError(streams.Stderr, errors.New("native sessions live in SQLite; use orb storage migrate <legacy-session-dir> to import a custom root before first launch"))
	}
	if migrate && len(argv) > 2 {
		sessionDirs = append(sessionDirs, argv[2:]...)
	}
	state, err := openNativeState(ctx, agentDir, migrate, sessionDirs...)
	if errors.Is(err, native.ErrLegacyMigration) && len(argv) > 1 && argv[0] == "bridge" && (argv[1] == "stop" || argv[1] == "status") {
		return runCLI(ctx, argv, streams)
	}
	if errors.Is(err, native.ErrLegacyMigration) && !migrate {
		if err = stopLegacyWriters(ctx, agentDir, streams); err == nil {
			state, err = openNativeState(ctx, agentDir, true, sessionDirs...)
		}
	}
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	defer func() { _ = state.Close() }()
	state.PruneEmpty(ctx)
	if len(argv) == 0 || argv[0] != "storage" {
		return runCLI(context.WithValue(ctx, nativeStateKey{}, state), argv, streams)
	}
	if migrate {
		_, _ = fmt.Fprintln(streams.Stdout, "Migration complete. Original files are retained; use this Orb version for this state root.")
		return 0
	}
	if err := runStorageCommand(ctx, state, argv[1:], streams); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	return 0
}

// runStorageCommand runs `orb storage` on the open native state.
func runStorageCommand(ctx context.Context, state *native.State, argv []string, streams cliStreams) error {
	switch {
	case len(argv) == 4 && argv[0] == "config":
		switch argv[2] {
		case "settings.json", "auth.json", "accounts.json", "trust.json", "models.json", "models-store.json", "keybindings.json":
		default:
			return errors.New("unknown native configuration document")
		}
		document := state.Document(filepath.Join(state.AgentDir, argv[2]))
		switch argv[1] {
		case "export":
			data, err := document.Read(ctx)
			if err == nil && data == nil {
				data = []byte("{}\n")
			}
			if err != nil {
				return err
			}
			return writeNativeExport(argv[3], data)
		case "import":
		default:
			return errStorageUsage
		}
		data, err := os.ReadFile(argv[3])
		if err == nil && argv[2] == "models.json" {
			parsed, parseErr := config.ParseModelConfig(data, "configuration import")
			err = parseErr
			if err == nil && parsed.Error() != "" {
				err = errors.New("invalid model configuration")
			}
		} else if err == nil {
			var object map[string]json.RawMessage
			if json.Unmarshal(data, &object) != nil || object == nil {
				err = errors.New("configuration must be a JSON object")
			}
		}
		if err != nil {
			return err
		}
		return document.Update(ctx, func([]byte) ([]byte, error) { return data, nil })
	case len(argv) == 2 && argv[0] == "backup":
		path, err := filepath.Abs(argv[1])
		if err == nil {
			err = state.DB.Backup(ctx, path)
		}
		if err == nil {
			_, _ = fmt.Fprintln(streams.Stdout, "Database backup saved to "+path)
		}
		return err
	case len(argv) == 2 && argv[0] == "restore":
		path, err := filepath.Abs(argv[1])
		var count int
		if err == nil {
			count, err = state.DB.RestoreSessions(ctx, path, "personal")
		}
		if err == nil {
			_, _ = fmt.Fprintf(streams.Stdout, "Recovered %d conversations. Current credentials, Bridge authority and delivery state are unchanged.\n", count)
		}
		return err
	case len(argv) == 1 && argv[0] == "sessions":
		// One JSON line per conversation, newest first: the list /resume shows, for apps and scripts.
		rows, err := state.Sessions().ListInfo(ctx, "", nil)
		out := json.NewEncoder(streams.Stdout)
		for _, row := range rows {
			_ = out.Encode(struct {
				ID       string  `json:"id"`
				Name     *string `json:"name,omitempty"`
				CWD      string  `json:"cwd"`
				Created  int64   `json:"created"`
				Modified int64   `json:"modified"`
				Messages int     `json:"messages"`
				First    string  `json:"first"`
			}{row.ID, row.Name, row.CWD, row.Created.UnixMilli(), row.Modified.UnixMilli(), row.MessageCount, row.FirstMessage})
		}
		return err
	case len(argv) == 2 && argv[0] == "delete":
		// A stored conversation, by the ID `orb storage sessions` lists; the Android app deletes through it.
		rows, err := state.Sessions().ListInfo(ctx, "", nil)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(rows, func(row session.SessionInfo) bool { return row.ID == argv[1] }) {
			return fmt.Errorf("no stored conversation %s", argv[1])
		}
		return state.Sessions().Delete(ctx, harness.SessionMetadata{ID: argv[1]})
	case len(argv) == 2 && argv[0] == "import":
		stored, err := state.Sessions().OpenPath(ctx, argv[1])
		if err == nil {
			_, _ = fmt.Fprintln(streams.Stdout, stored.Metadata().ID)
		}
		return err
	case len(argv) == 3 && argv[0] == "export":
		stored, err := state.Sessions().OpenPath(ctx, argv[1])
		if err != nil {
			return err
		}
		data, err := stored.Storage().(harness.ByteSessionStorage).Bytes()
		if err == nil {
			err = writeNativeExport(argv[2], data)
		}
		if err == nil {
			_, _ = fmt.Fprintln(streams.Stdout, "Exported to "+argv[2])
		}
		return err
	}
	return errStorageUsage
}

var errStorageUsage = errors.New("usage: orb storage migrate [legacy-session-dir...] | sessions | delete <session> | import <jsonl> | export <session> <jsonl> | backup <path> | restore <backup> | config import|export <name.json> <path>")

func writeNativeExport(path string, data []byte) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}
