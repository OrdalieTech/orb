package sqlite

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/ai/auth"
)

func TestDocumentsCommitRollbackAndConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "orb.db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if done, err := MigrationStatus(ctx, path, "native"); done || err != nil {
		t.Fatalf("empty initial database was not recoverable: %v %v", done, err)
	}
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
	writer, err := a.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	reader, openErr := Open(bounded, path)
	cancel()
	_ = writer.Rollback()
	if openErr != nil {
		t.Fatal("database open waited for writer", openErr)
	}
	_ = reader.Close()

	var wg sync.WaitGroup
	for _, db := range []*DB{a, b} {
		wg.Go(func() {
			for range 30 {
				err := db.Document("profile-a", "counter").Update(ctx, func(old []byte) ([]byte, error) {
					n, _ := strconv.Atoi(string(old))
					return []byte(strconv.Itoa(n + 1)), nil
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	doc := a.Document("profile-a", "counter")
	failed := errors.New("interrupted")
	if err := doc.Update(ctx, func([]byte) ([]byte, error) { return []byte("wrong"), failed }); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	got, err := doc.Read(ctx)
	if err != nil || string(got) != "60" {
		t.Fatalf("counter = %q, %v", got, err)
	}
	got, err = b.Document("profile-b", "counter").Read(ctx)
	if err != nil || got != nil {
		t.Fatalf("namespace leaked: %q, %v", got, err)
	}
	for _, db := range []*DB{a, b} {
		for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1"} {
			var got string
			if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
				t.Fatalf("%s = %q, %v", pragma, got, err)
			}
		}
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := os.Chmod(filepath.Dir(backup), 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	got, err = restored.Document("profile-a", "counter").Read(ctx)
	if err != nil || string(got) != "60" {
		t.Fatalf("backup = %q, %v", got, err)
	}
	if err := a.Backup(ctx, backup); err == nil {
		t.Fatal("backup overwrote existing file")
	}
	a.SetMaxOpenConns(1)
	var pages int
	if err := a.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecContext(ctx, "PRAGMA max_page_count="+strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	if err := doc.Update(ctx, func([]byte) ([]byte, error) { return []byte(strings.Repeat("x", 2<<20)), nil }); err == nil {
		t.Fatal("full database accepted write")
	}
	if got, err := doc.Read(ctx); err != nil || string(got) != "60" {
		t.Fatal("full database lost committed state", err)
	}

}

func TestNativeSettingsAndCredentialsUseDocuments(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "state", "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	agentDir := filepath.Join(t.TempDir(), "unused")
	doc := db.Document("config", "settings")
	a, err := config.NewSettingsManager(t.TempDir(), config.WithAgentDir(agentDir), config.WithGlobalDocument(doc))
	if err != nil {
		t.Fatal(err)
	}
	a.SetDefaultModelAndProvider("test", "test-model")
	b, err := config.NewSettingsManager(t.TempDir(), config.WithAgentDir(agentDir), config.WithGlobalDocument(doc))
	if err != nil {
		t.Fatal(err)
	}
	if b.GetDefaultModel() != "test-model" {
		t.Fatal("settings not persisted")
	}
	credentials, err := config.NewAuthStorageWithDocument(db.Document("config", "auth"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = credentials.Modify(ctx, "test", func(*auth.Credential) (*auth.Credential, error) { return auth.APIKeyCredential("test-secret"), nil }); err != nil {
		t.Fatal(err)
	}
	other, err := config.NewAuthStorageWithDocument(db.Document("config", "auth"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := other.Read(ctx, "test")
	if err != nil || got == nil || got.Key == nil || *got.Key != "test-secret" {
		t.Fatal("credential not persisted")
	}
	if err := credentials.Delete(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if got, err = other.Read(ctx, "test"); err != nil || got != nil {
		t.Fatal("credential cache retained deleted value")
	}
	if _, err := os.Stat(agentDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("document-backed config created legacy files")
	}
	trust, err := config.NewProjectTrustStoreWithDocument(db.Document("config", "trust"))
	if err != nil {
		t.Fatal(err)
	}
	trusted := true
	if err := trust.Set("/project", &trusted); err != nil {
		t.Fatal(err)
	}
	if got, err := trust.Get("/project/child"); err != nil || got == nil || !*got {
		t.Fatal("trust not persisted")
	}
	if err := trust.Set("/project", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := trust.Get("/project/child"); err != nil || got != nil {
		t.Fatal("trust deletion not persisted")
	}
}

func TestOpenRefusesUnsafeOrUnknownDatabase(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, "relative.db"); err == nil {
		t.Fatal("accepted relative path")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0600, 0644} {
		path := filepath.Join(root, strconv.Itoa(int(mode))+".db")
		if err := os.WriteFile(path, []byte("not a database"), mode); err != nil {
			t.Fatal(err)
		}
		if db, err := Open(ctx, path); err == nil {
			_ = db.Close()
			t.Fatal("accepted corrupt/unsafe database")
		}
		if data, _ := os.ReadFile(path); string(data) != "not a database" {
			t.Fatal("damaged source modified")
		}
	}
	path := filepath.Join(root, "future.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE foreign_sessions; DROP TABLE foreign_sources; DROP TABLE memory_items; DROP TABLE chat_pending; ALTER TABLE sessions DROP COLUMN preview; ALTER TABLE sessions DROP COLUMN message_count; PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err := db.Document("upgrade", "retained").Update(ctx, func([]byte) ([]byte, error) { return []byte("retained"), nil }); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Foreign("upgrade").List(ctx, "peer"); err != nil {
		t.Fatal("cache schema upgrade failed", err)
	}
	if data, err := db.Document("upgrade", "retained").Read(ctx); err != nil || string(data) != "retained" {
		t.Fatal("migration lost data", err)
	}
	if _, err := db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if db, err := Open(ctx, path); err == nil {
		_ = db.Close()
		t.Fatal("accepted future schema")
	}
}

func TestMigrationRestartConflictAndCutover(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "state", "orb.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	source := filepath.Join(root, "settings.json")
	broken := filepath.Join(root, "auth.json")
	if err := os.WriteFile(source, []byte(`{"future":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	sources := []MigrationSource{{Path: source, Namespace: "config", Key: "settings", Kind: "json"}, {Path: broken, Namespace: "config", Key: "auth", Kind: "json"}}
	if err := db.Migrate(ctx, "native", sources); err == nil {
		t.Fatal("damaged source published cutover")
	}
	done, err := db.Migrated(ctx, "native")
	if err != nil || done {
		t.Fatalf("premature cutover: %v %v", done, err)
	}
	// A failed source can be repaired; a successfully imported one cannot change.
	if err := os.WriteFile(broken, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, "native", sources); err != nil {
		t.Fatal(err)
	}
	done, err = db.Migrated(ctx, "native")
	if err != nil || !done {
		t.Fatalf("no cutover: %v %v", done, err)
	}
	if err := db.Document("config", "settings").Update(ctx, func([]byte) ([]byte, error) { return []byte(`{"new":true}`), nil }); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, "native", sources); err != nil {
		t.Fatal(err)
	}
	data, err := db.Document("config", "settings").Read(ctx)
	if err != nil || string(data) != `{"new":true}` {
		t.Fatal("restart reimported stale source", err)
	}
	original, err := os.ReadFile(source)
	if err != nil || string(original) != `{"future":true}` {
		t.Fatal("source was changed", err)
	}
	sources[0].Key = "settings"
	if err := db.Migrate(ctx, "other", sources); err == nil {
		t.Fatal("conflicting document overwritten")
	}
}

func TestMigrationProcessCrashBeforeCutover(t *testing.T) {
	ctx := context.Background()
	if root := os.Getenv("ORB_MIGRATION_CRASH_ROOT"); root != "" {
		db, err := Open(ctx, filepath.Join(root, "orb.db"))
		if err != nil {
			t.Fatal(err)
		}
		err = db.Migrate(ctx, "crash", []MigrationSource{{Path: filepath.Join(root, "settings.json"), Namespace: "config", Key: "settings", Kind: "json"}}, func() error { os.Exit(23); return nil })
		t.Fatal("did not interrupt", err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "settings.json")
	original := []byte(`{"setting":true}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestMigrationProcessCrashBeforeCutover$")
	command.Env = append(os.Environ(), "ORB_MIGRATION_CRASH_ROOT="+root)
	output, err := command.CombinedOutput()
	var failure *exec.ExitError
	if !errors.As(err, &failure) || failure.ExitCode() != 23 {
		t.Fatalf("child: %v %s", err, output)
	}
	db, err := Open(ctx, filepath.Join(root, "orb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if done, err := db.Migrated(ctx, "crash"); err != nil || done {
		t.Fatalf("premature cutover: %v %v", done, err)
	}
	sources := []MigrationSource{{Path: path, Namespace: "config", Key: "settings", Kind: "json"}}
	if err := os.WriteFile(path, []byte(`{"setting":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, "crash", sources); err == nil {
		t.Fatal("accepted changed source after crash")
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, "crash", sources); err != nil {
		t.Fatal(err)
	}
	if done, err := db.Migrated(ctx, "crash"); err != nil || !done {
		t.Fatalf("restart did not finish: %v %v", done, err)
	}
}
