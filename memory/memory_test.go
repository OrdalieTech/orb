package memory

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileStoreAppendGetQueryAndDelete(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	inputs := []Item{
		{Time: base, Content: "Alpha first", Tags: []string{"go", "sdk"}},
		{Time: base.Add(time.Minute), Content: "beta second", Tags: []string{"go"}},
		{Time: base.Add(2 * time.Minute), Content: "ALPHA third", Tags: []string{"go", "sdk"}},
	}
	ids := make([]string, len(inputs))
	for index, item := range inputs {
		ids[index], err = store.Append(ctx, item)
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Get(ctx, ids[1])
	if err != nil || got.Content != inputs[1].Content || got.ID != ids[1] {
		t.Fatalf("Get = %#v, %v", got, err)
	}

	tests := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{name: "newest first", want: []string{"ALPHA third", "beta second", "Alpha first"}},
		{name: "tag AND", filter: Filter{Tags: []string{"go", "sdk"}}, want: []string{"ALPHA third", "Alpha first"}},
		{name: "time window inclusive", filter: Filter{Since: base.Add(time.Minute), Until: base.Add(2 * time.Minute)}, want: []string{"ALPHA third", "beta second"}},
		{name: "case insensitive substring", filter: Filter{Contains: "aLpHa"}, want: []string{"ALPHA third", "Alpha first"}},
		{name: "limit", filter: Filter{Limit: 1}, want: []string{"ALPHA third"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items, queryErr := store.Query(ctx, test.filter)
			if queryErr != nil {
				t.Fatal(queryErr)
			}
			contents := make([]string, len(items))
			for index := range items {
				contents[index] = items[index].Content
			}
			if strings.Join(contents, "|") != strings.Join(test.want, "|") {
				t.Fatalf("Query = %v, want %v", contents, test.want)
			}
		})
	}

	for index := 0; index < 101; index++ {
		if _, err := store.Append(ctx, Item{Time: base.Add(time.Duration(index+3) * time.Minute), Content: "bulk"}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.Query(ctx, Filter{Limit: 1000})
	if err != nil || len(items) != 100 {
		t.Fatalf("capped Query len = %d, err = %v", len(items), err)
	}

	if err := store.Delete(ctx, ids[2]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, ids[2]); err == nil {
		t.Fatal("deleted item remained readable")
	}
	items, err = store.Query(ctx, Filter{Contains: "third"})
	if err != nil || len(items) != 0 {
		t.Fatalf("deleted item remained queryable: %#v, %v", items, err)
	}
}

func TestFileStoreConcurrentAccess(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const workers, perWorker = 16, 4
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := 0; index < perWorker; index++ {
				if _, appendErr := store.Append(context.Background(), Item{Content: "concurrent"}); appendErr != nil {
					t.Errorf("Append: %v", appendErr)
					return
				}
				if _, queryErr := store.Query(context.Background(), Filter{}); queryErr != nil {
					t.Errorf("Query: %v", queryErr)
					return
				}
			}
		}()
	}
	group.Wait()
	items, err := store.Query(context.Background(), Filter{})
	if err != nil || len(items) != workers*perWorker {
		t.Fatalf("Query len = %d, err = %v", len(items), err)
	}
}

func TestFileStoreSkipsCorruptLines(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), Item{Content: "before"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(store.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{broken json\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), Item{Content: "after"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.Query(context.Background(), Filter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("Query = %#v, %v", items, err)
	}
}
