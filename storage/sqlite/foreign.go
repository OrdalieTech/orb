package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const ForeignPreviewBytes = 32 << 10
const ForeignRetention = 7 * 24 * time.Hour

var ErrForeignSuperseded = errors.New("foreign refresh superseded or revoked")

type PreviewMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type ForeignSession struct {
	Peer, Namespace, ID, Instance, Name, CWD string
	RefreshedAt                              time.Time
	Messages                                 []PreviewMessage
}

// AddMessage retains only visible, completed conversation text. Callers must
// not feed streaming deltas, which would duplicate messages in offline previews.
func (s *ForeignSession) AddMessage(raw json.RawMessage) {
	if len(raw) > 1<<20 {
		return
	}
	var m struct {
		Role    string
		Content json.RawMessage
	}
	if json.Unmarshal(raw, &m) != nil || (m.Role != "user" && m.Role != "assistant") {
		return
	}
	var text string
	if json.Unmarshal(m.Content, &text) != nil {
		var blocks []struct{ Type, Text string }
		if json.Unmarshal(m.Content, &blocks) != nil {
			return
		}
		var b strings.Builder
		for _, block := range blocks {
			if block.Type == "text" {
				b.WriteString(clip(block.Text, (4<<10)-b.Len()))
				if b.Len() >= 4<<10 {
					break
				}
			}
		}
		text = b.String()
	}
	if text == "" {
		return
	}
	s.Messages = append(s.Messages, PreviewMessage{m.Role, clip(text, 4<<10)})
	for len(s.Messages) > 0 {
		data, _ := json.Marshal(s.Messages)
		if len(s.Messages) <= 8 && len(data) <= ForeignPreviewBytes {
			break
		}
		s.Messages = append([]PreviewMessage(nil), s.Messages[1:]...)
	}

}
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

type Foreign struct {
	db      *DB
	profile string
}

func (db *DB) Foreign(profile string) *Foreign { return &Foreign{db, profile} }

// Begin allocates an ordering token before network I/O. Forget advances the
// floor, so an in-flight response can never resurrect purged content.
func (c *Foreign) Begin(ctx context.Context, peer string) (int64, error) {
	var ticket int64
	err := c.db.QueryRowContext(ctx, `INSERT INTO foreign_sources VALUES(?,?,1,0) ON CONFLICT(profile,peer) DO UPDATE SET revision=revision+1 RETURNING revision`, c.profile, peer).Scan(&ticket)
	return ticket, err
}
func (c *Foreign) Put(ctx context.Context, ticket int64, s ForeignSession) error {
	if s.Peer == "" || s.Namespace == "" || s.ID == "" || s.Instance == "" || ticket <= 0 {
		return errors.New("incomplete foreign session")
	}
	for _, field := range []string{s.Peer, s.Namespace, s.ID, s.Instance, s.Name, s.CWD} {
		if len(field) > 4096 || !utf8.ValidString(field) {
			return errors.New("invalid foreign session metadata")
		}
	}
	size := 0
	for _, m := range s.Messages {
		size += len(m.Text)
		if (m.Role != "user" && m.Role != "assistant") || !utf8.ValidString(m.Text) {
			return errors.New("invalid preview")
		}
	}
	if size > ForeignPreviewBytes || len(s.Messages) > 8 {
		return errors.New("preview exceeds limit")
	}
	data, err := json.Marshal(s.Messages)
	if len(data) > ForeignPreviewBytes {
		return errors.New("preview exceeds limit")
	}
	if err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO foreign_sessions(profile,peer,namespace,id,instance,name,cwd,messages,refreshed,version)
 SELECT ?,?,?,?,?,?,?,?,?,? FROM foreign_sources WHERE profile=? AND peer=? AND floor<? AND revision>=?
 ON CONFLICT(profile,peer,namespace,id) DO UPDATE SET instance=excluded.instance,name=excluded.name,cwd=excluded.cwd,messages=excluded.messages,refreshed=excluded.refreshed,version=excluded.version WHERE excluded.version>=foreign_sessions.version`, c.profile, s.Peer, s.Namespace, s.ID, s.Instance, s.Name, s.CWD, data, time.Now().UnixMilli(), ticket, c.profile, s.Peer, ticket, ticket)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return ErrForeignSuperseded
	}
	// Bound both age and space, including profiles which contact many peers.
	_, err = tx.ExecContext(ctx, `DELETE FROM foreign_sessions WHERE profile=? AND (refreshed<? OR rowid IN (SELECT rowid FROM foreign_sessions WHERE profile=? AND peer=? ORDER BY refreshed DESC,version DESC LIMIT -1 OFFSET 128) OR rowid IN (SELECT rowid FROM foreign_sessions WHERE profile=? ORDER BY refreshed DESC,version DESC LIMIT -1 OFFSET 1024))`, c.profile, time.Now().Add(-ForeignRetention).UnixMilli(), c.profile, s.Peer, c.profile)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (c *Foreign) List(ctx context.Context, peer string) ([]ForeignSession, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT namespace,id,instance,name,cwd,messages,refreshed FROM foreign_sessions WHERE profile=? AND peer=? AND refreshed>=? ORDER BY refreshed DESC,version DESC LIMIT 128`, c.profile, peer, time.Now().Add(-ForeignRetention).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []ForeignSession{}
	for rows.Next() {
		s := ForeignSession{Peer: peer}
		var data []byte
		var stamp int64
		if err := rows.Scan(&s.Namespace, &s.ID, &s.Instance, &s.Name, &s.CWD, &data, &stamp); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &s.Messages); err != nil {
			return nil, err
		}
		s.RefreshedAt = time.UnixMilli(stamp)
		result = append(result, s)
	}
	return result, rows.Err()
}
func (c *Foreign) Forget(ctx context.Context, peer string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO foreign_sources VALUES(?,?,1,1) ON CONFLICT(profile,peer) DO UPDATE SET revision=revision+1,floor=revision+1`, c.profile, peer)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM foreign_sessions WHERE profile=? AND peer=?`, c.profile, peer); err != nil {
		return err
	}
	return tx.Commit()
}
