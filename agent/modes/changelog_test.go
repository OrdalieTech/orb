package modes

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"testing"
)

// TestBundledChangelogMatchesSource guards assets/CHANGELOG.md.gz against
// drift from the upstream-synced assets/CHANGELOG.md (Makefile
// product-assets-check cmp): the decompressed embed must equal the on-disk
// file byte-for-byte. Regenerate with `make product-assets` or `gzip -9 -n`.
func TestBundledChangelogMatchesSource(t *testing.T) {
	reader, err := gzip.NewReader(bytes.NewReader(bundledChangelogCompressed))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("assets/CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("assets/CHANGELOG.md.gz does not decompress to assets/CHANGELOG.md; run make product-assets")
	}
	if bundledChangelog() == "" {
		t.Fatal("bundled changelog is empty")
	}
}
