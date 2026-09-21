// Package sqlite supplies explicitly opened native persistence. Importing it
// does not create files, open a database, or start a service.
package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const applicationID = 0x4f524231

type DB struct{ *sql.DB }

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
		if info.Mode().Perm()&0077 != 0 || (name == filepath.Dir(path) && !info.IsDir()) || (name != filepath.Dir(path) && !info.Mode().IsRegular()) {
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
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{"_pragma": {"foreign_keys(ON)", "synchronous(FULL)", "busy_timeout(5000)"}, "_txlock": {"immediate"}}
	u.RawQuery = q.Encode()
	handle, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db := &DB{handle}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var id, version int
	if err = tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return nil, err
	}
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if (id != 0 && id != applicationID) || version > 2 {
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
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	var journal string
	if err = db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
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
	tx, err := d.db.BeginTx(ctx, nil)
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
	f, err = os.Open(path)
	if err != nil {
		return err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
