package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/internal/uuidv7"
	"github.com/OrdalieTech/orb/plugins/memory"
)

type sqlConnection interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Memory struct {
	db         *DB
	connection sqlConnection
	namespace  string
}

func (db *DB) Memory(namespace string) *Memory { return &Memory{db, autocommit{db}, namespace} }

var _ memory.TransactionalStore = (*Memory)(nil)

func (store *Memory) Append(ctx context.Context, item memory.Item) (string, error) {
	id, err := uuidv7.Generate(time.Now())
	if err != nil {
		return "", err
	}
	item.ID = id
	if item.Time.IsZero() {
		item.Time = time.Now().UTC()
	}
	data, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	_, err = store.connection.ExecContext(ctx, "INSERT INTO memory_items VALUES(?,?,?,?)", store.namespace, id, item.Time.UTC().Format("2006-01-02T15:04:05.000000000Z"), data)
	return id, err
}
func (store *Memory) Get(ctx context.Context, id string) (memory.Item, error) {
	var data []byte
	err := store.connection.QueryRowContext(ctx, "SELECT payload FROM memory_items WHERE namespace=? AND id=?", store.namespace, id).Scan(&data)
	var item memory.Item
	if err == nil {
		err = json.Unmarshal(data, &item)
	}
	return item, err
}
func (store *Memory) Delete(ctx context.Context, id string) error {
	_, err := store.connection.ExecContext(ctx, "DELETE FROM memory_items WHERE namespace=? AND id=?", store.namespace, id)
	return err
}
func (store *Memory) Query(ctx context.Context, filter memory.Filter) ([]memory.Item, error) {
	query := "SELECT payload FROM memory_items WHERE namespace=?"
	args := []any{store.namespace}
	if !filter.Since.IsZero() {
		query += " AND time>=?"
		args = append(args, filter.Since.UTC().Format("2006-01-02T15:04:05.000000000Z"))
	}
	if !filter.Until.IsZero() {
		query += " AND time<=?"
		args = append(args, filter.Until.UTC().Format("2006-01-02T15:04:05.000000000Z"))
	}
	for _, tag := range filter.Tags {
		query += " AND EXISTS(SELECT 1 FROM json_each(payload,'$.tags') WHERE value=?)"
		args = append(args, tag)
	}
	query += " ORDER BY time DESC,rowid DESC"
	rows, err := store.connection.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	result := []memory.Item{}
	for rows.Next() {
		var data []byte
		var item memory.Item
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		if filter.Contains != "" && !strings.Contains(strings.ToLower(item.Content), strings.ToLower(filter.Contains)) {
			continue
		}
		result = append(result, item)
		if len(result) == limit {
			break
		}
	}
	return result, rows.Err()
}
func (store *Memory) Transact(ctx context.Context, fn func(memory.Store) error) error {
	if fn == nil {
		return errors.New("memory transaction callback required")
	}
	tx, err := store.db.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = fn(&Memory{store.db, tx, store.namespace}); err != nil {
		return err
	}
	return tx.Commit()
}
func (store *Memory) importJournal(ctx context.Context, data []byte) error {
	items, err := filestore.ParseJournal(data)
	if err != nil {
		return err
	}
	tx, err := store.db.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			return err
		}
		var old []byte
		err = tx.QueryRowContext(ctx, "SELECT payload FROM memory_items WHERE namespace=? AND id=?", store.namespace, item.ID).Scan(&old)
		if err == nil {
			if string(old) != string(encoded) {
				return errors.New("conflicting memory item")
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO memory_items VALUES(?,?,?,?)", store.namespace, item.ID, item.Time.UTC().Format("2006-01-02T15:04:05.000000000Z"), encoded); err != nil {
			return err
		}
	}
	return tx.Commit()
}
