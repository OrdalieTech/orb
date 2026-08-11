package models

import (
	"bytes"
	"compress/gzip"
	"io"
	"reflect"
	"testing"
)

// TestGeneratedCatalogGzipRoundtrip proves the compressed literal decodes to
// the same catalog the accessors serve: gunzip must succeed and Decode of the
// decompressed JSON must equal Builtin's cached instance. Staleness against
// the source snapshots is guarded by cataloggen's
// TestRenderMatchesCheckedInCatalog.
func TestGeneratedCatalogGzipRoundtrip(t *testing.T) {
	reader, err := gzip.NewReader(bytes.NewReader(generatedCatalogGzipJSON))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	builtin, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.providers) == 0 {
		t.Fatal("decompressed catalog has no providers")
	}
	if !reflect.DeepEqual(decoded.providers, builtin.providers) {
		t.Fatal("decompressed catalog differs from Builtin()")
	}
}
