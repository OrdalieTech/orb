package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestProcessFileArgumentsSeparatesImageContent(t *testing.T) {
	cwd := t.TempDir()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cwd, "pixel.png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	processed, err := ProcessFileArguments([]string{path}, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if processed.Text != `<file name="`+path+`"></file>`+"\n" {
		t.Fatalf("text = %q", processed.Text)
	}
	if len(processed.Images) != 1 || processed.Images[0].MimeType != "image/png" || processed.Images[0].Data == "" {
		t.Fatalf("images = %#v", processed.Images)
	}
}
