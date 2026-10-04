//go:build !js && !wasip1

package codexsessions

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func lookup(ctx context.Context, home, id string) (thread, error) {
	path := filepath.Join(home, "state_5.sqlite")
	if _, err := os.Stat(path); err != nil {
		return thread{}, ErrNoCodexSession
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}).String())
	if err != nil {
		return thread{}, err
	}
	defer func() { _ = db.Close() }()
	var found thread
	err = db.QueryRowContext(ctx, `SELECT rollout_path, cwd, coalesce(name, '') FROM threads WHERE id = ?`, id).Scan(&found.rollout, &found.cwd, &found.name)
	if errors.Is(err, sql.ErrNoRows) {
		return thread{}, ErrNoCodexSession
	}
	return found, err
}
