package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/modes"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/engine/harness"
	nativeaccounts "github.com/OrdalieTech/orb/platforms/native/accounts"
	nativebridge "github.com/OrdalieTech/orb/platforms/native/bridge"
	"github.com/OrdalieTech/orb/platforms/native/sqlite"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/tui"
	"github.com/gofrs/flock"
)

var errLegacyMigration = errors.New("legacy Orb data needs migration; close other Orb processes, then run orb storage migrate")

// runningOrbError names another Orb still writing the data a migration moves.
type runningOrbError struct{ pid int }

func (e runningOrbError) Error() string {
	return fmt.Sprintf("close other Orb processes before migration (process %d is still running)", e.pid)
}

// stopLegacyWriters clears the way for this version's one-time migration. An
// Orb still running the previous version would keep writing files this one no
// longer reads, so in a terminal Orb names each and offers to stop it.
func stopLegacyWriters(ctx context.Context, agentDir string, streams cliStreams) error {
	for {
		err := requireOfflineMigration(ctx, agentDir)
		var running runningOrbError
		if !errors.As(err, &running) || !streams.StdinTTY || !streams.StderrTTY {
			return err
		}
		described := describeOrbProcess(running.pid)
		if described != "" {
			described = " (" + described + ")"
		}
		_, _ = fmt.Fprintf(streams.Stderr, "This Orb moves your conversations and settings into its database once. Orb process %d%s still runs the previous version and would keep writing where this one no longer reads.\nStop it and continue? It loses any turn in progress. [y/N] ", running.pid, described)
		answer, _ := bufio.NewReader(streams.Stdin).ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			return err
		}
		if err = stopOrbProcess(running.pid); err != nil {
			return err
		}
	}
}

type nativeState struct {
	mu          sync.Mutex
	sessionID   string
	sessionLock *flock.Flock
	db          *sqlite.DB
	agentDir    string
}
type nativeStateKey struct{}

func stateFromContext(ctx context.Context) *nativeState {
	state, _ := ctx.Value(nativeStateKey{}).(*nativeState)
	return state
}

func nativeStatePath(agentDir string) (string, error) {
	root := os.Getenv("ORB_STATE_HOME")
	if root == "" && os.Getenv(config.EnvAgentDir) != "" {
		root = filepath.Join(agentDir, "state")
	}
	if root == "" && os.Getenv("ORB_BRIDGE_HOME") != "" {
		root = filepath.Join(os.Getenv("ORB_BRIDGE_HOME"), "state")
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".orb", "state")
	}
	root, err := filepath.Abs(root)
	return filepath.Join(root, "orb.db"), err
}

func openNativeState(ctx context.Context, agentDir string, migrate bool, sessionDirs ...string) (_ *nativeState, err error) {
	agentDir, err = filepath.Abs(agentDir)
	if err != nil {
		return nil, err
	}
	path, err := nativeStatePath(agentDir)
	if err != nil {
		return nil, err
	}
	done, err := sqlite.MigrationStatus(ctx, path, "native")
	if err != nil {
		return nil, err
	}
	state := &nativeState{agentDir: agentDir}
	if done {
		if len(sessionDirs) > 0 {
			return nil, errors.New("native storage is already migrated; import additional JSONL files with orb storage import <path>")
		}
		state.db, err = sqlite.Open(ctx, path)
		return state, err
	}
	sources, err := state.migrationSources(sessionDirs)
	if err != nil {
		return nil, err
	}
	if len(sources) > 0 && !migrate {
		return nil, errLegacyMigration
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".migration.lock")
	locked, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("native storage migration is already running")
	}
	defer func() { _ = lock.Close() }()
	state.db, err = sqlite.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = state.db.Close()
		}
	}()
	if err = state.db.Migrate(ctx, "native", sources, func() error { return state.validateMigration(ctx, sources, sessionDirs) }); err != nil {
		return nil, err
	}
	return state, nil
}

func (state *nativeState) document(path string) *sqlite.Document {
	namespace, key := state.address(path)
	return state.db.Document(namespace, key)
}
func (state *nativeState) sessions() *sqlite.Sessions {
	return state.db.Sessions("personal")
}

func (state *nativeState) migrationSources(sessionDirs []string) ([]sqlite.MigrationSource, error) {
	var sources []sqlite.MigrationSource
	for _, name := range []string{"settings.json", "auth.json", "accounts.json", "trust.json", "models.json", "models-store.json", "keybindings.json", "oauth.json"} {
		path := filepath.Join(state.agentDir, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		kind := "json"
		if name == "models.json" {
			kind = "bytes"
		}
		namespace, key := state.address(path)
		sources = append(sources, sqlite.MigrationSource{Path: path, Namespace: namespace, Key: key, Kind: kind})
	}
	settings, err := config.NewSettingsManager("", config.WithAgentDir(state.agentDir), config.WithProjectTrusted(false))
	if err != nil {
		return nil, err
	}
	configured, err := config.ResolveSessionDir("", settings)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	// walk adds the files under root that source classifies, refusing symlinks.
	walk := func(root string, source func(path string, entry fs.DirEntry) (sqlite.MigrationSource, bool)) error {
		if root == "" {
			return nil
		}
		root, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) && path == root {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("migration source is a symlink: %s", path)
			}
			if entry.IsDir() || seen[path] {
				return nil
			}
			if found, ok := source(path, entry); ok {
				seen[path] = true
				sources = append(sources, found)
			}
			return nil
		})
	}
	for _, root := range append([]string{filepath.Join(state.agentDir, "sessions"), configured}, sessionDirs...) {
		if err := walk(root, func(path string, _ fs.DirEntry) (sqlite.MigrationSource, bool) {
			return sqlite.MigrationSource{Path: path, Namespace: "personal", Kind: "session"}, strings.HasSuffix(path, ".jsonl")
		}); err != nil {
			return nil, err
		}
	}
	bridge, err := bridgeDir("personal")
	if err != nil {
		return nil, err
	}
	for _, root := range []string{filepath.Dir(bridge), filepath.Join(filepath.Dir(filepath.Dir(bridge)), "instances"), filepath.Join(state.agentDir, "memory"), filepath.Join(state.agentDir, "chat"), os.Getenv("ORB_CHAT_DATA_DIR")} {
		if err := walk(root, func(path string, entry fs.DirEntry) (sqlite.MigrationSource, bool) {
			namespace, key := state.address(path)
			source := sqlite.MigrationSource{Path: path, Namespace: namespace, Key: key, Kind: "json"}
			switch entry.Name() {
			case "state.json", "attachment.json", "operations.json":
			case "admin.token", "stopped":
				source.Kind = "bytes"
			case "memory.jsonl":
				source.Kind = "memory"
				source.Namespace = "personal"
			case "spool.jsonl":
				source.Kind = "chat"
				source.Namespace = state.chatNamespace(filepath.Dir(path))
			default:
				if !strings.HasSuffix(path, ".jsonl") {
					return source, false
				}
				source.Kind = "session"
				source.Namespace = state.chatNamespace(filepath.Dir(path))
			}
			return source, true
		}); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

func (state *nativeState) settings(cwd, agentDir string, options ...config.Option) (*config.SettingsManager, error) {
	options = append(options, config.WithAgentDir(agentDir))
	if state != nil {
		options = append(options, config.WithGlobalDocument(state.document(filepath.Join(agentDir, "settings.json"))))
	}
	return config.NewSettingsManager(cwd, options...)
}
func (state *nativeState) auth(agentDir string) (*config.AuthStorage, error) {
	if state == nil {
		return config.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	}
	return config.NewAuthStorageWithDocument(state.document(filepath.Join(agentDir, "auth.json")))
}
func (state *nativeState) trust(agentDir string) (*config.ProjectTrustStore, error) {
	if state == nil {
		return config.NewProjectTrustStore(agentDir), nil
	}
	return config.NewProjectTrustStoreWithDocument(state.document(filepath.Join(agentDir, "trust.json")))
}

func runNativeCLI(ctx context.Context, argv []string, streams cliStreams) int {
	if len(argv) > 0 && (argv[0] == "--version" || argv[0] == "-v") {
		return runCLI(ctx, argv, streams)
	}
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if len(argv) > 0 && argv[0] == "--pi-files" {
		path, err := nativeStatePath(agentDir)
		var migrated bool
		if err == nil {
			migrated, err = sqlite.MigrationStatus(ctx, path, "native")
		}
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		if migrated {
			return reportCLIError(streams.Stderr, errors.New("this root uses native SQLite; select a separate PI_CODING_AGENT_DIR for Pi-file compatibility"))
		}
		return runCLI(ctx, argv[1:], streams)
	}
	migrate := len(argv) > 1 && argv[0] == "storage" && argv[1] == "migrate"
	if migrate {
		if err = requireOfflineMigration(ctx, agentDir); err != nil {
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
	if errors.Is(err, errLegacyMigration) && len(argv) > 1 && argv[0] == "bridge" && (argv[1] == "stop" || argv[1] == "status") {
		return runCLI(ctx, argv, streams)
	}
	if errors.Is(err, errLegacyMigration) && !migrate {
		if err = stopLegacyWriters(ctx, agentDir, streams); err == nil {
			state, err = openNativeState(ctx, agentDir, true, sessionDirs...)
		}
	}
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	defer func() { _ = state.close() }()
	state.pruneEmpty(ctx)
	if len(argv) > 0 && argv[0] == "storage" {
		if migrate {
			_, _ = fmt.Fprintln(streams.Stdout, "Migration complete. Original files are retained; use this Orb version for this state root.")
			return 0
		}
		if len(argv) == 5 && argv[1] == "config" {
			switch argv[3] {
			case "settings.json", "auth.json", "accounts.json", "trust.json", "models.json", "models-store.json", "keybindings.json":
			default:
				return reportCLIError(streams.Stderr, errors.New("unknown native configuration document"))
			}
			document := state.document(filepath.Join(agentDir, argv[3]))
			switch argv[2] {
			case "export":
				data, err := document.Read(ctx)
				if err == nil && data == nil {
					data = []byte("{}\n")
				}
				if err == nil {
					err = writeNativeExport(argv[4], data)
				}
				if err != nil {
					return reportCLIError(streams.Stderr, err)
				}
				return 0
			case "import":
				data, err := os.ReadFile(argv[4])
				if err == nil && argv[3] == "models.json" {
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
				if err == nil {
					err = document.Update(ctx, func([]byte) ([]byte, error) { return data, nil })
				}
				if err != nil {
					return reportCLIError(streams.Stderr, err)
				}
				return 0
			}
		}
		if len(argv) == 3 && argv[1] == "backup" {
			path, err := filepath.Abs(argv[2])
			if err == nil {
				err = state.db.Backup(ctx, path)
			}
			if err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			_, _ = fmt.Fprintln(streams.Stdout, "Database backup saved to "+path)
			return 0
		}
		if len(argv) == 3 && argv[1] == "restore" {
			path, err := filepath.Abs(argv[2])
			var count int
			if err == nil {
				count, err = state.db.RestoreSessions(ctx, path, "personal")
			}
			if err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			_, _ = fmt.Fprintf(streams.Stdout, "Recovered %d conversations. Current credentials, Bridge authority and delivery state are unchanged.\n", count)
			return 0
		}
		if len(argv) == 2 && argv[1] == "sessions" {
			// One JSON line per conversation, newest first: the list /resume shows, for apps and scripts.
			rows, err := state.sessions().ListInfo(ctx, "", nil)
			if err != nil {
				return reportCLIError(streams.Stderr, err)
			}
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
			return 0
		}
		if len(argv) == 3 && argv[1] == "delete" {
			// A stored conversation, by the ID `orb storage sessions` lists; the Android app deletes through it.
			rows, err := state.sessions().ListInfo(ctx, "", nil)
			if err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			if !slices.ContainsFunc(rows, func(row session.SessionInfo) bool { return row.ID == argv[2] }) {
				return reportCLIError(streams.Stderr, fmt.Errorf("no stored conversation %s", argv[2]))
			}
			if err = state.sessions().Delete(ctx, harness.SessionMetadata{ID: argv[2]}); err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			return 0
		}
		if len(argv) == 3 && argv[1] == "import" {
			stored, err := state.sessions().OpenPath(ctx, argv[2])
			if err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			_, _ = fmt.Fprintln(streams.Stdout, stored.Metadata().ID)
			return 0
		}
		if len(argv) == 4 && argv[1] == "export" {
			stored, err := state.sessions().OpenPath(ctx, argv[2])
			if err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			data, err := stored.Storage().(harness.ByteSessionStorage).Bytes()
			if err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			if err = writeNativeExport(argv[3], data); err != nil {
				return reportCLIError(streams.Stderr, err)
			}
			_, _ = fmt.Fprintln(streams.Stdout, "Exported to "+argv[3])
			return 0
		}
		return reportCLIError(streams.Stderr, errors.New("usage: orb storage migrate [legacy-session-dir...] | sessions | delete <session> | import <jsonl> | export <session> <jsonl> | backup <path> | restore <backup> | config import|export <name.json> <path>"))
	}
	return runCLI(context.WithValue(ctx, nativeStateKey{}, state), argv, streams)
}

func (state *nativeState) accounts(agentDir string, base auth.CredentialStore) *accounts.Store {
	path := filepath.Join(agentDir, "accounts.json")
	if state == nil {
		return nativeaccounts.NewStore(path, base)
	}
	return accounts.NewStoreWithDocument(state.document(path), base)
}

func (state *nativeState) models(agentDir string, credentials auth.CredentialStore, offline bool) (*config.ModelRegistry, error) {
	if state == nil {
		if offline {
			return config.NewOfflineModelRegistry(agentDir)
		}
		return config.NewModelRegistryWithCredentials(agentDir, credentials)
	}
	return config.NewModelRegistryWithDocuments(agentDir, credentials, state.document(filepath.Join(agentDir, "models.json")), state.document(filepath.Join(agentDir, "models-store.json")), !offline)
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
	path, _ := nativeStatePath(agentDir)
	return path
}

func (state *nativeState) bridgeStore(path string, quota int) (*nativebridge.Store, error) {
	if state == nil {
		return nativebridge.OpenStore(path, quota, nil)
	}
	return nativebridge.OpenStore(path, quota, state.document(path))
}
func (state *nativeState) read(ctx context.Context, path string) ([]byte, error) {
	if state == nil {
		return os.ReadFile(path)
	}
	data, err := state.document(path).Read(ctx)
	if err == nil && data == nil {
		err = os.ErrNotExist
	}
	return data, err
}
func (state *nativeState) write(ctx context.Context, path string, data []byte) error {
	if state == nil {
		if data == nil {
			return os.Remove(path)
		}
		return os.WriteFile(path, data, 0600)
	}
	return state.document(path).Update(ctx, func([]byte) ([]byte, error) { return data, nil })
}

func (state *nativeState) memory() memory.Store {
	if state == nil {
		return nil
	}
	return state.db.Memory("personal")
}

func (state *nativeState) keybindings() (tui.KeybindingsConfig, error) {
	if state == nil {
		return nil, nil
	}
	data, err := state.document(filepath.Join(state.agentDir, "keybindings.json")).Read(context.Background())
	if err != nil {
		return nil, err
	}
	bindings := tui.ParseKeybindings(data)
	if bindings == nil {
		bindings = tui.KeybindingsConfig{}
	}
	return bindings, nil
}
func (state *nativeState) ownerLock(id string) (*flock.Flock, error) {
	path, err := nativeStatePath(state.agentDir)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(filepath.Dir(path), "owners")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte("personal\x00" + id))
	return flock.New(filepath.Join(dir, fmt.Sprintf("%x.lock", key))), nil
}
func (state *nativeState) bindSession(manager *session.SessionManager) error {
	release, err := state.claimSession(manager)
	if err == nil {
		release()
	}
	return err
}
func (state *nativeState) claimSession(manager *session.SessionManager) (func(), error) {
	if state == nil {
		return func() {}, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	id := ""
	if manager.IsHarnessBacked() && manager.IsPersisted() {
		id = manager.GetSessionID()
	}
	if state.sessionID == id {
		return func() {}, nil
	}
	var lock *flock.Flock
	if id != "" {
		var err error
		lock, err = state.ownerLock(id)
		if err != nil {
			return nil, err
		}
		acquired, err := lock.TryLock()
		if err != nil || !acquired {
			_ = lock.Close()
			if err != nil {
				return nil, err
			}
			return nil, errors.New("conversation is already open in another Orb process")
		}
	}
	previous, previousID := state.sessionLock, state.sessionID
	state.sessionID, state.sessionLock = id, lock
	return func() {
		if previous != nil {
			_ = previous.Close()
			state.discardIfEmpty(previousID)
		}
	}, nil
}
func (state *nativeState) close() error {
	state.release()
	return state.db.Close()
}

// conversation shares this state's store with a claim of its own, for a
// process that keeps several conversations open at once (ACP).
func (state *nativeState) conversation() *nativeState {
	if state == nil {
		return nil
	}
	return &nativeState{db: state.db, agentDir: state.agentDir}
}

// release drops the conversation claim, discarding a conversation left empty.
func (state *nativeState) release() {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.sessionLock != nil {
		_ = state.sessionLock.Close()
		state.discardIfEmpty(state.sessionID)
	}
	state.sessionID, state.sessionLock = "", nil
}

// discardIfEmpty drops a conversation left without a message, unless an Orb
// holds it open.
func (state *nativeState) discardIfEmpty(id string) {
	if id == "" {
		return
	}
	lock, err := state.ownerLock(id)
	if err != nil {
		return
	}
	defer func() { _ = lock.Close() }()
	if acquired, err := lock.TryLock(); err == nil && acquired {
		_ = state.sessions().DeleteIfEmpty(context.Background(), id)
	}
}

// pruneEmpty drops the empty conversations earlier runs left behind. A minute
// of grace covers one that another Orb created but has not claimed yet.
func (state *nativeState) pruneEmpty(ctx context.Context) {
	ids, _ := state.sessions().EmptyIDs(ctx, time.Now().Add(-time.Minute))
	for _, id := range ids {
		state.discardIfEmpty(id)
	}
}
func (state *nativeState) deleteSession(id string) (modes.SessionDeleteMethod, error) {
	lock, err := state.ownerLock(id)
	if err != nil {
		return modes.SessionDeleteUnlink, err
	}
	acquired, err := lock.TryLock()
	defer func() { _ = lock.Close() }()
	if err != nil {
		return modes.SessionDeleteUnlink, err
	}
	if !acquired {
		return modes.SessionDeleteUnlink, errors.New("cannot delete an open conversation")
	}
	return modes.SessionDeleteUnlink, state.sessions().Delete(context.Background(), harness.SessionMetadata{ID: id})
}

func (state *nativeState) validateMigration(ctx context.Context, sources []sqlite.MigrationSource, sessionDirs []string) error {
	current, err := state.migrationSources(sessionDirs)
	if err != nil {
		return err
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	sort.Slice(current, func(i, j int) bool { return current[i].Path < current[j].Path })
	before, _ := json.Marshal(sources)
	after, _ := json.Marshal(current)
	if !bytes.Equal(before, after) {
		return errors.New("migration inventory changed while importing")
	}
	doc := func(name string) *sqlite.Document { return state.document(filepath.Join(state.agentDir, name)) }
	if err = config.MigrateAuthDocuments(ctx, doc("auth.json"), doc("settings.json"), doc("oauth.json")); err != nil {
		return errors.New("invalid legacy credentials")
	}
	auth, err := state.auth(state.agentDir)
	if err != nil {
		return errors.New("invalid imported credentials")
	}
	if _, err = state.accounts(state.agentDir, auth).Accounts(ctx); err != nil {
		return errors.New("invalid imported accounts")
	}
	trust, err := state.trust(state.agentDir)
	if err != nil {
		return err
	}
	if _, err = trust.Get("/"); err != nil {
		return errors.New("invalid imported trust decisions")
	}
	settings, err := state.settings("", state.agentDir, config.WithProjectTrusted(false))
	if err != nil {
		return err
	}
	if len(settings.DrainErrors()) > 0 {
		return errors.New("invalid imported settings")
	}
	data, err := doc("models.json").Read(ctx)
	if err != nil {
		return err
	}
	modelConfig, err := config.ParseModelConfig(data, "native models")
	if err != nil || modelConfig.Error() != "" {
		return errors.New("invalid imported model configuration")
	}
	return nil
}

func (state *nativeState) address(path string) (string, string) {
	if filepath.Dir(path) == state.agentDir {
		return "config", filepath.Base(path)
	}
	bridge, _ := bridgeDir("personal")
	for _, root := range []struct{ path, prefix string }{{filepath.Dir(bridge), "bridge"}, {filepath.Join(filepath.Dir(filepath.Dir(bridge)), "instances"), "instances"}} {
		relative, err := filepath.Rel(root.path, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return root.prefix + "/" + filepath.ToSlash(filepath.Dir(relative)), filepath.Base(relative)
		}
	}
	return "native", path
}
func (state *nativeState) chatNamespace(path string) string {
	relative, err := filepath.Rel(state.agentDir, path)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "chat/" + filepath.ToSlash(relative)
	}
	return "chat/" + path
}

func (state *nativeState) configureChild(options *agent.AgentSessionOptions) error {
	if state == nil || options.AgentDir != state.agentDir {
		return nil
	}
	var err error
	options.Settings, err = state.settings(options.CWD, state.agentDir)
	if err != nil {
		return err
	}
	if options.ModelRegistry == nil {
		auth, err := state.auth(state.agentDir)
		if err != nil {
			return err
		}
		options.ModelRegistry, err = state.models(state.agentDir, state.accounts(state.agentDir, auth), os.Getenv("PI_OFFLINE") != "")
		if err != nil {
			return err
		}
	}
	if options.SessionManager == nil {
		repo := state.db.Sessions("extensions")
		stored, err := repo.Create(context.Background(), harness.SessionCreateOptions{CWD: options.CWD})
		if err != nil {
			return err
		}
		options.SessionManager, err = session.FromHarnessStorage(stored.Storage(), session.WithHarnessRepo(repo))
		return err
	}
	return nil
}

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
