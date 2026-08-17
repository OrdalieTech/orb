package modes

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"sync"

	"github.com/OrdalieTech/orb/agent"
)

// Only the gzip form is embedded; assets/CHANGELOG.md stays on disk as the
// upstream-synced source (Makefile product-assets-check cmp) and
// TestBundledChangelogMatchesSource fails if the two drift. Regenerate with
// `make product-assets` or `gzip -9 -n`.
//
//go:embed assets/CHANGELOG.md.gz
var bundledChangelogCompressed []byte

var bundledChangelog = sync.OnceValue(func() string {
	reader, err := gzip.NewReader(bytes.NewReader(bundledChangelogCompressed))
	if err != nil {
		panic(err)
	}
	source, err := io.ReadAll(reader)
	if err != nil {
		panic(err)
	}
	if err := reader.Close(); err != nil {
		panic(err)
	}
	return agent.FormatChangelog(string(source))
})
