package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindByIDReadsHeadersOnlyAndFiltersCustomDirectoryCWD(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	other := filepath.Join(root, "other")
	for _, path := range []string{project, other} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, id, cwd string) string {
		path := filepath.Join(root, name)
		body := `{"type":"session","version":3,"id":"` + id + `","timestamp":"2025-01-01T00:00:00.000Z","cwd":"` + cwd + `"}` + "\n" +
			"this transcript body is deliberately not valid JSON\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	want := write("wanted.jsonl", "same-id", project)
	write("other.jsonl", "same-id", other)

	if got := FindByID(project, "same-id", root); got != want {
		t.Fatalf("FindByID = %q, want %q", got, want)
	}
	if got := FindByID(project, "missing", root); got != "" {
		t.Fatalf("missing FindByID = %q", got)
	}
}

func TestListContextPublishesSortedPartialSessionsAndHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"old", "middle", "new"} {
		path := filepath.Join(root, id+".jsonl")
		stamp := time.Date(2025, time.January, 1, 0, index, 0, 0, time.UTC)
		body := `{"type":"session","version":3,"id":"` + id + `","timestamp":"` + stamp.Format(time.RFC3339Nano) + `","cwd":"` + project + `"}` + "\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var updates []SessionListUpdate
	listed, err := ListContext(context.Background(), project, root, func(update SessionListUpdate) {
		copyUpdate := update
		copyUpdate.Sessions = append([]SessionInfo(nil), update.Sessions...)
		updates = append(updates, copyUpdate)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 || len(updates) == 0 {
		t.Fatalf("listed=%d updates=%#v", len(listed), updates)
	}
	last := updates[len(updates)-1]
	if last.Loaded != 3 || last.Total != 3 || len(last.Sessions) != 3 || last.Sessions[0].ID != "new" {
		t.Fatalf("final update = %#v", last)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ListContext(ctx, project, root, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled ListContext error = %v", err)
	}
}
