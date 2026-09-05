package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/internal/uuidv7"
)

// SessionV4CreateOptions mirrors upstream SessionCreateOptions.
type SessionV4CreateOptions struct {
	ID              *string
	ParentSessionID *string
}

// SessionV4RepoForkOptions combines fork selection with new-session identity.
type SessionV4RepoForkOptions struct {
	SessionV4ForkOptions
	SessionV4CreateOptions
}

func sessionV4CreateID(id *string) (string, error) {
	if id != nil {
		return *id, nil
	}
	return uuidv7.Generate(time.Now())
}

// InMemorySessionV4Repo keeps v4 sessions keyed by id in process memory.
type InMemorySessionV4Repo struct {
	mu       sync.Mutex
	sessions map[string]*InMemorySessionV4Storage

	// Now overrides the created-at clock (epoch milliseconds).
	Now func() int64
}

func NewInMemorySessionV4Repo() *InMemorySessionV4Repo {
	return &InMemorySessionV4Repo{sessions: make(map[string]*InMemorySessionV4Storage)}
}

func (repo *InMemorySessionV4Repo) Create(options SessionV4CreateOptions) (*InMemorySessionV4Storage, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	id, err := sessionV4CreateID(options.ID)
	if err != nil {
		return nil, err
	}
	if _, exists := repo.sessions[id]; exists {
		return nil, newSessionError(SessionErrorAlreadyExists, "Session already exists: %s", id)
	}
	storage := NewInMemorySessionV4Storage(SessionV4Metadata{
		ID: id, CreatedAt: sessionV4NowMS(repo.Now), ParentSessionID: options.ParentSessionID,
	})
	storage.Now = repo.Now
	repo.sessions[id] = storage
	return storage, nil
}

func (repo *InMemorySessionV4Repo) Open(metadata SessionV4Metadata) (*InMemorySessionV4Storage, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.requireStorage(metadata.ID)
}

func (repo *InMemorySessionV4Repo) List() []SessionV4Metadata {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	listed := make([]SessionV4Metadata, 0, len(repo.sessions))
	for _, storage := range repo.sessions {
		listed = append(listed, storage.Metadata())
	}
	return listed
}

func (repo *InMemorySessionV4Repo) Delete(metadata SessionV4Metadata) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	delete(repo.sessions, metadata.ID)
}

func (repo *InMemorySessionV4Repo) Fork(source SessionV4Metadata, options SessionV4RepoForkOptions) (*InMemorySessionV4Storage, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	sourceStorage, err := repo.requireStorage(source.ID)
	if err != nil {
		return nil, err
	}
	id, err := sessionV4CreateID(options.ID)
	if err != nil {
		return nil, err
	}
	if _, exists := repo.sessions[id]; exists {
		return nil, newSessionError(SessionErrorAlreadyExists, "Session already exists: %s", id)
	}
	parentSessionID := options.ParentSessionID
	if parentSessionID == nil {
		parentSessionID = &source.ID
	}
	forked, err := sourceStorage.fork(SessionV4Metadata{
		ID: id, CreatedAt: sessionV4NowMS(repo.Now), ParentSessionID: parentSessionID,
	}, options.SessionV4ForkOptions)
	if err != nil {
		return nil, err
	}
	repo.sessions[id] = forked
	return forked, nil
}

func (repo *InMemorySessionV4Repo) requireStorage(id string) (*InMemorySessionV4Storage, error) {
	storage, ok := repo.sessions[id]
	if !ok {
		return nil, newSessionError(SessionErrorNotFound, "Session not found: %s", id)
	}
	return storage, nil
}

// JSONLSessionV4CreateOptions mirrors upstream JsonlSessionCreateOptions.
type JSONLSessionV4CreateOptions struct {
	SessionV4CreateOptions
	CWD      string
	Metadata json.RawMessage
}

// JSONLSessionV4ForkOptions combines fork selection with new-session identity.
type JSONLSessionV4ForkOptions struct {
	SessionV4ForkOptions
	JSONLSessionV4CreateOptions
}

func sessionV4DirectoryName(cwd string) string {
	if len(cwd) > 0 && (cwd[0] == '/' || cwd[0] == '\\') {
		cwd = cwd[1:]
	}
	return "--" + strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(cwd) + "--"
}

func sessionV4FileName(createdAt int64, id string) string {
	timestamp := time.UnixMilli(createdAt).UTC().Format("2006-01-02T15:04:05.000Z")
	timestamp = strings.NewReplacer(":", "-", ".", "-").Replace(timestamp)
	return timestamp + "_" + transactionEncodeID(id) + ".jsonl"
}

// ListJSONLSessionV4Metadata lists v4 sessions below a sessions root without
// opening them: only each file's header line is read, and a file whose header
// is missing or undecodable is skipped instead of failing the listing.
func ListJSONLSessionV4Metadata(ctx context.Context, fs FileSystem, sessionsRoot string, cwd *string) ([]JSONLSessionV4Metadata, error) {
	return NewJSONLSessionV4Repo(fs, sessionsRoot).List(ctx, cwd)
}

// OpenJSONLSessionV4Storage loads one listed v4 session, rejecting metadata
// whose id no longer matches the file header.
func OpenJSONLSessionV4Storage(ctx context.Context, fs FileSystem, sessionsRoot string, metadata JSONLSessionV4Metadata) (*JSONLSessionV4Storage, error) {
	return NewJSONLSessionV4Repo(fs, sessionsRoot).loadStorage(ctx, metadata)
}

// JSONLSessionV4Repo stores v4 sessions in coding-agent-compatible cwd-encoded
// directories below one sessions root.
type JSONLSessionV4Repo struct {
	fs               FileSystem
	sessionsRootPath string

	// claimed reserves in-flight create/fork destinations: the durable filename
	// carries a timestamp, so the existence check alone lets two concurrent
	// calls both decide the same {cwd, id} is free and publish duplicates.
	claimMu      sync.Mutex
	claimed      map[string]bool
	openSessions map[string]*JSONLSessionV4Storage
	closed       bool

	// Now overrides the created-at clock (epoch milliseconds).
	Now func() int64
}

func NewJSONLSessionV4Repo(fs FileSystem, sessionsRoot string) *JSONLSessionV4Repo {
	return &JSONLSessionV4Repo{fs: fs, sessionsRootPath: sessionsRoot, claimed: map[string]bool{}, openSessions: map[string]*JSONLSessionV4Storage{}}
}

// claimDestination resolves and reserves the {cwd, id} a create or fork
// publishes to, returning the release for the caller to defer.
func (repo *JSONLSessionV4Repo) claimDestination(ctx context.Context, options JSONLSessionV4CreateOptions) (id, cwd string, release func(), err error) {
	if id, err = sessionV4CreateID(options.ID); err != nil {
		return "", "", nil, err
	}

	if cwd, err = repo.fs.AbsolutePath(ctx, options.CWD); err != nil {
		return "", "", nil, fileV4Result(err, "Failed to resolve session cwd %s", options.CWD)
	}
	key := cwd + "\x00" + id
	repo.claimMu.Lock()
	defer repo.claimMu.Unlock()
	if repo.closed {
		return "", "", nil, fmt.Errorf("JsonlSessionRepo is closed")
	}
	if repo.claimed[key] || repo.openSessions[key] != nil {
		return "", "", nil, newSessionError(SessionErrorAlreadyExists, "Session already exists: %s", id)
	}
	if repo.claimed == nil {
		repo.claimed = map[string]bool{}
	}
	repo.claimed[key] = true
	return id, cwd, func() {
		repo.claimMu.Lock()
		delete(repo.claimed, key)
		repo.claimMu.Unlock()
	}, nil
}

func (repo *JSONLSessionV4Repo) Create(ctx context.Context, options JSONLSessionV4CreateOptions) (*JSONLSessionV4Storage, error) {
	id, cwd, release, err := repo.claimDestination(ctx, options)
	if err != nil {
		return nil, err
	}
	defer release()
	header, path, err := repo.prepareCreate(ctx, id, cwd, options)
	if err != nil {
		return nil, err
	}
	storage, err := CreateJSONLSessionV4Storage(ctx, repo.fs, path, header)
	if err != nil {
		return nil, err
	}
	storage.Now = repo.Now
	return storage, repo.publishTransactionStorage(storage)
}

func (repo *JSONLSessionV4Repo) Open(ctx context.Context, metadata JSONLSessionV4Metadata) (*JSONLSessionV4Storage, error) {
	repo.claimMu.Lock()
	if repo.closed {
		repo.claimMu.Unlock()
		return nil, fmt.Errorf("JsonlSessionRepo is closed")
	}
	active := repo.openSessions[metadata.CWD+"\x00"+metadata.ID] != nil
	repo.claimMu.Unlock()
	if active {
		return nil, fmt.Errorf("Session is already open: %s", metadata.ID)
	}
	storage, err := repo.loadStorage(ctx, metadata)
	if err != nil {
		return nil, err
	}
	if err = repo.publishTransactionStorage(storage); err != nil {
		_ = storage.Close(ctx)
		return nil, err
	}
	return storage, nil
}

func (repo *JSONLSessionV4Repo) List(ctx context.Context, cwd *string) ([]JSONLSessionV4Metadata, error) {
	repo.claimMu.Lock()
	closed := repo.closed
	repo.claimMu.Unlock()
	if closed {
		return nil, fmt.Errorf("JsonlSessionRepo is closed")
	}
	directories, err := repo.sessionDirectories(ctx, cwd)
	if err != nil {
		return nil, err
	}
	metadata := make([]JSONLSessionV4Metadata, 0)
	for _, directory := range directories {
		files, err := repo.fs.ListDir(ctx, directory)
		if err != nil {
			return nil, fileV4Result(err, "Failed to list sessions directory %s", directory)
		}
		for _, file := range files {
			if file.Kind == FileKindDirectory || !strings.HasSuffix(file.Name, ".jsonl") {
				continue
			}
			// Listing reads only the header line, and a session whose header is
			// missing or undecodable is skipped: one corrupt file must not fail
			// the whole listing. Opening it still reports invalid_entry.
			lines, err := repo.fs.ReadTextLines(ctx, file.Path, 1)
			if err != nil {
				return nil, fileV4Result(err, "Failed to read session header %s", file.Path)
			}
			if len(lines) == 0 || lines[0] == "" {
				continue
			}
			releasedHeader, decodeErr := DecodeSessionV4TransactionHeader([]byte(lines[0]))
			header := SessionV4Header{ID: releasedHeader.ID, CreatedAt: releasedHeader.CreatedAt, CWD: releasedHeader.CWD, ParentSessionID: releasedHeader.ParentSessionID, LegacyParentSessionPath: releasedHeader.LegacyParentSessionPath}
			if decodeErr != nil {
				legacy, valid := parseTransactionLegacyHeader([]byte(lines[0]))
				if !valid {
					continue
				}
				stamp, _ := time.Parse(time.RFC3339Nano, legacy.Timestamp)
				header = SessionV4Header{ID: legacy.ID, CreatedAt: stamp.UnixMilli(), CWD: legacy.CWD}
				releasedHeader.StorageVersion = 1
				if legacy.ParentSession != nil {
					parentLines, readErr := repo.fs.ReadTextLines(ctx, *legacy.ParentSession, 1)
					if readErr == nil && len(parentLines) > 0 {
						if parent, err := DecodeSessionV4TransactionHeader([]byte(parentLines[0])); err == nil {
							header.ParentSessionID = &parent.ID
						} else if parent, ok := parseTransactionLegacyHeader([]byte(parentLines[0])); ok {
							header.ParentSessionID = &parent.ID
						}
					}
					if header.ParentSessionID == nil {
						header.LegacyParentSessionPath = legacy.ParentSession
					}
				}
			}
			if cwd != nil {
				resolved, resolveErr := repo.fs.AbsolutePath(ctx, *cwd)
				if resolveErr != nil {
					return nil, resolveErr
				}
				if header.CWD != resolved {
					continue
				}
			}
			discovered := jsonlV4Metadata(header, file.Path, file.MTimeMS)
			discovered.StorageVersion = releasedHeader.StorageVersion
			metadata = append(metadata, discovered)
		}
	}
	sort.SliceStable(metadata, func(left, right int) bool {
		if metadata[left].CreatedAt != metadata[right].CreatedAt {
			return metadata[left].CreatedAt > metadata[right].CreatedAt
		}
		if metadata[left].ID != metadata[right].ID {
			return metadata[left].ID < metadata[right].ID
		}
		return metadata[left].CWD < metadata[right].CWD
	})
	return metadata, nil
}

func (repo *JSONLSessionV4Repo) Delete(ctx context.Context, metadata JSONLSessionV4Metadata) error {
	repo.claimMu.Lock()
	closed := repo.closed
	active := repo.openSessions[metadata.CWD+"\x00"+metadata.ID] != nil
	repo.claimMu.Unlock()
	if closed {
		return fmt.Errorf("JsonlSessionRepo is closed")
	}
	if active {
		return fmt.Errorf("Session is open: %s", metadata.ID)
	}
	exists, err := repo.fs.Exists(ctx, metadata.Path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("Session file does not exist: %s", metadata.Path)
	}
	return fileV4Result(repo.fs.Remove(ctx, metadata.Path, false, false), "Failed to delete session %s", metadata.Path)
}

func (repo *JSONLSessionV4Repo) Fork(ctx context.Context, source JSONLSessionV4Metadata, options JSONLSessionV4ForkOptions) (*JSONLSessionV4Storage, error) {
	repo.claimMu.Lock()
	sourceStorage := repo.openSessions[source.CWD+"\x00"+source.ID]
	repo.claimMu.Unlock()
	var err error
	if sourceStorage == nil {
		sourceStorage, err = repo.loadStorage(ctx, source)
		if err == nil {
			defer func() { _ = sourceStorage.Close(ctx) }()
		}
	}
	if err != nil {
		return nil, err
	}
	options.ParentSessionID = &source.ID
	options.CWD = source.CWD
	id, cwd, release, err := repo.claimDestination(ctx, options.JSONLSessionV4CreateOptions)
	if err != nil {
		return nil, err
	}
	defer release()
	header, path, err := repo.prepareCreate(ctx, id, cwd, options.JSONLSessionV4CreateOptions)
	if err != nil {
		return nil, err
	}
	forked, err := sourceStorage.Fork(ctx, path, header, options.SessionV4ForkOptions)
	if err != nil {
		return nil, err
	}
	forked.Now = repo.Now
	return forked, repo.publishTransactionStorage(forked)
}

func (repo *JSONLSessionV4Repo) loadStorage(ctx context.Context, metadata JSONLSessionV4Metadata) (*JSONLSessionV4Storage, error) {
	exists, err := repo.fs.Exists(ctx, metadata.Path)
	if err != nil {
		return nil, fileV4Result(err, "Failed to check session %s", metadata.Path)
	}
	if !exists {
		return nil, newSessionError(SessionErrorNotFound, "Session not found: %s", metadata.ID)
	}
	storage, err := LoadJSONLSessionV4Storage(ctx, repo.fs, metadata.Path)
	if err != nil {
		return nil, err
	}
	if storage.Metadata().ID != metadata.ID || storage.Metadata().CWD != metadata.CWD {
		return nil, newSessionError(SessionErrorInvalidEntry, "Session identity does not match header: %s", metadata.ID)
	}
	storage.Now = repo.Now
	return storage, nil
}

func (repo *JSONLSessionV4Repo) prepareCreate(ctx context.Context, id, cwd string, options JSONLSessionV4CreateOptions) (SessionV4Header, string, error) {
	exists, err := repo.sessionIDExists(ctx, id, cwd)
	if err != nil {
		return SessionV4Header{}, "", err
	}
	if exists {
		return SessionV4Header{}, "", newSessionError(SessionErrorAlreadyExists, "Session already exists: %s", id)
	}
	createdAt := sessionV4NowMS(repo.Now)
	directory, err := repo.sessionDirectory(ctx, cwd)
	if err != nil {
		return SessionV4Header{}, "", err
	}
	path, err := repo.fs.JoinPath(ctx, directory, sessionV4FileName(createdAt, id))
	if err != nil {
		return SessionV4Header{}, "", fileV4Result(err, "Failed to resolve path for session %s", id)
	}

	if err := repo.fs.CreateDir(ctx, directory, true); err != nil {
		return SessionV4Header{}, "", fileV4Result(err, "Failed to create sessions directory")
	}
	return SessionV4Header{
		ID: id, CreatedAt: createdAt, CWD: cwd,
		ParentSessionID: options.ParentSessionID, Metadata: cloneHarnessRaw(options.Metadata),
	}, path, nil
}

func (repo *JSONLSessionV4Repo) sessionIDExists(ctx context.Context, id, cwd string) (bool, error) {
	directory, err := repo.sessionDirectory(ctx, cwd)
	if err != nil {
		return false, err
	}
	exists, err := repo.fs.Exists(ctx, directory)
	if err != nil {
		return false, fileV4Result(err, "Failed to check sessions directory %s", directory)
	}
	if !exists {
		return false, nil
	}
	files, err := repo.fs.ListDir(ctx, directory)
	if err != nil {
		return false, fileV4Result(err, "Failed to list sessions directory %s", directory)
	}
	suffix := "_" + transactionEncodeID(id) + ".jsonl"
	for _, file := range files {
		if file.Kind != FileKindDirectory && strings.HasSuffix(file.Name, suffix) {
			return true, nil
		}
	}
	return false, nil
}

func (repo *JSONLSessionV4Repo) sessionDirectories(ctx context.Context, cwd *string) ([]string, error) {
	root, err := repo.root(ctx)
	if err != nil {
		return nil, err
	}
	if cwd != nil {
		resolvedCwd, err := repo.fs.AbsolutePath(ctx, *cwd)
		if err != nil {
			return nil, fileV4Result(err, "Failed to resolve session cwd %s", *cwd)
		}
		directory, err := repo.sessionDirectory(ctx, resolvedCwd)
		if err != nil {
			return nil, err
		}
		exists, err := repo.fs.Exists(ctx, directory)
		if err != nil {
			return nil, fileV4Result(err, "Failed to check sessions directory %s", directory)
		}
		if !exists {
			return nil, nil
		}
		return []string{directory}, nil
	}
	exists, err := repo.fs.Exists(ctx, root)
	if err != nil {
		return nil, fileV4Result(err, "Failed to check sessions directory %s", root)
	}
	if !exists {
		return nil, nil
	}
	listed, err := repo.fs.ListDir(ctx, root)
	if err != nil {
		return nil, fileV4Result(err, "Failed to list sessions directory %s", root)
	}
	directories := make([]string, 0, len(listed))
	for _, entry := range listed {
		if entry.Kind == FileKindDirectory {
			directories = append(directories, entry.Path)
		}
	}
	return directories, nil
}

func (repo *JSONLSessionV4Repo) sessionDirectory(ctx context.Context, cwd string) (string, error) {
	root, err := repo.root(ctx)
	if err != nil {
		return "", err
	}
	directory, err := repo.fs.JoinPath(ctx, root, sessionV4DirectoryName(cwd))
	if err != nil {
		return "", fileV4Result(err, "Failed to resolve sessions directory for %s", cwd)
	}
	return directory, nil
}

func (repo *JSONLSessionV4Repo) root(ctx context.Context) (string, error) {
	root, err := repo.fs.AbsolutePath(ctx, repo.sessionsRootPath)
	if err != nil {
		return "", fileV4Result(err, "Failed to resolve sessions root %s", repo.sessionsRootPath)
	}
	return root, nil
}

func transactionEncodeID(id string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.!~*'()", rune(c)) {
			encoded.WriteByte(c)
		} else {
			encoded.WriteByte('%')
			encoded.WriteByte(hex[c>>4])
			encoded.WriteByte(hex[c&15])
		}
	}
	return encoded.String()
}

func (repo *JSONLSessionV4Repo) publishTransactionStorage(storage *JSONLSessionV4Storage) error {
	repo.claimMu.Lock()
	defer repo.claimMu.Unlock()
	key := storage.metadata.CWD + "\x00" + storage.metadata.ID
	if repo.openSessions == nil {
		repo.openSessions = map[string]*JSONLSessionV4Storage{}
	}
	if repo.openSessions[key] != nil {
		return fmt.Errorf("Session is already open: %s", storage.metadata.ID)
	}
	repo.openSessions[key] = storage
	storage.onClose = func() {
		repo.claimMu.Lock()
		defer repo.claimMu.Unlock()
		if repo.openSessions[key] == storage {
			delete(repo.openSessions, key)
		}
	}
	return nil
}
func (repo *JSONLSessionV4Repo) Close(_ context.Context) error {
	repo.claimMu.Lock()
	defer repo.claimMu.Unlock()
	repo.closed = true
	return nil
}
