// Package envtest is the shared conformance suite for harness.FileSystem
// port implementations (DECISIONS.md P10). Expectations are those of
// harness.NodeExecutionEnv, the reference port of upstream NodeExecutionEnv.
package envtest

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/engine/harness"
)

// Symlinker is the optional capability of backends that can create symbolic
// links; the suite runs its symlink checks only against such backends.
type Symlinker interface {
	Symlink(ctx context.Context, target, link string) error
}

// TestFileSystem runs the conformance suite. newFS must return a fresh file
// system whose working directory is an existing, empty, writable directory.
func TestFileSystem(t *testing.T, newFS func(t *testing.T) harness.FileSystem) {
	for _, check := range []struct {
		name string
		run  func(*testing.T, harness.FileSystem)
	}{
		{"Paths", testPaths},
		{"WriteRead", testWriteRead},
		{"TextDecoding", testTextDecoding},
		{"ReadTextLines", testReadTextLines},
		{"Append", testAppend},
		{"Rename", testRename},
		{"FileInfo", testFileInfo},
		{"ListDir", testListDir},
		{"Exists", testExists},
		{"CreateDir", testCreateDir},
		{"Remove", testRemove},
		{"Temp", testTemp},
		{"CanonicalPath", testCanonicalPath},
		{"KindErrors", testKindErrors},
		{"Cancellation", testCancellation},
		{"Concurrency", testConcurrency},
		{"Symlinks", testSymlinks},
	} {
		t.Run(check.name, func(t *testing.T) { check.run(t, newFS(t)) })
	}
}

func requireCode(t *testing.T, err error, code harness.FileErrorCode) {
	t.Helper()
	requireCodeAt(t, err, code, "")
}

// requireCodeAt also checks FileError.Path unless path is empty.
func requireCodeAt(t *testing.T, err error, code harness.FileErrorCode, path string) {
	t.Helper()
	var typed *harness.FileError
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %#v (%v), want *harness.FileError with code %s", err, err, code)
	}
	if path != "" && typed.Path != path {
		t.Fatalf("%s error path = %q, want %q", code, typed.Path, path)
	}
}

func must[T any](value T, err error) func(*testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func abs(t *testing.T, fsys harness.FileSystem, name string) string {
	t.Helper()
	return must(fsys.AbsolutePath(t.Context(), name))(t)
}

func join(t *testing.T, fsys harness.FileSystem, parts ...string) string {
	t.Helper()
	return must(fsys.JoinPath(t.Context(), parts...))(t)
}

func write(t *testing.T, fsys harness.FileSystem, name, content string) {
	t.Helper()
	noErr(t, fsys.WriteFile(t.Context(), name, []byte(content)))
}

func readText(t *testing.T, fsys harness.FileSystem, name string) string {
	t.Helper()
	return must(fsys.ReadTextFile(t.Context(), name))(t)
}

func exists(t *testing.T, fsys harness.FileSystem, name string) bool {
	t.Helper()
	return must(fsys.Exists(t.Context(), name))(t)
}

func kind(t *testing.T, fsys harness.FileSystem, name string) harness.FileKind {
	t.Helper()
	return must(fsys.FileInfo(t.Context(), name))(t).Kind
}

func testPaths(t *testing.T, fsys harness.FileSystem) {
	ctx := t.Context()
	wd := fsys.WorkingDirectory()
	if got := abs(t, fsys, "."); got != wd {
		t.Fatalf("AbsolutePath(.) = %q, want working directory %q", got, wd)
	}
	want := join(t, fsys, wd, "file.txt")
	if got := abs(t, fsys, "missing/../file.txt"); got != want {
		t.Fatalf("AbsolutePath(missing/../file.txt) = %q, want %q", got, want)
	}
	if got := abs(t, fsys, want); got != want {
		t.Fatalf("AbsolutePath is not idempotent: %q -> %q", want, got)
	}
	if got := join(t, fsys, wd, "nested", "..", "file.txt"); got != want {
		t.Fatalf("JoinPath(wd, nested, .., file.txt) = %q, want %q", got, want)
	}
	if got := abs(t, fsys, join(t, fsys, "a", "b")); got != join(t, fsys, wd, "a", "b") {
		t.Fatalf("relative JoinPath resolved to %q", got)
	}
	if exists(t, fsys, "missing") {
		t.Fatal("path resolution created the addressed path")
	}
	if _, err := fsys.AbsolutePath(ctx, "does/not/exist"); err != nil {
		t.Fatalf("AbsolutePath of a missing path failed: %v", err)
	}
}

func testWriteRead(t *testing.T, fsys harness.FileSystem) {
	const content = "one\r\ntwo\nthree\n"
	write(t, fsys, "nested/deep/file.txt", content)
	if got := kind(t, fsys, "nested/deep"); got != harness.FileKindDirectory {
		t.Fatalf("parent kind = %q, want directory", got)
	}
	if got := readText(t, fsys, "nested/deep/file.txt"); got != content {
		t.Fatalf("ReadTextFile = %q", got)
	}
	if got := must(fsys.ReadBinaryFile(t.Context(), abs(t, fsys, "nested/deep/file.txt")))(t); string(got) != content {
		t.Fatalf("ReadBinaryFile = %q", got)
	}
	write(t, fsys, "nested/deep/file.txt", "short")
	if got := readText(t, fsys, "nested/deep/file.txt"); got != "short" {
		t.Fatalf("overwrite did not truncate: %q", got)
	}
	binary := []byte{0, 1, 0xff, 0xfe, 'a', 0}
	noErr(t, fsys.WriteFile(t.Context(), "binary.bin", binary))
	if got := must(fsys.ReadBinaryFile(t.Context(), "binary.bin"))(t); !slices.Equal(got, binary) {
		t.Fatalf("ReadBinaryFile = %v, want %v", got, binary)
	}
	noErr(t, fsys.WriteFile(t.Context(), "empty.txt", nil))
	if got := must(fsys.ReadBinaryFile(t.Context(), "empty.txt"))(t); len(got) != 0 {
		t.Fatalf("empty file read %q", got)
	}
	_, err := fsys.ReadTextFile(t.Context(), "missing.txt")
	requireCodeAt(t, err, harness.FileErrorNotFound, abs(t, fsys, "missing.txt"))
	_, err = fsys.ReadBinaryFile(t.Context(), "missing.bin")
	requireCode(t, err, harness.FileErrorNotFound)
}

func testTextDecoding(t *testing.T, fsys harness.FileSystem) {
	noErr(t, fsys.WriteFile(t.Context(), "invalid.txt", []byte("a\xffb\xe2\x82")))
	if got := readText(t, fsys, "invalid.txt"); got != "a\uFFFDb\uFFFD" {
		t.Fatalf("invalid UTF-8 decoded as %q", got)
	}
	noErr(t, fsys.WriteFile(t.Context(), "bom.txt", []byte("\uFEFFhé")))
	if got := readText(t, fsys, "bom.txt"); got != "\uFEFFhé" {
		t.Fatalf("BOM text decoded as %q", got)
	}
	lines := must(fsys.ReadTextLines(t.Context(), "invalid.txt", 5))(t)
	if !slices.Equal(lines, []string{"a\uFFFDb\uFFFD"}) {
		t.Fatalf("invalid UTF-8 lines = %q", lines)
	}
}

func testReadTextLines(t *testing.T, fsys harness.FileSystem) {
	ctx := t.Context()
	write(t, fsys, "lines.txt", "one\r\ntwo\n\nthree\r\r\nfour")
	for _, tc := range []struct {
		max  int
		want []string
	}{
		{1, []string{"one"}},
		{2, []string{"one", "two"}},
		{10, []string{"one", "two", "", "three\r", "four"}},
		{0, []string{}},
		{-1, []string{}},
	} {
		got, err := fsys.ReadTextLines(ctx, "lines.txt", tc.max)
		if err != nil || got == nil || !slices.Equal(got, tc.want) {
			t.Fatalf("ReadTextLines(max=%d) = %#v, %v; want %#v", tc.max, got, err, tc.want)
		}
	}
	for content, want := range map[string][]string{"": {}, "\n": {""}, "a\n": {"a"}, "a\n\n": {"a", ""}, "\r\n": {""}, "a\rb": {"a\rb"}} {
		write(t, fsys, "shape.txt", content)
		if got := must(fsys.ReadTextLines(ctx, "shape.txt", 10))(t); got == nil || !slices.Equal(got, want) {
			t.Fatalf("ReadTextLines(%q) = %#v, want %#v", content, got, want)
		}
	}
	if got, err := fsys.ReadTextLines(ctx, "missing.txt", 0); err != nil || len(got) != 0 {
		t.Fatalf("ReadTextLines(missing, max=0) = %#v, %v; want no read", got, err)
	}
	_, err := fsys.ReadTextLines(ctx, "missing.txt", 1)
	requireCode(t, err, harness.FileErrorNotFound)
}

func testAppend(t *testing.T, fsys harness.FileSystem) {
	noErr(t, fsys.AppendFile(t.Context(), "log/app.txt", []byte("a")))
	noErr(t, fsys.AppendFile(t.Context(), "log/app.txt", []byte("b\n")))
	noErr(t, fsys.AppendFile(t.Context(), "log/app.txt", nil))
	if got := readText(t, fsys, "log/app.txt"); got != "ab\n" {
		t.Fatalf("appended content = %q", got)
	}
}

func testRename(t *testing.T, fsys harness.FileSystem) {
	write(t, fsys, "from.txt", "from")
	write(t, fsys, "to.txt", "old destination")
	noErr(t, fsys.RenameFile(t.Context(), "from.txt", "to.txt"))
	if got := readText(t, fsys, "to.txt"); got != "from" {
		t.Fatalf("destination = %q", got)
	}
	if exists(t, fsys, "from.txt") {
		t.Fatal("source still exists after rename")
	}
	noErr(t, fsys.CreateDir(t.Context(), "dir", true))
	noErr(t, fsys.RenameFile(t.Context(), "to.txt", "dir/moved.txt"))
	if got := readText(t, fsys, "dir/moved.txt"); got != "from" {
		t.Fatalf("moved = %q", got)
	}
	err := fsys.RenameFile(t.Context(), "missing.txt", "other.txt")
	requireCodeAt(t, err, harness.FileErrorNotFound, abs(t, fsys, "missing.txt"))
	requireCode(t, fsys.RenameFile(t.Context(), "dir/moved.txt", "absent/moved.txt"), harness.FileErrorNotFound)
	if exists(t, fsys, "other.txt") {
		t.Fatal("failed rename created its destination")
	}
}

func testFileInfo(t *testing.T, fsys harness.FileSystem) {
	write(t, fsys, "info/file.txt", "12345")
	info := must(fsys.FileInfo(t.Context(), "info/file.txt"))(t)
	if info.Name != "file.txt" || info.Path != abs(t, fsys, "info/file.txt") || info.Kind != harness.FileKindFile || info.Size != 5 {
		t.Fatalf("file FileInfo = %#v", info)
	}
	dir := must(fsys.FileInfo(t.Context(), "info"))(t)
	if dir.Name != "info" || dir.Path != abs(t, fsys, "info") || dir.Kind != harness.FileKindDirectory {
		t.Fatalf("directory FileInfo = %#v", dir)
	}
	_, err := fsys.FileInfo(t.Context(), "info/missing")
	requireCodeAt(t, err, harness.FileErrorNotFound, abs(t, fsys, "info/missing"))
	_, err = fsys.FileInfo(t.Context(), "info/file.txt/child")
	requireCode(t, err, harness.FileErrorNotDirectory)
}

func testListDir(t *testing.T, fsys harness.FileSystem) {
	write(t, fsys, "list/a.txt", "a")
	write(t, fsys, "list/.hidden", "h")
	write(t, fsys, "list/sub/nested.txt", "n")
	entries := must(fsys.ListDir(t.Context(), "list"))(t)
	got := map[string]harness.FileInfo{}
	for _, entry := range entries {
		got[entry.Name] = entry
	}
	if len(entries) != 3 || len(got) != 3 {
		t.Fatalf("ListDir = %#v, want a.txt, .hidden and sub", entries)
	}
	for name, kind := range map[string]harness.FileKind{"a.txt": harness.FileKindFile, ".hidden": harness.FileKindFile, "sub": harness.FileKindDirectory} {
		entry, ok := got[name]
		if !ok || entry.Kind != kind || entry.Path != join(t, fsys, abs(t, fsys, "list"), name) {
			t.Fatalf("ListDir entry %q = %#v", name, entry)
		}
	}
	if got["a.txt"].Size != 1 {
		t.Fatalf("ListDir size = %d", got["a.txt"].Size)
	}
	noErr(t, fsys.CreateDir(t.Context(), "empty", true))
	if entries := must(fsys.ListDir(t.Context(), "empty"))(t); len(entries) != 0 {
		t.Fatalf("empty ListDir = %#v", entries)
	}
	_, err := fsys.ListDir(t.Context(), "missing")
	requireCode(t, err, harness.FileErrorNotFound)
	_, err = fsys.ListDir(t.Context(), "list/a.txt")
	requireCode(t, err, harness.FileErrorNotDirectory)
}

func testExists(t *testing.T, fsys harness.FileSystem) {
	write(t, fsys, "exists/file.txt", "x")
	if !exists(t, fsys, "exists/file.txt") || !exists(t, fsys, "exists") || !exists(t, fsys, ".") {
		t.Fatal("existing paths reported missing")
	}
	if exists(t, fsys, "exists/missing") || exists(t, fsys, "missing/deeper") {
		t.Fatal("missing path reported present")
	}
	_, err := fsys.Exists(t.Context(), "exists/file.txt/child")
	requireCode(t, err, harness.FileErrorNotDirectory)
}

func testCreateDir(t *testing.T, fsys harness.FileSystem) {
	ctx := t.Context()
	noErr(t, fsys.CreateDir(ctx, "a/b/c", true))
	if kind(t, fsys, "a/b/c") != harness.FileKindDirectory {
		t.Fatal("recursive CreateDir did not create a directory")
	}
	noErr(t, fsys.CreateDir(ctx, "a/b/c", true))
	noErr(t, fsys.CreateDir(ctx, "a/new", false))
	if kind(t, fsys, "a/new") != harness.FileKindDirectory {
		t.Fatal("CreateDir did not create a directory")
	}
	requireCode(t, fsys.CreateDir(ctx, "x/y", false), harness.FileErrorNotFound)
	if exists(t, fsys, "x") {
		t.Fatal("non-recursive CreateDir created a parent")
	}
	requireCode(t, fsys.CreateDir(ctx, "a", false), harness.FileErrorUnknown)
	write(t, fsys, "a/file.txt", "x")
	requireCode(t, fsys.CreateDir(ctx, "a/file.txt/child/deeper", true), harness.FileErrorNotDirectory)
	t.Run("NonRecursiveBeneathFile", func(t *testing.T) {
		if runtime.GOOS == "wasip1" {
			t.Skip("wazero's path_create_directory reports ENOENT instead of ENOTDIR beneath a regular file")
		}
		requireCode(t, fsys.CreateDir(ctx, "a/file.txt/child", false), harness.FileErrorNotDirectory)
	})
}

func testRemove(t *testing.T, fsys harness.FileSystem) {
	ctx := t.Context()
	write(t, fsys, "rm/file.txt", "x")
	noErr(t, fsys.Remove(ctx, "rm/file.txt", false, false))
	if exists(t, fsys, "rm/file.txt") {
		t.Fatal("removed file still exists")
	}
	write(t, fsys, "rm/tree/child/file.txt", "x")
	requireCode(t, fsys.Remove(ctx, "rm/tree", false, false), harness.FileErrorUnknown)
	requireCode(t, fsys.Remove(ctx, "rm/tree", false, true), harness.FileErrorUnknown)
	if !exists(t, fsys, "rm/tree/child/file.txt") {
		t.Fatal("failed non-recursive Remove deleted content")
	}
	noErr(t, fsys.CreateDir(ctx, "rm/empty", true))
	requireCode(t, fsys.Remove(ctx, "rm/empty", false, false), harness.FileErrorUnknown)
	noErr(t, fsys.Remove(ctx, "rm/tree", true, false))
	if exists(t, fsys, "rm/tree") {
		t.Fatal("recursive Remove left the directory")
	}
	noErr(t, fsys.Remove(ctx, "rm/empty", true, false))
	if !exists(t, fsys, "rm") {
		t.Fatal("Remove deleted the parent")
	}
	requireCode(t, fsys.Remove(ctx, "rm/missing", false, false), harness.FileErrorNotFound)
	requireCode(t, fsys.Remove(ctx, "rm/missing", true, false), harness.FileErrorNotFound)
	noErr(t, fsys.Remove(ctx, "rm/missing", false, true))
	noErr(t, fsys.Remove(ctx, "rm/missing", true, true))
	noErr(t, fsys.Remove(ctx, "missing/deeper", true, true))
}

func testTemp(t *testing.T, fsys harness.FileSystem) {
	ctx := t.Context()
	var created []string
	t.Cleanup(func() {
		for _, name := range created {
			_ = fsys.Remove(context.Background(), name, true, true)
		}
	})
	temp := func(name string, err error) string {
		t.Helper()
		created = append(created, must(name, err)(t))
		if abs(t, fsys, name) != name {
			t.Fatalf("temp path %q is not absolute", name)
		}
		return name
	}
	first := temp(fsys.CreateTempDir(ctx, "envtest-"))
	second := temp(fsys.CreateTempDir(ctx, "envtest-"))
	if first == second {
		t.Fatalf("CreateTempDir returned %q twice", first)
	}
	for _, dir := range []string{first, second} {
		info := must(fsys.FileInfo(ctx, dir))(t)
		if info.Kind != harness.FileKindDirectory || !strings.HasPrefix(info.Name, "envtest-") {
			t.Fatalf("temp dir info = %#v", info)
		}
		if entries := must(fsys.ListDir(ctx, dir))(t); len(entries) != 0 {
			t.Fatalf("temp dir is not empty: %#v", entries)
		}
	}
	if info := must(fsys.FileInfo(ctx, temp(fsys.CreateTempDir(ctx, ""))))(t); !strings.HasPrefix(info.Name, "tmp-") {
		t.Fatalf("default temp dir prefix: %#v", info)
	}
	file := temp(fsys.CreateTempFile(ctx, "pre-", ".txt"))
	other := temp(fsys.CreateTempFile(ctx, "pre-", ".txt"))
	if file == other {
		t.Fatalf("CreateTempFile returned %q twice", file)
	}
	info := must(fsys.FileInfo(ctx, file))(t)
	if info.Kind != harness.FileKindFile || info.Size != 0 || !strings.HasPrefix(info.Name, "pre-") || !strings.HasSuffix(info.Name, ".txt") {
		t.Fatalf("temp file info = %#v", info)
	}
	write(t, fsys, file, "usable")
	if got := readText(t, fsys, file); got != "usable" {
		t.Fatalf("temp file content = %q", got)
	}
}

func testCanonicalPath(t *testing.T, fsys harness.FileSystem) {
	ctx := t.Context()
	write(t, fsys, "canon/file.txt", "x")
	root := must(fsys.CanonicalPath(ctx, "."))(t)
	canonical := must(fsys.CanonicalPath(ctx, "canon/../canon/file.txt"))(t)
	if want := join(t, fsys, root, "canon", "file.txt"); canonical != want {
		t.Fatalf("CanonicalPath = %q, want %q", canonical, want)
	}
	if again := must(fsys.CanonicalPath(ctx, canonical))(t); again != canonical {
		t.Fatalf("CanonicalPath is not idempotent: %q -> %q", canonical, again)
	}
	_, err := fsys.CanonicalPath(ctx, "canon/missing")
	requireCode(t, err, harness.FileErrorNotFound)
}

func testKindErrors(t *testing.T, fsys harness.FileSystem) {
	ctx := t.Context()
	write(t, fsys, "kind/file.txt", "x")
	_, err := fsys.ReadTextFile(ctx, "kind")
	requireCode(t, err, harness.FileErrorIsDirectory)
	_, err = fsys.ReadBinaryFile(ctx, "kind")
	requireCode(t, err, harness.FileErrorIsDirectory)
	_, err = fsys.ReadTextLines(ctx, "kind", 1)
	requireCode(t, err, harness.FileErrorIsDirectory)
	requireCode(t, fsys.WriteFile(ctx, "kind", []byte("x")), harness.FileErrorIsDirectory)
	requireCode(t, fsys.AppendFile(ctx, "kind", []byte("x")), harness.FileErrorIsDirectory)
	requireCode(t, fsys.WriteFile(ctx, "kind/file.txt/child", []byte("x")), harness.FileErrorNotDirectory)
	requireCode(t, fsys.WriteFile(ctx, "kind/file.txt/deep/child", []byte("x")), harness.FileErrorNotDirectory)
	requireCode(t, fsys.AppendFile(ctx, "kind/file.txt/child", []byte("x")), harness.FileErrorNotDirectory)
	_, err = fsys.ReadTextFile(ctx, "kind/file.txt/child")
	requireCode(t, err, harness.FileErrorNotDirectory)
	if got := readText(t, fsys, "kind/file.txt"); got != "x" {
		t.Fatalf("failed writes changed the file: %q", got)
	}
}

func testCancellation(t *testing.T, fsys harness.FileSystem) {
	write(t, fsys, "cancel/file.txt", "x")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, operation := range map[string]func() error{
		"ReadTextFile":   func() error { _, err := fsys.ReadTextFile(ctx, "cancel/file.txt"); return err },
		"ReadTextLines":  func() error { _, err := fsys.ReadTextLines(ctx, "cancel/file.txt", 10); return err },
		"ReadBinaryFile": func() error { _, err := fsys.ReadBinaryFile(ctx, "cancel/file.txt"); return err },
		"WriteFile":      func() error { return fsys.WriteFile(ctx, "cancel/other.txt", []byte("no")) },
		"ListDir":        func() error { _, err := fsys.ListDir(ctx, "cancel"); return err },
	} {
		t.Run(name, func(t *testing.T) { requireCode(t, operation(), harness.FileErrorAborted) })
	}
	if exists(t, fsys, "cancel/other.txt") {
		t.Fatal("cancelled WriteFile wrote")
	}
}

func testConcurrency(t *testing.T, fsys harness.FileSystem) {
	const writers = 16
	var group sync.WaitGroup
	errs := make(chan error, writers)
	for index := range writers {
		group.Go(func() {
			name := fmt.Sprintf("concurrent/%02d.txt", index)
			if err := fsys.WriteFile(t.Context(), name, []byte(name)); err != nil {
				errs <- err
				return
			}
			if got, err := fsys.ReadTextFile(t.Context(), name); err != nil || got != name {
				errs <- fmt.Errorf("%s read %q, %v", name, got, err)
			}
		})
	}
	group.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if entries := must(fsys.ListDir(t.Context(), "concurrent"))(t); len(entries) != writers {
		t.Fatalf("ListDir after concurrent writes = %d entries", len(entries))
	}
}

func testSymlinks(t *testing.T, fsys harness.FileSystem) {
	linker, ok := fsys.(Symlinker)
	if !ok {
		t.Skip("backend has no symlink capability")
	}
	ctx := t.Context()
	write(t, fsys, "links/target.txt", "target")
	noErr(t, linker.Symlink(ctx, "target.txt", "links/link.txt"))
	noErr(t, linker.Symlink(ctx, "missing.txt", "links/dangling"))
	info := must(fsys.FileInfo(ctx, "links/link.txt"))(t)
	if info.Kind != harness.FileKindSymlink || info.Name != "link.txt" || info.Path != abs(t, fsys, "links/link.txt") {
		t.Fatalf("symlink FileInfo = %#v", info)
	}
	kinds := map[string]harness.FileKind{}
	for _, entry := range must(fsys.ListDir(ctx, "links"))(t) {
		kinds[entry.Name] = entry.Kind
	}
	if kinds["link.txt"] != harness.FileKindSymlink || kinds["dangling"] != harness.FileKindSymlink || kinds["target.txt"] != harness.FileKindFile {
		t.Fatalf("ListDir kinds = %#v", kinds)
	}
	if got := readText(t, fsys, "links/link.txt"); got != "target" {
		t.Fatalf("read through symlink = %q", got)
	}
	if link, target := must(fsys.CanonicalPath(ctx, "links/link.txt"))(t), must(fsys.CanonicalPath(ctx, "links/target.txt"))(t); link != target {
		t.Fatalf("CanonicalPath(link) = %q, want %q", link, target)
	}
	if !exists(t, fsys, "links/dangling") {
		t.Fatal("Exists followed a dangling symlink")
	}
	_, err := fsys.CanonicalPath(ctx, "links/dangling")
	requireCode(t, err, harness.FileErrorNotFound)
	noErr(t, fsys.Remove(ctx, "links/link.txt", false, false))
	if exists(t, fsys, "links/link.txt") || !exists(t, fsys, "links/target.txt") {
		t.Fatal("Remove of a symlink did not remove only the link")
	}
}
