package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/OrdalieTech/orb/chat"
)

type Chat struct {
	db        *DB
	namespace string
}

func (db *DB) Chat(namespace string) *Chat { return &Chat{db, namespace} }

var _ chat.Spool = (*Chat)(nil)

func (store *Chat) Pending(ctx context.Context) ([]chat.Message, error) {
	rows, err := store.db.QueryContext(ctx, "SELECT payload FROM chat_pending WHERE namespace=? ORDER BY seq", store.namespace)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []chat.Message
	for rows.Next() {
		var data []byte
		var message chat.Message
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &message); err != nil {
			return nil, err
		}
		result = append(result, message)
	}
	return result, rows.Err()
}
func (store *Chat) Put(ctx context.Context, message chat.Message) error {
	if message.EventID == "" {
		return errors.New("chat event ID required")
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = store.db.exec(ctx, "INSERT INTO chat_pending(namespace,event_id,payload) VALUES(?,?,?)", store.namespace, message.EventID, data)
	return err
}
func (store *Chat) Ack(ctx context.Context, id string) error {
	_, err := store.db.exec(ctx, "DELETE FROM chat_pending WHERE seq=(SELECT seq FROM chat_pending WHERE namespace=? AND event_id=? ORDER BY seq LIMIT 1)", store.namespace, id)
	return err
}
func (store *Chat) importJournal(ctx context.Context, data []byte) error {
	messages, err := chat.ParseSpool(data)
	if err != nil {
		return err
	}
	tx, err := store.db.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// The journal fingerprint and pending rows commit together to fence restart replay.
	digest := sha256.Sum256(data)
	var previous []byte
	err = tx.QueryRowContext(ctx, "SELECT content FROM documents WHERE namespace='chat-import' AND key=?", store.namespace).Scan(&previous)
	if err == nil {
		if !bytes.Equal(previous, digest[:]) {
			return errors.New("chat migration source changed")
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO chat_pending(namespace,event_id,payload) VALUES(?,?,?)", store.namespace, message.EventID, encoded); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO documents VALUES('chat-import',?,?)", store.namespace, digest[:]); err != nil {
		return err
	}
	return tx.Commit()
}
