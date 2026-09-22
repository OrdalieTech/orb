package memory

import (
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/engine/harness/envtest"
)

func TestFileSystemConformance(t *testing.T) {
	for _, root := range []string{"", "/workspace"} {
		t.Run("root="+root, func(t *testing.T) {
			envtest.TestFileSystem(t, func(*testing.T) harness.FileSystem { return New(Options{Root: root}) })
		})
	}
}

func requireCode(t *testing.T, err error, code harness.FileErrorCode) {
	t.Helper()
	var typed *harness.FileError
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %v, want FileError(%s)", err, code)
	}
}

func TestConfinementAndInvalidPaths(t *testing.T) {
	fsys := New(Options{Root: "/workspace"})
	ctx := t.Context()
	for _, name := range []string{"/etc/passwd", "/workspace/../../escape", "/workspace-other/file", "../escape", "/"} {
		err := fsys.WriteFile(ctx, name, []byte("bad"))
		requireCode(t, err, harness.FileErrorPermissionDenied)
		if !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("%q: %v does not wrap fs.ErrPermission", name, err)
		}
	}
	requireCode(t, fsys.WriteFile(ctx, "bad\x00name", nil), harness.FileErrorInvalid)
	requireCode(t, fsys.Remove(ctx, "/workspace", true, true), harness.FileErrorPermissionDenied)
	if _, err := fsys.ReadBinaryFile(ctx, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v does not wrap fs.ErrNotExist", err)
	}
	if len(fsys.Snapshot()) != 0 {
		t.Fatalf("rejected writes stored files: %v", fsys.Snapshot())
	}
}

func TestLimits(t *testing.T) {
	fsys := New(Options{MaxBytes: 10, MaxFiles: 2})
	ctx := t.Context()
	if err := fsys.WriteFile(ctx, "a", []byte("12345")); err != nil {
		t.Fatal(err)
	}
	if err := fsys.WriteFile(ctx, "b", []byte("12345")); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		fsys.WriteFile(ctx, "c", nil),
		fsys.AppendFile(ctx, "a", []byte("x")),
		fsys.WriteFile(ctx, "a", []byte("123456")),
		fsys.WriteFile(ctx, "dir/c", nil),
	} {
		requireCode(t, err, harness.FileErrorUnknown)
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("%v does not wrap ErrLimitExceeded", err)
		}
	}
	if exists, _ := fsys.Exists(ctx, "dir"); exists {
		t.Fatal("rejected write created its parent")
	}
	if err := fsys.WriteFile(ctx, "a", []byte("1")); err != nil {
		t.Fatalf("shrinking rewrite: %v", err)
	}
	if err := fsys.RenameFile(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := fsys.WriteFile(ctx, "c", []byte("123456789")); err != nil {
		t.Fatalf("rename did not release the replaced file: %v", err)
	}
	if err := fsys.Remove(ctx, "c", false, false); err != nil {
		t.Fatal(err)
	}
	if err := fsys.WriteFile(ctx, "d", []byte("123456789")); err != nil {
		t.Fatalf("remove did not release its bytes: %v", err)
	}
}

func TestClockSnapshotAndDirectoryRename(t *testing.T) {
	stamp := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	fsys := New(Options{Root: "/workspace", Now: func() time.Time { return stamp }})
	ctx := t.Context()
	if err := fsys.WriteFile(ctx, "src/a/one.txt", []byte("one")); err != nil {
		t.Fatal(err)
	}
	info, err := fsys.FileInfo(ctx, "src/a/one.txt")
	if err != nil || info.MTimeMS != float64(stamp.UnixMilli()) {
		t.Fatalf("FileInfo = %#v, %v", info, err)
	}
	requireCode(t, fsys.RenameFile(ctx, "src", "src/a/inside"), harness.FileErrorInvalid)
	if err := fsys.RenameFile(ctx, "src", "dst"); err != nil {
		t.Fatal(err)
	}
	snapshot := fsys.Snapshot()
	if len(snapshot) != 1 || snapshot["/workspace/dst/a/one.txt"] != "one" {
		t.Fatalf("snapshot = %v", snapshot)
	}
	snapshot["/workspace/dst/a/one.txt"] = "changed"
	if got, _ := fsys.ReadTextFile(ctx, "dst/a/one.txt"); got != "one" {
		t.Fatalf("snapshot aliases file content: %q", got)
	}
	entries, err := fsys.ListDir(ctx, "/")
	if err == nil {
		t.Fatalf("ListDir outside the root = %#v", entries)
	}
	if fsys.WorkingDirectory() != "/workspace" {
		t.Fatalf("working directory = %q", fsys.WorkingDirectory())
	}
}
