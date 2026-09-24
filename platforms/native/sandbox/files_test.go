package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilesContainWritesAndSymlinkChanges(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	f := Files{WritableRoots: []string{root}}
	dest := filepath.Join(root, "new/deep/file")
	if err := f.MkdirAll(t.Context(), filepath.Dir(dest)); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile(t.Context(), dest, "allowed"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(outside, "file"), filepath.Join(root, "..", filepath.Base(outside), "file")} {
		if err := f.WriteFile(t.Context(), name, "blocked"); err == nil {
			t.Fatalf("outside write allowed: %s", name)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := f.MkdirAll(t.Context(), filepath.Join(link, "new/deep")); err == nil {
		t.Fatal("symlink escape created directories")
	}
	if err := f.WriteFile(t.Context(), filepath.Join(link, "file"), "blocked"); err == nil {
		t.Fatal("symlink escape wrote file")
	}
	if err := os.WriteFile(filepath.Join(outside, "existing"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	swapped := filepath.Join(root, "swapped")
	if err := os.WriteFile(swapped, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.Access(t.Context(), swapped); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(swapped); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "existing"), swapped); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile(t.Context(), swapped, "blocked"); err == nil {
		t.Fatal("symlink changed after access escaped")
	}
	data, err := os.ReadFile(filepath.Join(outside, "existing"))
	if err != nil || string(data) != "original" {
		t.Fatalf("outside changed: %q %v", data, err)
	}
}
