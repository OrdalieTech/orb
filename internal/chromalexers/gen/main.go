// Command gen rebuilds embedded.tar.gz from the embedded/ XML lexer configs.
// The archive must be deterministic: entries are sorted by name and carry no
// timestamps, owners, or other varying metadata, so identical inputs produce
// identical bytes.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"log"
	"os"
	"path"
	"sort"
)

func main() {
	log.SetFlags(0)
	entries, err := os.ReadDir("embedded")
	if err != nil {
		log.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			log.Fatalf("embedded/%s: unexpected directory", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	var buffer bytes.Buffer
	compressed, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		log.Fatal(err)
	}
	archive := tar.NewWriter(compressed)
	for _, name := range names {
		data, err := os.ReadFile(path.Join("embedded", name))
		if err != nil {
			log.Fatal(err)
		}
		header := &tar.Header{
			Name:   path.Join("embedded", name),
			Mode:   0o644,
			Size:   int64(len(data)),
			Format: tar.FormatUSTAR,
		}
		if err := archive.WriteHeader(header); err != nil {
			log.Fatal(err)
		}
		if _, err := archive.Write(data); err != nil {
			log.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		log.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("embedded.tar.gz", buffer.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}
