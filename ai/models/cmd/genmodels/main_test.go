package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSYNC5GeneratedFileReplacementUsesCleanSameDirectoryStage(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "generated.go")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeGeneratedFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new" {
		t.Fatalf("generated content = %q", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("generated mode = %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".generated.go.") {
			t.Fatalf("staging file was not cleaned: %s", entry.Name())
		}
	}
}

func TestSYNC5GeneratedFileRenameFailurePreservesPreviousOutput(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "generated.go")
	if err := os.WriteFile(path, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("rename failed")
	err := writeGeneratedFileWithRename(path, []byte("replacement"), func(string, string) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("write error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "previous" {
		t.Fatalf("failed replacement changed output to %q", content)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "generated.go" {
		t.Fatalf("failed replacement left staging files: %#v", entries)
	}
}
