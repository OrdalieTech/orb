package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestReleasedV4TransactionCorpus(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/F6HarnessTransactions/transactions.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Observations []struct {
			Name                string
			FirstSeq, Timestamp int64
			Writes              []json.RawMessage
			Prepared            SessionV4PreparedTransaction
			Error               *string
		}
		Headers []struct {
			Header json.RawMessage
			Valid  bool
		}
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range corpus.Observations {
		t.Run(scenario.Name, func(t *testing.T) {
			prepared, err := PrepareSessionV4Transaction(scenario.Writes, scenario.FirstSeq, scenario.Timestamp)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(prepared.Result, scenario.Prepared.Result) {
				t.Fatalf("result: %#v != %#v", prepared.Result, scenario.Prepared.Result)
			}
			for i, raw := range prepared.Writes {
				var expected, actual any
				if err := json.Unmarshal(raw, &actual); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(scenario.Prepared.Writes[i], &expected); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(expected, actual) {
					t.Fatalf("write: %s != %s", raw, scenario.Prepared.Writes[i])
				}
				var compact bytes.Buffer
				if err := json.Compact(&compact, scenario.Prepared.Writes[i]); err != nil {
					t.Fatal(err)
				}
				var actualCompact bytes.Buffer
				if err := json.Compact(&actualCompact, raw); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(compact.Bytes(), actualCompact.Bytes()) {
					t.Fatalf("field order: %s != %s", actualCompact.Bytes(), compact.Bytes())
				}
			}
			err = ValidateSessionV4Transaction(prepared.Writes, scenario.FirstSeq, func(string) bool { return false }, func(string) bool { return false })
			if scenario.Error == nil && err != nil {
				t.Fatal(err)
			}
			if scenario.Error != nil && (err == nil || err.Error() != *scenario.Error) {
				t.Fatalf("error: %v != %s", err, *scenario.Error)
			}
		})
	}
	for _, scenario := range corpus.Headers {
		_, err := DecodeSessionV4TransactionHeader(scenario.Header)
		if (err == nil) != scenario.Valid {
			t.Fatalf("header %s: %v", scenario.Header, err)
		}
	}
}

func TestReleasedV4TransactionState(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/F6HarnessTransactions/state.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Commits []struct {
			Writes   []json.RawMessage
			Prepared SessionV4PreparedTransaction
			Stats    SessionV4TransactionStats
		}
		Entries, EntriesDesc, Usage, Branch []json.RawMessage
		Values                              []SessionV4StoredValue
		List, ListDesc                      []SessionV4ListElement
		Stats                               SessionV4TransactionStats
	}
	if err = json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	storage := NewMemorySessionV4TransactionStorage()
	storage.Now = func() int64 { return 1770091506789 }
	compare := func(name string, got, want any) {
		t.Helper()
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		var x, y any
		_ = json.Unmarshal(a, &x)
		_ = json.Unmarshal(b, &y)
		if !reflect.DeepEqual(x, y) {
			t.Fatalf("%s: %s != %s", name, a, b)
		}
	}
	for _, batch := range corpus.Commits {
		result, err := storage.Commit(context.Background(), batch.Writes)
		if err != nil {
			t.Fatal(err)
		}
		compare("commit", result, SessionV4CommitResult{SessionV4TransactionResult: batch.Prepared.Result, Stats: batch.Stats})
	}
	entries, _ := storage.ScanEntries(SessionV4Scan{})
	compare("entries", entries, corpus.Entries)
	one := 1
	entries, _ = storage.ScanEntries(SessionV4Scan{Order: "desc", Limit: &one})
	compare("entries descending", entries, corpus.EntriesDesc)
	usage, _ := storage.ScanUsage(SessionV4Scan{})
	compare("usage", usage, corpus.Usage)
	branch, _ := storage.ScanBranch(SessionV4BranchScan{Start: "b", Order: "oldestFirst"})
	compare("branch", branch, corpus.Branch)
	values, _ := storage.ScanValues(SessionV4Address{Namespace: "test"})
	compare("values", values, corpus.Values)
	list, _ := storage.ReadList(SessionV4Address{Namespace: "test", Key: "list"}, SessionV4ListReadOptions{})
	compare("list", list, corpus.List)
	two := 2
	list, _ = storage.ReadList(SessionV4Address{Namespace: "test", Key: "list"}, SessionV4ListReadOptions{Order: "desc", Limit: &two})
	compare("list descending", list, corpus.ListDesc)
	stats, _ := storage.TransactionStats()
	compare("stats", stats, corpus.Stats)
}

func TestReleasedV4TransactionMigration(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/F6HarnessTransactions/migration.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Records    []string
		Normalized transactionMigration
	}
	if err = json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	lines := make([][]byte, len(corpus.Records))
	for i, line := range corpus.Records {
		lines[i] = []byte(line)
	}
	got, err := normalizeTransactionLegacyV3(lines, func(id string, _ int64) (string, error) { return id, nil })
	if err != nil {
		t.Fatal(err)
	}
	for i, raw := range got.Writes {
		var actual, expected bytes.Buffer
		_ = json.Compact(&actual, raw)
		_ = json.Compact(&expected, corpus.Normalized.Writes[i])
		if !bytes.Equal(actual.Bytes(), expected.Bytes()) {
			t.Fatalf("write %d: %s != %s", i, actual.Bytes(), expected.Bytes())
		}
	}
	if !reflect.DeepEqual(got.ImportedUsage, corpus.Normalized.ImportedUsage) || got.NextSeq != corpus.Normalized.NextSeq {
		t.Fatalf("migration stats: %#v != %#v", got, corpus.Normalized)
	}
}

func TestReleasedV4TransactionFiles(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/F6HarnessTransactions/files.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Header                SessionV4TransactionHeader
		InitialWrites, Writes []json.RawMessage
		Timestamp             int64
		Result                SessionV4CommitResult
		Content, Repaired     string
		Entries               []json.RawMessage
	}
	if err = json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	fs := &NodeExecutionEnv{CWD: root}
	path := root + "/session.jsonl"
	ctx := context.Background()
	storage, err := CreateJSONLSessionV4TransactionStorage(ctx, fs, path, corpus.Header, corpus.InitialWrites, func() int64 { return corpus.Timestamp })
	if err != nil {
		t.Fatal(err)
	}
	result, err := storage.Commit(ctx, corpus.Writes)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, corpus.Result) {
		t.Fatalf("result: %#v != %#v", result, corpus.Result)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != corpus.Content {
		t.Fatalf("file: %s != %s", content, corpus.Content)
	}
	if err = storage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(content, []byte(`{"kind":"entry"`)...), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJSONLSessionV4TransactionStorage(ctx, fs, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(repaired) != corpus.Repaired {
		t.Fatalf("repair: %s", repaired)
	}
	if err = reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReleasedV4TransactionForks(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/F6HarnessTransactions/forks.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Source SessionV4TransactionSnapshot
		Cases  []struct {
			Options SessionV4TransactionForkOptions
			Writes  []json.RawMessage
			NextSeq int64
			Error   *string
		}
	}
	if err = json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range corpus.Cases {
		writes, nextSeq, err := createTransactionFork(corpus.Source, scenario.Options)
		if scenario.Error != nil {
			if err == nil || err.Error() != *scenario.Error {
				t.Fatalf("error: %v != %s", err, *scenario.Error)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if nextSeq != scenario.NextSeq || len(writes) != len(scenario.Writes) {
			t.Fatalf("snapshot sizes: %d %d", nextSeq, len(writes))
		}
		for i, raw := range writes {
			var actual, expected bytes.Buffer
			_ = json.Compact(&actual, raw)
			_ = json.Compact(&expected, scenario.Writes[i])
			if !bytes.Equal(actual.Bytes(), expected.Bytes()) {
				t.Fatalf("fork write %d: %s != %s", i, actual.Bytes(), expected.Bytes())
			}
		}
	}
}

func TestReleasedV4TransactionRepo(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/F6HarnessTransactions/repos.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err = json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	fs := &NodeExecutionEnv{CWD: root}
	repo := NewJSONLSessionV4Repo(fs, root+"/sessions")
	repo.Now = func() int64 { return 1770091506789 }
	id := "id / +?"
	cwd := "/fixture/project"
	created, err := repo.Create(ctx, JSONLSessionV4CreateOptions{SessionV4CreateOptions: SessionV4CreateOptions{ID: &id}, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	metadata := created.Metadata()
	errorText := func(err error) any {
		if err == nil {
			return nil
		}
		return err.Error()
	}
	_, openErr := repo.Open(ctx, metadata)
	deleteErr := repo.Delete(ctx, metadata)
	_, createErr := repo.Create(ctx, JSONLSessionV4CreateOptions{SessionV4CreateOptions: SessionV4CreateOptions{ID: &id}, CWD: cwd})
	content, err := os.ReadFile(metadata.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = created.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := repo.Open(ctx, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}
	listed, err := repo.List(ctx, &cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Delete(ctx, metadata); err != nil {
		t.Fatal(err)
	}
	missingErr := repo.Delete(ctx, metadata)
	if err = repo.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, closedErr := repo.List(ctx, nil)
	got := map[string]any{"metadata": metadata, "content": string(content), "list": listed, "duplicateOpen": errorText(openErr), "deleteOpen": errorText(deleteErr), "duplicateCreate": errorText(createErr), "missingDelete": errorText(missingErr), "closedList": errorText(closedErr)}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var normalized any
	if err = json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	var normalize func(any) any
	normalize = func(value any) any {
		switch v := value.(type) {
		case string:
			return strings.ReplaceAll(v, root, "<fixture>")
		case []any:
			for i := range v {
				v[i] = normalize(v[i])
			}
		case map[string]any:
			for k, item := range v {
				if k == "modifiedAt" {
					v[k] = "<modifiedAt>"
				} else {
					v[k] = normalize(item)
				}
			}
		}
		return value
	}
	normalized = normalize(normalized)
	if !reflect.DeepEqual(normalized, want) {
		actual, _ := json.MarshalIndent(normalized, "", "  ")
		t.Fatalf("repo: %s != %s", actual, data)
	}
}

func TestReleasedV4TransactionFileErrors(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/F6HarnessTransactions/files.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Failures []struct{ Content, Message, Cause string }
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	fs := &NodeExecutionEnv{CWD: t.TempDir()}
	for _, failure := range fixture.Failures {
		if err = fs.WriteFile(context.Background(), "session.jsonl", []byte(failure.Content)); err != nil {
			t.Fatal(err)
		}
		_, err = OpenJSONLSessionV4TransactionStorage(context.Background(), fs, "session.jsonl", nil)
		if err == nil || err.Error() != failure.Message || errors.Unwrap(err) == nil || errors.Unwrap(err).Error() != failure.Cause {
			t.Fatalf("file error: %v (%v), want %s (%s)", err, errors.Unwrap(err), failure.Message, failure.Cause)
		}
	}
}
