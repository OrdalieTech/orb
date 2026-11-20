package filestore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/internal/uuidv7"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/gofrs/flock"
)

const queryLimit = 100

type FileStore struct {
	path     string
	lockPath string
}

var _ memory.TransactionalStore = (*FileStore)(nil)
var _ memory.RemovalStore = (*FileStore)(nil)

type fileTransaction struct {
	items   map[string]storedItem
	removed map[string]string
	records []fileRecord
	order   int
}

type fileRecord struct {
	Item   *memory.Item `json:"item,omitempty"`
	Delete string       `json:"delete,omitempty"`
	Purge  string       `json:"purge,omitempty"`
	Batch  []fileRecord `json:"batch,omitempty"`
	// Digest is a compacted tombstone's digest: the deleted item itself is gone from the journal.
	Digest string `json:"digest,omitempty"`
}

type storedItem struct {
	memory.Item
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

func (store *FileStore) Append(ctx context.Context, item memory.Item) (string, error) {
	return appendItem(ctx, item, store.append)
}

func appendItem(ctx context.Context, item memory.Item, appendRecord func(context.Context, fileRecord) error) (string, error) {
	item, id, err := prepareItem(item)
	if err != nil {
		return "", err
	}
	if err := appendRecord(ctx, fileRecord{Item: &item}); err != nil {
		return "", err
	}
	return id, nil
}

func prepareItem(item memory.Item) (memory.Item, string, error) {
	now := time.Now().UTC()
	if item.ID == "" {
		id, err := uuidv7.Generate(now)
		if err != nil {
			return memory.Item{}, "", err
		}
		item.ID = id
	}
	if item.Time.IsZero() {
		item.Time = now
	}
	return item, item.ID, nil
}

func (store *FileStore) Get(ctx context.Context, id string) (memory.Item, error) {
	items, err := store.read(ctx)
	if err != nil {
		return memory.Item{}, err
	}
	return getItem(items, id)
}

func getItem(items map[string]storedItem, id string) (memory.Item, error) {
	item, ok := items[id]
	if !ok {
		return memory.Item{}, fmt.Errorf("memory: item %q not found", id)
	}
	return item.Item, nil
}

func (store *FileStore) Query(ctx context.Context, filter memory.Filter) ([]memory.Item, error) {
	items, err := store.read(ctx)
	if err != nil {
		return nil, err
	}
	return queryItems(items, filter), nil
}

func queryItems(items map[string]storedItem, filter memory.Filter) []memory.Item {
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
	if limit == 0 || limit > queryLimit {
		limit = queryLimit
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	out := make([]memory.Item, len(result))
	for index := range result {
		out[index] = result[index].Item
	}
	return out
}

func (store *FileStore) Delete(ctx context.Context, id string) error {
	return store.append(ctx, fileRecord{Delete: id})
}

func (store *FileStore) Purge(ctx context.Context, id string) error {
	return store.Transact(ctx, func(tx memory.Store) error {
		return tx.(memory.RemovalStore).Purge(ctx, id)
	})
}

func (store *FileStore) Removed(ctx context.Context) (removed map[string]string, err error) {
	err = store.locked(ctx, true, func() error {
		_, removed, err = store.readStateLocked(ctx)
		return err
	})
	return removed, err
}

func (store *FileStore) Transact(ctx context.Context, fn func(memory.Store) error) error {
	if fn == nil {
		return errors.New("memory: transaction callback is required")
	}
	return store.locked(ctx, false, func() error {
		items, removed, err := store.readStateLocked(ctx)
		if err != nil {
			return err
		}
		order := 0
		for _, item := range items {
			order = max(order, item.order+1)
		}
		transaction := &fileTransaction{items: items, removed: removed, order: order}
		if err := fn(transaction); err != nil {
			return err
		}
		return store.appendRecordsLocked(ctx, transaction.records)
	})
}

// locked runs fn under the store's flock, shared or exclusive, polling until ctx ends.
func (store *FileStore) locked(ctx context.Context, shared bool, fn func() error) error {
	lock := flock.New(store.lockPath)
	try := lock.TryLockContext
	if shared {
		try = lock.TryRLockContext
	}
	locked, err := try(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

func (transaction *fileTransaction) Append(ctx context.Context, item memory.Item) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	item, id, err := prepareItem(item)
	if err != nil {
		return "", err
	}
	transaction.records = append(transaction.records, fileRecord{Item: &item})
	transaction.items[id] = storedItem{Item: item, order: transaction.order}
	delete(transaction.removed, id)
	transaction.order++
	return id, nil
}

func (transaction *fileTransaction) Get(ctx context.Context, id string) (memory.Item, error) {
	if err := ctx.Err(); err != nil {
		return memory.Item{}, err
	}
	return getItem(transaction.items, id)
}

func (transaction *fileTransaction) Query(ctx context.Context, filter memory.Filter) ([]memory.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return queryItems(transaction.items, filter), nil
}

func (transaction *fileTransaction) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if old, ok := transaction.items[id]; ok {
		raw, err := json.Marshal(old.Item)
		if err != nil {
			return err
		}
		if transaction.removed == nil {
			transaction.removed = map[string]string{}
		}
		transaction.removed[id] = jsonwire.Digest(raw)
	}
	transaction.records = append(transaction.records, fileRecord{Delete: id})
	delete(transaction.items, id)
	return nil
}

func (transaction *fileTransaction) Purge(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, hasItem := transaction.items[id]
	_, hasRemoved := transaction.removed[id]
	if !hasItem && !hasRemoved {
		return nil
	}
	transaction.records = append(transaction.records, fileRecord{Purge: id})
	delete(transaction.items, id)
	delete(transaction.removed, id)
	return nil
}

func (transaction *fileTransaction) Removed(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(transaction.removed))
	for k, v := range transaction.removed {
		out[k] = v
	}
	return out, nil
}

func (store *FileStore) append(ctx context.Context, record fileRecord) error {
	return store.locked(ctx, false, func() error { return store.appendRecordsLocked(ctx, []fileRecord{record}) })
}

func (store *FileStore) appendRecordsLocked(ctx context.Context, records []fileRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	line, err := journalLine(records)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(store.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(line); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if info, statErr := os.Stat(store.path); err != nil || statErr != nil || info.Size() < compactAt {
		return err
	}
	raw, err := os.ReadFile(store.path)
	if err != nil {
		return err
	}
	live, err := compacted(raw)
	if live == nil || err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(store.path), ".memory-*")
	if err != nil {
		return err
	}
	if _, err = temp.Write(live); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), store.path)
	}
	if err != nil {
		_ = os.Remove(temp.Name())
	}
	return err
}

// compactAt is the journal size from which a write rewrites it to its live items, once it is
// several times what they take: every remember, replace and forget appends a line, while what is
// live stays bounded by the memory tools.
const compactAt = 256 << 10

// compacted is the journal holding only its live items, in their order, then its tombstones as
// digests; nil while it is still mostly live.
func compacted(raw []byte) ([]byte, error) {
	items, removed, err := readJournalState(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	live := make([]storedItem, 0, len(items))
	for _, item := range items {
		live = append(live, item)
	}
	sort.Slice(live, func(i, j int) bool { return live[i].order < live[j].order })
	out := []byte{}
	for i := range live {
		line, err := journalLine([]fileRecord{{Item: &live[i].Item}})
		if err != nil {
			return nil, err
		}
		out = append(out, line...)
	}
	for _, id := range slices.Sorted(maps.Keys(removed)) {
		line, err := journalLine([]fileRecord{{Delete: id, Digest: removed[id]}})
		if err != nil {
			return nil, err
		}
		out = append(out, line...)
	}
	if len(out)*4 > len(raw) {
		return nil, nil
	}
	return out, nil
}

// journalLine is records as one journal line, a batch when there are several.
func journalLine(records []fileRecord) ([]byte, error) {
	record := records[0]
	if len(records) > 1 {
		record = fileRecord{Batch: records}
	}
	line, err := json.Marshal(record)
	return append(line, '\n'), err
}

func (store *FileStore) read(ctx context.Context) (items map[string]storedItem, err error) {
	err = store.locked(ctx, true, func() error {
		items, err = store.readLocked(ctx)
		return err
	})
	return items, err
}

func (store *FileStore) readLocked(ctx context.Context) (map[string]storedItem, error) {
	items, _, err := store.readStateLocked(ctx)
	return items, err
}

func (store *FileStore) readStateLocked(ctx context.Context) (map[string]storedItem, map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]storedItem{}, map[string]string{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	return readJournalState(file)
}

// readJournal replays a journal into its items.
func readJournal(file io.Reader) (map[string]storedItem, error) {
	items, _, err := readJournalState(file)
	return items, err
}

// readJournalState replays a journal into its live items and locally deleted digest tombstones.
func readJournalState(file io.Reader) (map[string]storedItem, map[string]string, error) {
	// ponytail: linear scan + tombstones; index/compaction when >10k items.
	items := make(map[string]storedItem)
	removed := make(map[string]string)
	reader := bufio.NewReader(file)
	order := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		var record fileRecord
		if len(line) > 0 && json.Unmarshal(line, &record) == nil {
			applyFileRecord(items, removed, record, &order)
		} else {
			order++
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, nil, readErr
		}
	}
	return items, removed, nil
}

func applyFileRecord(items map[string]storedItem, removed map[string]string, record fileRecord, order *int) {
	records := record.Batch
	if len(records) == 0 {
		records = []fileRecord{record}
	}
	for _, current := range records {
		switch {
		case current.Purge != "":
			delete(items, current.Purge)
			delete(removed, current.Purge)
		case current.Delete != "" && current.Digest != "":
			if removed != nil {
				removed[current.Delete] = current.Digest
			}
		case current.Delete != "":
			if old, ok := items[current.Delete]; ok && removed != nil {
				if raw, err := json.Marshal(old.Item); err == nil {
					removed[current.Delete] = jsonwire.Digest(raw)
				}
			}
			delete(items, current.Delete)
		case current.Item != nil && current.Item.ID != "":
			items[current.Item.ID] = storedItem{Item: *current.Item, order: *order}
			delete(removed, current.Item.ID)
		}
		*order = *order + 1
	}
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

// ParseJournal validates an offline migration source without skipping damaged records.
func ParseJournal(content []byte) ([]memory.Item, error) {
	items := map[string]storedItem{}
	order := 0
	for index, line := range bytes.Split(content, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record fileRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("invalid memory record at line %d", index+1)
		}
		records := record.Batch
		if len(records) == 0 {
			records = []fileRecord{record}
		} else if record.Item != nil || record.Delete != "" || record.Purge != "" {
			return nil, fmt.Errorf("invalid memory batch at line %d", index+1)
		}
		for _, current := range records {
			count := 0
			if current.Item != nil {
				count++
			}
			if current.Delete != "" {
				count++
			}
			if current.Purge != "" {
				count++
			}
			if len(current.Batch) != 0 || count != 1 || current.Item != nil && current.Item.ID == "" {
				return nil, fmt.Errorf("invalid memory record at line %d", index+1)
			}
		}
		applyFileRecord(items, nil, record, &order)
	}
	ordered := make([]storedItem, 0, len(items))
	for _, item := range items {
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].order < ordered[j].order })
	result := make([]memory.Item, 0, len(items))
	for _, item := range ordered {
		result = append(result, item.Item)
	}
	return result, nil
}

// Journal is the same journal kept in one document, for hosts whose storage is documents (a
// Worker object, a browser); the document's update is the transaction.
type Journal struct{ Document document.Document }

var _ memory.TransactionalStore = Journal{}
var _ memory.RemovalStore = Journal{}

func (j Journal) items(ctx context.Context) (map[string]storedItem, error) {
	raw, err := j.Document.Read(ctx)
	if err != nil {
		return nil, err
	}
	return readJournal(bytes.NewReader(raw))
}

func (j Journal) Removed(ctx context.Context) (map[string]string, error) {
	raw, err := j.Document.Read(ctx)
	if err != nil {
		return nil, err
	}
	_, removed, err := readJournalState(bytes.NewReader(raw))
	return removed, err
}

func (j Journal) Purge(ctx context.Context, id string) error {
	return j.Transact(ctx, func(store memory.Store) error {
		return store.(memory.RemovalStore).Purge(ctx, id)
	})
}

func (j Journal) Append(ctx context.Context, item memory.Item) (id string, err error) {
	err = j.Transact(ctx, func(store memory.Store) error {
		id, err = store.Append(ctx, item)
		return err
	})
	return id, err
}

func (j Journal) Get(ctx context.Context, id string) (memory.Item, error) {
	items, err := j.items(ctx)
	if err != nil {
		return memory.Item{}, err
	}
	return getItem(items, id)
}

func (j Journal) Query(ctx context.Context, filter memory.Filter) ([]memory.Item, error) {
	items, err := j.items(ctx)
	if err != nil {
		return nil, err
	}
	return queryItems(items, filter), nil
}

func (j Journal) Delete(ctx context.Context, id string) error {
	return j.Transact(ctx, func(store memory.Store) error { return store.Delete(ctx, id) })
}

func (j Journal) Transact(ctx context.Context, fn func(memory.Store) error) error {
	if fn == nil {
		return errors.New("memory: transaction callback is required")
	}
	return j.Document.Update(ctx, func(raw []byte) ([]byte, error) {
		items, removed, err := readJournalState(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		transaction := &fileTransaction{items: items, removed: removed, order: len(items)}
		for _, item := range items {
			transaction.order = max(transaction.order, item.order+1)
		}
		if err = fn(transaction); err != nil || len(transaction.records) == 0 {
			return raw, err
		}
		line, err := journalLine(transaction.records)
		if raw = append(raw, line...); err != nil || len(raw) < compactAt {
			return raw, err
		}
		live, err := compacted(raw)
		if live == nil || err != nil {
			return raw, err
		}
		return live, nil
	})
}
