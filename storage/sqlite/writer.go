package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gofrs/flock"
)

// writerWait matches the busy_timeout Open configures.
const writerWait = 30 * time.Second

var errWriterBusy = errors.New("database is locked: writer queue wait exceeded 30s")

// writerLock serializes write transactions across processes with a blocking
// OS lock (flock / LockFileEx) so waiters queue in the kernel. SQLite's busy
// handler sleeps between polls, so a process that just committed re-acquires
// while its peers sleep, and on slow FULL commits a peer starves past the busy
// timeout. Readers never take this lock.
type writerLock struct {
	// slot admits one kernel waiter per handle; channel senders queue FIFO.
	slot chan struct{}
	file *flock.Flock
}

func newWriterLock(path string) *writerLock {
	return &writerLock{slot: make(chan struct{}, 1), file: flock.New(path)}
}

// acquire waits at most writerWait. A blocking OS lock call cannot be
// interrupted, so a caller that gives up leaves the call behind; it unlocks as
// soon as it is granted and only then frees the slot for this handle.
func (l *writerLock) acquire(ctx context.Context) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	timer := time.NewTimer(writerWait)
	defer timer.Stop()
	select {
	case l.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errWriterBusy
	}
	release := func() {
		_ = l.file.Unlock()
		<-l.slot
	}
	locked := make(chan error, 1)
	go func() { locked <- l.file.Lock() }()
	var err error
	select {
	case err = <-locked:
		if err != nil {
			<-l.slot
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = errWriterBusy
	}
	go func() {
		if <-locked == nil {
			release()
		} else {
			<-l.slot
		}
	}()
	return nil, err
}

// writeTx releases the writer lock when the transaction ends.
type writeTx struct {
	*sql.Tx
	release func()
}

// begin starts a BEGIN IMMEDIATE transaction under the writer lock. Every
// write in this package must go through begin or exec.
func (db *DB) begin(ctx context.Context) (*writeTx, error) {
	release, err := db.writeLock.acquire(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		release()
		return nil, err
	}
	return &writeTx{tx, release}, nil
}

func (tx *writeTx) Commit() error {
	defer tx.done()
	return tx.Tx.Commit()
}

func (tx *writeTx) Rollback() error {
	defer tx.done()
	return tx.Tx.Rollback()
}

func (tx *writeTx) done() {
	if tx.release != nil {
		tx.release()
		tx.release = nil
	}
}

// exec runs one write statement as its own transaction.
func (db *DB) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx, err := db.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

// autocommit routes a repository's single-statement writes through the writer lock.
type autocommit struct{ *DB }

func (w autocommit) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return w.exec(ctx, query, args...)
}
