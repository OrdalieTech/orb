package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// A queued writer gives up on its own deadline, readers never queue, and an
// abandoned kernel wait must not wedge later writes from either handle.
func TestWriterQueueHonorsDeadlinesAndSparesReaders(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "orb.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	set := func(value string) func([]byte) ([]byte, error) {
		return func([]byte) ([]byte, error) { return []byte(value), nil }
	}
	if err := a.Document("n", "k").Update(ctx, set("0")); err != nil {
		t.Fatal(err)
	}

	held, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- a.Document("n", "k").Update(ctx, func([]byte) ([]byte, error) {
			close(held)
			<-release
			return []byte("1"), nil
		})
	}()
	<-held
	for _, db := range []*DB{a, b} {
		bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		got, err := db.Document("n", "k").Read(bounded)
		if err != nil || string(got) != "0" {
			cancel()
			t.Fatalf("reader queued behind writer: %q %v", got, err)
		}
		start := time.Now()
		err = db.Document("n", "k").Update(bounded, set("lost"))
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
			t.Fatalf("queued writer ignored its deadline: %v after %v", err, time.Since(start))
		}
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	for i, db := range []*DB{b, a} {
		if err := db.Document("n", "k").Update(ctx, set(string(rune('2'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := b.Document("n", "k").Read(ctx); err != nil || string(got) != "3" {
		t.Fatalf("document = %q, %v", got, err)
	}
}
