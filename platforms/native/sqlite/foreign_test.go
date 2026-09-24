package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OrdalieTech/orb/engine/harness"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestForeignCacheIsolationBoundsAndRevocation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "private", "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	c := db.Foreign("personal")
	ticket, err := c.Begin(ctx, "peer-a")
	if err != nil {
		t.Fatal(err)
	}
	entry := ForeignSession{Peer: "peer-a", Namespace: "orb.instance/1", ID: "session", Instance: "instance", Name: "Preview"}
	for range 12 {
		entry.AddMessage(json.RawMessage(`{"role":"user","content":"` + strings.Repeat("a", 10000) + `"}`))
	}
	entry.AddMessage(json.RawMessage(`{"role":"assistant","content":[{"type":"thinking","thinking":"secret"},{"type":"text","text":"visible"},{"type":"toolCall","arguments":{"secret":true}}]}`))
	entry.AddMessage(json.RawMessage(`{"role":"toolResult","content":"secret"}`))
	if err := c.Put(ctx, ticket, entry); err != nil {
		t.Fatal(err)
	}
	rows, err := c.List(ctx, "peer-a")
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %v %v", rows, err)
	}
	encoded, _ := json.Marshal(rows[0].Messages)
	if len(rows[0].Messages) > 8 || len(encoded) > ForeignPreviewBytes || strings.Contains(string(encoded), "secret") || !strings.Contains(string(encoded), "visible") {
		t.Fatal("unsafe/unbounded preview")
	}
	for _, other := range []*Foreign{db.Foreign("other"), c} {
		peer := "peer-a"
		if other == c {
			peer = "peer-b"
		}
		got, err := other.List(ctx, peer)
		if err != nil || len(got) != 0 {
			t.Fatal("origin leaked", err)
		}
	}
	newer, _ := c.Begin(ctx, "peer-a")
	entry.Name = "New"
	if err := c.Put(ctx, newer, entry); err != nil {
		t.Fatal(err)
	}
	entry.Name = "Old"
	if err := c.Put(ctx, ticket, entry); !errors.Is(err, ErrForeignSuperseded) {
		t.Fatal("late refresh accepted", err)
	}
	rows, _ = c.List(ctx, "peer-a")
	if rows[0].Name != "New" {
		t.Fatal("late refresh overwrote newer data")
	}
	if err := c.Forget(ctx, "peer-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, newer, entry); !errors.Is(err, ErrForeignSuperseded) {
		t.Fatal("revoked refresh accepted", err)
	}
	rows, _ = c.List(ctx, "peer-a")
	if len(rows) != 0 {
		t.Fatal("late response restored revoked content")
	}
}

func TestForeignCacheMultipleProcesses(t *testing.T) {
	ctx := context.Background()
	path := os.Getenv("ORB_SQLITE_TEST_DB")
	if path != "" {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		for i := 0; i < 160; i++ {
			cache := db.Foreign("test")
			ticket, err := cache.Begin(ctx, "peer")
			if err != nil {
				t.Fatal(err)
			}
			entry := ForeignSession{Peer: "peer", Namespace: "sessions", ID: fmt.Sprint(i), Instance: "instance", Name: "concurrent"}
			entry.AddMessage(json.RawMessage(`{"role":"assistant","content":"completed remote message"}`))
			if err := cache.Put(ctx, ticket, entry); err != nil && !errors.Is(err, ErrForeignSuperseded) {
				t.Fatal(err)
			}
		}
		return
	}
	path = filepath.Join(t.TempDir(), "private", "orb.db")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			cmd := exec.Command(exe, "-test.run=^TestForeignCacheMultipleProcesses$")
			cmd.Env = append(os.Environ(), "ORB_SQLITE_TEST_DB="+path)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child: %s %v", out, err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
	rows, err := db.Foreign("test").List(ctx, "peer")
	if err != nil || len(rows) != 128 {
		t.Fatalf("lost sessions: %d %v", len(rows), err)
	}
	// The cache cannot appear as an owned session or survive its retention window.
	local, err := db.Sessions("test").List(ctx, harness.SessionListOptions{})
	if err != nil || len(local) != 0 {
		t.Fatal("foreign sessions became local", err)
	}
	if _, err := db.Exec("UPDATE foreign_sessions SET refreshed=0"); err != nil {
		t.Fatal(err)
	}
	rows, err = db.Foreign("test").List(ctx, "peer")
	if err != nil || len(rows) != 0 {
		t.Fatal("expired content returned", err)
	}
}
