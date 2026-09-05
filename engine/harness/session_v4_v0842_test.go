package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func v4Str(value string) *string { return &value }

func v4Repo(t *testing.T) (*JSONLSessionV4Repo, *NodeExecutionEnv, string) {
	t.Helper()
	root := t.TempDir()
	env := &NodeExecutionEnv{CWD: root}
	t.Cleanup(func() { _ = env.Cleanup() })
	return NewJSONLSessionV4Repo(env, root), env, root
}

func v4Create(t *testing.T, repo *JSONLSessionV4Repo, id, cwd string) *JSONLSessionV4Storage {
	t.Helper()
	storage, err := repo.Create(context.Background(), JSONLSessionV4CreateOptions{
		SessionV4CreateOptions: SessionV4CreateOptions{ID: &id}, CWD: cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	return storage
}

func v4Code(t *testing.T, err error) SessionErrorCode {
	t.Helper()
	var failure *SessionError
	if !errors.As(err, &failure) {
		t.Fatalf("error %v is not a SessionError", err)
	}
	return failure.Code
}

// failOnce injects a single filesystem failure so a create or fork fails after
// claiming its destination.
type failOnce struct {
	FileSystem
	mu            sync.Mutex
	write, rename bool
}

func (fs *failOnce) trip(target *bool) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fail := *target
	*target = false
	return fail
}

func (fs *failOnce) WriteFile(ctx context.Context, path string, data []byte) error {
	if fs.trip(&fs.write) {
		return &FileError{Code: FileErrorUnknown, Path: path, Err: errors.New("injected creation failure")}
	}
	return fs.FileSystem.WriteFile(ctx, path, data)
}

func (fs *failOnce) RenameFile(ctx context.Context, from, to string) error {
	if fs.trip(&fs.rename) {
		return &FileError{Code: FileErrorUnknown, Path: from, Err: errors.New("injected fork failure")}
	}
	return fs.FileSystem.RenameFile(ctx, from, to)
}

func TestSessionV4DecodeErrorsSeparateSyntaxFromSchema(t *testing.T) {
	for line, want := range map[string]SessionV4DecodeErrorKind{
		"{":                          SessionV4DecodeSyntax,
		`[1]`:                        SessionV4DecodeSchema,
		`{"kind":"unknown","seq":1}`: SessionV4DecodeSchema,
	} {
		if _, err := DecodeSessionV4Mutation([]byte(line)); err == nil || err.Kind != want {
			t.Fatalf("DecodeSessionV4Mutation(%q) = %#v, want kind %q", line, err, want)
		}
	}
	for line, want := range map[string]SessionV4DecodeErrorKind{
		"not json":                      SessionV4DecodeSyntax,
		`{"kind":"header","version":5}`: SessionV4DecodeSchema,
		`{"kind":"header","version":4,"id":"s","createdAt":0,"cwd":"/c","metadata":"x"}`: SessionV4DecodeSchema,
	} {
		if _, err := DecodeSessionV4Header([]byte(line)); err == nil || err.Kind != want {
			t.Fatalf("DecodeSessionV4Header(%q) = %#v, want kind %q", line, err, want)
		}
	}

	// The file-context wrapper keeps the decode message and stays unwrappable.
	err := func() error { _, err := ParseSessionV4Mutation([]byte("{"), "/s.jsonl", 3); return err }()
	if got := err.Error(); got != "Invalid JSONL v4 session /s.jsonl: line 3 is not valid JSON" {
		t.Fatalf("ParseSessionV4Mutation error = %q", got)
	}
	var decodeErr *SessionV4DecodeError
	if v4Code(t, err) != SessionErrorInvalidEntry || !errors.As(err, &decodeErr) || decodeErr.Kind != SessionV4DecodeSyntax {
		t.Fatalf("wrapped error = %v (%#v)", err, decodeErr)
	}
}

func TestSessionV4FactNameRoundTripsClearedValues(t *testing.T) {
	for _, mutation := range []SessionV4Mutation{
		{Kind: "fact", Seq: 1, Fact: "name", Name: "Example"},
		{Kind: "fact", Seq: 2, Fact: "name", NameCleared: true},
		{Kind: "fact", Seq: 3, Fact: "label", TargetID: "entry-1", Label: v4Str("checkpoint")},
	} {
		encoded, err := MarshalSessionV4Mutation(mutation)
		if err != nil {
			t.Fatal(err)
		}
		decoded, decodeErr := DecodeSessionV4Mutation(encoded)
		if decodeErr != nil {
			t.Fatalf("decode %s: %v", encoded, decodeErr)
		}
		reencoded, err := MarshalSessionV4Mutation(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if string(reencoded) != string(encoded) || decoded.NameCleared != mutation.NameCleared {
			t.Fatalf("round trip of %s = %s (%+v)", encoded, reencoded, decoded)
		}
		if mutation.NameCleared && string(encoded) != `{"kind":"fact","seq":2,"fact":"name"}` {
			t.Fatalf("cleared name line = %s", encoded)
		}
	}
	if _, err := DecodeSessionV4Mutation([]byte(`{"kind":"fact","seq":1,"fact":"name","name":5}`)); err == nil ||
		err.Kind != SessionV4DecodeSchema {
		t.Fatalf("non-string name decode = %#v", err)
	}
}

func TestSessionV4ClearsSessionNamesDurably(t *testing.T) {
	ctx := context.Background()
	repo, _, root := v4Repo(t)
	storage := v4Create(t, repo, "session", root)
	if err := storage.SetName("Temporary"); err != nil {
		t.Fatal(err)
	}
	if err := storage.ClearName(); err != nil {
		t.Fatal(err)
	}

	assert := func(label string, storage *JSONLSessionV4Storage) {
		t.Helper()
		if name, ok := storage.Name(); ok {
			t.Fatalf("%s Name() = %q", label, name)
		}
		if _, err := storage.Log(SessionV4LogOptions{}); err == nil || !strings.Contains(err.Error(), "no longer supported") {
			t.Fatalf("removed log API: %v", err)
		}
		values, err := storage.ScanValues(SessionV4Address{Namespace: "pi.session.name"})
		if err != nil || len(values) != 0 {
			t.Fatalf("name values: %#v, %v", values, err)
		}
	}
	assert("live", storage)

	if err := storage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := repo.Open(ctx, storage.Metadata())
	if err != nil {
		t.Fatal(err)
	}
	assert("reopened", reopened)

	forked, err := repo.Fork(ctx, storage.Metadata(), JSONLSessionV4ForkOptions{
		SessionV4ForkOptions: SessionV4ForkOptions{Scope: "tree"},
		JSONLSessionV4CreateOptions: JSONLSessionV4CreateOptions{
			SessionV4CreateOptions: SessionV4CreateOptions{ID: v4Str("fork")}, CWD: root,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := forked.Name(); ok {
		t.Fatalf("forked Name() = %q, %v", name, ok)
	}

	var memory SessionV4NameClearer = NewInMemorySessionV4Storage(SessionV4Metadata{ID: "session"})
	if err := memory.(*InMemorySessionV4Storage).SetName("Temporary"); err != nil {
		t.Fatal(err)
	}
	if err := memory.ClearName(); err != nil {
		t.Fatal(err)
	}
	if name, ok := memory.(*InMemorySessionV4Storage).Name(); ok {
		t.Fatalf("memory Name() = %q, %v", name, ok)
	}
}

func TestSessionV4LoadRejectsCorruptionWithoutRepair(t *testing.T) {
	ctx := context.Background()
	const header = `{"v":4,"kind":"header","id":"s","storageVersion":1,"createdAt":0,"cwd":"/c"}` + "\n"
	for _, test := range []struct{ name, tail, want string }{
		// A well-formed final line is corruption, never a torn append.
		{name: "invalid-final-mutation", tail: `{"kind":"unknown","seq":1}` + "\n", want: "Invalid JSONL write kind: unknown"},
		{
			name: "missing-parent",
			tail: `{"kind":"entry","type":"custom","id":"e","customType":"x","parentId":"missing","seq":1,"timestamp":0}` + "\n",
			want: "Missing parent entry: missing",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			env := &NodeExecutionEnv{CWD: root}
			t.Cleanup(func() { _ = env.Cleanup() })
			path := filepath.Join(root, test.name+".jsonl")
			content := header + test.tail
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadJSONLSessionV4Storage(ctx, env, path)
			want := fmt.Sprintf("Invalid JSONL storage %s: line 2", path)
			if err == nil || err.Error() != want || errors.Unwrap(err) == nil || errors.Unwrap(err).Error() != test.want {
				t.Fatalf("open error = %q, want %q", err, want)
			}
			if written, readErr := os.ReadFile(path); readErr != nil || string(written) != content {
				t.Fatalf("file was modified: %s (%v)", written, readErr)
			}
		})
	}
}

func TestSessionV4ListSkipsUndecodableHeaders(t *testing.T) {
	ctx := context.Background()
	repo, env, root := v4Repo(t)
	v4Create(t, repo, "valid", root)
	for _, test := range []struct{ id, content string }{
		{id: "malformed-header", content: "not json\n"},
		{id: "invalid-header-metadata", content: `{"kind":"header","version":4,"id":"x","createdAt":0,"cwd":"/c","metadata":"invalid"}` + "\n"},
	} {
		created := v4Create(t, repo, test.id, root)
		metadata := created.Metadata()
		if err := created.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metadata.Path, []byte(test.content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Open(ctx, metadata); err == nil || !strings.Contains(err.Error(), "invalid header") {
			t.Fatalf("%s open error = %v", test.id, err)
		}
		listed, err := ListJSONLSessionV4Metadata(ctx, env, root, &root)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != 1 || listed[0].ID != "valid" {
			t.Fatalf("%s listing = %+v", test.id, listed)
		}
		if written, readErr := os.ReadFile(metadata.Path); readErr != nil || string(written) != test.content {
			t.Fatalf("%s file was modified: %s (%v)", test.id, written, readErr)
		}
		if err := os.Remove(metadata.Path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenJSONLSessionV4StorageLoadsListedMetadata(t *testing.T) {
	ctx := context.Background()
	repo, env, root := v4Repo(t)
	created := v4Create(t, repo, "listed", root)
	if _, err := created.AppendEntry(json.RawMessage(`{"type":"custom","id":"e1","parentId":null,"customType":"note"}`), "main"); err != nil {
		t.Fatal(err)
	}
	listed, err := ListJSONLSessionV4Metadata(ctx, env, root, nil)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listing = %+v, %v", listed, err)
	}
	storage, err := OpenJSONLSessionV4Storage(ctx, env, root, listed[0])
	if err != nil {
		t.Fatal(err)
	}
	if entry, ok := storage.Entry("e1"); !ok || entry.CustomType != "note" {
		t.Fatalf("opened entry = %+v, %v", entry, ok)
	}
	mismatched := listed[0]
	mismatched.ID = "other"
	if _, err := OpenJSONLSessionV4Storage(ctx, env, root, mismatched); v4Code(t, err) != SessionErrorInvalidEntry {
		t.Fatalf("mismatched id error = %v", err)
	}
}

func TestJSONLSessionV4RepoRejectsConflictingCreation(t *testing.T) {
	ctx := context.Background()
	for _, kinds := range [][2]string{{"create", "create"}, {"create", "fork"}, {"fork", "fork"}} {
		t.Run(kinds[0]+"-"+kinds[1], func(t *testing.T) {
			repo, env, root := v4Repo(t)
			cwd := filepath.Join(root, "workspace")
			source := v4Create(t, repo, "source", cwd).Metadata()
			// A fixed clock makes both calls resolve the same durable filename,
			// which is exactly when the existence check alone is not enough.
			repo.Now = func() int64 { return 1767225600000 }
			run := func(kind string) error {
				if kind == "create" {
					_, err := repo.Create(ctx, JSONLSessionV4CreateOptions{
						SessionV4CreateOptions: SessionV4CreateOptions{ID: v4Str("same")}, CWD: cwd,
					})
					return err
				}
				_, err := repo.Fork(ctx, source, JSONLSessionV4ForkOptions{
					SessionV4ForkOptions: SessionV4ForkOptions{Scope: "tree"},
					JSONLSessionV4CreateOptions: JSONLSessionV4CreateOptions{
						SessionV4CreateOptions: SessionV4CreateOptions{ID: v4Str("same")}, CWD: cwd,
					},
				})
				return err
			}

			start := make(chan struct{})
			results := make([]error, 2)
			var group sync.WaitGroup
			for index, kind := range kinds {
				group.Add(1)
				go func() {
					defer group.Done()
					<-start
					results[index] = run(kind)
				}()
			}
			close(start)
			group.Wait()

			failures := 0
			for _, err := range results {
				if err == nil {
					continue
				}
				failures++
				if code := v4Code(t, err); code != SessionErrorAlreadyExists {
					t.Fatalf("conflict error code = %s (%v)", code, err)
				}
			}
			if failures != 1 {
				t.Fatalf("failures = %d, want exactly one", failures)
			}
			listed, err := ListJSONLSessionV4Metadata(ctx, env, root, &cwd)
			if err != nil {
				t.Fatal(err)
			}
			matched := 0
			for _, metadata := range listed {
				if metadata.ID == "same" {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("sessions named same = %d (%+v)", matched, listed)
			}
		})
	}
}

func TestJSONLSessionV4RepoClaimsOneDestinationAtATime(t *testing.T) {
	ctx := context.Background()
	repo, _, root := v4Repo(t)
	options := JSONLSessionV4CreateOptions{SessionV4CreateOptions: SessionV4CreateOptions{ID: v4Str("same")}, CWD: root}
	_, _, release, err := repo.claimDestination(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.claimDestination(ctx, options); v4Code(t, err) != SessionErrorAlreadyExists {
		t.Fatalf("second claim error = %v", err)
	}
	other := options
	other.ID = v4Str("other")
	if _, _, _, err := repo.claimDestination(ctx, other); err != nil {
		t.Fatalf("unrelated destination claim: %v", err)
	}
	release()
	if _, _, _, err := repo.claimDestination(ctx, options); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
}

func TestJSONLSessionV4RepoReleasesDestinationAfterFailure(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"create", "fork"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			env := &NodeExecutionEnv{CWD: root}
			t.Cleanup(func() { _ = env.Cleanup() })
			failing := &failOnce{FileSystem: env}
			repo := NewJSONLSessionV4Repo(failing, root)
			cwd := filepath.Join(root, "workspace")
			source := v4Create(t, repo, "source", cwd).Metadata()
			run := func() error {
				if kind == "create" {
					_, err := repo.Create(ctx, JSONLSessionV4CreateOptions{
						SessionV4CreateOptions: SessionV4CreateOptions{ID: v4Str("retry")}, CWD: cwd,
					})
					return err
				}
				_, err := repo.Fork(ctx, source, JSONLSessionV4ForkOptions{
					SessionV4ForkOptions: SessionV4ForkOptions{Scope: "tree"},
					JSONLSessionV4CreateOptions: JSONLSessionV4CreateOptions{
						SessionV4CreateOptions: SessionV4CreateOptions{ID: v4Str("retry")}, CWD: cwd,
					},
				})
				return err
			}

			failing.mu.Lock()
			failing.write, failing.rename = kind == "create", kind == "fork"
			failing.mu.Unlock()
			if err := run(); err == nil || !strings.Contains(err.Error(), "injected") {
				t.Fatalf("injected failure error = %v", err)
			}
			if err := run(); err != nil {
				t.Fatalf("retry after failure: %v", err)
			}
			listed, err := ListJSONLSessionV4Metadata(ctx, env, root, &cwd)
			if err != nil {
				t.Fatal(err)
			}
			matched := 0
			for _, metadata := range listed {
				if metadata.ID == "retry" {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("sessions named retry = %d (%+v)", matched, listed)
			}
		})
	}
}
