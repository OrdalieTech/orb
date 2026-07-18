package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/internal/uuidv7"
)

type SessionCreateOptions struct {
	ID                string
	CWD               string
	ParentSessionPath *string
	Metadata          json.RawMessage
}

type SessionForkOptions struct {
	SessionCreateOptions
	EntryID  string
	Position ForkPosition
}

type SessionListOptions struct {
	CWD string
}

type SessionRepo interface {
	Create(context.Context, SessionCreateOptions) (*Session, error)
	Open(context.Context, SessionMetadata) (*Session, error)
	List(context.Context, SessionListOptions) ([]SessionMetadata, error)
	Delete(context.Context, SessionMetadata) error
	Fork(context.Context, SessionMetadata, SessionForkOptions) (*Session, error)
}

// InMemorySessionRepo retains insertion ordering, matching upstream's Map.
type InMemorySessionRepo struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	order    []string
}

func NewInMemorySessionRepo() *InMemorySessionRepo {
	return &InMemorySessionRepo{sessions: make(map[string]*Session)}
}

func (repo *InMemorySessionRepo) Create(_ context.Context, options SessionCreateOptions) (*Session, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	id := options.ID
	if id == "" {
		var err error
		id, err = uuidv7.Generate(time.Now())
		if err != nil {
			return nil, err
		}
	}
	metadata := SessionMetadata{ID: id, CreatedAt: formatHarnessTimestamp(time.Now())}
	storage, err := NewInMemorySessionStorage(nil, metadata)
	if err != nil {
		return nil, err
	}
	session := NewSession(storage)
	if _, exists := repo.sessions[id]; !exists {
		repo.order = append(repo.order, id)
	}
	repo.sessions[id] = session
	return session, nil
}

func (repo *InMemorySessionRepo) Open(_ context.Context, metadata SessionMetadata) (*Session, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	session, ok := repo.sessions[metadata.ID]
	if !ok {
		return nil, newSessionError(SessionErrorNotFound, "Session not found: %s", metadata.ID)
	}
	return session, nil
}

func (repo *InMemorySessionRepo) List(context.Context, SessionListOptions) ([]SessionMetadata, error) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	result := make([]SessionMetadata, 0, len(repo.order))
	for _, id := range repo.order {
		if session, ok := repo.sessions[id]; ok {
			result = append(result, session.Metadata())
		}
	}
	return result, nil
}

func (repo *InMemorySessionRepo) Delete(_ context.Context, metadata SessionMetadata) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	delete(repo.sessions, metadata.ID)
	for index, id := range repo.order {
		if id == metadata.ID {
			repo.order = append(repo.order[:index], repo.order[index+1:]...)
			break
		}
	}
	return nil
}

func (repo *InMemorySessionRepo) Fork(ctx context.Context, sourceMetadata SessionMetadata, options SessionForkOptions) (*Session, error) {
	source, err := repo.Open(ctx, sourceMetadata)
	if err != nil {
		return nil, err
	}
	entries, err := EntriesToFork(source.Storage(), options.EntryID, options.Position)
	if err != nil {
		return nil, err
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	id := options.ID
	if id == "" {
		id, err = uuidv7.Generate(time.Now())
		if err != nil {
			return nil, err
		}
	}
	metadata := SessionMetadata{ID: id, CreatedAt: formatHarnessTimestamp(time.Now())}
	storage, err := NewInMemorySessionStorage(entries, metadata)
	if err != nil {
		return nil, err
	}
	session := NewSession(storage)
	if _, exists := repo.sessions[id]; !exists {
		repo.order = append(repo.order, id)
	}
	repo.sessions[id] = session
	return session, nil
}

type JSONLSessionRepo struct {
	FS           FileSystem
	SessionsRoot string

	mu           sync.Mutex
	resolvedRoot string
}

func NewJSONLSessionRepo(fileSystem FileSystem, sessionsRoot string) *JSONLSessionRepo {
	return &JSONLSessionRepo{FS: fileSystem, SessionsRoot: sessionsRoot}
}

func (repo *JSONLSessionRepo) sessionsRoot(ctx context.Context) (string, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.resolvedRoot != "" {
		return repo.resolvedRoot, nil
	}
	root, err := repo.FS.AbsolutePath(ctx, repo.SessionsRoot)
	if err != nil {
		return "", fileSystemSessionError(err, "Failed to resolve sessions root %s", repo.SessionsRoot)
	}
	repo.resolvedRoot = root
	return root, nil
}

func encodeHarnessCWD(cwd string) string {
	if strings.HasPrefix(cwd, "/") || strings.HasPrefix(cwd, "\\") {
		cwd = cwd[1:]
	}
	cwd = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(cwd)
	return "--" + cwd + "--"
}

func (repo *JSONLSessionRepo) sessionDir(ctx context.Context, cwd string) (string, error) {
	root, err := repo.sessionsRoot(ctx)
	if err != nil {
		return "", err
	}
	path, err := repo.FS.JoinPath(ctx, root, encodeHarnessCWD(cwd))
	if err != nil {
		return "", fileSystemSessionError(err, "Failed to resolve session directory for %s", cwd)
	}
	return path, nil
}

func (repo *JSONLSessionRepo) Create(ctx context.Context, options SessionCreateOptions) (*Session, error) {
	id := options.ID
	var err error
	if id == "" {
		id, err = uuidv7.Generate(time.Now())
		if err != nil {
			return nil, err
		}
	}
	createdAt := formatHarnessTimestamp(time.Now())
	dir, err := repo.sessionDir(ctx, options.CWD)
	if err != nil {
		return nil, err
	}
	if err := repo.FS.CreateDir(ctx, dir, true); err != nil {
		return nil, fileSystemSessionError(err, "Failed to create session directory %s", dir)
	}
	name := strings.NewReplacer(":", "-", ".", "-").Replace(createdAt) + "_" + id + ".jsonl"
	path, err := repo.FS.JoinPath(ctx, dir, name)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to resolve session file path for %s", id)
	}
	metadata := SessionMetadata{
		ID: id, CreatedAt: createdAt, CWD: options.CWD, Path: path,
		ParentSessionPath: cloneHarnessString(options.ParentSessionPath), Metadata: cloneHarnessRaw(options.Metadata),
	}
	if err := validateHarnessMetadata(metadata); err != nil {
		return nil, err
	}
	header, err := encodeHarnessHeader(metadata)
	if err != nil {
		return nil, err
	}
	if err := repo.FS.WriteFile(ctx, path, header); err != nil {
		return nil, fileSystemSessionError(err, "Failed to create session %s", path)
	}
	storage, err := rehydrateJSONLSession(header, path, func(line []byte) error {
		return repo.FS.AppendFile(context.Background(), path, line)
	})
	if err != nil {
		return nil, err
	}
	return NewSession(storage), nil
}

func (repo *JSONLSessionRepo) Open(ctx context.Context, metadata SessionMetadata) (*Session, error) {
	exists, err := repo.FS.Exists(ctx, metadata.Path)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to check session %s", metadata.Path)
	}
	if !exists {
		return nil, newSessionError(SessionErrorNotFound, "Session not found: %s", metadata.Path)
	}
	content, err := repo.FS.ReadBinaryFile(ctx, metadata.Path)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to read session %s", metadata.Path)
	}
	storage, err := rehydrateJSONLSession(content, metadata.Path, func(line []byte) error {
		return repo.FS.AppendFile(context.Background(), metadata.Path, line)
	})
	if err != nil {
		return nil, err
	}
	return NewSession(storage), nil
}

// OpenPath resolves and opens one explicit JSONL path without filtering it
// through List, so malformed sessions remain observable to callers.
func (repo *JSONLSessionRepo) OpenPath(ctx context.Context, path string) (*Session, error) {
	resolved, err := repo.FS.AbsolutePath(ctx, path)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to resolve session %s", path)
	}
	return repo.Open(ctx, SessionMetadata{Path: resolved})
}

// OpenRuntimePath composes the harness repository with coding-session resume
// semantics for missing and zero-byte explicit files.
func (repo *JSONLSessionRepo) OpenRuntimePath(ctx context.Context, path, cwd string) (*Session, error) {
	resolved, err := repo.FS.AbsolutePath(ctx, path)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to resolve session %s", path)
	}
	exists, err := repo.FS.Exists(ctx, resolved)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to check session %s", resolved)
	}
	if !exists {
		return repo.newRuntimeSession(ctx, resolved, cwd, false)
	}
	content, err := repo.FS.ReadBinaryFile(ctx, resolved)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to read session %s", resolved)
	}
	if len(content) == 0 {
		return repo.newRuntimeSession(ctx, resolved, cwd, true)
	}
	storage, err := rehydrateRuntimeJSONLSession(content, resolved, func(line []byte) error {
		return repo.FS.AppendFile(context.Background(), resolved, line)
	})
	if err != nil {
		return nil, fmt.Errorf("Session file is not a valid pi session: %s", resolved) //nolint:staticcheck // Upstream text.
	}
	return NewSession(storage), nil
}

func (repo *JSONLSessionRepo) newRuntimeSession(ctx context.Context, path, cwd string, initialize bool) (*Session, error) {
	id, err := uuidv7.Generate(time.Now())
	if err != nil {
		return nil, err
	}
	metadata := SessionMetadata{ID: id, CreatedAt: formatHarnessTimestamp(time.Now()), CWD: cwd, Path: path}
	if err := validateHarnessMetadata(metadata); err != nil {
		return nil, err
	}
	header, err := encodeHarnessHeader(metadata)
	if err != nil {
		return nil, err
	}
	if initialize {
		if err := repo.FS.WriteFile(ctx, path, header); err != nil {
			return nil, fileSystemSessionError(err, "Failed to initialize session %s", path)
		}
		storage, err := rehydrateJSONLSession(header, path, func(line []byte) error {
			return repo.FS.AppendFile(context.Background(), path, line)
		})
		if err != nil {
			return nil, err
		}
		return NewSession(storage), nil
	}
	return repo.openDelayedRuntimeBytes(ctx, path, header)
}

// OpenRuntimeBytes binds an unmaterialized coding-session snapshot to a path.
// The bytes remain pending until the first assistant message, matching the
// coding runtime's delayed persistence.
func (repo *JSONLSessionRepo) OpenRuntimeBytes(ctx context.Context, path string, content []byte) (*Session, error) {
	resolved, err := repo.FS.AbsolutePath(ctx, path)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to resolve session %s", path)
	}
	exists, err := repo.FS.Exists(ctx, resolved)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to check session %s", resolved)
	}
	if exists {
		return nil, fmt.Errorf("session file already exists: %s", resolved)
	}
	return repo.openDelayedRuntimeBytes(ctx, resolved, content)
}

func (repo *JSONLSessionRepo) openDelayedRuntimeBytes(_ context.Context, path string, content []byte) (*Session, error) {
	pending := append([]byte(nil), content...)
	flushed := false
	storage, err := rehydrateRuntimeJSONLSession(content, path, func(line []byte) error {
		if flushed {
			return repo.FS.AppendFile(context.Background(), path, line)
		}
		pending = append(pending, line...)
		if !harnessAssistantMessageLine(line) {
			return nil
		}
		exists, existsErr := repo.FS.Exists(context.Background(), path)
		if existsErr != nil {
			pending = pending[:len(pending)-len(line)]
			return existsErr
		}
		if exists {
			pending = pending[:len(pending)-len(line)]
			return fmt.Errorf("session file already exists: %s", path)
		}
		var writeErr error
		if writer, ok := repo.FS.(interface {
			WriteFileExclusive(context.Context, string, []byte) error
		}); ok {
			writeErr = writer.WriteFileExclusive(context.Background(), path, pending)
		} else {
			writeErr = repo.FS.WriteFile(context.Background(), path, pending)
		}
		if writeErr != nil {
			pending = pending[:len(pending)-len(line)]
			return writeErr
		}
		flushed = true
		pending = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	return NewSession(storage), nil
}

func harnessAssistantMessageLine(line []byte) bool {
	var entry struct {
		Type    string `json:"type"`
		Message struct {
			Role string `json:"role"`
		} `json:"message"`
	}
	return json.Unmarshal(line, &entry) == nil && entry.Type == "message" && entry.Message.Role == "assistant"
}

func (repo *JSONLSessionRepo) List(ctx context.Context, options SessionListOptions) ([]SessionMetadata, error) {
	dirs := make([]string, 0)
	if options.CWD != "" {
		dir, err := repo.sessionDir(ctx, options.CWD)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, dir)
	} else {
		root, err := repo.sessionsRoot(ctx)
		if err != nil {
			return nil, err
		}
		exists, err := repo.FS.Exists(ctx, root)
		if err != nil {
			return nil, fileSystemSessionError(err, "Failed to check sessions root %s", root)
		}
		if !exists {
			return []SessionMetadata{}, nil
		}
		entries, err := repo.FS.ListDir(ctx, root)
		if err != nil {
			return nil, fileSystemSessionError(err, "Failed to list sessions root %s", root)
		}
		for _, entry := range entries {
			if entry.Kind == FileKindDirectory {
				dirs = append(dirs, entry.Path)
			}
		}
	}
	result := make([]SessionMetadata, 0)
	for _, dir := range dirs {
		exists, err := repo.FS.Exists(ctx, dir)
		if err != nil {
			return nil, fileSystemSessionError(err, "Failed to check session directory %s", dir)
		}
		if !exists {
			continue
		}
		files, err := repo.FS.ListDir(ctx, dir)
		if err != nil {
			return nil, fileSystemSessionError(err, "Failed to list sessions in %s", dir)
		}
		for _, file := range files {
			if file.Kind == FileKindDirectory || !strings.HasSuffix(file.Name, ".jsonl") {
				continue
			}
			metadata, loadErr := loadHarnessJSONLMetadata(ctx, repo.FS, file.Path)
			if loadErr != nil {
				var typed *SessionError
				if errors.As(loadErr, &typed) && typed.Code == SessionErrorInvalidSession {
					continue
				}
				return nil, loadErr
			}
			result = append(result, metadata)
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		leftTime, leftErr := time.Parse(time.RFC3339Nano, result[left].CreatedAt)
		rightTime, rightErr := time.Parse(time.RFC3339Nano, result[right].CreatedAt)
		if leftErr != nil || rightErr != nil {
			return false
		}
		return leftTime.After(rightTime)
	})
	return result, nil
}

func loadHarnessJSONLMetadata(ctx context.Context, fileSystem FileSystem, path string) (SessionMetadata, error) {
	lines, err := fileSystem.ReadTextLines(ctx, path, 1)
	if err != nil {
		return SessionMetadata{}, fileSystemSessionError(err, "Failed to read session header %s", path)
	}
	if len(lines) == 0 || trimHarnessJSSpace(lines[0]) == "" {
		return SessionMetadata{}, invalidHarnessSession(path, "missing session header")
	}
	header, err := parseHarnessHeader([]byte(lines[0]), path)
	if err != nil {
		return SessionMetadata{}, err
	}
	return SessionMetadata{
		ID: header.ID, CreatedAt: header.Timestamp, CWD: header.CWD, Path: path,
		ParentSessionPath: cloneHarnessString(header.ParentSession), Metadata: cloneHarnessRaw(header.Metadata),
	}, nil
}

func (repo *JSONLSessionRepo) Delete(ctx context.Context, metadata SessionMetadata) error {
	if err := repo.FS.Remove(ctx, metadata.Path, false, true); err != nil {
		return fileSystemSessionError(err, "Failed to delete session %s", metadata.Path)
	}
	return nil
}

func (repo *JSONLSessionRepo) Fork(ctx context.Context, sourceMetadata SessionMetadata, options SessionForkOptions) (*Session, error) {
	source, err := repo.Open(ctx, sourceMetadata)
	if err != nil {
		return nil, err
	}
	entries, err := EntriesToFork(source.Storage(), options.EntryID, options.Position)
	if err != nil {
		return nil, err
	}
	create := options.SessionCreateOptions
	if create.ParentSessionPath == nil {
		create.ParentSessionPath = cloneHarnessString(&sourceMetadata.Path)
	}
	if len(create.Metadata) == 0 {
		create.Metadata = cloneHarnessRaw(sourceMetadata.Metadata)
	}
	fork, err := repo.Create(ctx, create)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := fork.Storage().AppendEntry(entry); err != nil {
			return nil, err
		}
	}
	return fork, nil
}

// ImportJSONL installs exact upstream bytes into this repository and returns
// a storage whose later appends remain durable.
func (repo *JSONLSessionRepo) ImportJSONL(ctx context.Context, content []byte) (*Session, error) {
	parsed, err := RehydrateJSONLSession(content, "<import>")
	if err != nil {
		return nil, err
	}
	metadata := parsed.Metadata()
	dir, err := repo.sessionDir(ctx, metadata.CWD)
	if err != nil {
		return nil, err
	}
	name := strings.NewReplacer(":", "-", ".", "-").Replace(metadata.CreatedAt) + "_" + metadata.ID + ".jsonl"
	path, err := repo.FS.JoinPath(ctx, dir, name)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to resolve session file path for %s", metadata.ID)
	}
	return repo.importJSONLAt(ctx, content, path)
}

// ImportJSONLAt installs exact upstream bytes at the runtime-selected import
// destination and keeps later appends bound to that file.
func (repo *JSONLSessionRepo) ImportJSONLAt(ctx context.Context, content []byte, path string) (*Session, error) {
	return repo.importJSONLAt(ctx, content, path)
}

func (repo *JSONLSessionRepo) importJSONLAt(ctx context.Context, content []byte, path string) (*Session, error) {
	resolved, err := repo.FS.AbsolutePath(ctx, path)
	if err != nil {
		return nil, fileSystemSessionError(err, "Failed to resolve imported session %s", path)
	}
	if err := repo.FS.CreateDir(ctx, filepath.Dir(resolved), true); err != nil {
		return nil, fileSystemSessionError(err, "Failed to create session directory %s", filepath.Dir(resolved))
	}
	if err := repo.FS.WriteFile(ctx, resolved, content); err != nil {
		return nil, fileSystemSessionError(err, "Failed to import session %s", path)
	}
	storage, err := rehydrateJSONLSession(content, resolved, func(line []byte) error {
		return repo.FS.AppendFile(context.Background(), resolved, line)
	})
	if err != nil {
		return nil, err
	}
	return NewSession(storage), nil
}

func fileSystemSessionError(err error, format string, arguments ...any) error {
	if err == nil {
		return nil
	}
	code := SessionErrorStorage
	var fileError *FileError
	if errors.As(err, &fileError) && fileError.Code == FileErrorNotFound {
		code = SessionErrorNotFound
	}
	message := fmt.Sprintf(format, arguments...)
	return &SessionError{Code: code, Err: fmt.Errorf("%s: %w", message, err)}
}
