// Package sqlite supplies explicitly opened native persistence. Importing it
// does not create files, open a database, or start a service.
package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/gofrs/flock"

	_ "modernc.org/sqlite"
)

const applicationID = 0x4f524231

// MigrationStatus inspects an existing root without upgrading its schema.
func MigrationStatus(ctx context.Context, path, name string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("database must be a regular file")
	}
	handle, err := sql.Open("sqlite", fileURI(path, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		return false, err
	}
	defer func() { _ = handle.Close() }()
	var id, version int
	if err = handle.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return false, err
	}
	if err = handle.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return false, err
	}
	if id == 0 && version == 0 {
		var count int
		if err := handle.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return false, err
		}
		if count == 0 {
			return false, nil
		}
	}
	if id != applicationID || version < 1 || version > 4 {
		return false, errors.New("unsupported database schema")
	}
	return (&DB{DB: handle}).Migrated(ctx, name)
}

// DB is an open database. Writes made through the embedded *sql.DB bypass the
// cross-process writer queue; this package routes every write through begin.
type DB struct {
	*sql.DB
	writeLock *writerLock
}

// fileURI names a native path as an SQLite URI. A Windows drive path needs a
// leading slash so "C:" is not read as the authority; SQLite's win32 VFS drops
// the slash before "X:".
func fileURI(path, query string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: query}
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	return u.String()
}

// groupOrOtherAccess reports POSIX group or other permission bits. Windows
// has none (Go reports 0666/0777 there); the profile directory's ACL is what
// keeps the database private on that platform.
func groupOrOtherAccess(mode os.FileMode) bool {
	return runtime.GOOS != "windows" && mode.Perm()&0o077 != 0
}

func Open(ctx context.Context, path string) (_ *DB, err error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("database path must be absolute")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	for _, name := range []string{filepath.Dir(path), path, path + "-wal", path + "-shm"} {
		info, statErr := os.Lstat(name)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, statErr
		}
		if groupOrOtherAccess(info.Mode()) || (name == filepath.Dir(path) && !info.IsDir()) || (name != filepath.Dir(path) && !info.Mode().IsRegular()) {
			return nil, errors.New("database and directory must be private regular paths")
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = f.Close()
	}
	if err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	// FULL commits on slow disks can leave another process waiting beyond five seconds.
	q := url.Values{"_pragma": {"foreign_keys(ON)", "synchronous(FULL)", "busy_timeout(30000)"}, "_txlock": {"immediate"}}
	handle, err := sql.Open("sqlite", fileURI(path, q.Encode()))
	if err != nil {
		return nil, err
	}
	db := &DB{handle, newWriterLock(path + ".write.lock")}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	// Opening an initialized WAL database must not queue behind application writers.
	var id, version int
	var schemaLock *flock.Flock
	for {
		if err = db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
			return nil, err
		}
		if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return nil, err
		}
		if id == applicationID && version == 4 {
			var journal string
			if err = db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
				return nil, err
			}
			if journal == "wal" {
				return db, nil
			}
		}
		if schemaLock != nil {
			break
		}
		// Serialize first opens, then recheck: another opener may have finished the schema.
		schemaLock = flock.New(path + ".schema.lock")
		defer func() { _ = schemaLock.Close() }()
		if _, err = schemaLock.TryLockContext(ctx, 10*time.Millisecond); err != nil {
			return nil, err
		}
	}
	tx, err := db.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return nil, err
	}
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if (id != 0 && id != applicationID) || version > 4 {
		return nil, errors.New("unsupported database schema")
	}
	if id == 0 {
		var tables int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return nil, err
		}
		if tables != 0 || version != 0 {
			return nil, errors.New("not an Orb database")
		}
	}
	if version == 0 {
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS documents (
 namespace TEXT NOT NULL, key TEXT NOT NULL, content BLOB NOT NULL,
 PRIMARY KEY(namespace,key)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS sessions (
 namespace TEXT NOT NULL, id TEXT NOT NULL, header BLOB NOT NULL,
 cwd TEXT NOT NULL, created TEXT NOT NULL, modified TEXT NOT NULL,
 name TEXT NOT NULL DEFAULT '', revision INTEGER NOT NULL DEFAULT 0,
 parent_id TEXT, archived INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(namespace,id)
);
CREATE INDEX IF NOT EXISTS sessions_activity ON sessions(namespace,cwd,modified DESC,id);
CREATE INDEX IF NOT EXISTS sessions_created ON sessions(namespace,archived,created DESC,id DESC);
CREATE INDEX IF NOT EXISTS sessions_project_created ON sessions(namespace,archived,cwd,created DESC,id DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS session_search USING fts5(name,cwd,content='sessions',content_rowid='rowid');
CREATE TRIGGER IF NOT EXISTS sessions_insert AFTER INSERT ON sessions BEGIN
 INSERT INTO session_search(rowid,name,cwd) VALUES(new.rowid,new.name,new.cwd);
END;
CREATE TRIGGER IF NOT EXISTS sessions_delete AFTER DELETE ON sessions BEGIN
 INSERT INTO session_search(session_search,rowid,name,cwd) VALUES('delete',old.rowid,old.name,old.cwd);
END;
CREATE TRIGGER IF NOT EXISTS sessions_update AFTER UPDATE OF name,cwd ON sessions
 WHEN new.name<>old.name OR new.cwd<>old.cwd BEGIN
 INSERT INTO session_search(session_search,rowid,name,cwd) VALUES('delete',old.rowid,old.name,old.cwd);
 INSERT INTO session_search(rowid,name,cwd) VALUES(new.rowid,new.name,new.cwd);
END;
CREATE TABLE IF NOT EXISTS entries (
 namespace TEXT NOT NULL, session_id TEXT NOT NULL, seq INTEGER NOT NULL,
 id TEXT NOT NULL, payload BLOB NOT NULL,
 PRIMARY KEY(namespace,session_id,seq), UNIQUE(namespace,session_id,id),
 FOREIGN KEY(namespace,session_id) REFERENCES sessions(namespace,id) ON DELETE CASCADE
) WITHOUT ROWID;
PRAGMA application_id=%d;
PRAGMA user_version=1;`, applicationID))
		if err != nil {
			return nil, err
		}
	}
	if version < 2 {
		_, err = tx.ExecContext(ctx, `CREATE TABLE foreign_sources(profile TEXT NOT NULL,peer TEXT NOT NULL,revision INTEGER NOT NULL,floor INTEGER NOT NULL,PRIMARY KEY(profile,peer)) WITHOUT ROWID;
CREATE TABLE foreign_sessions(profile TEXT NOT NULL,peer TEXT NOT NULL,namespace TEXT NOT NULL,id TEXT NOT NULL,instance TEXT NOT NULL,name TEXT NOT NULL,cwd TEXT NOT NULL,messages BLOB NOT NULL,refreshed INTEGER NOT NULL,version INTEGER NOT NULL,PRIMARY KEY(profile,peer,namespace,id));
CREATE INDEX foreign_recent ON foreign_sessions(profile,peer,refreshed DESC,version DESC);
CREATE INDEX foreign_expiry ON foreign_sessions(profile,refreshed DESC,version DESC);
PRAGMA user_version=2;`)
		if err != nil {
			return nil, err
		}
	}
	if version < 3 {
		_, err = tx.ExecContext(ctx, `CREATE TABLE memory_items(namespace TEXT NOT NULL,id TEXT NOT NULL,time TEXT NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(namespace,id));
CREATE INDEX memory_recent ON memory_items(namespace,time DESC);
CREATE TABLE chat_pending(seq INTEGER PRIMARY KEY,namespace TEXT NOT NULL,event_id TEXT NOT NULL,payload BLOB NOT NULL);
CREATE INDEX chat_pending_events ON chat_pending(namespace,event_id,seq);
CREATE INDEX chat_pending_order ON chat_pending(namespace,seq);
PRAGMA user_version=3;`)
		if err != nil {
			return nil, err
		}
	}
	if version < 4 {
		_, err = tx.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN preview TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN message_count INTEGER NOT NULL DEFAULT 0;
UPDATE sessions SET message_count=(SELECT count(*) FROM entries WHERE namespace=sessions.namespace AND session_id=sessions.id AND json_extract(payload,'$.type')='message'),
preview=coalesce((SELECT substr(CASE json_type(payload,'$.message.content') WHEN 'text' THEN json_extract(payload,'$.message.content') WHEN 'array' THEN (SELECT group_concat(json_extract(value,'$.text'),'') FROM json_each(entries.payload,'$.message.content') WHERE json_extract(value,'$.type')='text') ELSE '' END,1,4096) FROM entries WHERE namespace=sessions.namespace AND session_id=sessions.id AND json_extract(payload,'$.message.role')='user' ORDER BY seq LIMIT 1),'');
PRAGMA user_version=4;`)
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	release, err := db.writeLock.acquire(ctx)
	if err != nil {
		return nil, err
	}
	var journal string
	err = db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal)
	release()
	if err != nil {
		return nil, err
	}
	if journal != "wal" {
		return nil, errors.New("SQLite WAL unavailable")
	}
	return db, nil
}

type Document struct {
	db             *DB
	namespace, key string
}

func (db *DB) Document(namespace, key string) *Document { return &Document{db, namespace, key} }

func (d *Document) Read(ctx context.Context) ([]byte, error) {
	var data []byte
	err := d.db.QueryRowContext(ctx, "SELECT content FROM documents WHERE namespace=? AND key=?", d.namespace, d.key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return data, err
}

// Update serializes read-modify-write across processes; nil deletes the document.
func (d *Document) Update(ctx context.Context, change func([]byte) ([]byte, error)) error {
	tx, err := d.db.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var data []byte
	err = tx.QueryRowContext(ctx, "SELECT content FROM documents WHERE namespace=? AND key=?", d.namespace, d.key).Scan(&data)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return err
	}
	next, err := change(bytes.Clone(data))
	if err != nil {
		return err
	}
	if next == nil {
		_, err = tx.ExecContext(ctx, "DELETE FROM documents WHERE namespace=? AND key=?", d.namespace, d.key)
	} else if missing || !bytes.Equal(data, next) {
		_, err = tx.ExecContext(ctx, "INSERT INTO documents VALUES(?,?,?) ON CONFLICT(namespace,key) DO UPDATE SET content=excluded.content", d.namespace, d.key, next)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Backup creates a consistent standalone snapshot without copying a live WAL.
func (db *DB) Backup(ctx context.Context, path string) (err error) {
	if !filepath.IsAbs(path) {
		return errors.New("backup path must be absolute")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !parent.IsDir() || groupOrOtherAccess(parent.Mode()) {
		return errors.New("backup directory must be private")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", path)
	if err != nil {
		return err
	}
	// Windows flushes only handles with write access.
	f, err = os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return err
	}
	// Windows cannot flush a directory opened read-only (FlushFileBuffers needs
	// GENERIC_WRITE); NTFS journals the new directory entry itself.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

// MigrationSource names an explicit offline source. Originals are never changed.
// The caller must quiesce legacy writers and hold its native-root migration lock.
type MigrationSource struct {
	Path, Namespace, Key, Kind string
}

func (db *DB) Migrated(ctx context.Context, name string) (bool, error) {
	data, err := db.Document("migration/"+name, "@complete").Read(ctx)
	return len(data) != 0, err
}

// Migrate checkpoints each source independently, then verifies the complete
// inventory before publishing cutover. Retrying never overwrites native writes.
func (db *DB) Migrate(ctx context.Context, name string, sources []MigrationSource, verify ...func() error) error {
	if name == "" {
		return errors.New("migration name required")
	}
	if done, err := db.Migrated(ctx, name); err != nil || done {
		return err
	}
	sources = slices.Clone(sources)
	slices.SortFunc(sources, func(a, b MigrationSource) int { return strings.Compare(a.Path, b.Path) })
	for i, source := range sources {
		if !filepath.IsAbs(source.Path) || source.Namespace == "" || (i > 0 && sources[i-1].Path == source.Path) || (source.Kind != "session" && source.Kind != "json" && source.Kind != "bytes" && source.Kind != "memory" && source.Kind != "chat") {
			return errors.New("invalid migration inventory")
		}
	}
	namespace := "migration/" + name
	manifest, err := json.Marshal(sources)
	if err != nil {
		return err
	}
	if err := importDocument(ctx, db.Document(namespace, "@manifest"), manifest); err != nil {
		return fmt.Errorf("migration inventory changed: %w", err)
	}
	fingerprints := make([][]byte, len(sources))
	type parentReference struct{ namespace, id, path string }
	parents := []parentReference{}
	sourceIDs := map[string]string{}
	for i, source := range sources {
		data, err := readMigrationSource(source.Path)
		if err != nil {
			return fmt.Errorf("migration source %s: %w", source.Path, err)
		}
		if source.Kind == "session" {
			var header struct {
				ID     string `json:"id"`
				Parent string `json:"parentSession"`
			}
			line, _, _ := bytes.Cut(data, []byte{'\n'})
			if err := json.Unmarshal(line, &header); err != nil {
				return fmt.Errorf("invalid session header: %s", source.Path)
			}
			sourceIDs[source.Namespace+"\x00"+source.Path] = header.ID
			if header.Parent != "" {
				parents = append(parents, parentReference{source.Namespace, header.ID, header.Parent})
			}
		}
		digest := sha256.Sum256(data)
		fingerprints[i] = digest[:]
		checkpoint := db.Document(namespace, source.Path)
		previous, err := checkpoint.Read(ctx)
		if err != nil {
			return err
		}
		if previous != nil {
			if !bytes.Equal(previous, digest[:]) {
				return fmt.Errorf("migration source changed: %s", source.Path)
			}
			continue
		}
		if source.Kind == "json" && !json.Valid(data) {
			return fmt.Errorf("invalid JSON in migration source %s", source.Path)
		}
		// A crash between importing rows and checkpointing cannot accept a changed source.
		if err = importDocument(ctx, db.Document(namespace, "@pending/"+source.Path), digest[:]); err != nil {
			return fmt.Errorf("migration source changed: %s", source.Path)
		}
		switch source.Kind {
		case "memory":
			err = db.Memory(source.Namespace).importJournal(ctx, data)
		case "chat":
			err = db.Chat(source.Namespace).importJournal(ctx, data)
		case "session":
			_, err = db.Sessions(source.Namespace).Import(ctx, data)
		case "json", "bytes":
			if source.Kind == "json" && !json.Valid(data) {
				return fmt.Errorf("invalid JSON in migration source %s", source.Path)
			}
			err = importDocument(ctx, db.Document(source.Namespace, source.Key), data)
		}
		if err != nil {
			return fmt.Errorf("migration source %s: %w", source.Path, err)
		}
		if err = importDocument(ctx, checkpoint, digest[:]); err != nil {
			return err
		}
	}
	for i, source := range sources {
		data, err := readMigrationSource(source.Path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if !bytes.Equal(digest[:], fingerprints[i]) {
			return fmt.Errorf("migration source changed: %s", source.Path)
		}
	}

	// File parent references become native IDs; headers remain byte-compatible exports.
	for _, parent := range parents {
		id := sourceIDs[parent.namespace+"\x00"+parent.path]
		if id == "" {
			continue
		}
		if _, err := db.exec(ctx, "UPDATE sessions SET parent_id=? WHERE namespace=? AND id=?", id, parent.namespace, parent.id); err != nil {
			return err
		}
	}
	for _, check := range verify {
		if err := check(); err != nil {
			return err
		}
	}
	return importDocument(ctx, db.Document(namespace, "@complete"), manifest)
}

func readMigrationSource(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("migration requires regular source files")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.New("migration source replaced")
	}
	const limit = 256 << 20
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(data) > limit {
		return nil, errors.New("migration source exceeds 256 MiB")
	}
	return data, err
}

func importDocument(ctx context.Context, document *Document, data []byte) error {
	return document.Update(ctx, func(old []byte) ([]byte, error) {
		if old != nil && !bytes.Equal(old, data) {
			return nil, errors.New("conflicting imported document")
		}
		return data, nil
	})
}

// RestoreSessions recovers missing conversations without rolling back authority,
// credentials, operation receipts, or chat delivery state. Conflicts fail closed;
// rerunning after interruption accepts only identical already-restored journals.
func (db *DB) RestoreSessions(ctx context.Context, path, namespace string) (int, error) {
	if _, err := MigrationStatus(ctx, path, "native"); err != nil {
		return 0, err
	}
	source, err := sql.Open("sqlite", fileURI(path, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		return 0, err
	}
	defer func() { _ = source.Close() }()
	repo := (&DB{DB: source}).Sessions(namespace)
	sessions, err := repo.List(ctx, harness.SessionListOptions{})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, metadata := range sessions {
		stored, err := repo.Open(ctx, metadata)
		if err != nil {
			return count, err
		}
		data, err := stored.Storage().(harness.ByteSessionStorage).Bytes()
		if err != nil {
			return count, err
		}
		var parent string
		if err = source.QueryRowContext(ctx, "SELECT coalesce(parent_id,'') FROM sessions WHERE namespace=? AND id=?", namespace, metadata.ID).Scan(&parent); err != nil {
			return count, err
		}
		if _, err = db.Sessions(namespace).importJournal(ctx, data, parent, true); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
