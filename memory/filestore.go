package memory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/internal/uuidv7"
	"github.com/gofrs/flock"
)

const queryLimit = 100

type FileStore struct {
	path     string
	lockPath string
}

type fileRecord struct {
	Item   *Item  `json:"item,omitempty"`
	Delete string `json:"delete,omitempty"`
}

type storedItem struct {
	Item
	order int
}

func NewFileStore(dir string) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("memory: directory is required")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "memory.jsonl")
	return &FileStore{path: path, lockPath: path + ".lock"}, nil
}

func (store *FileStore) Append(ctx context.Context, item Item) (string, error) {
	now := time.Now().UTC()
	id, err := uuidv7.Generate(now)
	if err != nil {
		return "", err
	}
	item.ID = id
	if item.Time.IsZero() {
		item.Time = now
	}
	if err := store.append(ctx, fileRecord{Item: &item}); err != nil {
		return "", err
	}
	return id, nil
}

func (store *FileStore) Get(ctx context.Context, id string) (Item, error) {
	items, err := store.read(ctx)
	if err != nil {
		return Item{}, err
	}
	item, ok := items[id]
	if !ok {
		return Item{}, fmt.Errorf("memory: item %q not found", id)
	}
	return item.Item, nil
}

func (store *FileStore) Query(ctx context.Context, filter Filter) ([]Item, error) {
	items, err := store.read(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]storedItem, 0, len(items))
	contains := strings.ToLower(filter.Contains)
	for _, item := range items {
		if !filter.Since.IsZero() && item.Time.Before(filter.Since) ||
			!filter.Until.IsZero() && item.Time.After(filter.Until) ||
			contains != "" && !strings.Contains(strings.ToLower(item.Content), contains) ||
			!hasAllTags(item.Tags, filter.Tags) {
			continue
		}
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Time.Equal(result[right].Time) {
			return result[left].order > result[right].order
		}
		return result[left].Time.After(result[right].Time)
	})
	limit := filter.Limit
	if limit <= 0 || limit > queryLimit {
		limit = queryLimit
	}
	if len(result) > limit {
		result = result[:limit]
	}
	out := make([]Item, len(result))
	for index := range result {
		out[index] = result[index].Item
	}
	return out, nil
}

func (store *FileStore) Delete(ctx context.Context, id string) error {
	return store.append(ctx, fileRecord{Delete: id})
}

func (store *FileStore) append(ctx context.Context, record fileRecord) error {
	lock := flock.New(store.lockPath)
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	defer func() { _ = lock.Unlock() }()

	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(store.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(line, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (store *FileStore) read(ctx context.Context) (map[string]storedItem, error) {
	lock := flock.New(store.lockPath)
	locked, err := lock.TryRLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ctx.Err()
	}
	defer func() { _ = lock.Unlock() }()

	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]storedItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	// ponytail: linear scan + tombstones; index/compaction when >10k items.
	items := make(map[string]storedItem)
	reader := bufio.NewReader(file)
	order := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		var record fileRecord
		if len(line) > 0 && json.Unmarshal(line, &record) == nil {
			switch {
			case record.Delete != "":
				delete(items, record.Delete)
			case record.Item != nil && record.Item.ID != "":
				items[record.Item.ID] = storedItem{Item: *record.Item, order: order}
			}
		}
		order++
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return items, nil
}

func hasAllTags(itemTags, required []string) bool {
	if len(required) == 0 {
		return true
	}
	tags := make(map[string]struct{}, len(itemTags))
	for _, tag := range itemTags {
		tags[tag] = struct{}{}
	}
	for _, tag := range required {
		if _, ok := tags[tag]; !ok {
			return false
		}
	}
	return true
}
