package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/uuidv7"
)

type Sessions struct {
	db        *DB
	namespace string
}

func (db *DB) Sessions(namespace string) *Sessions { return &Sessions{db, namespace} }

func (r *Sessions) Create(ctx context.Context, options harness.SessionCreateOptions) (*harness.Session, error) {
	return r.create(ctx, options, nil, "")
}

func (r *Sessions) create(ctx context.Context, options harness.SessionCreateOptions, entries []harness.SessionTreeEntry, parent string) (*harness.Session, error) {
	if !filepath.IsAbs(options.CWD) {
		return nil, errors.New("session workspace must be absolute")
	}
	id := options.ID
	var err error
	if id == "" {
		id, err = uuidv7.Generate(time.Now())
		if err != nil {
			return nil, err
		}
	}
	metadata := harness.SessionMetadata{ID: id, CWD: options.CWD, CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), ParentSessionPath: options.ParentSessionPath, Metadata: options.Metadata}
	mem, err := harness.NewInMemorySessionStorage(entries, metadata)
	if err != nil {
		return nil, err
	}
	content, err := harness.MarshalSessionJSONL(mem, options.CWD)
	if err != nil {
		return nil, err
	}
	metadata, err = r.importJournal(ctx, content, parent, false)
	if err != nil {
		return nil, err
	}
	return r.Open(ctx, metadata)
}

func (r *Sessions) Open(ctx context.Context, metadata harness.SessionMetadata) (*harness.Session, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var header []byte
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT header,revision FROM sessions WHERE namespace=? AND id=?", r.namespace, metadata.ID).Scan(&header, &revision); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM entries WHERE namespace=? AND session_id=? ORDER BY seq", r.namespace, metadata.ID)
	if err != nil {
		return nil, err
	}
	var content bytes.Buffer
	content.Write(header)
	content.WriteByte('\n')
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			_ = rows.Close()
			return nil, err
		}
		content.Write(data)
		content.WriteByte('\n')
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	storage, err := harness.OpenSessionJournal(content.Bytes(), func(line []byte) error {
		entry, err := harness.ParseSessionTreeEntry(line)
		if err != nil {
			return err
		}
		// SessionStorage serializes callbacks. The SQL revision also fences handles
		// opened by another process; a rejected commit never advances memory.
		tx, err := r.db.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		result, err := tx.Exec(`UPDATE sessions SET revision=revision+1, modified=?, name=CASE WHEN ?='session_info' THEN ? ELSE name END,preview=CASE WHEN preview='' THEN ? ELSE preview END,message_count=message_count+? WHERE namespace=? AND id=? AND revision=?`, time.Now().UTC().Format(time.RFC3339Nano), entry.Type, entry.Name, userPreview(entry.Message), boolInt(entry.Type == "message"), r.namespace, metadata.ID, revision)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return fmt.Errorf("session ownership changed: %s", metadata.ID)
		}
		if _, err = tx.Exec("INSERT INTO entries VALUES(?,?,?,?,?)", r.namespace, metadata.ID, revision+1, entry.ID, bytes.TrimSuffix(line, []byte{'\n'})); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		revision++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return harness.NewSession(storage), nil
}

func (r *Sessions) List(ctx context.Context, options harness.SessionListOptions) ([]harness.SessionMetadata, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT header FROM sessions WHERE namespace=? AND (?='' OR cwd=?) ORDER BY modified DESC,id`, r.namespace, options.CWD, options.CWD)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []harness.SessionMetadata{}
	for rows.Next() {
		var header []byte
		if err := rows.Scan(&header); err != nil {
			return nil, err
		}
		journal, err := harness.RehydrateJSONLSession(header, "")
		if err != nil {
			return nil, err
		}
		result = append(result, journal.Metadata())
	}
	return result, rows.Err()
}

func (r *Sessions) Delete(ctx context.Context, metadata harness.SessionMetadata) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE namespace=? AND id=?", r.namespace, metadata.ID)
	return err
}

func (r *Sessions) Fork(ctx context.Context, source harness.SessionMetadata, options harness.SessionForkOptions) (*harness.Session, error) {
	s, err := r.Open(ctx, source)
	if err != nil {
		return nil, err
	}
	entries, err := harness.EntriesToFork(s.Storage(), options.EntryID, options.Position)
	if err != nil {
		return nil, err
	}
	if options.CWD == "" {
		options.CWD = s.Metadata().CWD
	}
	return r.create(ctx, options.SessionCreateOptions, entries, source.ID)
}

var _ harness.SessionRepo = (*Sessions)(nil)

// Import upgrades legacy Pi trees without modifying their source files.
func (r *Sessions) Import(ctx context.Context, content []byte) (harness.SessionMetadata, error) {
	lines := bytes.Split(bytes.TrimSpace(content), []byte{'\n'})
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) != 0 && !json.Valid(line) {
			return harness.SessionMetadata{}, fmt.Errorf("invalid session record at line %d", i+1)
		}
	}
	var header struct {
		Type    string
		Version int
	}
	if len(lines) == 0 || json.Unmarshal(lines[0], &header) != nil || header.Type != "session" || header.Version > 3 || header.Version < 0 {
		return harness.SessionMetadata{}, errors.New("unsupported session header")
	}
	if header.Version < 3 {
		records := session.ParseSessionEntries(string(content))
		digest, sequence := sha256.Sum256(content), 0
		_, err := session.MigrateSessionEntries(records, func() (string, error) {
			sequence++
			id := sha256.Sum256([]byte(fmt.Sprintf("%x:%d", digest, sequence)))
			return fmt.Sprintf("%x", id[:4]), nil
		})
		if err != nil {
			return harness.SessionMetadata{}, err
		}
		var migrated bytes.Buffer
		for _, record := range records {
			data, err := record.MarshalJSON()
			if err != nil {
				return harness.SessionMetadata{}, err
			}
			migrated.Write(data)
			migrated.WriteByte('\n')
		}
		content = migrated.Bytes()
	}
	return r.importJournal(ctx, content, "", true)
}

func (r *Sessions) importJournal(ctx context.Context, content []byte, parent string, allowIdentical bool) (harness.SessionMetadata, error) {
	s, err := harness.RehydrateJSONLSession(content, "")
	if err != nil {
		return harness.SessionMetadata{}, err
	}
	m := s.Metadata()
	header := s.HeaderJSON()
	entries := s.Entries()
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.ID == "" || seen[entry.ID] || (entry.ParentID != nil && !seen[*entry.ParentID]) {
			return m, fmt.Errorf("invalid session tree at entry %q", entry.ID)
		}
		seen[entry.ID] = true
	}
	for _, entry := range entries {
		if (entry.TargetID != nil && !seen[*entry.TargetID]) || (entry.FirstKeptEntryID != "" && !seen[entry.FirstKeptEntryID]) {
			return m, fmt.Errorf("missing session reference at entry %q", entry.ID)
		}
	}
	var normalized bytes.Buffer
	normalized.Write(header)
	normalized.WriteByte('\n')
	payloads := make([][]byte, len(entries))
	name, preview, count := "", "", 0
	for i, entry := range entries {
		if entry.Type == "message" {
			count++
			if preview == "" {
				preview = userPreview(entry.Message)
			}
		}
		payloads[i], err = harness.MarshalSessionTreeEntry(entry)
		if err != nil {
			return m, err
		}
		normalized.Write(payloads[i])
		normalized.WriteByte('\n')
		if entry.Type == "session_info" {
			name = entry.Name
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE namespace=? AND id=?", r.namespace, m.ID).Scan(&exists); err != nil {
		return m, err
	}
	if exists != 0 {
		if !allowIdentical {
			return m, fmt.Errorf("session already exists: %s", m.ID)
		}
		// Release the write reservation before opening a read snapshot.
		if err = tx.Rollback(); err != nil {
			return m, err
		}
		current, err := r.Open(ctx, m)
		if err != nil {
			return m, err
		}
		data, err := current.Storage().(harness.ByteSessionStorage).Bytes()
		if err != nil {
			return m, err
		}
		if !bytes.Equal(data, normalized.Bytes()) {
			return m, fmt.Errorf("conflicting session import: %s", m.ID)
		}
		return m, nil
	}
	modified := m.CreatedAt
	if len(entries) > 0 {
		modified = entries[len(entries)-1].Timestamp
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions(namespace,id,header,cwd,created,modified,name,revision,parent_id,preview,message_count) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, r.namespace, m.ID, header, m.CWD, m.CreatedAt, modified, name, len(entries), parent, preview, count)
	if err != nil {
		return m, err
	}
	for i, entry := range entries {
		if _, err = tx.ExecContext(ctx, "INSERT INTO entries VALUES(?,?,?,?,?)", r.namespace, m.ID, i+1, entry.ID, payloads[i]); err != nil {
			return m, err
		}
	}
	return m, tx.Commit()
}

type CatalogQuery struct {
	CWD, Search, Cursor string
	Limit               int
	Archived            bool
}
type CatalogEntry struct {
	ParentID     string `json:"parent_id,omitempty"`
	Preview      string `json:"preview,omitempty"`
	MessageCount int    `json:"message_count"`
	ID           string `json:"id"`
	CWD          string `json:"cwd"`
	Name         string `json:"name"`
	Created      string `json:"created"`
	Modified     string `json:"modified"`
}
type CatalogPage struct {
	Sessions []CatalogEntry `json:"sessions"`
	Next     string         `json:"next,omitempty"`
}

// Catalog pages on immutable creation order so streaming cannot move a row
// across page boundaries. Views can sort received summaries by live activity.
func (r *Sessions) Catalog(ctx context.Context, q CatalogQuery) (CatalogPage, error) {
	result := CatalogPage{Sessions: []CatalogEntry{}}
	if q.Limit == 0 {
		q.Limit = 128
	}
	if q.Limit < 1 || q.Limit > 128 || len(q.Cursor) > 4096 || len(q.Search) > 1024 {
		return result, errors.New("invalid catalog query")
	}
	query := "SELECT sessions.id,sessions.cwd,sessions.name,sessions.created,sessions.modified,sessions.preview,sessions.message_count,coalesce(sessions.parent_id,'') FROM sessions WHERE namespace=? AND archived=?"
	args := []any{r.namespace, q.Archived}
	if q.CWD != "" {
		query += " AND sessions.cwd=?"
		args = append(args, q.CWD)
	}
	if q.Search != "" {
		words := strings.Fields(q.Search)
		for i, w := range words {
			words[i] = `"` + strings.ReplaceAll(w, `"`, `""`) + `"*`
		}
		if len(words) > 0 {
			query = strings.Replace(query, "FROM sessions", "FROM session_search CROSS JOIN sessions ON sessions.rowid=session_search.rowid", 1)
			query += " AND session_search MATCH ?"
			args = append(args, strings.Join(words, " AND "))
		}
	}
	if q.Cursor != "" {
		var cursor [6]string
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor[0] == "" || cursor[1] == "" || cursor[2] != q.CWD || cursor[3] != q.Search || cursor[4] != r.namespace || cursor[5] != strconv.FormatBool(q.Archived) {
			return result, errors.New("invalid catalog cursor")
		}
		query += " AND (created,id)<(?,?)"
		args = append(args, cursor[0], cursor[1])
	}
	query += " ORDER BY created DESC,id DESC LIMIT ?"
	args = append(args, q.Limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var entry CatalogEntry
		if err = rows.Scan(&entry.ID, &entry.CWD, &entry.Name, &entry.Created, &entry.Modified, &entry.Preview, &entry.MessageCount, &entry.ParentID); err != nil {
			return result, err
		}
		result.Sessions = append(result.Sessions, entry)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Sessions) > q.Limit {
		result.Sessions = result.Sessions[:q.Limit]
		last := result.Sessions[q.Limit-1]
		data, _ := json.Marshal([6]string{last.Created, last.ID, q.CWD, q.Search, r.namespace, strconv.FormatBool(q.Archived)})
		result.Next = base64.RawURLEncoding.EncodeToString(data)
	}
	return result, nil
}

// OpenPath accepts a true JSONL import path or a native session ID.
func (r *Sessions) OpenPath(ctx context.Context, reference string) (*harness.Session, error) {
	if strings.ContainsAny(reference, `/\`) || strings.HasSuffix(reference, ".jsonl") {
		data, err := os.ReadFile(reference)
		if err != nil {
			return nil, err
		}
		metadata, err := r.Import(ctx, data)
		if err != nil {
			return nil, err
		}
		return r.Open(ctx, metadata)
	}
	rows, err := r.db.QueryContext(ctx, "SELECT id FROM sessions WHERE namespace=? AND id>=? AND id<? ORDER BY id=? DESC,id LIMIT 2", r.namespace, reference, reference+"\uffff", reference)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("session %q: %w", reference, fs.ErrNotExist)
	}
	if len(ids) > 1 && ids[0] != reference {
		return nil, errors.New("ambiguous session ID")
	}
	return r.Open(ctx, harness.SessionMetadata{ID: ids[0]})
}

func (r *Sessions) ListInfo(ctx context.Context, cwd string, update session.SessionListUpdateFunc) ([]session.SessionInfo, error) {
	var result []session.SessionInfo
	query := CatalogQuery{CWD: cwd}
	nextUpdate := 128
	for {
		page, err := r.Catalog(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Sessions {
			created, _ := time.Parse(time.RFC3339Nano, entry.Created)
			modified, _ := time.Parse(time.RFC3339Nano, entry.Modified)
			info := session.SessionInfo{ID: entry.ID, ParentID: entry.ParentID, CWD: entry.CWD, Created: created, Modified: modified, FirstMessage: entry.Preview, AllMessagesText: entry.Preview, MessageCount: entry.MessageCount}
			if entry.Name != "" {
				name := entry.Name
				info.Name = &name
			}
			result = append(result, info)
		}
		if update != nil && (len(result) >= nextUpdate || page.Next == "") {
			nextUpdate = max(nextUpdate*2, len(result)*2)
			update(session.SessionListUpdate{Loaded: len(result), Total: len(result), Sessions: append([]session.SessionInfo(nil), result...)})
		}
		if page.Next == "" {
			break
		}
		query.Cursor = page.Next
	}
	return result, nil
}

func userPreview(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var preview ForeignSession
	preview.AddMessage(raw)
	if len(preview.Messages) == 1 && preview.Messages[0].Role == "user" {
		return clip(preview.Messages[0].Text, 1024)
	}
	return ""
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
