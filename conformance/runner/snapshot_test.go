package runner

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The Orb-owned render-golden files (D35): every file the ORB_UPDATE_F12
// mechanism may rewrite. The ordered JSON codec must reproduce each byte for
// byte, otherwise an update run would corrupt untouched values.
var orbOwnedSnapshotFamilies = []string{
	"F12",
	"F12-app",
	"F12-commands",
	"F12-export-jsonl",
	"F12-shutdown",
	"F12-ui-lifecycle",
	"F12-visible-commands",
	"WP450",
}

func TestSnapshotCodecRoundTripsOrbOwnedFixtures(t *testing.T) {
	for _, family := range orbOwnedSnapshotFamilies {
		entries, err := os.ReadDir(filepath.Join(FixtureRoot(), family))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			t.Run(family+"/"+name, func(t *testing.T) {
				data, err := ReadFixture(family, name)
				if err != nil {
					t.Fatal(err)
				}
				root, err := decodeOrderedJSON(data)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := appendOrderedJSON(nil, root, 0)
				if err != nil {
					t.Fatal(err)
				}
				encoded = append(encoded, '\n')
				if !bytes.Equal(data, encoded) {
					t.Fatal(ByteDiff(data, encoded))
				}
			})
		}
	}
}

func TestSnapshotCodecPreservesWireDetails(t *testing.T) {
	data := []byte("{\n  \"later\": 1e+09,\n  \"earlier\": \"\\ud800\"\n}")
	root, err := decodeOrderedJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := appendOrderedJSON(nil, root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, encoded) {
		t.Fatal(ByteDiff(data, encoded))
	}
}
