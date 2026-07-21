package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
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

func TestReadPipedStdin(t *testing.T) {
	content, err := ReadPipedStdin(strings.NewReader("\ufeff  prompt\u3000"))
	if err != nil || content == nil || *content != "prompt" {
		t.Fatalf("content = %v, err = %v", content, err)
	}
	empty, err := ReadPipedStdin(strings.NewReader(" \r\n\t"))
	if err != nil || empty != nil {
		t.Fatalf("empty content = %v, err = %v", empty, err)
	}
	nextLine, err := ReadPipedStdin(strings.NewReader("\u0085"))
	if err != nil || nextLine == nil || *nextLine != "\u0085" {
		t.Fatalf("ECMAScript-non-whitespace content = %v, err = %v", nextLine, err)
	}
}

func TestProcessTextFileArguments(t *testing.T) {
	cwd := t.TempDir()
	first := filepath.Join(cwd, "first.txt")
	second := filepath.Join(cwd, "second.txt")
	empty := filepath.Join(cwd, "empty.txt")
	writeCLIFile(t, first, "first")
	writeCLIFile(t, second, "second\n")
	writeCLIFile(t, empty, "")

	got, err := ProcessTextFileArguments([]string{"first.txt", empty, second}, cwd)
	if err != nil {
		t.Fatal(err)
	}
	want := `<file name="` + first + `">` + "\nfirst\n</file>\n" +
		`<file name="` + second + `">` + "\nsecond\n\n</file>\n"
	if got != want {
		t.Fatalf("file text mismatch\nwant: %q\n got: %q", want, got)
	}
}

func TestProcessTextFileArgumentsReportsResolvedMissingPath(t *testing.T) {
	cwd := t.TempDir()
	_, err := ProcessTextFileArguments([]string{"missing.txt"}, cwd)
	want := "File not found: " + filepath.Join(cwd, "missing.txt")
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestBuildInitialMessageConcatenatesWithoutSeparatorsAndConsumesFirstMessage(t *testing.T) {
	stdin := "stdin"
	args := CLIArgs{Messages: []string{"first", "second"}}
	message := BuildInitialMessage(&args, &stdin, "<file></file>\n")
	if message == nil || *message != "stdin<file></file>\nfirst" {
		t.Fatalf("message = %v", message)
	}
	if len(args.Messages) != 1 || args.Messages[0] != "second" {
		t.Fatalf("remaining messages = %#v", args.Messages)
	}

	empty := CLIArgs{}
	if got := BuildInitialMessage(&empty, nil, ""); got != nil {
		t.Fatalf("empty initial message = %q", *got)
	}
}

func TestCLITextUsesNodeUTF8Replacement(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "invalid.txt")
	if err := os.WriteFile(path, []byte{0xff, 0xff, 0xe2, 0x82}, 0o644); err != nil {
		t.Fatal(err)
	}
	text, err := ProcessTextFileArguments([]string{"invalid.txt"}, cwd)
	if err != nil {
		t.Fatal(err)
	}
	want := `<file name="` + path + `">` + "\n���\n</file>\n"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	stdin, err := ReadPipedStdin(strings.NewReader(string([]byte{0xff, 0xff, 0xe2, 0x82})))
	if err != nil || stdin == nil || *stdin != "���" {
		t.Fatalf("stdin = %#v, error = %v", stdin, err)
	}
}

func TestReadPipedStdinUsesJavaScriptTrimSet(t *testing.T) {
	content, err := ReadPipedStdin(strings.NewReader("\ufefftrimmed\ufeff"))
	if err != nil || content == nil || *content != "trimmed" {
		t.Fatalf("BOM-trimmed content = %#v, error = %v", content, err)
	}
	content, err = ReadPipedStdin(strings.NewReader("\u0085preserved\u0085"))
	if err != nil || content == nil || *content != "\u0085preserved\u0085" {
		t.Fatalf("U+0085 content = %#v, error = %v", content, err)
	}
}

func writeCLIFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
