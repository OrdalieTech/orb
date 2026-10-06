// Package native is the native host's durable state: the SQLite database that
// holds settings, credentials, model catalogs, sessions and Bridge documents,
// its one-time import from Pi-compatible files, and the per-conversation owner
// locks that keep one Orb process writing each conversation (DECISIONS.md
// "Native SQLite storage").
package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/host"
	nativeaccounts "github.com/OrdalieTech/orb/platforms/native/accounts"
	nativebridge "github.com/OrdalieTech/orb/platforms/native/bridge"
	"github.com/OrdalieTech/orb/platforms/native/sqlite"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/gofrs/flock"
)

var ErrLegacyMigration = errors.New("legacy Orb data needs migration; close other Orb processes, then run orb storage migrate")

// RunningError names another Orb still writing the data a migration moves.
type RunningError struct{ PID int }

func (e RunningError) Error() string {
	return fmt.Sprintf("close other Orb processes before migration (process %d is still running)", e.PID)
}

// StopLegacyWriters clears the way for the one-time migration. An Orb still
// running the previous version would keep writing files this one no longer
// reads, so confirm, given its pid and command line, decides whether to stop
// each; a nil confirm refuses.
func StopLegacyWriters(ctx context.Context, agentDir string, confirm func(pid int, command string) bool) error {
	for {
		err := RequireOfflineMigration(ctx, agentDir)
		var running RunningError
		if !errors.As(err, &running) || confirm == nil || !confirm(running.PID, describeProcess(running.PID)) {
			return err
		}
		if err = stopProcess(running.PID); err != nil {
			return err
		}
	}
}

// State is the open native database for one agent directory. A nil State is
// the file-backed fallback: every constructor then reads the Pi-compatible
// files under the agent directory.
type State struct {
	DB       *sqlite.DB
	AgentDir string
	// credentials, when set, holds auth.json instead of the database.
	credentials host.Document

	mu          sync.Mutex
	sessionID   string
	sessionLock *flock.Flock
}

// Path is the database file for agentDir: ORB_STATE_HOME, else beside an
// explicit agent dir or Bridge home, else ~/.orb/state.
func Path(agentDir string) (string, error) {
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

// Migrated reports whether agentDir's state already lives in the database.
func Migrated(ctx context.Context, agentDir string) (bool, error) {
	path, err := Path(agentDir)
	if err != nil {
		return false, err
	}
	return sqlite.MigrationStatus(ctx, path, "native")
}

// Open opens agentDir's database, importing the legacy files and sessionDirs
// once when migrate is set; without it, pending legacy data is
// ErrLegacyMigration. credentials, when set, replaces auth.json.
func Open(ctx context.Context, agentDir string, migrate bool, credentials host.Document, sessionDirs ...string) (_ *State, err error) {
	agentDir, err = filepath.Abs(agentDir)
	if err != nil {
		return nil, err
	}
	path, err := Path(agentDir)
	if err != nil {
		return nil, err
	}
	done, err := sqlite.MigrationStatus(ctx, path, "native")
	if err != nil {
		return nil, err
	}
	state := &State{AgentDir: agentDir, credentials: credentials}
	if done {
		if len(sessionDirs) > 0 {
			return nil, errors.New("native storage is already migrated; import additional JSONL files with orb storage import <path>")
		}
		state.DB, err = sqlite.Open(ctx, path)
		return state, err
	}
	sources, err := state.migrationSources(sessionDirs)
	if err != nil {
		return nil, err
	}
	if len(sources) > 0 && !migrate {
		return nil, ErrLegacyMigration
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
	state.DB, err = sqlite.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = state.DB.Close()
		}
	}()
	if err = state.DB.Migrate(ctx, "native", sources, func() error { return state.validateMigration(ctx, sources, sessionDirs) }); err != nil {
		return nil, err
	}
	return state, nil
}

// Document implements host.Store: kernel paths under the agent dir and the
// Bridge homes map to their namespaces, anything else is keyed by its path.
func (state *State) Document(path string) host.Document {
	namespace, key := state.address(path)
	return state.DB.Document(namespace, key)
}

func (state *State) config(name string) host.Document {
	return state.Document(filepath.Join(state.AgentDir, name))
}

func (state *State) Sessions() *sqlite.Sessions { return state.DB.Sessions("personal") }

func (state *State) migrationSources(sessionDirs []string) ([]sqlite.MigrationSource, error) {
	var sources []sqlite.MigrationSource
	for _, name := range []string{"settings.json", "auth.json", "accounts.json", "trust.json", "models.json", "models-store.json", "keybindings.json", "oauth.json"} {
		path := filepath.Join(state.AgentDir, name)
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
	settings, err := config.NewSettingsManager("", config.WithAgentDir(state.AgentDir), config.WithProjectTrusted(false))
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
	for _, root := range append([]string{filepath.Join(state.AgentDir, "sessions"), configured}, sessionDirs...) {
		if err := walk(root, func(path string, _ fs.DirEntry) (sqlite.MigrationSource, bool) {
			return sqlite.MigrationSource{Path: path, Namespace: "personal", Kind: "session"}, strings.HasSuffix(path, ".jsonl")
		}); err != nil {
			return nil, err
		}
	}
	bridge, err := nativebridge.Dir("personal")
	if err != nil {
		return nil, err
	}
	for _, root := range []string{filepath.Dir(bridge), filepath.Join(filepath.Dir(filepath.Dir(bridge)), "instances"), filepath.Join(state.AgentDir, "memory"), filepath.Join(state.AgentDir, "chat"), os.Getenv("ORB_CHAT_DATA_DIR")} {
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
				source.Namespace = state.ChatNamespace(filepath.Dir(path))
			default:
				if !strings.HasSuffix(path, ".jsonl") {
					return source, false
				}
				source.Kind = "session"
				source.Namespace = state.ChatNamespace(filepath.Dir(path))
			}
			return source, true
		}); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

func (state *State) validateMigration(ctx context.Context, sources []sqlite.MigrationSource, sessionDirs []string) error {
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
	if err = config.MigrateAuthDocuments(ctx, state.config("auth.json"), state.config("settings.json"), state.config("oauth.json")); err != nil {
		return errors.New("invalid legacy credentials")
	}
	auth, err := state.Auth(state.AgentDir)
	if err != nil {
		return errors.New("invalid imported credentials")
	}
	if _, err = state.Accounts(state.AgentDir, auth).Accounts(ctx); err != nil {
		return errors.New("invalid imported accounts")
	}
	trust, err := state.Trust(state.AgentDir)
	if err != nil {
		return err
	}
	if _, err = trust.Get("/"); err != nil {
		return errors.New("invalid imported trust decisions")
	}
	settings, err := state.Settings("", state.AgentDir, config.WithProjectTrusted(false))
	if err != nil {
		return err
	}
	if len(settings.DrainErrors()) > 0 {
		return errors.New("invalid imported settings")
	}
	data, err := state.config("models.json").Read(ctx)
	if err != nil {
		return err
	}
	modelConfig, err := config.ParseModelConfig(data, "native models")
	if err != nil || modelConfig.Error() != "" {
		return errors.New("invalid imported model configuration")
	}
	return nil
}

func (state *State) address(path string) (string, string) {
	if filepath.Dir(path) == state.AgentDir {
		return "config", filepath.Base(path)
	}
	bridge, _ := nativebridge.Dir("personal")
	for _, root := range []struct{ path, prefix string }{{filepath.Dir(bridge), "bridge"}, {filepath.Join(filepath.Dir(filepath.Dir(bridge)), "instances"), "instances"}} {
		relative, err := filepath.Rel(root.path, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return root.prefix + "/" + filepath.ToSlash(filepath.Dir(relative)), filepath.Base(relative)
		}
	}
	return "native", path
}

// ChatNamespace is the namespace of a chat gateway's data directory.
func (state *State) ChatNamespace(path string) string {
	relative, err := filepath.Rel(state.AgentDir, path)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "chat/" + filepath.ToSlash(relative)
	}
	return "chat/" + path
}

func (state *State) Settings(cwd, agentDir string, options ...config.Option) (*config.SettingsManager, error) {
	options = append(options, config.WithAgentDir(agentDir))
	if state != nil {
		options = append(options, config.WithGlobalDocument(state.Document(filepath.Join(agentDir, "settings.json"))))
	}
	return config.NewSettingsManager(cwd, options...)
}

func (state *State) Auth(agentDir string) (*config.AuthStorage, error) {
	switch {
	case state == nil:
		return config.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	case state.credentials != nil:
		return config.NewAuthStorageWithDocument(state.credentials)
	}
	return config.NewAuthStorageWithDocument(state.Document(filepath.Join(agentDir, "auth.json")))
}

func (state *State) Trust(agentDir string) (*config.ProjectTrustStore, error) {
	if state == nil {
		return config.NewProjectTrustStore(agentDir), nil
	}
	return config.NewProjectTrustStoreWithDocument(state.Document(filepath.Join(agentDir, "trust.json")))
}

func (state *State) Accounts(agentDir string, base auth.CredentialStore) *accounts.Store {
	path := filepath.Join(agentDir, "accounts.json")
	if state == nil {
		return nativeaccounts.NewStore(path, base)
	}
	return accounts.NewStoreWithDocument(state.Document(path), base)
}

func (state *State) Models(agentDir string, credentials auth.CredentialStore, offline bool) (*config.ModelRegistry, error) {
	if state == nil {
		if offline {
			return config.NewOfflineModelRegistry(agentDir)
		}
		return config.NewModelRegistryWithCredentials(agentDir, credentials)
	}
	return config.NewModelRegistryWithDocuments(agentDir, credentials, state.Document(filepath.Join(agentDir, "models.json")), state.Document(filepath.Join(agentDir, "models-store.json")), !offline)
}

func (state *State) BridgeStore(path string, quota int) (*nativebridge.Store, error) {
	if state == nil {
		return nativebridge.OpenStore(path, quota, nil)
	}
	return nativebridge.OpenStore(path, quota, state.Document(path))
}

// Read and Write fall back to private files without a database; an absent
// document reads as os.ErrNotExist, and writing nil deletes it.
func (state *State) Read(ctx context.Context, path string) ([]byte, error) {
	if state == nil {
		return os.ReadFile(path)
	}
	data, err := state.Document(path).Read(ctx)
	if err == nil && data == nil {
		err = os.ErrNotExist
	}
	return data, err
}

func (state *State) Write(ctx context.Context, path string, data []byte) error {
	if state == nil {
		if data == nil {
			return os.Remove(path)
		}
		return os.WriteFile(path, data, 0600)
	}
	return state.Document(path).Update(ctx, func([]byte) ([]byte, error) { return data, nil })
}

func (state *State) Memory() memory.Store {
	if state == nil {
		return nil
	}
	return state.DB.Memory("personal")
}

// ChildSession is a new session for an extension's child agent.
func (state *State) ChildSession(cwd string) (*session.SessionManager, error) {
	repo := state.DB.Sessions("extensions")
	stored, err := repo.Create(context.Background(), harness.SessionCreateOptions{CWD: cwd})
	if err != nil {
		return nil, err
	}
	return session.FromHarnessStorage(stored.Storage(), session.WithHarnessRepo(repo))
}

// OwnerLock is the lock an Orb process holds while it writes conversation id.
func (state *State) OwnerLock(id string) (*flock.Flock, error) {
	path, err := Path(state.AgentDir)
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

// BindSession claims manager's conversation and keeps the claim.
func (state *State) BindSession(manager *session.SessionManager) error {
	release, err := state.ClaimSession(manager)
	if err == nil {
		release()
	}
	return err
}

// ClaimSession takes manager's conversation for this state, refusing one
// another Orb process holds; the returned func releases the previous claim.
func (state *State) ClaimSession(manager *session.SessionManager) (func(), error) {
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
		lock, err = state.OwnerLock(id)
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

func (state *State) Close() error {
	state.Release()
	return state.DB.Close()
}

// Conversation shares this state's store with a claim of its own, for a
// process that keeps several conversations open at once (ACP).
func (state *State) Conversation() *State {
	if state == nil {
		return nil
	}
	return &State{DB: state.DB, AgentDir: state.AgentDir, credentials: state.credentials}
}

// Release drops the conversation claim, discarding a conversation left empty.
func (state *State) Release() {
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
func (state *State) discardIfEmpty(id string) {
	if id == "" {
		return
	}
	lock, err := state.OwnerLock(id)
	if err != nil {
		return
	}
	defer func() { _ = lock.Close() }()
	if acquired, err := lock.TryLock(); err == nil && acquired {
		_ = state.Sessions().DeleteIfEmpty(context.Background(), id)
	}
}

// PruneEmpty drops the empty conversations earlier runs left behind. A minute
// of grace covers one that another Orb created but has not claimed yet.
func (state *State) PruneEmpty(ctx context.Context) {
	ids, _ := state.Sessions().EmptyIDs(ctx, time.Now().Add(-time.Minute))
	for _, id := range ids {
		state.discardIfEmpty(id)
	}
}

// DeleteSession deletes a stored conversation no Orb holds open.
func (state *State) DeleteSession(id string) error {
	lock, err := state.OwnerLock(id)
	if err != nil {
		return err
	}
	acquired, err := lock.TryLock()
	defer func() { _ = lock.Close() }()
	if err != nil {
		return err
	}
	if !acquired {
		return errors.New("cannot delete an open conversation")
	}
	return state.Sessions().Delete(context.Background(), harness.SessionMetadata{ID: id})
}
