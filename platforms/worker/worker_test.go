package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/engine/harness/envtest"
	"github.com/OrdalieTech/orb/platforms/memory"
)

// memKV is an in-process KV that copies values like Durable Object storage.
type memKV struct {
	mu   sync.Mutex
	data map[string][]byte
	fail error
}

func newMemKV() *memKV { return &memKV{data: map[string][]byte{}} }

func (kv *memKV) Get(_ context.Context, keys []string) (map[string][]byte, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	result := map[string][]byte{}
	for _, key := range keys {
		if value, ok := kv.data[key]; ok {
			result[key] = bytes.Clone(value)
		}
	}
	return result, nil
}

func (kv *memKV) List(_ context.Context, prefix string) (map[string][]byte, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	result := map[string][]byte{}
	for key, value := range kv.data {
		if strings.HasPrefix(key, prefix) {
			result[key] = bytes.Clone(value)
		}
	}
	return result, nil
}

func (kv *memKV) Write(_ context.Context, put map[string][]byte, del []string) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if kv.fail != nil {
		return kv.fail
	}
	for key, value := range put {
		kv.data[key] = bytes.Clone(value)
	}
	for _, key := range del {
		delete(kv.data, key)
	}
	return nil
}

func openFS(t *testing.T, kv KV) *FileSystem {
	t.Helper()
	fsys, err := OpenFileSystem(t.Context(), kv, filesNamespace, memory.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return fsys
}

func TestFileSystemConformance(t *testing.T) {
	envtest.TestFileSystem(t, func(t *testing.T) harness.FileSystem { return openFS(t, newMemKV()) })
}

func TestFileSystemSurvivesRestart(t *testing.T) { testSurvivesRestart(t, newMemKV()) }

func TestStoreDocuments(t *testing.T) { testDocuments(t, newMemKV()) }

func TestInstanceResumesAfterRestart(t *testing.T) { testInstanceResumes(t, newMemKV()) }

func TestWriteThroughFailureLatches(t *testing.T) {
	ctx := t.Context()
	kv := newMemKV()
	fsys := openFS(t, kv)
	kv.fail = errors.New("storage unavailable")
	if err := fsys.WriteFile(ctx, "/a.txt", []byte("a")); err == nil || !strings.Contains(err.Error(), "storage unavailable") {
		t.Fatalf("write error = %v", err)
	}
	kv.fail = nil
	if err := fsys.WriteFile(ctx, "/b.txt", []byte("b")); err == nil || !strings.Contains(err.Error(), "ahead of storage") {
		t.Fatalf("latched error = %v", err)
	}
	if text, err := fsys.ReadTextFile(ctx, "/a.txt"); err != nil || text != "a" {
		t.Fatalf("read after failure = %q, %v", text, err)
	}
	if snapshot := openFS(t, kv).Snapshot(); len(snapshot) != 0 {
		t.Fatalf("restart restored uncommitted files: %v", snapshot)
	}
}

// testSurvivesRestart mutates a tree through every write path, crossing
// chunk boundaries, then reopens it from the same storage and requires the
// same files, directories and times, and no stray keys.
func testSurvivesRestart(t *testing.T, kv KV) {
	ctx := t.Context()
	fsys := openFS(t, kv)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	pattern := func(size int) []byte {
		data := make([]byte, size)
		for index := range data {
			data[index] = byte(index % 251)
		}
		return data
	}
	must(fsys.WriteFile(ctx, "/workspace/a.txt", []byte("alpha")))
	must(fsys.CreateDir(ctx, "/workspace/empty/nested", true))
	must(fsys.WriteFile(ctx, "/workspace/big.bin", pattern(3*chunkSize+123)))
	journal := "/agent/sessions/--workspace--/journal.jsonl"
	must(fsys.WriteFile(ctx, journal, []byte("{\"type\":\"session\"}\n")))
	line := bytes.Repeat([]byte("x"), 149)
	for range 1000 {
		must(fsys.AppendFile(ctx, journal, append(slices.Clone(line), '\n')))
	}
	size := int64(len("{\"type\":\"session\"}\n") + 150*1000)
	must(fsys.AppendFile(ctx, journal, bytes.Repeat([]byte("y"), int(chunkSize-size%chunkSize))))
	must(fsys.AppendFile(ctx, journal, []byte("z")))
	must(fsys.AppendFile(ctx, "/workspace/created-by-append.txt", []byte("appended")))
	must(fsys.WriteFile(ctx, "/workspace/dir/x.txt", []byte("x")))
	must(fsys.CreateDir(ctx, "/workspace/dir/sub", false))
	must(fsys.RenameFile(ctx, "/workspace/dir", "/workspace/moved"))
	must(fsys.WriteFile(ctx, "/workspace/big.bin", pattern(chunkSize+1)))
	must(fsys.WriteFile(ctx, "/workspace/gone/y.txt", pattern(2*chunkSize)))
	must(fsys.Remove(ctx, "/workspace/gone", true, false))
	must(fsys.WriteFile(ctx, "/workspace/r1", []byte("1")))
	must(fsys.WriteFile(ctx, "/workspace/r2", pattern(2*chunkSize+5)))
	must(fsys.RenameFile(ctx, "/workspace/r1", "/workspace/r2"))
	temp, err := fsys.CreateTempFile(ctx, "orb-", ".txt")
	must(err)
	must(fsys.AppendFile(ctx, temp, []byte("temp")))
	_, err = fsys.CreateTempDir(ctx, "dir-")
	must(err)

	want := tree(t, fsys)
	reopened := openFS(t, kv)
	requireSameTree(t, want, tree(t, reopened))
	requireKeys(t, kv, want)

	// A reopened journal appends without a cached tail.
	must(reopened.AppendFile(ctx, journal, []byte("after restart\n")))
	want = tree(t, reopened)
	requireSameTree(t, want, tree(t, openFS(t, kv)))
	requireKeys(t, kv, want)

	must(reopened.Remove(ctx, "/workspace", true, false))
	must(reopened.Remove(ctx, "/agent", true, false))
	must(reopened.Remove(ctx, "/tmp", true, false))
	remaining, err := kv.List(ctx, filesNamespace)
	must(err)
	if len(remaining) != 0 {
		t.Fatalf("removing everything left keys: %v", slices.Sorted(maps.Keys(remaining)))
	}
}

type node struct {
	info harness.FileInfo
	data string
}

func tree(t *testing.T, fsys harness.FileSystem) map[string]node {
	t.Helper()
	nodes := map[string]node{}
	var walk func(string)
	walk = func(dir string) {
		infos, err := fsys.ListDir(t.Context(), dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, info := range infos {
			current := node{info: info}
			if info.Kind == harness.FileKindDirectory {
				walk(info.Path)
			} else {
				data, err := fsys.ReadBinaryFile(t.Context(), info.Path)
				if err != nil {
					t.Fatal(err)
				}
				current.data = string(data)
			}
			nodes[info.Path] = current
		}
	}
	walk("/")
	return nodes
}

func requireSameTree(t *testing.T, want, got map[string]node) {
	t.Helper()
	if !slices.Equal(slices.Sorted(maps.Keys(want)), slices.Sorted(maps.Keys(got))) {
		t.Fatalf("restored paths = %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	for name, expected := range want {
		actual := got[name]
		if actual.info.Kind != expected.info.Kind || actual.info.Size != expected.info.Size || actual.data != expected.data {
			t.Fatalf("%s restored as %s/%d bytes, want %s/%d bytes", name, actual.info.Kind, actual.info.Size, expected.info.Kind, expected.info.Size)
		}
		if math.Abs(actual.info.MTimeMS-expected.info.MTimeMS) > 0.01 {
			t.Fatalf("%s mtime = %f, want %f", name, actual.info.MTimeMS, expected.info.MTimeMS)
		}
	}
}

// requireKeys demands exactly one metadata key per entry and one key per
// chunk of each file.
func requireKeys(t *testing.T, kv KV, nodes map[string]node) {
	t.Helper()
	stored, err := kv.List(t.Context(), filesNamespace)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{}
	for name, current := range nodes {
		want = append(want, metaKey(filesNamespace, name))
		for index := range chunkCount(current.info.Size) {
			want = append(want, chunkKey(filesNamespace, name, index))
		}
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(stored)); !slices.Equal(got, want) {
		t.Fatalf("stored keys = %v\nwant %v", got, want)
	}
}

func testDocuments(t *testing.T, kv KV) {
	ctx := t.Context()
	open := func() *Store {
		documents, err := OpenFileSystem(ctx, kv, documentsNamespace, memory.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return NewStore(documents, map[string][]byte{"/agent/settings.json": []byte(`{"defaultProvider":"seed"}`)})
	}
	read := func(store *Store, name string) string {
		t.Helper()
		data, err := store.Document(name).Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	store := open()
	if got := read(store, "/agent/settings.json"); got != `{"defaultProvider":"seed"}` {
		t.Fatalf("default settings = %q", got)
	}
	if got, err := store.Document("/agent/auth.json").Read(ctx); got != nil || err != nil {
		t.Fatalf("missing document = %q, %v", got, err)
	}
	err := store.Document("/agent/settings.json").Update(ctx, func(current []byte) ([]byte, error) {
		return append(bytes.TrimSuffix(current, []byte("}")), []byte(`,"theme":"dark"}`)...), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	store = open()
	if got := read(store, "/agent/settings.json"); got != `{"defaultProvider":"seed","theme":"dark"}` {
		t.Fatalf("restarted settings = %q", got)
	}
	if err := store.Document("/agent/settings.json").Update(ctx, func([]byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if got := read(open(), "/agent/settings.json"); got != `{"defaultProvider":"seed"}` {
		t.Fatalf("deleted settings fell back to %q", got)
	}
}

// testInstanceResumes runs a tool-using turn, restarts the object from the
// same storage and requires the same journal, history and files, then
// replaces the session over RPC and requires the restart to follow it.
func testInstanceResumes(t *testing.T, kv KV) {
	ctx := t.Context()
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	open := func() *Instance {
		t.Helper()
		instance, err := Open(ctx, Options{KV: kv, Model: provider.GetModel(), StreamFn: provider.StreamSimple})
		if err != nil {
			t.Fatal(err)
		}
		return instance
	}
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("write", map[string]any{"path": "notes/hello.txt", "content": "persisted\n"}, faux.ToolCallOptions{ID: "call-write"})),
		faux.AssistantMessage(faux.ToolCall("read", map[string]any{"path": "notes/hello.txt"}, faux.ToolCallOptions{ID: "call-read"})),
		faux.AssistantMessage("done"),
	})
	first := open()
	if err := first.Session().Prompt(ctx, "Write and read a note."); err != nil {
		t.Fatal(err)
	}
	if tools := first.Session().GetActiveToolNames(); slices.Contains(tools, "bash") || !slices.Contains(tools, "write") {
		t.Fatalf("active tools = %v", tools)
	}
	journal := first.Session().Manager().GetSessionFile()
	history := roles(t, first)
	first.Dispose()

	second := open()
	if got := second.Session().Manager().GetSessionFile(); got != journal {
		t.Fatalf("resumed %s, want %s", got, journal)
	}
	if got := roles(t, second); !slices.Equal(got, history) {
		t.Fatalf("resumed history = %v, want %v", got, history)
	}
	if text, err := second.Files.ReadTextFile(ctx, path.Join(Workspace, "notes/hello.txt")); err != nil || text != "persisted\n" {
		t.Fatalf("restored note = %q, %v", text, err)
	}

	frames := serve(t, second, `{"id":"n","type":"new_session"}`, `{"id":"s","type":"get_state"}`, `{"id":"f","type":"fork","entryId":"x"}`)
	var state struct {
		Data struct {
			SessionFile  string `json:"sessionFile"`
			MessageCount int    `json:"messageCount"`
		} `json:"data"`
	}
	if !strings.Contains(frames[0], `"success":true`) || json.Unmarshal([]byte(frames[1]), &state) != nil ||
		state.Data.SessionFile == journal || state.Data.MessageCount != 0 || !strings.Contains(frames[2], `"success":false`) {
		t.Fatalf("new_session frames = %v", frames)
	}
	third := open()
	if got := third.Session().Manager().GetSessionFile(); got != state.Data.SessionFile {
		t.Fatalf("restart resumed %s, want the new session %s", got, state.Data.SessionFile)
	}

	switched, err := json.Marshal(map[string]string{"id": "w", "type": "switch_session", "sessionPath": journal})
	if err != nil {
		t.Fatal(err)
	}
	frames = serve(t, third, string(switched), `{"id":"s","type":"get_state"}`, `{"id":"x","type":"switch_session","sessionPath":"/workspace/outside.jsonl"}`)
	if !strings.Contains(frames[0], `"success":true`) || json.Unmarshal([]byte(frames[1]), &state) != nil ||
		state.Data.SessionFile != journal || state.Data.MessageCount != len(history) || !strings.Contains(frames[2], `"success":false`) {
		t.Fatalf("switch_session frames = %v", frames)
	}
	fourth := open()
	defer fourth.Dispose()
	if got := roles(t, fourth); fourth.Session().Manager().GetSessionFile() != journal || !slices.Equal(got, history) {
		t.Fatalf("restart after switching resumed %s with %v", fourth.Session().Manager().GetSessionFile(), got)
	}
}

func roles(t *testing.T, instance *Instance) []string {
	t.Helper()
	var result []string
	for _, entry := range instance.Session().Manager().GetEntries() {
		if entry.Type != "message" {
			continue
		}
		var message struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(entry.Message, &message); err != nil {
			t.Fatal(err)
		}
		result = append(result, message.Role)
	}
	if len(result) < 4 {
		t.Fatalf("history too short: %v", result)
	}
	return result
}

// serve sends commands one at a time and returns their response frames.
func serve(t *testing.T, instance *Instance, commands ...string) []string {
	t.Helper()
	commandReader, commandWriter := io.Pipe()
	frameReader, frameWriter := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- instance.Serve(context.Background(), commandReader, frameWriter, io.Discard)
		_ = frameWriter.Close()
	}()
	frames := make(chan string, 64)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(frameReader)
		for scanner.Scan() {
			frames <- scanner.Text()
		}
	}()
	var responses []string
	for _, command := range commands {
		if _, err := fmt.Fprintln(commandWriter, command); err != nil {
			t.Fatal(err)
		}
	wait:
		for {
			select {
			case frame := <-frames:
				if strings.Contains(frame, `"type":"response"`) {
					responses = append(responses, frame)
					break wait
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("no response to %s", command)
			}
		}
	}
	_ = commandWriter.Close()
	if code := <-done; code != 0 || len(responses) != len(commands) {
		t.Fatalf("Serve exit %d with responses %v", code, responses)
	}
	return responses
}
